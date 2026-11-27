"""Exercise destructive site actions on the workspace smoke's disposable server."""
import csv

from playwright.async_api import expect
from smoke import check


async def check_bulk_sites(page, control, browser):
    async def create(slug):
        await page.goto(control + "/sites/new")
        await page.get_by_label("Site name").fill(slug)
        await page.get_by_label("Site address").fill(slug)
        await page.get_by_role("button", name="Create site").click()
        await expect(page.get_by_role("heading", name=slug, exact=True)).to_be_visible()

    drafts = ["cleanup-draft-a", "cleanup-draft-b"]
    released = "keep-released-site"
    for slug in [*drafts, released]:
        await create(slug)
    for path, text in [("index.html", "<p>Keep</p>"), ("a.css", "p{}"), ("b.js", "const kept = true;")]:
        await page.get_by_role("link", name="New file").click()
        await page.get_by_label("File path").fill(path)
        await page.get_by_label("File contents").fill(text)
        await page.get_by_role("button", name="Save file").click()
    await page.get_by_role("button", name="Publish draft").click()
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

    await page.goto(control + "/sites")
    directory = page.get_by_role("table", name="Site directory")
    await expect(directory.get_by_role("row").filter(has_text=released).get_by_role("checkbox")).to_have_count(0)
    await directory.get_by_role("button", name=f"Delete draft {drafts[0]} ({drafts[0]})", exact=True).click()
    await expect(page.get_by_role("listitem")).to_have_text([f"{drafts[0]} ({drafts[0]})"])
    await page.get_by_role("link", name="Cancel", exact=True).click()
    # Native checkboxes and explicit confirmation remain usable without JavaScript.
    ctx = await browser.new_context(java_script_enabled=False, storage_state=await page.context.storage_state())
    try:
        plain = await ctx.new_page()
        await plain.goto(control + "/sites")
        table = plain.get_by_role("table", name="Site directory")
        for slug in drafts:
            await table.get_by_role("checkbox", name=f"Select draft {slug} ({slug})").check()
        await plain.get_by_role("button", name="Delete selected drafts…").click()
        for slug in drafts:
            await expect(plain.get_by_role("listitem").filter(has_text=slug)).to_be_visible()
        await plain.get_by_role("button", name="Delete draft sites", exact=True).click()
        for slug in drafts:
            await expect(table.get_by_role("link", name=slug, exact=True)).to_have_count(0)
        await table.get_by_role("link", name=released, exact=True).click()
        history = plain.get_by_role("region", name="Releases")
        await expect(history.get_by_role("rowheader")).to_have_count(2)
        await expect(history.get_by_role("checkbox")).to_have_count(0)
        await expect(history.get_by_role("button", name="Download selected (CSV)")).to_have_count(0)
    finally:
        await ctx.close()
