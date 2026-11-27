#!/usr/bin/env python3
# /// script
# requires-python = ">=3.12"
# dependencies = ["httpx==0.28.1", "mcp==1.30.0", "playwright==1.65.2"]
# ///
"""Exercise the copied Unix command behind an authenticated, loopback-only corporate proxy.

Build with make build VERSION=<installer pin>. For a published build, supply the
downloaded release directory (archive and SHA256SUMS) with --release-dir.
"""
import argparse
import asyncio
import base64
from collections import Counter
from contextlib import contextmanager
import hashlib
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from importlib import import_module
import json
from pathlib import Path
import platform
import secrets
import socket
import ssl
import subprocess
import tarfile
import tempfile
import threading
from urllib.parse import urlsplit

from playwright.async_api import async_playwright
from smoke import check

quick = import_module("smoke-quickstart")
fixture = import_module("smoke-browser")


def test_ca(root):
    def openssl(*args, data=None):
        return subprocess.run(["openssl", *args], cwd=root, input=data, stdout=subprocess.PIPE,
                              stderr=subprocess.DEVNULL, check=True).stdout
    openssl("req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1", "-subj", "/CN=Fixture Root",
            "-keyout", "ca.key", "-out", "ca.pem", "-addext", "basicConstraints=critical,CA:TRUE")
    openssl("req", "-newkey", "rsa:2048", "-nodes", "-subj", "/CN=relay.localhost", "-keyout", "tls.key", "-out", "tls.csr")
    (root / "names").write_text("subjectAltName=DNS:relay.localhost,DNS:github.com\nextendedKeyUsage=serverAuth\n")
    openssl("x509", "-req", "-in", "tls.csr", "-CA", "ca.pem", "-CAkey", "ca.key", "-CAcreateserial",
            "-days", "1", "-extfile", "names", "-out", "tls.pem")
    pub = openssl("x509", "-in", "tls.pem", "-pubkey", "-noout")
    spki = openssl("pkey", "-pubin", "-outform", "DER", data=pub)
    tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    tls.load_cert_chain(root / "tls.pem", root / "tls.key")
    return tls, base64.b64encode(hashlib.sha256(spki).digest()).decode()


def release_files(root, binary, published):
    version = subprocess.check_output([binary, "version"], text=True).strip()
    arch = {"arm64": "arm64", "aarch64": "arm64", "x86_64": "amd64"}[platform.machine()]
    name = f"shoowcase-{version}-{platform.system().lower()}-{arch}.tar.gz"
    if not published:
        with tarfile.open(root / name, "w:gz") as tar:
            tar.add(binary, "shoowcase", recursive=False)
        sha = hashlib.sha256((root / name).read_bytes()).hexdigest()
        (root / "SHA256SUMS").write_text(f"{sha}  {name}\n")
    directory = published or root
    return {f"/openhoo/shoowcase/releases/download/{version}/{n}": (directory / n).read_bytes()
            for n in ("SHA256SUMS", name)}


