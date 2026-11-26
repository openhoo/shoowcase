#!/usr/bin/env python3
# /// script
# requires-python = ">=3.12"
# dependencies = ["httpx==0.28.1", "mcp==1.30.0", "playwright==1.65.2"]
# ///
"""Exercise the workspace UI, revocable keys and an interactive site in a clean local instance."""
import argparse
import asyncio
import hashlib
import io
import zipfile
from functools import cache
from importlib import import_module
from pathlib import Path

from playwright.async_api import async_playwright, expect
from smoke import check

browser_smoke = import_module("smoke-browser")
ROOT = Path(__file__).resolve().parents[1]
EXAMPLE = ROOT / "examples/counter"

@cache
def axe_source():
    path = ROOT / "runtime/node_modules/axe-core/axe.min.js"
    if not path.is_file():
        raise RuntimeError("Run npm ci --prefix runtime --ignore-scripts before the workspace smoke.")
    return path.read_text(encoding="utf-8")

async def fits(page, name):
    check(await page.evaluate("document.documentElement.scrollWidth <= innerWidth"), f"{name} overflows at 320px")
    await page.evaluate(axe_source())
    # Return only rule IDs and selectors; do not expose the key receipt or page text.
    violations = await page.evaluate("""async () => {
      const results = await axe.run(document, {runOnly: {type: 'tag',
        values: ['wcag2a','wcag2aa','wcag21a','wcag21aa','wcag22a','wcag22aa']},
        resultTypes: ['violations']});
      return results.violations.map(v => [v.id, v.nodes.map(n => n.target)]);
    }""")
    check(not violations, f"{name}: axe WCAG violations: {violations}")


async def enroll(page, control):
    await page.goto(control)
    await page.keyboard.press("Tab")
    skip = page.get_by_role("link", name="Skip to content")
    await expect(skip).to_be_focused()
    await page.keyboard.press("Enter")
    await expect(page.get_by_role("main")).to_be_focused()
    await fits(page, "landing")
    await page.get_by_role("link", name="Create an account").click()
    await fits(page, "account enrollment")
    name = page.get_by_label("Display name")
    await name.fill("Alex Morgan")
    # From the only text field, a keyboard user can submit without a mouse.
    await name.press("Tab")
    await expect(page.get_by_role("button", name="Create account")).to_be_focused()
    await page.keyboard.press("Enter")
    await expect(page.get_by_role("heading", name="No sites yet")).to_be_visible()
    await fits(page, "empty sites list")


async def create_key(page, endpoint):
    await page.get_by_role("link", name="Agent keys", exact=True).click()
    await fits(page, "agent keys")
    await page.get_by_label("Key name").fill("Workshop publisher")
    await page.get_by_label("Access", exact=True).select_option("publisher")
    await page.get_by_label("Expires in").select_option("7")
    await page.get_by_role("button", name="Create key").click()
    check(await page.get_by_label("MCP server URL").input_value() == endpoint, "wrong MCP URL")
    await fits(page, "key receipt")
    secret = await page.get_by_label("Secret key", exact=True).input_value()
    check(bool(secret), "no key on receipt")
    await page.get_by_role("link", name="Back to agent keys").click()
    check(secret not in await page.content(), "key visible after leaving the receipt")
    return secret


async def manual_site(page):
    await page.get_by_role("link", name="Sites", exact=True).click()
    await page.get_by_role("link", name="New site", exact=True).first.click()
    await page.get_by_label("Site name").fill("Workshop tally")
    await page.get_by_label("Site address").fill("workshop-tally")
    await page.get_by_role("button", name="Create site").click()
    await page.get_by_role("link", name="Import ZIP").click()
    with io.BytesIO() as buf:
        with zipfile.ZipFile(buf, "w", compression=zipfile.ZIP_DEFLATED) as z:
            for name in ("index.html", "app.js"):
                z.writestr("Workshop tally/" + name, (EXAMPLE / name).read_bytes())
        await page.get_by_label("ZIP archive").set_input_files(
            {"name": "workshop.zip", "mimeType": "application/zip", "buffer": buf.getvalue()})
    await fits(page, "ZIP importer")
    await page.get_by_role("button", name="Import files").click()
    await expect(page.get_by_role("status")).to_have_text("ZIP imported into the draft.")
    await page.get_by_role("link", name="Upload file").click()
    await fits(page, "file upload")
    await page.get_by_label("Choose a file").set_input_files(EXAMPLE / "style.css")
    await page.get_by_role("button", name="Upload to draft").click()
    await expect(page.get_by_role("status")).to_have_text("File uploaded to the draft.")
    await page.get_by_role("link", name="app.js", exact=True).click()
    editor = page.get_by_label("File contents")
    code = await editor.input_value()
    check(code == (EXAMPLE / "app.js").read_text(), "editor did not recover the original program")
    code += "\n// Workshop edition.\n"
    await editor.fill(code)
    await fits(page, "source editor")
    await page.get_by_role("button", name="Save file").click()
    await expect(page.get_by_role("status")).to_have_text("File saved to the draft.")
    await fits(page, "site detail")
    return code


