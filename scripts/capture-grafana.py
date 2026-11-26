#!/usr/bin/env python3
# /// script
# requires-python = ">=3.12"
# dependencies = ["playwright==1.65.2"]
# ///
"""Exercise a disposable Shoowcase instance, verify real Grafana queries, and capture the three dashboards."""
import argparse
from concurrent.futures import ThreadPoolExecutor
from contextlib import closing, contextmanager
from datetime import datetime, timezone
import json
import math
import os
from pathlib import Path
import secrets
import socket
import sqlite3
import subprocess
import tempfile
import time
from urllib.parse import parse_qs, urlencode, urlsplit
from urllib.request import ProxyHandler, build_opener
import uuid

from playwright.sync_api import sync_playwright
from smoke import Browser, MCP, check

ROOT = Path(__file__).resolve().parents[1]
LOCAL = build_opener(ProxyHandler({}))
DATASOURCE = "shoowcase-prometheus"
DASHBOARDS = {"shoowcase-http": "http", "shoowcase-mcp": "mcp", "shoowcase-db": "database"}


def port_number(value):
    port = int(value)
    if not 0 < port <= 65535:
        raise argparse.ArgumentTypeError("expected a local TCP port between 1 and 65535")
    return port


@contextmanager
def server(binary, collector_port, instance):
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    with tempfile.TemporaryDirectory(prefix="shoowcase-grafana-") as state:
        token = secrets.token_urlsafe(32)
        # Never use inherited credentials, exporters, state, database or workspace.
        env = {k: v for k, v in os.environ.items() if not k.startswith(("SHOOWCASE_", "OTEL_"))}
        env.update(SHOOWCASE_STATE_DIR=state, SHOOWCASE_LISTEN=f"127.0.0.1:{port}", SHOOWCASE_DEV_TOKEN=token,
                   OTEL_EXPORTER_OTLP_METRICS_ENDPOINT=f"http://127.0.0.1:{collector_port}/v1/metrics",
                   OTEL_METRIC_EXPORT_INTERVAL="1000", OTEL_RESOURCE_ATTRIBUTES=f"service.instance.id={instance}")
        with subprocess.Popen([binary, "-dev"], cwd=state, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL) as app:
            try:
                browser = Browser(port)
                for _ in range(200):
                    if app.poll() is not None:
                        break
                    try:
                        healthy = browser.request("GET", f"http://localhost:{port}/healthz").status == 200
                    except ConnectionRefusedError:
                        healthy = False
                    if healthy:
                        break
                    time.sleep(.05)
                else:
                    raise RuntimeError("disposable Shoowcase instance did not become healthy")
                check(app.poll() is None, "disposable Shoowcase instance exited during startup")
                yield port, token, Path(state) / "shoowcase.db"
            finally:
                app.terminate()
                try:
                    app.wait(timeout=6)
                except subprocess.TimeoutExpired:
                    app.kill()


def traffic(port, token, database, seconds):
    agent, reader, person = MCP(port, token, "dashboard-demo"), MCP(port, token, "dashboard-reader"), Browser(port)
    origin = f"http://localhost:{port}"
    rid = lambda: str(uuid.uuid4())
    site = agent.call("create_site", {"name": "Dashboard sample", "slug": "dashboard-sample", "request_id": rid()})
    key = {"site_id": site["site_id"]}

    def publish(revision, edition):
        draft = agent.call("upload_files", {**key, "expected_revision": revision, "request_id": rid(),
                          "files": [{"path": "index.html", "text": f"<h1>Dashboard sample {edition}</h1>"}]})
        agent.call("publish_site", {**key, "revision": draft["revision"], "request_id": rid()})
        return draft["revision"]

    revision = publish(site["revision"], 0)
    person.navigate(site["site_url"]).expect(200, "sign into sample release")
    start, clock = int(time.time() * 1000), time.monotonic()
    tick, stop = 0, clock + seconds
    # Contend only on this run's temporary SQLite database; the app has one connection.
    # An exclusive read window queues a live content request and readiness probes behind it.
    # This makes intermittent used capacity / wait durations observable at scrape time.
    with closing(sqlite3.connect(database)) as lock, ThreadPoolExecutor(max_workers=10) as pool:
        while time.monotonic() < stop:
            tick += 1
            phase = tick % 24
            agent.call("list_sites", {"limit": 5})
            if phase < 13:
                agent.call("get_site", key)
            if tick % 7 == 0:
                agent.call("get_site", {"site_id": "missing-sample"}, "not_found")
            if tick % 19 == 0:
                revision = publish(revision, tick)
            person.request("GET", site["site_url"]).expect(200, "signed-in sample release")
            if phase < 12:
                Browser(port).request("GET", site["site_url"]).expect(303, "private release")
                Browser(port).request("POST", origin + "/mcp", b"{}").expect(401, "MCP without credentials")
            if phase in (1, 3, 5, 7, 9, 11):
                lock.execute("BEGIN EXCLUSIVE")
                try:
                    signed = pool.submit(person.request, "GET", site["site_url"])
                    time.sleep(.04)
                    read = pool.submit(reader.call, "get_site", key)
                    probes = [pool.submit(Browser(port).request, "GET", origin + "/readyz") for _ in range(6)]
                    time.sleep(.50 + phase / 50)
                finally:
                    lock.rollback()
                signed.result().expect(200, "concurrent content request")
                read.result()
                for probe in probes:
                    probe.result().expect(200, "concurrent readiness probe")
            time.sleep(.65 if phase < 12 else 1.25)
    return start, int(time.time() * 1000)


