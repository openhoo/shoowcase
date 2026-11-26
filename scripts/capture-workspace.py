#!/usr/bin/env python3
# /// script
# requires-python = ">=3.12"
# dependencies = ["httpx==0.28.1", "mcp==1.30.0", "playwright==1.65.2"]
# ///
"""Capture the actual directory in Chrome, using only a disposable local fixture.

    make build
    npm ci --prefix runtime --ignore-scripts
    uv run scripts/capture-workspace.py --binary ./bin/shoowcase --output docs/media
"""
import argparse
import asyncio
from datetime import datetime, timedelta, timezone
from importlib import import_module
from pathlib import Path
import sqlite3

from playwright.async_api import async_playwright, expect

browser = import_module("smoke-browser")
workspace = import_module("smoke-workspace")
# Name, slug, draft file count, state, audience, synthetic activity offset.
SITES = [
    ("Design handbook", "design-handbook", 7, "changes", "workspace", 0),
    ("Product changelog", "product-changelog", 4, "published", "public", 100),
    ("Research library", "research-library", 6, "published", "workspace", 180),
    ("Open studio", "open-studio", 3, "draft", "workspace", 1400),
    ("Service playbook", "service-playbook", 5, "changes", "workspace", 1560),
    ("Team onboarding", "team-onboarding", 4, "published", "workspace", 2680),
    ("Field notes", "field-notes", 2, "draft", "workspace", 4380),
    ("Spring showcase", "spring-showcase", 5, "offline", "offline", 6120),
]


async def seed(agent, page):
    call, rid = browser.call, browser.rid
    for name, slug, count, status, audience, _ in SITES:
        site = await call(agent, "create_site", name=name, slug=slug, request_id=rid())
        files = [
            {"path": "index.html" if index == 0 else f"notes-{index}.html", "text": f"<h1>{name}</h1><p>Section {index + 1}</p>"}
            for index in range(count)
        ]
        args = {"site_id": slug}
        draft = await call(agent, "upload_files", **args, expected_revision=site["revision"], files=files, request_id=rid())
        rev = draft["revision"]
        if status == "draft":
            continue
        await call(agent, "publish_site", **args, revision=rev, request_id=rid())
        if audience == "public":
            receipt = await call(agent, "set_site_access", "approval_required", **args, visibility=audience, revision=rev, request_id=rid())
            await browser.approve(page, receipt, rev)
        elif status == "offline":
            await call(agent, "set_site_access", **args, visibility="offline", revision=rev, request_id=rid())
        if status == "changes":
            await call(agent, "upload_files", **args, expected_revision=rev, files=[{"path": "index.html", "text": f"<h1>{name}</h1><p>Updated edition</p>"}], request_id=rid())


def fixture_times(state):
    # Only the history timestamps are synthetic, in the private database made
    # by local_server. No endpoint, real account or external database is accepted.
    now = datetime(2026, 11, 26, 14, 32, tzinfo=timezone.utc)
    with sqlite3.connect(state / "shoowcase.db") as db:
        for _, slug, _, _, _, minutes in SITES:
            newest = db.execute("SELECT MAX(created_at) FROM sc_audit WHERE site=?", (slug,)).fetchone()[0]
            assert newest is not None
            offset = int((now - timedelta(minutes=minutes)).timestamp() * 1000) - newest
            db.execute("UPDATE sc_audit SET created_at=created_at+? WHERE site=?", (offset, slug))


async def capture(binary, output, channel):
    output.mkdir(parents=True, exist_ok=True)
    # The existing acceptance fixture owns the server lifetime and shutdown.
    async with browser.local_server(binary, fixture_workspace="Design studio", fixture_paths=True) as (endpoint, token, _, state), async_playwright() as pw:
        chrome = await pw.chromium.launch(channel=channel)
        try:
            ctx = await chrome.new_context(viewport={"width": 1440, "height": 1000}, device_scale_factor=1, locale="en-US", reduced_motion="reduce")
            page = await ctx.new_page()
            errors = []
            page.on("pageerror", lambda _: errors.append(True))
            control = endpoint.removesuffix("/mcp")
            await page.goto(control)
            await page.get_by_role("link", name="Create an account").click()
            await page.get_by_label("Display name").fill("Mira Cole")
            await page.get_by_role("button", name="Create account").click()
            async with browser.connect(endpoint, token) as agent:
                await seed(agent, page)
            fixture_times(state)
            await page.goto(control + "/sites")
            for name, *_ in SITES:
                await expect(page.get_by_role("link", name=name, exact=True)).to_be_visible()
            # Search by slug (differs from display name), and use server-applied
            # filters directly through their labelled form controls.
            search = page.locator('input[name="q"]')
            await search.fill("product-change")
            await search.press("Enter")
            await expect(page.get_by_role("link", name="Product changelog")).to_be_visible()
            await expect(page.get_by_role("link", name="Design handbook")).to_have_count(0)
            await page.get_by_role("link", name="Reset filters").click()
            await page.get_by_label("Status", exact=True).select_option("changes")
            await page.get_by_label("Audience", exact=True).select_option("workspace")
            await page.get_by_role("button", name="Apply filters").click()
            await expect(page.get_by_role("link", name="Service playbook")).to_be_visible()
            await expect(page.get_by_role("link", name="Open studio")).to_have_count(0)
            await page.get_by_role("link", name="Reset filters").click()
            await page.get_by_label("Sort by").select_option("name")
            await page.get_by_role("button", name="Apply filters").click()
            await expect(page.get_by_role("link", name="Field notes")).to_be_visible()
            ordered = await page.get_by_role("main").get_by_role("link").all_text_contents()
            names = [s.strip() for s in ordered if s.strip() in {row[0] for row in SITES}]
            assert names == sorted(row[0] for row in SITES), names
            await page.goto(control + "/sites")
            await page.keyboard.press("Tab")
            await expect(page.get_by_role("link", name="Skip to content")).to_be_focused()
            await page.keyboard.press("Enter")
            await expect(page.get_by_role("main")).to_be_focused()
            await search.focus()
            await expect(search).to_be_focused()
            await page.evaluate("document.activeElement.blur(); scrollTo(0, 0)")
            await workspace.fits(page, "desktop directory")
            desktop = output / "workspace.png"
            await page.screenshot(path=desktop, full_page=True, animations="disabled")
            await page.set_viewport_size({"width": 390, "height": 844})
            toggle = page.get_by_role("button", name="Filter and sort")
            await expect(toggle).to_have_attribute("aria-expanded", "false")
            await toggle.press("Space")
            await expect(page.get_by_label("Audience", exact=True)).to_be_visible()
            await toggle.press("Space")
            await expect(toggle).to_have_attribute("aria-expanded", "false")
            await page.evaluate("document.activeElement.blur(); scrollTo(0, 0)")
            await workspace.fits(page, "mobile directory")
            mobile = output / "workspace-mobile.png"
            await page.screenshot(path=mobile, animations="disabled")
            await page.set_viewport_size({"width": 320, "height": 740})
            await workspace.fits(page, "narrow directory")
            assert not errors, "directory caused a browser error"
            print(f"Verified {len(SITES)} sites, search, combined filters, sorting, keyboard access and axe at 1440/390/320 px.")
            print(desktop)
            print(mobile)
        finally:
            await chrome.close()


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("--binary", required=True, type=Path)
    cli.add_argument("--output", required=True, type=Path, help="directory for the desktop and mobile PNGs")
    cli.add_argument("--browser-channel", default="chrome")
    args = cli.parse_args()
    asyncio.run(capture(args.binary.resolve(strict=True), args.output, args.browser_channel))