async def try_counter(page, url):
    await page.goto(url)
    await expect(page.locator("#count")).to_have_text("0")
    await expect(page.locator("#add")).to_be_enabled(timeout=10000)
    await page.locator("#add").focus()
    await page.keyboard.press("Enter")
    await expect(page.locator("#count")).to_have_text("1")
    await page.get_by_role("button", name="Reset").click()
    await expect(page.locator("#count")).to_have_text("0")
    await fits(page, "interactive site")


async def add_sample_sites(agent):
    # Both sample sites are created through the public MCP client contract.
    call, rid = browser_smoke.call, browser_smoke.rid
    for title, slug, publish in [("Field handbook", "field-handbook", True), ("Onboarding notes", "onboarding-notes", False)]:
        site = await call(agent, "create_site", name=title, slug=slug, request_id=rid())
        snapshot = await call(agent, "upload_files", site_id=slug, expected_revision=site["revision"],
                              files=[{"path": "index.html", "text": f"<h1>{title}</h1>"}], request_id=rid())
        if publish:
            await call(agent, "publish_site", site_id=slug, revision=snapshot["revision"], request_id=rid())


async def release_counter(page, other, control, http):
    await try_counter(page, await page.get_by_role("link", name="Preview draft").get_attribute("href"))
    await page.goto(control + "/sites/workshop-tally")
    await page.get_by_role("button", name="Publish draft").click()
    stable = await page.get_by_role("link", name="Open live site").get_attribute("href")
    http.request("GET", stable).expect(303, "anonymous reader of private site")
    await try_counter(page, stable)
    await page.goto(control + "/sites/workshop-tally")
    await page.get_by_label("Site audience").select_option("public")
    await page.get_by_role("button", name="Update access").click()
    await expect(page.get_by_role("heading", name="Review public release")).to_be_visible()
    http.request("GET", stable).expect(303, "site before approval")
    await page.get_by_role("button", name="Approve public release").click()
    await expect(page.get_by_role("heading", name="Approval recorded")).to_be_visible()
    http.request("GET", stable).expect(200, "anonymous reader of public site")
    await try_counter(other, stable)
    return stable


async def revoke_pending(page, pending_review, control, endpoint, secret, agent, http):
    call, rid = browser_smoke.call, browser_smoke.rid
    key = {"site_id": "workshop-tally"}
    first = await call(agent, "get_site", **key)
    head = await call(agent, "upload_files", **key, expected_revision=first["revision"],
                      files=[{"path": "agenda.html", "text": "<h1>Next workshop</h1>"}], request_id=rid())
    addition = b"p{font-size:1rem}"
    upload = await call(agent, "prepare_upload", **key, expected_revision=head["revision"], path="extra.css",
                        size=len(addition), sha256=hashlib.sha256(addition).hexdigest(), request_id=rid())
    public = await call(agent, "publish_site", "approval_required", **key, revision=head["revision"], request_id=rid())
    await pending_review.goto(public["approval_url"])
    await expect(pending_review.get_by_role("button", name="Approve public release")).to_be_visible()

    await page.goto(control + "/admin")
    await fits(page, "admin members")
    await expect(page.get_by_text("Alex Morgan", exact=True)).to_be_visible()
    await page.get_by_role("link", name="Workspace keys").click()
    await fits(page, "admin keys")
    await page.get_by_text("Revoke Workshop publisher", exact=True).click()
    await page.get_by_label("Disconnect agents using this key.").check()
    await page.get_by_role("button", name="Revoke key").click()
    await expect(page.get_by_text("Revoked", exact=True)).to_be_visible()
    await page.get_by_role("link", name="Activity").click()
    await expect(page.get_by_text("Agent key revoked", exact=True)).to_be_visible()
    await fits(page, "admin activity")

    http.request("POST", endpoint, b"{}", {"Authorization": "Bearer " + secret, "Content-Type": "application/json"}).expect(401, "revoked credential")
    http.request("PUT", upload["upload_url"], addition).expect(403, "revoked exact-byte ticket")
    async with pending_review.expect_response(lambda r: r.request.method == "POST") as submit:
        await pending_review.get_by_role("button", name="Approve public release").click()
    check((await submit.value).status == 403, "revoked approval still accepted")
    # Missing content is indistinguishable from private content to a guest.
    # The signed-in owner's site cookie confirms the unpublished page is absent.
    missing = await page.context.request.get(first["site_url"] + "agenda.html", max_redirects=0)
    check(missing.status == 404, "revoked approval changed the live site")
    await page.goto(control + "/sites/workshop-tally")
    check(await page.get_by_text("extra.css", exact=True).count() == 0, "revoked ticket changed draft")