def get_json(url):
    with LOCAL.open(url, timeout=5) as response:
        return json.load(response)


def check_stack(grafana):
    ds = get_json(f"{grafana}/api/datasources/uid/{DATASOURCE}")
    check((ds["uid"], ds["type"], ds["access"], ds["url"]) == (DATASOURCE, "prometheus", "proxy", "http://prometheus:9090"),
          "Grafana does not have the sample stack's local Prometheus datasource")
    for uid in DASHBOARDS:
        doc = get_json(f"{grafana}/api/dashboards/uid/{uid}")
        check(doc["dashboard"]["uid"] == uid and doc["meta"]["provisioned"] and doc["meta"]["folderUid"] == "shoowcase",
              f"{uid}: expected a provisioned dashboard in the local Shoowcase folder")


def check_export(grafana, instance):
    # Prove that the new process reached this specific Grafana datasource before sending workload.
    # A successful POST to a host port is insufficient: a separate local collector may be bound to it.
    probe = f'http_server_request_duration_seconds_count{{job="shoowcase",instance="{instance}"}}'
    url = f"{grafana}/api/datasources/proxy/uid/{DATASOURCE}/api/v1/query?" + urlencode({"query": probe})
    deadline = time.monotonic() + 25
    while time.monotonic() < deadline:
        response = get_json(url)
        if response["status"] == "success" and any(float(i["value"][1]) > 0 for i in response["data"]["result"]):
            return
        time.sleep(1)
    raise RuntimeError("The local Grafana datasource never saw this process's health check. Check the collector port and Prometheus target.")


def frames(result):
    values = []
    for frame in result.get("frames", []):
        for field, column in zip(frame["schema"]["fields"], frame["data"]["values"], strict=True):
            if field["type"] == "number":
                values.extend(x for x in column if isinstance(x, (int, float)) and math.isfinite(x))
    return values


def check_links(page, uid, start, end, instance):
    page.get_by_role("button", name="Shoowcase dashboards").click()
    menu_items = page.locator('a[role="menuitem"][href*="/d/shoowcase-"]')
    menu_items.first.wait_for()
    destinations = set()
    for href in menu_items.evaluate_all("items => items.map(item => item.href)"):
        u = urlsplit(href)
        destinations.add(u.path.split("/")[2])
        params = parse_qs(u.query)
        for key, millis in {"from": start, "to": end}.items():
            expected = datetime.fromtimestamp(millis / 1000, timezone.utc)
            check(datetime.fromisoformat(params[key][0]) == expected, f"{uid}: linked dashboard loses {key} timestamp")
        check(params.get("var-instance") == [instance] and params.get("var-service") == ["shoowcase"],
              f"{uid}: linked dashboard loses the fixture filters")
    check(destinations == DASHBOARDS.keys() - {uid}, f"{uid}: expected links to both other dashboards")
    page.keyboard.press("Escape")
    menu_items.first.wait_for(state="hidden")


