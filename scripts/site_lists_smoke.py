"""Compare the personal and administrative directories on a disposable workspace."""
from playwright.async_api import expect

from smoke import check


async def check_site_lists(admin, member, control):
    group = "Reading circle"
    admin_site, shared_site, member_site = "Admin notes", "Reading list", "Studio notes"
    subject = await member.get_by_role("region", name="Account details").locator("code").inner_text()
    await admin.goto(control + "/admin?view=groups")
    await admin.get_by_label("Group name", exact=True).fill(group)
    await admin.get_by_label("Group role", exact=True).select_option("reader")
    await admin.get_by_role("button", name="Create group").click()
    await admin.get_by_label("Member sign-in ID").fill(subject)
    await admin.get_by_role("button", name="Add to group").click()
    remove_member = admin.get_by_role("button", name=f"Remove {subject} from group")
    await expect(remove_member).to_be_visible()
    group_url = admin.url

    for author, title in [(admin, admin_site), (admin, shared_site), (member, member_site)]:
        await author.goto(control + "/sites/new")
        await author.get_by_label("Site name").fill(title)
        if title == shared_site:
            await author.get_by_label("Budget group").select_option(label=group)
        await author.get_by_role("button", name="Create site").click()
        await expect(author.get_by_role("heading", name=title, exact=True)).to_be_visible()

    async def check_directories(admin_page, member_page):
        for page, titles in [(admin_page, [admin_site, shared_site]), (member_page, [shared_site, member_site])]:
            await page.goto(control + "/sites?sort=name")
            rows = page.get_by_role("rowheader").get_by_role("link")
            await expect(rows).to_have_text(titles)

        await admin_page.goto(control + "/admin")
        nav = admin_page.get_by_role("navigation", name="Administration views")
        await nav.get_by_role("link", name="All sites", exact=True).click()
        rows = admin_page.get_by_role("rowheader").get_by_role("link")
        await expect(rows).to_have_count(3)
        for title in (admin_site, shared_site, member_site):
            await expect(admin_page.get_by_role("rowheader").get_by_role("link", name=title, exact=True)).to_be_visible()
        search = admin_page.get_by_label("Search sites", exact=True)
        await search.fill(member_site)
        await search.press("Enter")
        await expect(rows).to_have_text([member_site])
        await expect(nav.get_by_role("link", name="All sites", exact=True)).to_have_attribute("aria-current", "page")

        forbidden = await member_page.goto(control + "/admin?view=sites")
        check(forbidden.status == 403, "member opened the administrative site directory")

    await check_directories(admin, member)
    native_admin = await admin.context.browser.new_context(java_script_enabled=False, storage_state=await admin.context.storage_state())
    native_member = await member.context.browser.new_context(java_script_enabled=False, storage_state=await member.context.storage_state())
    try:
        await check_directories(await native_admin.new_page(), await native_member.new_page())
    finally:
        await native_member.close()
        await native_admin.close()

    # The existing member session must immediately lose the group entry.
    await admin.goto(group_url)
    await remove_member.click()
    await admin.get_by_role("button", name="Remove from group", exact=True).click()
    await expect(admin.get_by_text("This group has no members.")).to_be_visible()
    await member.goto(control + "/sites")
    await expect(member.get_by_role("rowheader").get_by_role("link")).to_have_text([member_site])
    return member_site
