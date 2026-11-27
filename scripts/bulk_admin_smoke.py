"""Bulk account, group and key workflows on the workspace smoke's disposable server."""

from playwright.async_api import expect


async def check_navigation_labels(page):
    # An accessible name alone passed even when every link was just an icon.
    for landmark, labels in (
        ("Workspace", ["Sites", "Quickstart", "Agent keys", "Admin", "Account"]),
        ("Administration views", ["Members", "Groups", "Quotas", "Workspace settings", "Workspace keys", "Activity"]),
    ):
        nav = page.get_by_role("navigation", name=landmark, exact=True)
        links = nav.get_by_role("link")
        await expect(links).to_have_count(len(labels))
        for name in labels:
            link = nav.get_by_role("link", name=name, exact=True)
            await expect(link).to_have_text(name, use_inner_text=True)
            visible = await link.evaluate("""(a) => {
                const walker = document.createTreeWalker(a, NodeFilter.SHOW_TEXT);
                while (walker.nextNode()) {
                    const text = walker.currentNode;
                    if (!text.textContent.trim()) continue;
                    const range = document.createRange();
                    range.selectNodeContents(text);
                    if (range.getBoundingClientRect().width < 4) continue;
                    let el = text.parentElement;
                    while (el !== a && el.getBoundingClientRect().width > 1) el = el.parentElement;
                    if (el === a) return true;
                }
                return false;
            }""")
            assert visible, f"{landmark}: {name} must have a visible label"
        await expect(nav.locator('a[aria-current="page"]')).to_have_count(1)


async def _check_rows(page, table_name, names, checked=True):
    table = page.get_by_role("table", name=table_name, exact=True)
    for name in names:
        row = table.get_by_role("row").filter(has=page.get_by_role("rowheader").filter(has_text=name))
        await row.get_by_role("checkbox").set_checked(checked)


async def member_selection(page, control):
    names = ["Review member Ada", "Review member Ben"]
    for index, name in enumerate(names):
        await page.goto(control + "/admin")
        await page.get_by_text("Add a member", exact=True).click()
        await page.get_by_label("Sign-in ID", exact=True).fill(f"review-member-{index}")
        await page.get_by_label("Display name", exact=True).fill(name)
        await page.get_by_role("button", name="Add member", exact=True).click()

    await check_navigation_labels(page)
    table = page.get_by_role("table", name="Member directory")
    await _check_rows(page, "Member directory", names)
    await expect(page.get_by_role("button", name="Restore selected")).to_be_disabled()
    await page.get_by_role("button", name="Suspend selected").click()
    for name in names:
        await expect(page.get_by_role("listitem").filter(has_text=name)).to_be_visible()
    await page.get_by_role("button", name="Suspend members", exact=True).click()
    for name in names:
        await expect(table.get_by_role("row").filter(has_text=name)).to_contain_text("Suspended")
    # Restore one via its row to create a mixed selection; neither bulk action fits both.
    await table.get_by_role("button", name="Restore " + names[0], exact=True).click()
    await page.get_by_role("button", name="Restore access", exact=True).click()
    await _check_rows(page, "Member directory", names)
    for action in ("Restore selected", "Suspend selected"):
        await expect(page.get_by_role("button", name=action)).to_be_disabled()
    await _check_rows(page, "Member directory", [names[0]], False)
    await page.get_by_role("button", name="Restore selected").click()
    await page.get_by_role("button", name="Restore access", exact=True).click()
    for name in names:
        await expect(table.get_by_role("row").filter(has_text=name)).to_contain_text("Active")


async def delete_groups_without_javascript(page, control, browser):
    # An explicitly script-free Chrome context must still submit native row checkboxes,
    # show the exact confirmation and handle cancel. Copy only this fixture's session.
    ctx = await browser.new_context(java_script_enabled=False, storage_state=await page.context.storage_state())
    try:
        plain = await ctx.new_page()
        names = ["Delete fixture group A", "Delete fixture group B"]
        keep = "Keep fixture group"
        for name in [*names, keep]:
            await plain.goto(control + "/admin?view=groups")
            field = plain.get_by_label("Group name", exact=True)
            if not await field.is_visible():
                await plain.get_by_text("Create a group", exact=True).click()
            await field.fill(name)
            await plain.get_by_role("button", name="Create group", exact=True).click()
            await expect(plain.get_by_role("heading", name=name)).to_be_visible()
        await plain.goto(control + "/admin?view=groups")
        await check_navigation_labels(plain)
        await expect(plain.get_by_role("checkbox", name="Select all on this page")).to_have_count(0)
        for submit in (False, True):
            await _check_rows(plain, "Group directory", names)
            await plain.get_by_role("button", name="Delete selected", exact=True).click()
            await expect(plain.get_by_role("listitem")).to_have_count(2)
            for name in names:
                await expect(plain.get_by_role("listitem").filter(has_text=name)).to_be_visible()
            if submit:
                await plain.get_by_role("button", name="Delete groups", exact=True).click()
            else:
                await plain.get_by_role("link", name="Cancel", exact=True).click()
            # Cancel and submit both navigate back to a server-rendered directory.
            # Let that navigation finish before checking what the server retained.
            await expect(plain.get_by_role("table", name="Group directory", exact=True)).to_be_visible()
            for name in names:
                await expect(plain.get_by_role("link", name=name, exact=True)).to_have_count(int(not submit))
            await expect(plain.get_by_role("link", name=keep, exact=True)).to_be_visible()
    finally:
        await ctx.close()


