"""Check the control UI's appearance in the real browser."""

import re

from playwright.async_api import expect

from native_handoff_smoke import native_page
from smoke import check


def picker(page):
    return page.get_by_role("combobox", name="Appearance", exact=True)


async def expect_scheme(page, scheme, override=None):
    root = page.locator("html")
    await expect(root).to_have_css("color-scheme", scheme)
    if override:
        await expect(root).to_have_attribute("data-theme", override)
    else:
        await expect(root).not_to_have_attribute("data-theme", re.compile(".*"))


async def check_appearance(page, origin, browser, fits):
    await page.emulate_media(color_scheme="light")
    await page.goto(origin + "/keys")
    await expect(picker(page)).to_have_value("system")
    await expect_scheme(page, "light")
    body = page.locator("body")
    light_background = await body.evaluate("el => getComputedStyle(el).backgroundColor")

    await page.emulate_media(color_scheme="dark")
    await expect_scheme(page, "dark")
    await expect(body).not_to_have_css("background-color", light_background)
    await fits(page, "dark agent keys")

    choice = picker(page)
    for _ in range(12):
        await page.keyboard.press("Tab")
        if await choice.evaluate("el => el.matches(':focus-visible')"):
            break
    await expect(choice).to_be_focused()
    check(await choice.evaluate("""el => {
      const style = getComputedStyle(el);
      return style.outlineStyle !== 'none' && parseFloat(style.outlineWidth) > 0;
    }"""), "the appearance picker has no visible keyboard focus indicator")
    # Native select type-ahead activates the explicit Light choice on a dark OS.
    await page.keyboard.press("l")
    await expect(choice).to_have_value("light")
    await expect_scheme(page, "light", "light")

    other = await page.context.new_page()
    try:
        await other.emulate_media(color_scheme="dark")
        await other.goto(origin + "/admin")
        await expect(picker(other)).to_have_value("light")
        await expect_scheme(other, "light", "light")

        await page.emulate_media(color_scheme="light")
        await choice.select_option("dark")
        await expect_scheme(page, "dark", "dark")
        await expect(picker(other)).to_have_value("dark")
        await expect_scheme(other, "dark", "dark")
        await other.set_viewport_size({"width": 1200, "height": 780})
        await fits(other, "dark workspace administration")

        # The same-origin startup script must apply the saved choice even
        # while the main bundle is blocked and the native form is visible.
        async with native_page(page, origin + "/quickstart?setup=manual"):
            await expect(page.get_by_label("What can this agent do?")).to_be_visible()
            await expect_scheme(page, "dark", "dark")
        await expect(choice).to_have_value("dark")
        await page.reload()
        await expect(choice).to_have_value("dark")
        await expect_scheme(page, "dark", "dark")

        await choice.select_option("system")
        await expect_scheme(page, "light")
        await expect(picker(other)).to_have_value("system")
        await expect_scheme(other, "dark")
        await page.reload()
        await expect(choice).to_have_value("system")
        await expect_scheme(page, "light")
    finally:
        await other.close()

    native = await browser.new_context(
        color_scheme="dark", java_script_enabled=False,
        storage_state=await page.context.storage_state(),
        viewport={"width": 320, "height": 780},
    )
    try:
        narrow = await native.new_page()
        await narrow.goto(origin + "/keys")
        await expect(narrow.locator("#native-page")).to_be_visible()
        await expect(narrow.get_by_label("Key name")).to_be_visible()
        await expect_scheme(narrow, "dark")
        await expect(narrow.locator("body")).not_to_have_css("background-color", light_background)
        await narrow.emulate_media(color_scheme="light")
        await expect_scheme(narrow, "light")
    finally:
        await native.close()

    print("PASS: appearance follows System; keyboard, manual overrides, early native render, storage across tabs and reload, dark axe and script-free colors.")