async def admin_image(page, directory, name):
    if directory is None:
        return
    # Only called on the disposable tenant fixture. Never capture key receipts
    # or sign-in callbacks. Restore the mobile viewport for the acceptance test.
    previous = page.viewport_size
    try:
        await page.set_viewport_size({"width": 1200, "height": 840})
        await page.evaluate("document.fonts.ready")
        nav = page.get_by_role("navigation", name="Administration views")
        await nav.evaluate("""el => { document.activeElement.blur(); window.scrollTo({
            top: window.scrollY + el.getBoundingClientRect().top - 24, behavior: 'instant'
        }); }""")
        await page.evaluate("new Promise(done => requestAnimationFrame(() => requestAnimationFrame(done)))")
        directory.mkdir(parents=True, exist_ok=True)
        await page.screenshot(path=directory / f"{name}.png", animations="disabled")
    finally:
        await page.set_viewport_size(previous)


async def manage_workspace_access(page, control, media=None):
    await page.goto(control + "/admin")
    nav = page.get_by_role("navigation", name="Administration views")
    subject = "workshop-editor"
    await page.get_by_text("Add a member", exact=True).click()
    await page.get_by_label("Sign-in ID", exact=True).fill(subject)
    await page.get_by_label("Display name", exact=True).fill("Sam Editor")
    await fits(page, "provision member")
    await page.get_by_role("button", name="Add member").click()
    await expect(page.get_by_text("Sam Editor", exact=True)).to_be_visible()

    await nav.get_by_role("link", name="Groups").click()
    await page.get_by_label("Group name", exact=True).fill("Authors")
    await page.get_by_label("Group role", exact=True).select_option("editor")
    await fits(page, "create group")
    await page.get_by_role("button", name="Create group").click()
    await expect(page.get_by_role("heading", name="Authors")).to_be_visible()
    await page.get_by_label("Member sign-in ID").fill(subject)
    await page.get_by_role("button", name="Add to group").click()
    await expect(page.get_by_role("button", name=f"Remove {subject} from group")).to_be_visible()
    await page.get_by_text("Edit group", exact=True).click()
    await page.get_by_label("Current group name").fill("Releases")
    await page.get_by_label("Current group role").select_option("publisher")
    await fits(page, "edit populated group")
    await page.get_by_role("button", name="Save group").click()
    await expect(page.get_by_role("heading", name="Releases")).to_be_visible()

    await nav.get_by_role("link", name="Workspace settings").click()
    await page.get_by_label("Access mode").select_option("managed")
    await fits(page, "workspace access settings")
    await page.get_by_role("button", name="Save workspace settings").click()
    await expect(page.get_by_label("Access mode")).to_have_value("managed")
    await admin_image(page, media, "access-mode")
    await nav.get_by_role("link", name="Groups").click()
    await page.get_by_role("link", name="Releases").click()
    await admin_image(page, media, "groups")
    await page.get_by_role("button", name=f"Remove {subject} from group").click()
    await expect(page.get_by_text("This group has no members.")).to_be_visible()
    await page.get_by_text("Delete group", exact=True).first.click()
    await page.get_by_label("Remove this group and all its grants.").check()
    await page.get_by_role("button", name="Delete group").click()
    await expect(page.get_by_text("No groups yet.")).to_be_visible()
    await nav.get_by_role("link", name="Workspace settings").click()
    await page.get_by_label("Access mode").select_option("identity")
    await page.get_by_role("button", name="Save workspace settings").click()
    await expect(page.get_by_label("Access mode")).to_have_value("identity")

    await nav.get_by_role("link", name="Activity").click()
    await expect(page.get_by_role("heading", name="Audit log")).to_be_visible()
    await expect(page.get_by_text("Group deleted", exact=True)).to_be_visible()
    await fits(page, "tenant audit log")
    await admin_image(page, media, "audit-log")
    export = await page.get_by_role("link", name="Open JSON export").get_attribute("href")
    response = await page.context.request.get(control + export)
    check(response.status == 200, "audit export unavailable")
    report = await response.json()
    events = report["events"]
    steps = [e["action"] for e in events]
    expected = ["member_provisioned", "group_created", "group_member_added", "group_updated",
                "workspace_access_changed", "group_member_removed", "group_deleted", "workspace_access_changed"]
    check([step for step in steps if step in expected] == expected, "missing or unordered tenant audit records")
    check(report["next_after"] == events[-1]["sequence"], "audit export checkpoint differs from last event")
    tail = await page.context.request.get(control + export, params={"after": str(report["next_after"])})
    check(tail.status == 200 and (await tail.json())["events"] == [], "audit export repeated checkpointed events")


