"""Exercise destructive site actions on the workspace smoke's disposable server."""
import csv

from playwright.async_api import expect
from smoke import check


async def check_bulk_sites(page, control, browser, http):
    async def create(slug):
        await page.goto(control + "/sites/new")
        await page.get_by_label("Site name").fill(slug)
        await page.get_by_label("Site address").fill(slug)
        await page.get_by_role("button", name="Create site").click()
        await expect(page.get_by_role("heading", name=slug, exact=True)).to_be_visible()

    drafts = ["cleanup-draft-a", "cleanup-draft-b"]
    offline, released = "cleanup-offline-site", "cleanup-live-site"
    for slug in [*drafts, offline]:
        await create(slug)
    await page.get_by_role("link", name="New file").click()
    await page.get_by_label("File path").fill("index.html")
    await page.get_by_label("File contents").fill("<h1>Offline fixture</h1>")
    await page.get_by_role("button", name="Save file").click()
    await page.get_by_role("button", name="Publish draft").click()
    await page.get_by_label("Site audience").select_option("offline")
    await page.get_by_role("button", name="Update access").click()
    await expect(page.get_by_role("link", name="Open live site")).to_have_count(0)
    await create(released)
    for path, text in [("index.html", "<p>Keep</p>"), ("a.css", "p{}"), ("b.js", "const kept = true;")]:
        await page.get_by_role("link", name="New file").click()
        await page.get_by_label("File path").fill(path)
        await page.get_by_label("File contents").fill(text)
        await page.get_by_role("button", name="Save file").click()
    await page.get_by_role("button", name="Publish draft").click()
    # Check the JS-enabled browser exercised React rather than accidentally
    # running the native fallback in both contexts.
    await expect(page.locator("#app").get_by_role("heading", name=released, exact=True)).to_be_visible()
    files = page.get_by_role("region", name="Draft files")
    releases = page.get_by_role("region", name="Releases")
    await files.get_by_role("checkbox", name="Select file a.css").check()
    await files.get_by_role("checkbox", name="Select file b.js").check()
    await expect(files.get_by_role("status")).to_have_text("2 selected")
    await expect(releases.get_by_role("button", name="Download selected (CSV)")).to_be_disabled()
    await files.get_by_role("button", name="Remove selected files…").click()
    await expect(page.get_by_role("listitem")).to_have_text(["a.css", "b.js"])
    await page.get_by_role("button", name="Remove files", exact=True).click()
    await expect(files.get_by_role("rowheader")).to_have_count(1)
    await expect(files.get_by_role("rowheader")).to_have_text("index.html")
    await page.get_by_role("button", name="Publish draft").click()
    await expect(releases.get_by_role("rowheader")).to_have_count(2)
    previous = releases.get_by_role("row").filter(has_text="Previous")
    await previous.get_by_role("checkbox").check()
    revision = await previous.get_by_role("rowheader").inner_text()
    async with page.expect_download() as download:
        await releases.get_by_role("button", name="Download selected (CSV)").click()
    with (await (await download.value).path()).open(encoding="utf-8-sig", newline="") as stream:
        rows = list(csv.reader(stream))
    check(len(rows) == 2 and rows[0] == ["Revision", "Released", "Status"] and rows[1][0] == revision.strip()
          and rows[1][-1] == "Previous", "exported releases did not match the selected record")

    # The selected release actually serves a public page before deletion.
    stable = await page.get_by_role("link", name="Open live site").get_attribute("href")
    # Establish a real host-scoped reader session while the live release is private.
    reader = await page.context.new_page()
    await reader.goto(stable)
    await expect(reader.get_by_text("Keep", exact=True)).to_be_visible()
    await page.get_by_label("Site audience").select_option("public")
    await page.get_by_role("button", name="Update access").click()
    await page.get_by_role("button", name="Approve public release").click()
    await expect(page.get_by_role("heading", name="Approval recorded")).to_be_visible()
    http.request("GET", stable).expect(200, "public site before deletion")

    await page.goto(control + "/sites")
    directory = page.get_by_role("table", name="Site directory")
    delete = directory.get_by_role("button", name=f"Delete site {released} ({released})", exact=True)
    await expect(delete.locator("svg")).to_be_visible()
    await delete.click()
    await expect(page.get_by_role("listitem")).to_have_count(1)
    await expect(page.get_by_role("listitem")).to_contain_text(released)
    await page.get_by_role("link", name="Cancel", exact=True).click()
    await expect(directory).to_be_visible()
    http.request("GET", stable).expect(200, "cancel kept the public release online")

    # Native controls can delete mixed publication states as one selection.
    ctx = await browser.new_context(java_script_enabled=False, storage_state=await page.context.storage_state())
    try:
        plain = await ctx.new_page()
        await plain.goto(control + "/sites")
        table = plain.get_by_role("table", name="Site directory")
        selected = [drafts[0], offline]
        for slug in selected:
            await table.get_by_role("checkbox", name=f"Select site {slug} ({slug})", exact=True).check()
        remove = plain.get_by_role("button", name="Delete selected sites…", exact=True)
        await expect(remove.locator("svg")).to_be_visible()
        await remove.click()
        await expect(plain.get_by_role("listitem")).to_have_count(2)
        for slug in selected:
            await expect(plain.get_by_role("listitem").filter(has_text=slug)).to_be_visible()
        await plain.get_by_role("button", name="Delete sites", exact=True).click()
        await expect(table).to_be_visible()
        for slug in selected:
            await expect(table.get_by_role("link", name=slug, exact=True)).to_have_count(0)
        await expect(table.get_by_role("link", name=drafts[1], exact=True)).to_be_visible()
        await table.get_by_role("link", name=released, exact=True).click()
        history = plain.get_by_role("region", name="Releases")
        await expect(history.get_by_role("rowheader")).to_have_count(2)
        await expect(history.get_by_role("checkbox")).to_have_count(0)
    finally:
        await ctx.close()

    # Delete a live public site from its detail screen and verify delivery stops.
    await page.goto(control + "/sites/" + released)
    await page.get_by_role("button", name="Delete site", exact=True).click()
    await expect(page.get_by_role("listitem")).to_contain_text(released)
    await page.get_by_role("button", name="Delete site", exact=True).click()
    await expect(directory).to_be_visible()
    await expect(directory.get_by_role("link", name=released, exact=True)).to_have_count(0)
    await expect(directory.get_by_role("link", name=drafts[1], exact=True)).to_be_visible()
    for method in ("GET", "HEAD"):
        # Anonymous requests for missing and private sites share the sign-in redirect.
        blocked = http.request(method, stable)
        blocked.expect(303, "anonymous reader of deleted site")
        check("etag" not in blocked.headers and b"<p>Keep</p>" not in blocked.body,
              "deleted public site still returned content or its cache validator")
    response = await reader.goto(stable)
    check(response and response.status == 404, "existing reader session still reached deleted site")
    await reader.close()
