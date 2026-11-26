#!/usr/bin/env python3
# /// script
# requires-python = ">=3.12"
# dependencies = ["httpx==0.28.1", "mcp==1.30.0", "playwright==1.65.2"]
# ///
"""Run the SDK and browser acceptance test with Chrome and a built Shoowcase binary.

    browser_env=$(mktemp -d)
    python3.12 -m venv "$browser_env"
    "$browser_env/bin/python" -m pip install --require-hashes --only-binary=:all: -r scripts/browser-requirements.txt
    "$browser_env/bin/python" scripts/smoke-browser.py --binary ./bin/shoowcase
"""

import argparse
import asyncio
import hashlib
import os
import socket
import subprocess
import tempfile
import uuid
from contextlib import asynccontextmanager
from pathlib import Path

import httpx
from mcp import ClientSession
from mcp.client.streamable_http import streamable_http_client
from playwright.async_api import async_playwright, expect

from smoke import Browser, check


@asynccontextmanager
async def connect(endpoint, token):
    async with httpx.AsyncClient(headers={"Authorization": "Bearer " + token}, trust_env=False) as http:
        async with streamable_http_client(endpoint, http_client=http) as (receive, send, _):
            async with ClientSession(receive, send) as session:
                await session.initialize()
                yield session


async def call(session, tool, error=None, **args):
    result = await session.call_tool(tool, args)
    data = result.structuredContent or {}
    check(bool(result.isError) == bool(error) and data.get("error", {}).get("code") == error,
          f"{tool}: expected {error or 'success'}, got {data.get('error', {}).get('code', 'success')}")
    return data


def rid():
    return str(uuid.uuid4())


@asynccontextmanager
async def local_server(binary, *, fixture_workspace=None, fixture_paths=False):
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        port = probe.getsockname()[1]
    http = Browser(port)
    control = f"http://localhost:{port}"
    with tempfile.TemporaryDirectory(prefix="shoowcase-browser-") as state:
        # The trial must never touch an existing database or inherit a production token.
        env = {k: v for k, v in os.environ.items() if not k.startswith("SHOOWCASE_")}
        env.update(SHOOWCASE_LISTEN=f"127.0.0.1:{port}", SHOOWCASE_PUBLIC="true", SHOOWCASE_STATE_DIR=state)
        if fixture_workspace:
            env["SHOOWCASE_DEV_WORKSPACE"] = fixture_workspace
        with subprocess.Popen([binary, "-dev"], cwd=state, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL) as server:
            try:
                for _ in range(200):
                    try:
                        if http.request("GET", control + "/healthz").status == 200:
                            break
                    except OSError:
                        pass
                    check(server.poll() is None, "Shoowcase stopped before becoming ready")
                    await asyncio.sleep(.05)
                else:
                    raise RuntimeError("Shoowcase did not become ready")
                token = (Path(state) / "agent-token").read_text().strip()
                values = (control + "/mcp", token, http)
                yield (*values, Path(state)) if fixture_paths else values
            finally:
                server.terminate()
                try:
                    server.wait(timeout=8)
                except subprocess.TimeoutExpired:
                    server.kill()


async def create_draft(author, html, css):
    catalog = {t.name for t in (await author.list_tools()).tools}
    check({"create_site", "upload_files", "prepare_upload"} <= catalog, "SDK tool discovery")
    site = await call(author, "create_site", name="Team onboarding", request_id=rid())
    key = {"site_id": site["site_id"]}
    head = await call(author, "upload_files", **key, expected_revision=site["revision"],
                      files=[{"path": "index.html", "text": html}], request_id=rid())
    meta = dict(**key, expected_revision=head["revision"], size=len(css),
                sha256=hashlib.sha256(css).hexdigest(), request_id=rid())
    await call(author, "prepare_upload", "invalid_argument", path="bad.svg", **meta)
    upload = await call(author, "prepare_upload", path="style.css", **meta)
    return key, head, meta, upload


async def upload_css(page, example, head, meta, upload):
    # Start a real workspace browser session with the usual preview sign-in flow.
    await page.goto(head["preview_url"])
    await expect(page.get_by_role("heading", level=1)).to_have_text("A good place to start.")
    await page.goto(upload["browser_upload_url"])
    await page.get_by_text("Verify the file checksum").click()
    await expect(page.get_by_text(meta["sha256"])).to_be_visible()
    check(await page.evaluate("document.documentElement.scrollWidth <= innerWidth"), "upload form overflows narrow viewport")
    await page.get_by_label("Select file").set_input_files(example / "style.css")
    await page.get_by_role("button", name="Upload to draft").click()
    await expect(page.get_by_role("heading", name="Upload complete")).to_be_visible()


