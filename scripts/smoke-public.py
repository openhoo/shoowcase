# /// script
# requires-python = ">=3.11"
# dependencies = ["playwright==1.65.2"]
# ///
"""Verify the actual public control UI in Chromium, with and without JavaScript.

The live check only opens the public homepage and password form, in fresh browser
contexts. It blocks external requests and any HTTP method other than GET/HEAD.
"""
import argparse
import asyncio
import contextlib
import os
from pathlib import Path
import socket
import subprocess
import tempfile
from urllib.parse import urlsplit
from urllib.request import urlopen

from playwright.async_api import async_playwright, expect


@contextlib.asynccontextmanager
async def start_local(binary):
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    with tempfile.TemporaryDirectory(prefix="shoowcase-public-") as directory:
        env = {k: v for k, v in os.environ.items() if not k.startswith(("SHOOWCASE_", "OTEL_"))}
        env.update(SHOOWCASE_LISTEN=f"127.0.0.1:{port}", SHOOWCASE_STATE_DIR=directory,
                   SHOOWCASE_LOCAL_WORKSPACE="local", SHOOWCASE_PUBLIC_REGISTRATION="true", SHOOWCASE_PUBLIC="true")
        with subprocess.Popen([binary, "-dev"], cwd=directory, env=env, stdout=subprocess.DEVNULL,
                              stderr=subprocess.DEVNULL) as server:
            origin = f"http://localhost:{port}"
            try:
                for _ in range(150):
                    try:
                        with urlopen(origin + "/readyz", timeout=1) as resp:
                            if resp.status == 200:
                                break
                    except OSError:
                        pass
                    if server.poll() is not None:
                        raise RuntimeError("Disposable server stopped during startup")
                    await asyncio.sleep(.1)
                else:
                    raise RuntimeError("Disposable server did not start")
                yield origin
            finally:
                server.terminate()
                server.wait(timeout=12)


async def check_keyboard_handoff(browser, origin):
    # Gate the actual network response and React's first scheduled work separately.
    # No sleeps: Tab goes to the browser before the bundle/first commit can help it.
    for phase, path in (("download", "/"), ("render", "/"), ("download", "/auth/register")):
        ctx = await browser.new_context()
        page = await ctx.new_page()
        entered, release = asyncio.Event(), asyncio.Event()
        async def hold(route):
            entered.set()
            await release.wait()
            await route.continue_()
        if phase == "download":
            await page.route("**/console.js", hold)
        else:
            await page.add_init_script("""{
              const post = MessagePort.prototype.postMessage;
              const pending = [];
              MessagePort.prototype.postMessage = function(...args) {
                pending.push(() => post.apply(this, args));
              };
              window.resumeRender = () => {
                MessagePort.prototype.postMessage = post;
                pending.splice(0).forEach(fn => fn());
              };
            }""")
        try:
            await page.goto(origin + path, wait_until="commit" if phase == "download" else "load")
            if phase == "download":
                await asyncio.wait_for(entered.wait(), timeout=5)
            await page.locator("body").wait_for(state="attached")
            if path == "/":
                await page.keyboard.press("Tab")
                await expect(page.get_by_role("link", name="Skip to content")).to_be_focused(timeout=1500)
            else:
                username = page.get_by_label("Username", exact=True)
                await username.click(timeout=1500)
                await page.keyboard.type("early-editor")
                await page.keyboard.press("ArrowLeft")
            if phase == "download":
                release.set()
                await page.wait_for_load_state("load")
            else:
                await page.evaluate("window.resumeRender()")
            if path == "/":
                await expect(page.locator("#app > :not(#native-page) main")).to_be_visible()
                await expect(page.get_by_role("link", name="Skip to content")).to_be_focused()
                await page.keyboard.press("Enter")
                await expect(page.get_by_role("main")).to_be_focused()
            else:
                await expect(username).to_be_focused()
                await page.keyboard.type("X")
                await expect(username).to_have_value("early-editoXr")
        finally:
            release.set()
            await ctx.close()
    print("PASS: first Tab during bundle download and first render, skip target, native input value, focus and caret.")