@contextmanager
def proxy(tls, control, backend, assets, secret):
    # Model a TLS-inspecting company proxy. It can serve only two fixture hosts;
    # it never opens an outbound connection other than the disposable loopback app.
    counts = Counter()
    expected = "Basic " + base64.b64encode(("worker:" + secret).encode()).decode()
    # This fixture covers one onboarding flow. Forward only literal routes
    # for that flow; client-controlled request targets never go to the backend.
    paths = {p: p for p in ("/", "/ui.css", "/console.js", "/account", "/sites", "/quickstart",
                            "/quickstart/install.sh", "/quickstart/install.ps1", "/quickstart/redeem", "/mcp")}
    paths["/auth/login"] = "/auth/login?next=%2Faccount"

    class Origin(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def handle_request(self):
            if self.headers.get("Proxy-Authorization") is not None:
                self.send_error(400)
                return
            target = self.headers.get("Host")
            if target == "github.com" and self.command == "GET" and self.path in assets:
                counts["checksum" if self.path.endswith("/SHA256SUMS") else "archive"] += 1
                data = assets[self.path]
                self.send_response(200)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)
            elif target == control and self.command in ("GET", "POST") and (path := paths.get(self.path.partition("?")[0])):
                counts[path] += 1
                connection = http.client.HTTPConnection("127.0.0.1", backend, timeout=15)
                try:
                    headers = {k: v for k, v in self.headers.items() if k.lower() != "connection"}
                    data = self.rfile.read(int(self.headers.get("Content-Length", 0)))
                    connection.request(self.command, path, data, headers)
                    resp = connection.getresponse()
                    self.send_response_only(resp.status)
                    for k, v in resp.getheaders():
                        if k.lower() not in ("connection", "transfer-encoding", "content-length"):
                            self.send_header(k, v)
                    data = resp.read()
                    self.send_header("Content-Length", str(len(data)))
                    self.end_headers()
                    self.wfile.write(data)
                finally:
                    connection.close()
            else:
                self.send_error(404)

        do_GET = do_POST = handle_request

    class Connect(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def do_CONNECT(self):
            if self.path not in (control, "github.com:443"):
                self.send_error(403)
                return
            if self.headers.get("Proxy-Authorization") != expected or counts["closed"]:
                self.send_response(407)
                self.send_header("Proxy-Authenticate", 'Basic realm="fixture"')
                self.end_headers()
                return
            self.send_response_only(200, "Connection established")
            self.end_headers()
            counts["connect"] += 1
            try:
                with tls.wrap_socket(self.connection, server_side=True) as channel:
                    Origin(channel, self.client_address, self.server)
            except (ssl.SSLError, OSError):  # Includes the expected untrusted-CA probe.
                pass

    with ThreadingHTTPServer(("127.0.0.1", 0), Connect) as server:
        threading.Thread(target=server.serve_forever, daemon=True).start()
        try:
            yield server.server_port, counts
        finally:
            server.shutdown()


async def main(args):
    binary = args.binary.resolve(strict=True)
    with tempfile.TemporaryDirectory(prefix="shoowcase-proxy-") as temp, socket.socket() as guard:
        root = Path(temp)
        tls, spki = test_ca(root)
        assets = release_files(root, binary, args.release_dir)
        guard.bind(("127.0.0.1", 0))  # No listener at this port. Only CONNECT reaches the origin.
        host = f"relay.localhost:{guard.getsockname()[1]}"
        base = "https://" + host
        secret = secrets.token_hex(24)
        async with fixture.local_server(str(binary), control_origin=base) as (internal, _, _), async_playwright() as pw:
            with proxy(tls, host, urlsplit(internal).port, assets, secret) as (port, counts):
                home = root / "user"
                clean = quick.profile(home)
                net = {**clean, "HTTPS_PROXY": f"http://worker:{secret}@127.0.0.1:{port}",
                       "SHOOWCASE_CA_FILE": str(root / "ca.pem")}
                for label, env in (("direct connection", clean), ("untrusted certificate", net),
                                   ("wrong proxy password", {**net, "HTTPS_PROXY": f"http://wrong@127.0.0.1:{port}"})):
                    # No CA passed to the untrusted-certificate probe.
                    ca = [] if label == "untrusted certificate" else ["--cacert", str(root / "ca.pem")]
                    p = await asyncio.create_subprocess_exec("curl", "-qfsS", "--max-time", "4", *ca, base + "/healthz",
                        env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                    check(await p.wait() != 0, label + " unexpectedly worked")
                browser = await pw.chromium.launch(channel=args.browser_channel, args=["--ignore-certificate-errors-spki-list=" + spki],
                        proxy={"server": f"http://127.0.0.1:{port}", "username": "worker", "password": secret, "bypass": "<-loopback>"})
                try:
                    ctx = await browser.new_context(permissions=["clipboard-read", "clipboard-write"])
                    page = await ctx.new_page()
                    await page.goto(base)
                    await page.get_by_role("link", name="Create an account").click()
                    await page.get_by_label("Display name").fill("Proxy fixture")
                    await page.get_by_role("button", name="Create account").click()
                    await page.get_by_role("link", name="Quickstart", exact=True).first.click()
                    await page.get_by_role("radio", name="Cursor", exact=True).check()
                    await page.get_by_label("Use a company proxy").check()
                    await page.get_by_role("button", name="Create key and get command").click()
                    await page.get_by_role("button", name="Copy command", exact=True).click()
                    command = await page.evaluate("navigator.clipboard.readText()")
                    check(command == await page.get_by_label("Terminal command").input_value(), "clipboard differs from receipt")
                finally:
                    await browser.close()
                output = await quick.run(["/bin/sh"], net, home, stdin=command.encode())
                reg, config = quick.registration("cursor", home, output)
                check(Path(reg["command"]).read_bytes() == binary.read_bytes(), "installed bytes differ")
                check(secret not in config and secret.encode() not in output and secret not in command, "proxy credential exposed")
                check(counts["/quickstart/install.sh"] == counts["checksum"] == counts["archive"] == counts["/quickstart/redeem"] == 1,
                      "installer, manifest, archive or redemption did not take the authenticated route")
                before = counts["/mcp"]
                await asyncio.wait_for(quick.bridge(reg, clean, home), 20)
                after = counts["/mcp"]
                check(after >= before + 3, "relay did not use the saved route")
                counts["closed"] = 1
                failed = await quick.run([reg["command"], *reg["args"]], clean, home,
                    stdin=b'{"jsonrpc":"2.0","id":1,"method":"tools/list"}\n')
                check(json.loads(failed).get("error") and secret.encode() not in failed and counts["/mcp"] == after,
                      "refused proxy did not fail closed")
                print(json.dumps({"setup_requests": 4, "mcp_requests": counts["/mcp"] - before,
                       "fresh_process": True, "tools_list": True, "list_sites": True,
                       "direct_route_blocked": True, "bad_proxy_rejected": True, "unknown_ca_rejected": True, "relay_failed_closed": True,
                       "source": "published archive" if args.release_dir else "candidate archive"}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--release-dir", type=Path)
    parser.add_argument("--browser-channel", default="chrome")
    asyncio.run(main(parser.parse_args()))
