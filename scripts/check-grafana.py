#!/usr/bin/env python3
"""Evaluate the checked-in Grafana queries using promtool and a synthetic time series fixture."""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[1]
OBS = ROOT / "deploy/observability"


def queries():
    dashboards = {}
    for file in sorted((OBS / "dashboards").glob("*.json")):
        d = json.loads(file.read_text())
        uid = d["uid"]
        if uid in dashboards:
            raise ValueError(f"{file}: duplicate uid {uid}")
        targets = {}
        for panel in d["panels"]:
            for t in panel.get("targets", []):
                if not t.get("expr"):
                    continue
                key = (panel["id"], t["refId"])
                if key in targets:
                    raise ValueError(f"{file}: duplicate panel target {key}")
                targets[key] = t["expr"]
        dashboards[uid] = targets
    return dashboards


def interpolate(expr, replica=".*"):
    for token, value in (("${service:regex}", "shoowcase"), ("${instance:regex}", replica),
                         ("$__rate_interval", "5m")):
        expr = expr.replace(token, value)
    if re.search(r"\$(?:[a-zA-Z_{])", expr):
        raise ValueError(f"unsupported fixture variable: {expr}")
    return expr


def series(metric, values, replica="a", service="shoowcase", **attrs):
    labels = {"job": service, "instance": replica, "otel_scope_name": "github.com/openhoo/shoowcase", **attrs}
    selector = ",".join(f"{k}={json.dumps(v)}" for k, v in labels.items())
    return {"series": metric + "{" + selector + "}", "values": values}


def run(binary=None):
    dashboards = queries()
    cases = []

    def case(name, inputs):
        c = {"name": name, "interval": "1m", "input_series": inputs, "promql_expr_test": []}
        cases.append(c)
        return c

    def verify(c, uid, panel_id, expected, replica=".*", labels="{}"):
        expr = dashboards["shoowcase-" + uid][panel_id, "A"]
        c["promql_expr_test"].append({
            "expr": interpolate(expr, replica), "eval_time": "5m",
            "exp_samples": [] if expected is None else [{"labels": labels, "value": expected}],
        })

    # Parse every expression in the dashboards. Use count to accept any output
    # labels and limit the empty-input check to syntax rather than null policy.
    empty = case("all dashboard expressions parse", [])
    for metrics in dashboards.values():
        for expr in metrics.values():
            empty["promql_expr_test"].append({
                "expr": "count((" + interpolate(expr) + ")) < 0", "eval_time": "5m", "exp_samples": [],
            })

    http = "http_server_request_duration_seconds_count"
    mcp = "shoowcase_mcp_tool_duration_seconds_count"
    pool = {"db_client_connection_pool_name": "store"}

    def requests(increase, replica="a", service="shoowcase", **attrs):
        return series(http, f"0+{increase}x5", replica, service, shoowcase_surface="control", **attrs)

    def calls(increase, outcome):
        return series(mcp, f"0+{increase}x5", gen_ai_tool_name="list_sites", shoowcase_outcome=outcome)

    def pool_input(name, value, replica, **attrs):
        return series(name, f"{value}+0x5", replica, **pool, **attrs)

    db_count = "db_client_connection_count"
    db_max = "db_client_connection_max"
    healthy = requests(600, http_response_status_code="200")
    quiet = case("observed traffic without server errors", [healthy, calls(120, "ok")])
    verify(quiet, "http", 2, 0)
    verify(quiet, "mcp", 2, 100)
    idle = case("zero traffic does not imply a successful or failing request", [
        requests(0, http_response_status_code="200"), calls(0, "ok"),
    ])
    verify(idle, "http", 2, None)
    verify(idle, "mcp", 2, None)

    mixed = case("replicas, interrupted requests and a second service", [
        requests(480, http_response_status_code="200"), requests(60, http_response_status_code="503", error_type="503"),
        requests(60, error_type="interrupted"), requests(600, "b", http_response_status_code="200"),
        requests(6000, "other", "unrelated", http_response_status_code="500"),
        requests(6000, otel_scope_name="different.http.instrument", http_response_status_code="200"),
        calls(480, "ok"), calls(120, "forbidden"),
        pool_input(db_count, 4, "a", db_client_connection_state="used"), pool_input(db_max, 4, "a"),
        pool_input(db_count, 2, "b", db_client_connection_state="used"), pool_input(db_max, 12, "b"),
    ])
    verify(mixed, "http", 1, 20)
    verify(mixed, "http", 2, 10)
    verify(mixed, "http", 2, 20, replica="a")
    verify(mixed, "mcp", 1, 10)
    verify(mixed, "mcp", 2, 80)
    verify(mixed, "db", 1, 6)
    verify(mixed, "db", 2, 16)
    verify(mixed, "db", 3, 100)
    # Replica A has 80% of requests <= 0.1s; B has none. Aggregate
    # histogram buckets first: fleet p95 is 0.925s, not a mean of p95s.
    latency = case("weighted fleet p95 from cumulative buckets", [
        requests(600), requests(600, replica="b"),
        *[series("http_server_request_duration_seconds_bucket", f"0+{n}x5", replica, le=bucket)
          for replica, small in (("a", 480), ("b", 0)) for bucket, n in (("0.1", small), ("1", 600), ("+Inf", 600))],
    ])
    verify(latency, "http", 3, .925)
    db_waits = case("wait duration is a mean per actual wait in seconds", [
        series("shoowcase_db_pool_waits_total", "0+120x5", **pool),
        series("shoowcase_db_pool_wait_duration_seconds_total", "0+60x5", **pool),
        series("shoowcase_db_pool_waits_total", "0+0x5", "b", **pool),
        series("shoowcase_db_pool_wait_duration_seconds_total", "0+0x5", "b", **pool),
    ])
    verify(db_waits, "db", 6, .5, labels='{instance="a",job="shoowcase"}')
    denied = case("no successful MCP tool call", [calls(120, "forbidden")])
    verify(denied, "mcp", 2, 0)

    if binary:
        command = [binary]
    else:
        # Render the actual Compose pin without starting the stack or creating
        # a password. This one-shot promtool process has no network or mounts.
        pin = subprocess.check_output([
            "docker", "compose", "-f", str(OBS / "compose.yaml"), "config", "--images", "prometheus",
        ], text=True, env={**os.environ, "SHOOWCASE_GRAFANA_PASSWORD_FILE": os.devnull}).strip()
        if "@sha256:" not in pin or "\n" in pin:
            raise RuntimeError("expected the pinned Prometheus image in the local observability stack")
        command = ["docker", "run", "--rm", "-i", "--network=none", "--read-only",
                   "--tmpfs", "/tmp:size=128m,mode=1777", "--cap-drop=ALL", "--entrypoint", "/bin/promtool", pin]
    spec = {"evaluation_interval": "1m", "fuzzy_compare": True, "tests": cases}
    subprocess.run([*command, "test", "rules", "/dev/stdin"], input=json.dumps(spec), text=True, check=True)
    print(f"Validated {sum(map(len, dashboards.values()))} dashboard queries and {len(cases)-1} traffic scenarios.")


if __name__ == "__main__":
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--promtool", help="use this local promtool binary instead of the pinned container")
    run(ap.parse_args().promtool)