async def delete_password_account(admin, member, control, browser, fits):
    # A real password user owns one site; the admin's Private guide and another
    # member must survive both cancel and confirmed deletion.
    owner = "studio-member"
    neighbor = "Keep this member"
    site_name, site_id = "Offboarding " + "A" * 62, "offboarding-guide"
    await member.goto(control + "/sites/new")
    await member.get_by_label("Site name").fill(site_name)
    await member.get_by_label("Site address").fill(site_id)
    await member.get_by_role("button", name="Create site").click()
    await expect(member.get_by_role("heading", name=site_name, exact=True)).to_be_visible()

    await admin.goto(control + "/admin")
    await admin.get_by_text("Add a member", exact=True).click()
    await admin.get_by_label("Sign-in ID", exact=True).fill("offboarding-neighbor")
    await admin.get_by_label("Display name", exact=True).fill(neighbor)
    await admin.get_by_role("button", name="Add member", exact=True).click()
    await admin.set_viewport_size({"width": 320, "height": 780})
    await admin.get_by_role("button", name=f"Delete account {owner}", exact=True).click()

    async def reviewed(page):
        await expect(page.get_by_role("heading", name="Review account deletion", exact=True)).to_be_visible()
        entries = page.get_by_role("listitem")
        await expect(entries).to_have_count(2)
        await expect(entries.filter(has_text=owner)).to_be_visible()
        await expect(entries.filter(has_text=site_name)).to_contain_text(site_id)
        await expect(entries.filter(has_text=neighbor)).to_have_count(0)
        await expect(page.get_by_text("This cannot be undone.", exact=False).first).to_be_visible()
        # Long site names and opaque sign-in IDs must remain legible, even
        # when there's no whitespace at which a mobile browser can wrap.
        assert await entries.evaluate_all("""items => items.every(el => {
            const range = document.createRange();
            range.selectNodeContents(el);
            return [...range.getClientRects()].every(r => r.left >= 0 && r.right <= innerWidth + 1);
        })"""), "account deletion review clips text at 320px"

    await reviewed(admin)
    await fits(admin, "account deletion review at 320px")
    await admin.get_by_role("link", name="Cancel", exact=True).click()
    await expect(admin.get_by_role("link", name=owner, exact=True)).to_be_visible()
    await expect(admin.get_by_role("link", name=neighbor, exact=True)).to_be_visible()
    await member.reload()
    await expect(member.get_by_role("heading", name=site_name, exact=True)).to_be_visible()

    plain = await browser.new_page(java_script_enabled=False, storage_state=await admin.context.storage_state(),
                                   viewport={"width": 320, "height": 780})
    try:
        await plain.goto(control + "/admin")
        await plain.get_by_role("link", name=owner, exact=True).click()
        await plain.get_by_role("button", name="Delete account…", exact=True).click()
        await reviewed(plain)
        await plain.get_by_role("button", name="Permanently delete accounts", exact=True).press("Enter")
        await expect(plain.get_by_role("status").filter(has_text="permanently deleted")).to_be_visible()
        await expect(plain.get_by_role("link", name=owner, exact=True)).to_have_count(0)
        await expect(plain.get_by_role("link", name=neighbor, exact=True)).to_be_visible()
        await plain.goto(control + "/sites")
        await expect(plain.get_by_role("link", name=site_name, exact=True)).to_have_count(0)
        await expect(plain.get_by_role("link", name="Private guide", exact=True)).to_be_visible()
        await member.goto(control + "/account")
        await expect(member.get_by_role("button", name="Sign in with password")).to_be_visible()
    finally:
        await plain.context.close()


async def revoke_keys(page, control):
    names = ["Revoke fixture key A", "Revoke fixture key B"]
    keep = "Keep fixture key"
    for name in [*names, keep]:
        await page.goto(control + "/keys")
        await page.get_by_label("Key name", exact=True).fill(name)
        await page.get_by_role("button", name="Create key", exact=True).click()
        await expect(page.get_by_role("heading", name="Connect your agent")).to_be_visible()
    await page.goto(control + "/admin?view=keys")
    region = page.get_by_role("region", name="Workspace keys", exact=True)
    table = region.get_by_role("table")
    for submit in (False, True):
        await _check_rows(page, "Workspace keys", names)
        await expect(region.get_by_role("status")).to_have_text("2 selected")
        master = region.get_by_role("checkbox", name="Select all on this page")
        await expect(master).to_have_js_property("indeterminate", True)
        await region.get_by_role("button", name="Revoke selected keys").click()
        await expect(page.get_by_role("heading", name="2 selected keys")).to_be_visible()
        for name in names:
            await expect(page.get_by_role("listitem").filter(has_text=name)).to_be_visible()
        if submit:
            await page.get_by_role("button", name="Revoke keys", exact=True).click()
        else:
            await page.get_by_role("link", name="Cancel", exact=True).click()
        await expect(table).to_be_visible()
        for name in names:
            await expect(table.get_by_role("row").filter(has_text=name)).to_contain_text("Revoked" if submit else "Active")
        await expect(table.get_by_role("row").filter(has_text=keep)).to_contain_text("Active")


async def check_bulk_admin(page, control, browser):
    await member_selection(page, control)
    await delete_groups_without_javascript(page, control, browser)
    await revoke_keys(page, control)
