#!/usr/bin/env python3
# /// script
# requires-python = ">=3.12"
# dependencies = ["httpx==0.28.1", "mcp==1.30.0", "playwright==1.65.2"]
# ///
"""Connect four agents from a real Chrome session and test their registered MCP bridges.

    uv run scripts/smoke-quickstart.py --binary ./bin/shoowcase

Uses a disposable development account and client homes. By default the terminal
uses the candidate binary (no release download); --published-installer runs the
actual shell command and downloads the installer’s pinned GitHub release.
"""
import argparse
import asyncio
from importlib import import_module
import json
import os
from pathlib import Path
import shlex
import tempfile
import tomllib

import httpx
from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client
from playwright.async_api import async_playwright, expect

from smoke import check

fixture = import_module("smoke-browser")
NAMES = {"codex": "Codex", "claude": "Claude Code", "copilot": "GitHub Copilot CLI", "cursor": "Cursor"}


def profile(home):
    # Include PATH for native clients. Never inherit live keys, homes or providers.
    (home / ".codex").mkdir(parents=True)
    return {"HOME": str(home), "PATH": os.environ["PATH"], "CODEX_HOME": str(home / ".codex"),
            "COPILOT_HOME": str(home / ".copilot"), "CLAUDE_CONFIG_DIR": str(home / ".claude"),
            "XDG_CONFIG_HOME": str(home / ".config"), "XDG_CACHE_HOME": str(home / ".cache"),
            "XDG_DATA_HOME": str(home / ".local/share"), "NO_COLOR": "1", "CI": "1",
            "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1", "DISABLE_AUTOUPDATER": "1"}


async def run(args, env, home, *, stdin=None):
    p = await asyncio.create_subprocess_exec(*args, cwd=home, env=env,
            stdin=asyncio.subprocess.PIPE if stdin is not None else asyncio.subprocess.DEVNULL,
            stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.STDOUT)
    try:
        output, _ = await asyncio.wait_for(p.communicate(stdin), 190)
    finally:
        if p.returncode is None:
            p.kill()
            await p.wait()
    # Do not leak command arguments or output on failure: they may contain a key.
    check(p.returncode == 0, f"{Path(args[0]).name}: exit {p.returncode}")
    return output


async def register(page, control, endpoint, client, env, home, binary, published):
    await page.goto(control + "/quickstart")
    await page.get_by_role("radio", name=NAMES[client], exact=True).check()
    await page.get_by_label("Where will you run the command?").select_option("unix")
    await page.get_by_label("Key lifetime").select_option("7")
    await page.get_by_role("button", name="Create key and get command").click()
    await expect(page.get_by_role("heading", name="Connect " + NAMES[client], exact=True)).to_be_visible()
    check(await page.get_by_label("First prompt").input_value() != "", "no first prompt")
    command = await page.get_by_label("Terminal command").input_value()
    await page.get_by_role("button", name="Copy command", exact=True).click()
    # Only compare inside the browser so its ephemeral credentials are never logged.
    copied = await page.evaluate('async () => (await navigator.clipboard.readText()) === document.querySelector("#setup-command").value')
    check(copied, "the copy button did not copy the displayed command")
    check(await page.evaluate("document.documentElement.scrollWidth <= innerWidth"), "receipt overflows the viewport")
    words = shlex.split(command)
    check(len(words) == 10 and words[:-1] == ["curl", "-fsSL", control + "/quickstart/install.sh",
          "|", "sh", "-s", "--", client, endpoint], "unexpected shell command format")
    code = words[-1]
    if published:
        # Execute only the checked grammar. Pass the command on stdin, never in a shell history.
        await run(["/bin/sh"], env, home, stdin=command.encode())
    else:
        await run([binary, "connect", client, "--url", endpoint, "--setup-code", code], env, home)
    async with httpx.AsyncClient(trust_env=False) as http:
        replay = await http.post(control + "/quickstart/redeem", json={"client": client, "code": code})
        check(replay.status_code == 401, "a used setup command was accepted again")
    return code


def registration(client, home):
    if client == "codex":
        source = home / ".codex/config.toml"
        data = tomllib.loads(source.read_text())["mcp_servers"]["shoowcase"]
    else:
        candidates = {"claude": [".claude/.claude.json", ".claude.json"],
                      "copilot": [".copilot/mcp-config.json"], "cursor": [".cursor/mcp.json"]}[client]
        source = next((home / key for key in candidates if (home / key).is_file()), None)
        check(source is not None, "missing " + client + " registration")
        data = json.loads(source.read_text())["mcpServers"]["shoowcase"]
    return data, source


async def bridge(data, env, home):
    params = StdioServerParameters(command=data["command"], args=data["args"], env=env, cwd=str(home))
    async with stdio_client(params) as (rx, tx), ClientSession(rx, tx) as session:
        await session.initialize()
        check("list_sites" in {t.name for t in (await session.list_tools()).tools}, "MCP tool list missing")
        result = await session.call_tool("list_sites", {})
        check(not result.isError and result.structuredContent.get("sites") == [], "cannot read fresh workspace")


