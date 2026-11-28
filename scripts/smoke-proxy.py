#!/usr/bin/env python3
# /// script
# requires-python = ">=3.12"
# dependencies = ["httpx==0.28.1", "mcp==1.30.0", "playwright==1.65.2"]
# ///
"""Exercise the copied Unix or Windows command through an authenticated, loopback-only proxy.

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
import os
from pathlib import Path
import platform
import re
import secrets
import shutil
import socket
import ssl
import subprocess
import tarfile
import tempfile
import threading
from urllib.parse import urlsplit
import zipfile

from playwright.async_api import async_playwright
from smoke import check

quick = import_module("smoke-quickstart")
fixture = import_module("smoke-browser")
WINDOWS = os.name == "nt"


@contextmanager
def revocation_server(root):
    # Windows Schannel checks revocation by default. Publish only the fixture CA's
    # signed, empty CRL on loopback so the real Windows validation stays enabled.
    class CRL(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def do_GET(self):
            if self.path != "/issuer.crl":
                self.send_error(404)
                return
            data = (root / "issuer.crl").read_bytes()
            self.send_response(200)
            self.send_header("Content-Type", "application/pkix-crl")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

    with ThreadingHTTPServer(("127.0.0.1", 0), CRL) as server:
        threading.Thread(target=server.serve_forever, daemon=True).start()
        try:
            yield f"http://127.0.0.1:{server.server_port}/issuer.crl"
        finally:
            server.shutdown()


def test_ca(root, crl_url):
    executable = shutil.which("openssl")
    if not executable and WINDOWS:
        executable = Path(os.environ["ProgramFiles"]) / "Git/usr/bin/openssl.exe"

    def openssl(*args, data=None):
        return subprocess.run([executable or "openssl", *args], cwd=root, input=data, stdout=subprocess.PIPE,
                              stderr=subprocess.DEVNULL, check=True).stdout
    openssl("req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1", "-subj", "/CN=Fixture Root",
            "-keyout", "ca.key", "-out", "ca.pem", "-addext", "basicConstraints=critical,CA:TRUE",
            "-addext", "keyUsage=critical,keyCertSign,cRLSign")
    openssl("req", "-newkey", "rsa:2048", "-nodes", "-subj", "/CN=relay.localhost", "-keyout", "tls.key", "-out", "tls.csr")
    (root / "names").write_text("subjectAltName=DNS:relay.localhost,DNS:github.com\nextendedKeyUsage=serverAuth\n"
                                "basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\n"
                                f"authorityKeyIdentifier=keyid,issuer\ncrlDistributionPoints=URI:{crl_url}\n")
    openssl("x509", "-req", "-in", "tls.csr", "-CA", "ca.pem", "-CAkey", "ca.key", "-CAcreateserial",
            "-days", "1", "-extfile", "names", "-out", "tls.pem")
    (root / "index.txt").touch()
    (root / "crlnumber").write_text("01\n")
    (root / "issuer.cnf").write_text("[ca]\ndefault_ca=fixture\n[fixture]\ndatabase=index.txt\n"
                                     "private_key=ca.key\ncertificate=ca.pem\ndefault_md=sha256\n"
                                     "crlnumber=crlnumber\ndefault_crl_days=1\n")
    openssl("ca", "-config", "issuer.cnf", "-gencrl", "-out", "issuer.pem")
    openssl("crl", "-in", "issuer.pem", "-outform", "DER", "-out", "issuer.crl")
    pub = openssl("x509", "-in", "tls.pem", "-pubkey", "-noout")
    spki = openssl("pkey", "-pubin", "-outform", "DER", data=pub)
    tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    tls.load_cert_chain(root / "tls.pem", root / "tls.key")
    return tls, base64.b64encode(hashlib.sha256(spki).digest()).decode()


def release_files(root, binary, published):
    version = subprocess.check_output([binary, "version"], text=True).strip()
    arch = {"arm64": "arm64", "aarch64": "arm64", "amd64": "amd64", "x86_64": "amd64"}[platform.machine().lower()]
    extension = "zip" if WINDOWS else "tar.gz"
    name = f"shoowcase-{version}-{platform.system().lower()}-{arch}.{extension}"
    if not published:
        if WINDOWS:
            with zipfile.ZipFile(root / name, "w", zipfile.ZIP_DEFLATED) as archive:
                archive.write(binary, "shoowcase.exe")
        else:
            with tarfile.open(root / name, "w:gz") as archive:
                archive.add(binary, "shoowcase", recursive=False)
        sha = hashlib.sha256((root / name).read_bytes()).hexdigest()
        (root / "SHA256SUMS").write_text(f"{sha}  {name}\n")
    directory = published or root
    return {f"/openhoo/shoowcase/releases/download/{version}/{n}": (directory / n).read_bytes()
            for n in ("SHA256SUMS", name)}


def terminal(home):
    env = quick.profile(home)
    if not WINDOWS:
        return env, ["/bin/sh"], "curl"

    # Keep the runner's system tools, but isolate all user and client data.
    for key in ("OS", "SystemRoot", "WINDIR", "ComSpec", "PATHEXT", "PROCESSOR_ARCHITECTURE",
                "PROCESSOR_ARCHITEW6432", "ProgramFiles", "ProgramFiles(x86)", "ProgramW6432"):
        if key in os.environ:
            env[key] = os.environ[key]
    for key, path in {"USERPROFILE": home, "LOCALAPPDATA": home / "AppData/Local",
                      "APPDATA": home / "AppData/Roaming", "TMP": home / "Temp", "TEMP": home / "Temp"}.items():
        path.mkdir(parents=True, exist_ok=True)
        env[key] = str(path)
    system32 = Path(env["SystemRoot"]) / "System32"
    env["PATH"] = str(system32) + os.pathsep + env["PATH"]
    shell = [str(system32 / "WindowsPowerShell/v1.0/powershell.exe"), "-NoLogo", "-NoProfile", "-NonInteractive",
             "-Command", "if ($PSVersionTable.PSVersion.Major -ne 5 -or $PSVersionTable.PSVersion.Minor -ne 1) { exit 98 }; "
                         "$ErrorActionPreference='Stop'; & ([ScriptBlock]::Create([Console]::In.ReadToEnd())); exit $LASTEXITCODE"]
    return env, shell, str(system32 / "curl.exe")


@contextmanager
def proxy(tls, control, backend, assets, secret):
    # Model a TLS-inspecting company proxy. It can serve only two fixture hosts;
    # it never opens an outbound connection other than the disposable loopback app.
    counts = Counter()
    expected = "Basic " + base64.b64encode(("worker:" + secret).encode()).decode()
    # This fixture covers one onboarding flow. Forward only literal routes
    # for that flow; client-controlled request targets never go to the backend.
    paths = {p: p for p in ("/", "/healthz", "/ui.css", "/console.js", "/account", "/sites", "/quickstart", "/quickstart?setup=manual",
                            "/quickstart/install.sh", "/quickstart/install.ps1", "/quickstart/redeem", "/mcp")}
    paths["/auth/login"] = "/auth/login?next=%2Faccount"
    # Admit only the two immutable asset paths shipped by this fixture binary.
    conn = http.client.HTTPConnection("127.0.0.1", backend, timeout=15)
    try:
        for name in ("ui.css", "console.js"):
            conn.request("GET", "/" + name, headers={"Host": control})
            asset = conn.getresponse()
            check(asset.status == 200, f"fixture asset unavailable: {name}")
            digest = hashlib.sha256(asset.read()).hexdigest()
            path = f"/ui/{digest}/{name}"
            paths[path] = path
    finally:
        conn.close()


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
            elif target == control and self.command in ("GET", "POST") and (
                path := paths.get(self.path) or paths.get(self.path.partition("?")[0])
            ):
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
    with (tempfile.TemporaryDirectory(prefix="shoowcase-proxy-") as temp, socket.socket() as guard,
          revocation_server(Path(temp)) as crl_url):
        root = Path(temp)
        tls, spki = test_ca(root, crl_url)
        assets = release_files(root, binary, args.release_dir)
        guard.bind(("127.0.0.1", 0))  # No listener at this port. Only CONNECT reaches the origin.
        host = f"relay.localhost:{guard.getsockname()[1]}"
        base = "https://" + host
        secret = secrets.token_hex(24)
        async with fixture.local_server(str(binary), control_origin=base) as (internal, _, _), async_playwright() as pw:
            with proxy(tls, host, urlsplit(internal).port, assets, secret) as (port, counts):
                home = root / "user"
                clean, shell, curl = terminal(home)
                net = {**clean, "HTTPS_PROXY": f"http://worker:{secret}@127.0.0.1:{port}",
                       "SHOOWCASE_CA_FILE": str(root / "ca.pem")}
                for label, env in (("trusted proxy", net), ("direct connection", clean), ("untrusted certificate", net),
                                   ("wrong proxy password", {**net, "HTTPS_PROXY": f"http://wrong@127.0.0.1:{port}"})):
                    # No CA passed to the untrusted-certificate probe.
                    ca = [] if label == "untrusted certificate" else ["--cacert", str(root / "ca.pem")]
                    p = await asyncio.create_subprocess_exec(curl, "-qfsS", "--max-time", "8", *ca, base + "/healthz",
                        env=env, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
                    _, error = await p.communicate()
                    # Only report native Windows trust codes, never proxy URLs or credentials.
                    codes = re.findall(rb"\b(?:CERT_TRUST_|SEC_E_|CRYPT_E_|CERT_E_)[A-Z0-9_]+\b|\b0x[0-9a-fA-F]{8}\b", error)
                    check((p.returncode == 0) == (label == "trusted proxy"),
                          f"{label}: unexpected curl exit {p.returncode}; trust codes: {b' '.join(codes).decode() or 'none'}")
                check(counts["/healthz"] == 1, "only the trusted probe should reach the backend")
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
                    await page.get_by_role("link", name="Set up manually").click()
                    await page.get_by_role("radio", name="Cursor", exact=True).check()
                    await page.get_by_label("Where will you run the command?").select_option("windows" if WINDOWS else "unix")
                    await page.get_by_label("Use a company proxy").check()
                    await page.get_by_role("button", name="Create key and get command").click()
                    await page.get_by_role("button", name="Copy command", exact=True).click()
                    command = await page.evaluate("navigator.clipboard.readText()")
                    command_label = "PowerShell command" if WINDOWS else "Terminal command"
                    check(command.replace("\r\n", "\n") == await page.get_by_label(command_label).input_value(),
                          "clipboard differs from receipt")
                finally:
                    await browser.close()
                installer = "/quickstart/install.ps1" if WINDOWS else "/quickstart/install.sh"
                stages = (installer, "checksum", "archive", "/quickstart/redeem")
                try:
                    output = await quick.run(shell, net, home, stdin=command.encode())
                except RuntimeError as err:
                    # Counts identify the failed stage without exposing any part of the pasted command.
                    raise RuntimeError(f"setup failed; requests: { {stage: counts[stage] for stage in stages} }") from err
                reg, config = quick.registration("cursor", home, output)
                check(Path(reg["command"]).read_bytes() == binary.read_bytes(), "installed bytes differ")
                if WINDOWS:
                    check(Path(reg["command"]) == Path(clean["LOCALAPPDATA"]) / "Shoowcase/bin/shoowcase.exe",
                          "unexpected Windows installation path")
                    sealed = (Path(clean["APPDATA"]) / "shoowcase/agents/cursor.json").read_bytes()
                    check(sealed and secret.encode() not in sealed and base.encode() not in sealed,
                          "Windows credential store contains plaintext")
                check(secret not in config and secret.encode() not in output and secret not in command, "proxy credential exposed")
                check(all(counts[stage] == 1 for stage in stages),
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
                print(json.dumps({"platform": platform.system(), "shell": "Windows PowerShell 5.1" if WINDOWS else "sh",
                       "setup_requests": 4, "mcp_requests": counts["/mcp"] - before,
                       "fresh_process": True, "tools_list": True, "list_sites": True,
                       "direct_route_blocked": True, "bad_proxy_rejected": True, "unknown_ca_rejected": True, "relay_failed_closed": True,
                       "source": "published archive" if args.release_dir else "candidate archive"}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--release-dir", type=Path)
    parser.add_argument("--browser-channel", default="chrome")
    asyncio.run(main(parser.parse_args()))