def capture(grafana, start, end, instance, output, channel):
    panels_seen = 0
    with sync_playwright() as pw, tempfile.TemporaryDirectory(prefix="shoowcase-panels-") as staging:
        browser = pw.chromium.launch(channel=channel)
        ctx = browser.new_context(viewport={"width": 1500, "height": 860}, device_scale_factor=1, timezone_id="UTC")
        try:
            for uid, name in DASHBOARDS.items():
                meta = ctx.request.get(f"{grafana}/api/dashboards/uid/{uid}")
                check(meta.ok, f"dashboard {uid} is not provisioned on the local Grafana")
                doc = meta.json()
                panels = {p["id"]: p for p in doc["dashboard"]["panels"] if p.get("targets")}
                check(set(panels) == set(range(1, 7)), f"{uid}: expected six query panels")
                page, queries = ctx.new_page(), []
                page.on("response", lambda response: queries.append(response) if urlsplit(response.url).path == "/api/ds/query" else None)
                params = urlencode({"from": start, "to": end, "var-service": "shoowcase", "var-instance": instance,
                                    "theme": "dark", "timezone": "utc"})
                page.goto(grafana + doc["meta"]["url"] + "?" + params, wait_until="domcontentloaded")
                page.get_by_text(doc["dashboard"]["title"], exact=True).first.wait_for(timeout=30000)
                close_menu = page.get_by_role("button", name="Close menu", exact=True)
                if close_menu.is_visible():
                    close_menu.click()
                page.mouse.move(0, 0)
                for panel in panels.values():
                    page.get_by_role("region", name=panel["title"], exact=True).wait_for(timeout=30000)
                deadline = time.monotonic() + 30
                results = {}
                while time.monotonic() < deadline:
                    while queries:
                        response = queries.pop(0)
                        check(response.ok, f"{uid}: Grafana datasource HTTP {response.status}")
                        data = response.json()["results"]
                        for q in response.request.post_data_json["queries"]:
                            expr = q.get("expr", "")
                            if not expr:
                                continue
                            result = data[q["refId"]]
                            check(not result.get("error") and result.get("status", 200) == 200,
                                  f"{uid}: datasource query error: {result.get('error', result.get('status'))}")
                            check(instance in expr, f"{uid}: unfiltered datasource query would mix fixture and other data")
                            values = frames(result)
                            check(values, f"{uid}: datasource query has no finite data: {expr}")
                            panel_id = int(q["requestId"].removesuffix(q["refId"]))
                            check(panel_id in panels, f"{uid}: unexpected panel {panel_id}")
                            results[panel_id] = max(values)
                    if results.keys() == panels.keys():
                        break
                    page.wait_for_timeout(200)
                check(results.keys() == panels.keys(), f"{uid}: missing measurements for panels {panels.keys() - results.keys()}")
                positive = {"shoowcase-http": [1, 3, 4, 5], "shoowcase-mcp": [1, 2, 3, 4, 5, 6], "shoowcase-db": [2, 4, 5, 6]}
                for panel_id in positive[uid]:
                    check(results[panel_id] > 0, f"{uid}: workload produced no positive {panels[panel_id]['title']}")
                # Verify rendered panels too: a successful upstream query can still fail a Grafana transformation.
                for panel in panels.values():
                    box = page.get_by_role("region", name=panel["title"], exact=True).bounding_box()
                    check(box and 0 <= box["y"] and box["y"] + box["height"] <= 860 and box["x"] + box["width"] <= 1500,
                          f"{uid}: part of {panel['title']} would be outside the screenshot")
                for label in {"No data"} | {p["fieldConfig"]["defaults"]["noValue"] for p in panels.values()}:
                    page.get_by_text(label, exact=True).first.wait_for(state="hidden", timeout=10000)
                check(not page.get_by_label("Panel error", exact=False).count(), f"{uid}: rendered panel error")
                check_links(page, uid, start, end, instance)
                page.evaluate("document.fonts.ready")
                page.mouse.move(0, 0)
                page.screenshot(path=Path(staging) / f"{name}.png", animations="disabled")
                page.close()
                print(f"PASS {uid}: {len(panels)} panels with finite data; {len(results)} verified Prometheus queries", flush=True)
                panels_seen += len(panels)
        finally:
            browser.close()
        output.mkdir(parents=True, exist_ok=True)
        for name in DASHBOARDS.values():
            path = Path(staging) / f"{name}.png"
            (output / path.name).write_bytes(path.read_bytes())
    return panels_seen


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, default=ROOT / "bin/shoowcase")
    parser.add_argument("--output", type=Path, default=ROOT / "docs/media/grafana")
    parser.add_argument("--seconds", type=int, default=135, help="traffic duration, at least 75 seconds")
    parser.add_argument("--grafana-port", type=port_number, default=os.environ.get("SHOOWCASE_GRAFANA_PORT", "13000"))
    parser.add_argument("--collector-port", type=port_number, default=os.environ.get("SHOOWCASE_OTLP_PORT", "24318"))
    parser.add_argument("--browser-channel", default="chrome")
    args = parser.parse_args()
    if args.seconds < 75:
        parser.error("at least 75 seconds are needed for the Prometheus rate windows")
    grafana = f"http://127.0.0.1:{args.grafana_port}"
    check_stack(grafana)
    instance = "demo-" + uuid.uuid4().hex[:8]
    print(f"Verifying local metric ingestion for disposable instance {instance}", flush=True)
    with server(args.binary.resolve(strict=True), args.collector_port, instance) as (port, token, database):
        check_export(grafana, instance)
        print(f"Confirmed ingestion. Sending {args.seconds}s of synthetic workload.", flush=True)
        start, end = traffic(port, token, database, args.seconds)
        # Give the 1s exporter and 5s scrape a chance to deliver the last observations.
        time.sleep(7)
        print(f"Capturing the fixture's UTC window {start}–{end}", flush=True)
        total = capture(grafana, start, end, instance, args.output, args.browser_channel)
    print(f"PASS: verified {total} panels; screenshots saved to {args.output}")


if __name__ == "__main__":
    main()