async def filter_directory(page):
    async def open_filters():
        toggle = page.get_by_role("button", name="Filter and sort")
        if await toggle.is_visible() and await toggle.get_attribute("aria-expanded") != "true":
            await toggle.focus()
            await page.keyboard.press("Enter")
            await expect(toggle).to_have_attribute("aria-expanded", "true")

    await fits(page, "site directory")
    table = page.get_by_role("table", name="Site directory")
    rows = table.get_by_role("link")
    await expect(rows).to_have_count(3)
    await page.get_by_label("Search sites").fill("WORKSHOP")
    await open_filters()
    await page.get_by_label("Status", exact=True).select_option("changes")
    await page.get_by_label("Audience", exact=True).select_option("public")
    await page.get_by_role("button", name="Apply filters").click()
    await expect(rows).to_have_text(["Workshop tally"])
    await page.get_by_label("Search sites").fill("no-such-site")
    await page.get_by_label("Search sites").press("Enter")
    await expect(page.get_by_role("heading", name="No matching sites")).to_be_visible()
    await page.get_by_role("link", name="Reset filters").click()
    await expect(rows).to_have_count(3)
    await open_filters()
    await page.get_by_label("Sort by").select_option("name")
    await page.get_by_role("button", name="Apply filters").click()
    await expect(rows).to_have_text(["Field handbook", "Onboarding notes", "Workshop tally"])


async def workspace(binary, channel, screenshot, admin_media):
    async with browser_smoke.local_server(binary) as (endpoint, _, http), async_playwright() as pw:
        control = endpoint.removesuffix("/mcp")
        browser = await pw.chromium.launch(channel=channel)
        try:
            ctx = await browser.new_context(viewport={"width": 320, "height": 780}, device_scale_factor=1)
            page = await ctx.new_page()
            anon = await browser.new_page(viewport={"width": 320, "height": 780})
            await enroll(page, control)
            await manage_workspace_access(page, control, admin_media)
            secret = await create_key(page, endpoint)
            async with browser_smoke.connect(endpoint, secret) as agent:
                await add_sample_sites(agent)
                source = await manual_site(page)
                read = await browser_smoke.call(agent, "get_site", site_id="workshop-tally", path="app.js")
                check(read["file"]["text"] == source, "MCP did not see manual edits")
                await release_counter(page, anon, control, http)
                await revoke_pending(page, await ctx.new_page(), control, endpoint, secret, agent, http)
            await page.get_by_role("link", name="Sites", exact=True).click()
            await filter_directory(page)
            if screenshot:
                await page.set_viewport_size({"width": 1200, "height": 810})
                await page.evaluate("document.activeElement.blur()")
                await page.screenshot(path=screenshot, animations="disabled")
        finally:
            await browser.close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--browser-channel", default="chrome")
    parser.add_argument("--screenshot", type=Path, help="optional desktop PNG of the synthetic workspace")
    parser.add_argument("--admin-media", type=Path, help="optional directory for synthetic group, access settings and audit screenshots")
    args = parser.parse_args()
    asyncio.run(workspace(args.binary.resolve(strict=True), args.browser_channel, args.screenshot, args.admin_media))
    print("PASS: tenant administration, groups, audit export, filtered site directory, enrollment, keys, MCP, ZIP, editor, private and public counter, revocation, axe, keyboard and 320px layouts")
