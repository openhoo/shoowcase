"""Keep edits to the native console when its JavaScript arrives late."""
import asyncio
from contextlib import asynccontextmanager
from urllib.parse import parse_qs

from playwright.async_api import expect

from smoke import check


@asynccontextmanager
async def native_page(page, url):
    requested, release = asyncio.Event(), asyncio.Event()

    async def hold(route):
        requested.set()
        await release.wait()
        await route.continue_()

    await page.route("**/console.js", hold)
    try:
        await page.goto(url, wait_until="commit")
        await asyncio.wait_for(requested.wait(), timeout=10)
        await expect(page.locator("#native-page")).to_be_visible()
        yield
    finally:
        release.set()
        await page.wait_for_load_state("load")
        await page.unroute("**/console.js", hold)


async def leave_native_form(page):
    # A click on noninteractive page furniture takes focus out of the native UI.
    await page.locator("footer").click()
    check(await page.evaluate("document.activeElement === document.body"),
          "footer click did not leave the native form")


async def save_workspace_mode(page, control, before, after):
    url = control + "/admin?view=settings"
    field = page.get_by_label("Access mode")
    async with native_page(page, url):
        await expect(field).to_have_value(before)
        await page.get_by_label("Workspace display name").click()
        await page.keyboard.press("Tab")
        await expect(field).to_be_focused()
        # Native select type-ahead: "i" for Identity, "m" for Managed.
        await page.keyboard.press(after[0])
        await expect(field).to_have_value(after)
        await leave_native_form(page)
    async with page.expect_response(lambda r: r.url == control + "/admin" and r.request.method == "POST") as response:
        await page.get_by_role("button", name="Save workspace settings").click()
    post = await response.value
    # Inspect only the mode; the other form fields include a CSRF token.
    check(parse_qs(post.request.post_data or "").get("mode") == [after],
          f"workspace settings submitted the wrong access mode; expected {after}")
    check(post.status == 303, "workspace settings did not redirect after saving")
    await page.goto(url)
    await expect(field).to_have_value(after)


async def check_quickstart_handoff(page, control):
    # Exercise each kind of edit alone so one control cannot mask another.
    for kind in ("checkbox", "radio", "select"):
        async with native_page(page, control + "/quickstart?setup=manual"):
            if kind == "checkbox":
                field = page.get_by_role("checkbox", name="Use a company proxy")
                await expect(field).not_to_be_checked()
                await field.check()
            elif kind == "radio":
                value = await page.locator("input[type=radio]:not(:checked)").first.input_value()
                field = page.locator(f'input[type=radio][value="{value}"]')
                await field.check()
            else:
                field = page.get_by_label("Where will you run the command?")
                value = "windows" if await field.input_value() == "unix" else "unix"
                # Programmatic select_option fires input/change without focusing the control.
                await field.select_option(value)
            await leave_native_form(page)
        if kind == "select":
            await expect(field).to_have_value(value)
        else:
            await expect(field).to_be_checked()