async def edit_and_release(editor, page, http, key, html, digest):
    found = await call(editor, "list_sites")
    check(key["site_id"] in {s["site_id"] for s in found["sites"]}, "new client could not find site")
    uploaded = await call(editor, "get_site", **key, path="style.css")
    check(uploaded["file"]["digest"] == digest, "browser upload checksum")
    await expect(page.get_by_text(uploaded["revision"], exact=True)).to_be_visible()
    one = await call(editor, "publish_site", **key, revision=uploaded["revision"], request_id=rid())
    stable = one["site_url"]
    http.request("GET", stable).expect(303, "anonymous workspace reader")
    two = await call(editor, "upload_files", **key, expected_revision=one["revision"],
                     files=[{"path": "index.html", "text": html.replace("09:00", "10:00")}], request_id=rid())
    old = await call(editor, "get_site", **key, revision=one["revision"], path="index.html")
    check(old["selected_revision"] == one["revision"] and old["file"]["text"] == html, "historical file selection")
    await page.goto(old["preview_url"])
    await expect(page.get_by_text("09:00", exact=True)).to_be_visible()
    await page.goto(two["preview_url"])
    await expect(page.get_by_text("10:00", exact=True)).to_be_visible()
    check(await page.evaluate("document.documentElement.scrollWidth <= innerWidth"), "example overflows narrow viewport")
    live = await call(editor, "publish_site", **key, revision=two["revision"], request_id=rid())
    public = await call(editor, "set_site_access", "approval_required", **key,
                        revision=two["revision"], visibility="public", request_id=rid())
    await approve(page, public, two["revision"])
    check(b"10:00" in http.request("GET", stable).expect(200, "anonymous published page").body, "wrong public version")
    rollback = await call(editor, "rollback_site", "approval_required", **key,
                          release_id=one["release_id"], expected_release_id=live["release_id"], request_id=rid())
    await approve(page, rollback, one["revision"])
    final = await call(editor, "get_site", **key)
    check(final["site_url"] == stable and final["live_revision"] == one["revision"] and final["revision"] == two["revision"]
          and final["visibility"] == "public", "rollback changed address, draft or audience")
    check(b"09:00" in http.request("GET", stable).expect(200, "anonymous restored page").body, "wrong restored version")


async def workflow(binary, channel, example):
    css = (example / "style.css").read_bytes()
    html = (example / "index.html").read_text(encoding="utf-8")
    check("09:00" in html, "the example must have an editable start time")
    async with local_server(binary) as (endpoint, token, http):
        async with connect(endpoint, token) as author:
            key, head, meta, upload = await create_draft(author, html, css)
        async with async_playwright() as pw:
            browser = await pw.chromium.launch(channel=channel)
            try:
                page = await browser.new_page(viewport={"width": 360, "height": 780})
                await upload_css(page, example, head, meta, upload)
                # A separate SDK client recovers the site without the original chat.
                async with connect(endpoint, token) as editor:
                    await edit_and_release(editor, page, http, key, html, meta["sha256"])
            finally:
                await browser.close()


async def approve(page, pending, revision):
    await page.goto(pending["approval_url"])
    await expect(page.get_by_text(revision, exact=True)).to_be_visible()
    await page.locator("form button").click()
    await expect(page.get_by_role("heading", name="Approval recorded")).to_be_visible()
    await expect(page.get_by_text(revision, exact=True)).to_be_visible()


if __name__ == "__main__":
    args = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    args.add_argument("--binary", required=True, type=Path, help="Shoowcase binary to start on loopback")
    args.add_argument("--browser-channel", default="chrome", help="installed Chrome channel (default: chrome); use chromium for Playwright Chromium")
    config = args.parse_args()
    asyncio.run(workflow(config.binary.resolve(strict=True), config.browser_channel, Path(__file__).resolve().parents[1] / "examples/onboarding"))
    print("PASS: two Python SDK clients and a mobile-width browser; found, edited, uploaded, previewed, published and rolled back the example")