async def check_browser(browser, origin, live, screenshots=None):
    for js in (True, False):
        ctx = await browser.new_context(java_script_enabled=js, service_workers="block")
        errors = []
        unexpected = set()
        async def local_reads_only(route):
            request = route.request
            if request.method not in ("GET", "HEAD") or urlsplit(request.url).netloc != urlsplit(origin).netloc:
                # Do not retain query strings, cookies, or signed URLs.
                unexpected.add((request.method, urlsplit(request.url).hostname))
                await route.abort()
            else:
                await route.continue_()
        await ctx.route("**/*", local_reads_only)
        page = await ctx.new_page()
        page.on("pageerror", lambda _error: errors.append("page error"))
        try:
            pages = [("/", "A home for your team’s sites.", 200),
                     ("/auth/login?next=/account", "Sign in", 200)]
            if not live:
                pages.extend([("/auth/register", "Create an account", 200),
                              ("/upload?t=expired-test-value", "Unable to upload this file", 400),
                              ("/approve?t=expired-test-value", "Unable to review this release", 400)])
            for width in (320, 1200):
                await page.set_viewport_size({"width":width,"height":800})
                for path, heading, status in pages:
                    response = await page.goto(origin + path)
                    assert response and response.status == status, "Unexpected status for public screen"
                    h = page.get_by_role("heading", name=heading, exact=True)
                    await expect(h).to_be_visible()
                    if js:
                        # A native fallback with the correct heading must not pass the React gate.
                        await expect(page.locator("#app > :not(#native-page) main h1")).to_have_text(heading)
                    if path.startswith("/auth/"):
                        await expect(page.get_by_label("Username", exact=True)).to_be_visible()
                        await expect(page.get_by_label("Password", exact=True)).to_have_attribute("type", "password")
                        button = page.get_by_role("button", name="Create account" if "register" in path else "Login", exact=True)
                        if "register" not in path:
                            await expect(button).to_have_text("Login", use_inner_text=True)
                        form = button.locator("xpath=ancestor::form")
                        await expect(form).to_have_attribute("method","post")
                        await expect(form).to_have_attribute("action","/auth/register" if "register" in path else "/auth/local")
                        assert await form.locator("input[name=flow]").input_value(), "Missing signed form flow"
                        if live:
                            await expect(page.get_by_role("link",name="Create a local account")).to_have_count(0)
                    if path == "/":
                        for region in ("banner", "main"):
                            login = page.get_by_role(region).get_by_role("link", name="Login", exact=True)
                            await expect(login).to_have_text("Login", use_inner_text=True)
                            await expect(login).to_have_attribute("href", "/auth/login?next=/sites")
                        await page.keyboard.press("Tab")
                        await expect(page.get_by_role("link",name="Skip to content")).to_be_focused()
                        await page.keyboard.press("Enter")
                        await expect(page.get_by_role("main")).to_be_focused()
                    assert await page.evaluate("document.documentElement.scrollWidth <= window.innerWidth"), "Public screen overflows its viewport"
                    if screenshots and not live and js and path in ("/", "/auth/login?next=/account"):
                        name = "landing" if path == "/" else "login"
                        await page.screenshot(path=screenshots / f"{name}-{width}.png",full_page=True,animations="disabled")
            assert not errors and not unexpected, "Public UI had a client error or requested an external resource or write"
            print(f"PASS: {'React mount' if js else 'native HTML'} at 320/1200 px; {len(pages)} public pages, signed form, keyboard, no external requests or writes.",flush=True)
        finally:
            await ctx.close()


async def main(args):
    async with async_playwright() as pw:
        browser = await pw.chromium.launch(channel=args.browser_channel)
        try:
            if args.live:
                await check_browser(browser, "https://shoowcase.openhoo.dev", True)
            else:
                if args.screenshots:
                    args.screenshots.mkdir(parents=True, exist_ok=True)
                async with start_local(args.binary.resolve(strict=True)) as origin:
                    await check_keyboard_handoff(browser, origin)
                    await check_browser(browser, origin, False, args.screenshots)
        finally:
            await browser.close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--binary", type=Path, help="Shoowcase executable for a disposable local fixture")
    mode.add_argument("--live", action="store_true", help="Read-only test on the public Apps control host")
    parser.add_argument("--browser-channel", default="chrome")
    parser.add_argument("--screenshots", type=Path, help="Optional local fixture screenshot directory")
    asyncio.run(main(parser.parse_args()))