async def codex_discovery(env, home):
    # Native app-server exposes MCP discovery without a model turn or a sign-in.
    proc = await asyncio.create_subprocess_exec("codex", "app-server", "--stdio", cwd=home, env=env,
        stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.DEVNULL, limit=2 * 1024 * 1024)
    async def send(payload):
        proc.stdin.write(json.dumps(payload).encode() + b"\n")
        await proc.stdin.drain()
    async def rpc(rid, method, params):
        await send({"id": rid, "method": method, "params": params})
        while line := await proc.stdout.readline():
            msg = json.loads(line)
            if msg.get("id") == rid:
                check("result" in msg, "native Codex rejected " + method)
                return msg["result"]
        raise AssertionError("native Codex disconnected")
    try:
        async with asyncio.timeout(45):
            await rpc(1, "initialize", {"clientInfo": {"name": "shoowcase-smoke", "version": "1"}})
            await send({"method": "initialized"})
            result = await rpc(2, "mcpServerStatus/list", {"serverName": "shoowcase"})
            check(any("list_sites" in row["tools"] for row in result["data"] if row["name"] == "shoowcase"),
                  "native Codex did not discover Shoowcase tools")
    finally:
        if proc.returncode is None:
            proc.terminate()
        await proc.wait()


async def main(args):
    binary = str(args.binary.resolve(strict=True))
    async with fixture.local_server(binary) as (endpoint, _, _), async_playwright() as pw:
        control = endpoint.removesuffix("/mcp")
        browser = await pw.chromium.launch(channel=args.browser_channel)
        try:
            context = await browser.new_context(viewport={"width": 360, "height": 780},
                                                permissions=["clipboard-read", "clipboard-write"])
            page = await context.new_page()
            await page.goto(control)
            await page.get_by_role("link", name="Create an account").click()
            await page.get_by_label("Display name").fill("Quickstart acceptance")
            await page.get_by_role("button", name="Create account").click()
            await expect(page.get_by_role("link", name="Quickstart", exact=True).first).to_be_visible()
            for client in args.clients.split(","):
                check(client in NAMES, "unknown test client")
                with tempfile.TemporaryDirectory(prefix="shoowcase-quickstart-") as temp:
                    home = Path(temp)
                    env = profile(home)
                    code = await register(page, control, endpoint, client, env, home, binary, args.published_installer)
                    reg, path = registration(client, home)
                    credentials = list(home.rglob(f"agents/{client}.json"))
                    check(len(credentials) == 1, "expected one private personal credential file")
                    cred = credentials[0]
                    check(cred.stat().st_mode & 0o077 == 0 and cred.parent.stat().st_mode & 0o077 == 0, "credential permissions")
                    token = json.loads(cred.read_text())["token"]
                    check(token and token not in path.read_text() and code not in path.read_text(), "credential in agent configuration")
                    check(token not in await page.content(), "long-lived key on setup receipt")
                    await asyncio.wait_for(bridge(reg, env, home), 30)
                    if args.native_clients and client == "codex":
                        await codex_discovery(env, home)
                        print("PASS: Codex app-server discovered Shoowcase tools")
                    if args.native_clients and client == "copilot":
                        native = json.loads(await run(["copilot", "mcp", "get", "--json", "shoowcase"], env, home))
                        check("shoowcase" in json.dumps(native) and reg["command"] in json.dumps(native),
                              "GitHub Copilot CLI did not load the registration")
                        print("PASS: GitHub Copilot CLI read the registered local server")
                    if args.native_clients and client == "claude":
                        native = await run(["claude", "mcp", "get", "shoowcase"], env, home)
                        check(b"Connected" in native, "Claude Code did not complete the MCP handshake")
                        print("PASS: Claude Code opened the registered connection")
                    await page.get_by_role("link", name="Agent keys", exact=True).last.click()
                    row = page.get_by_role("listitem").filter(has=page.get_by_text(NAMES[client], exact=True)).first
                    await row.locator("summary").click()
                    await row.get_by_role("checkbox").check()
                    await row.get_by_role("button", name="Revoke key").click()
                    async with httpx.AsyncClient(trust_env=False) as http:
                        r = await http.post(endpoint, headers={"Authorization": "Bearer " + token},
                                            json={"jsonrpc": "2.0", "method": "tools/list", "id": 1})
                        check(r.status_code == 401, "revoked agent credential still accepted")
                    print(f"PASS: {NAMES[client]} – copied command, registration, MCP list_sites, single use and revocation")
        finally:
            await browser.close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--clients", default="codex,claude,copilot,cursor")
    parser.add_argument("--browser-channel", default="chrome")
    parser.add_argument("--published-installer", action="store_true")
    parser.add_argument("--native-clients", action="store_true", help="also run installed Codex, Claude Code and GitHub Copilot CLIs for selected clients")
    asyncio.run(main(parser.parse_args()))
