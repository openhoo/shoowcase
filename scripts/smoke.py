#!/usr/bin/env python3
"""Black-box release check. Starts a private loopback server with a temporary database."""
import argparse
import hashlib
import http.client
import json
import os
import secrets
import socket
import struct
import subprocess
import tempfile
import time
import uuid
import zlib
from dataclasses import dataclass
from html.parser import HTMLParser
from http.cookies import SimpleCookie
from pathlib import Path
from urllib.parse import urlencode, urljoin, urlsplit


def check(ok, message):
    if not ok:
        raise RuntimeError(message)


@dataclass
class Response:
    status: int
    headers: dict
    body: bytes

    def expect(self, code, label):
        check(self.status == code, f"{label}: expected HTTP {code}, got {self.status}")
        return self


class Browser:
    """Redirects are explicit. Model host-only cookies; dial loopback even for *.localhost."""
    def __init__(self, port):
        self.port = port
        self.cookies = {}

    def request(self, method, url, body=None, headers=None):
        u = urlsplit(url)
        check(u.scheme == "http" and u.port == self.port and u.hostname and
              (u.hostname == "localhost" or u.hostname.endswith(".localhost")), "nonlocal URL from service")
        conn = http.client.HTTPConnection("127.0.0.1", self.port, timeout=10)
        jar = self.cookies.setdefault(u.netloc, {})
        hdr = {"Host": u.netloc, **(headers or {})}
        if jar:
            hdr["Cookie"] = "; ".join(f"{k}={v}" for k, v in jar.items())
        try:
            conn.request(method, u.path + ("?" + u.query if u.query else ""), body, hdr)
            r = conn.getresponse()
            for k, v in r.getheaders():
                if k.lower() == "set-cookie":
                    cookie = SimpleCookie(v)
                    for n, morsel in cookie.items():
                        check(not morsel["domain"], "browser cookie widened beyond its issuing host")
                        if morsel["max-age"] in ("-1", "0"):
                            jar.pop(n, None)
                        else:
                            jar[n] = morsel.value
            return Response(r.status, {k.lower(): v for k, v in r.getheaders()}, r.read())
        finally:
            conn.close()

    def navigate(self, url):
        for _ in range(12):
            r = self.request("GET", url)
            if r.status not in {301, 302, 303, 307, 308}:
                return r
            url = urljoin(url, r.headers["location"])
        raise RuntimeError("browser redirect loop")


class MCP:
    def __init__(self, port, token, client):
        self.browser = Browser(port)
        self.url = f"http://localhost:{port}/mcp"
        self.headers = {
            "Authorization": "Bearer " + token, "Content-Type": "application/json",
            "Accept": "application/json, text/event-stream",
        }
        self.seq = 0
        hello = self.rpc("initialize", {"protocolVersion": "2025-11-25", "capabilities": {},
                                        "clientInfo": {"name": client, "version": "1.0"}})
        check(hello["protocolVersion"] == "2025-11-25", "unexpected MCP protocol version")
        self.version = hello["serverInfo"]["version"]
        self.headers["MCP-Protocol-Version"] = hello["protocolVersion"]
        self.rpc("notifications/initialized", {}, notify=True)

    def rpc(self, method, params, notify=False):
        msg = {"jsonrpc": "2.0", "method": method, "params": params}
        if not notify:
            self.seq += 1
            msg["id"] = self.seq
        response = self.browser.request("POST", self.url, json.dumps(msg), self.headers)
        response.expect(202 if notify else 200, method)
        check("mcp-session-id" not in response.headers, "stateless MCP set a session cookie/header")
        if notify:
            return
        payload = json.loads(response.body)
        check(payload.get("id") == self.seq and "result" in payload, f"{method}: missing JSON-RPC result")
        return payload["result"]

    def call(self, tool, args, expected_error=None):
        r = self.rpc("tools/call", {"name": tool, "arguments": args})
        data = r["structuredContent"]
        check(json.loads(r["content"][0]["text"]) == data, f"{tool}: SDK and text results disagree")
        actual = data.get("error", {}).get("code")
        check(bool(r.get("isError")) == bool(expected_error) and actual == expected_error,
              f"{tool}: expected {expected_error or 'success'}, got {actual or 'success'}")
        return data


class Approval(HTMLParser):
    def __init__(self):
        super().__init__()
        self.csrf = None

    def handle_starttag(self, tag, attrs):
        props = dict(attrs)
        if tag == "input" and props.get("name") == "csrf":
            self.csrf = props.get("value")


def image_fixture():
    """A genuine >250 KiB RGB PNG with deterministic pseudorandom pixels."""
    w = h = 300
    pixels = hashlib.shake_256(b"Shoowcase smoke").digest(w*h*3)
    scanlines = b"".join(b"\0" + pixels[i:i+w*3] for i in range(0, len(pixels), w*3))

    def chunk(kind, data):
        return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind+data))
    return (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 2, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(scanlines)) + chunk(b"IEND", b""))


def exercise(port, token, version):
    control = f"http://localhost:{port}"
    anonymous, person = Browser(port), Browser(port)
    unauth = anonymous.request("POST", control + "/mcp", b"{}").expect(401, "anonymous MCP")
    check("resource_metadata=" in unauth.headers.get("www-authenticate", ""), "missing bearer challenge")
    agent = MCP(port, token, "agent-a")
    check(agent.version == version, "MCP advertises a different version than the binary")
    catalog = {t["name"] for t in agent.rpc("tools/list", {})["tools"]}
    check({"create_site", "get_site", "upload_files", "prepare_upload", "list_sites", "publish_site",
           "set_site_access", "rollback_site"}.issubset(catalog), "incomplete tools/list")
    rid = lambda: str(uuid.uuid4())
    site = agent.call("create_site", {"name": "Project Fieldnotes", "slug": "fieldnotes", "request_id": rid()})
    key = {"site_id": site["site_id"]}
    stable = site["site_url"]
    rev0 = site["revision"]
    check(site["visibility"] == "workspace" and not site["release_id"], "new site unexpectedly live")
    html = lambda marker: f'<!doctype html><html lang="en"><head><title>Fieldnotes</title></head><body><main><h1>{marker}</h1><img src="assets/map.png" alt="Map"></main></body></html>'
    draft1 = {"path": "index.html", "text": html("Week one")}
    draft2 = {"path": "index.html", "text": html("Week two")}
    write = {**key, "files": [draft1], "expected_revision": rev0, "request_id": rid()}
    out1 = agent.call("upload_files", write)
    # A second independent negotiated client can recover and retry after a lost response.
    peer = MCP(port, token, "agent-b")
    check(peer.call("upload_files", write)["revision"] == out1["revision"], "retry created a second revision")
    peer.call("upload_files", {**write, "files": [draft2], "request_id": rid()}, "conflict")
    check(peer.call("get_site", {**key, "path": "index.html"})["file"]["text"] == draft1["text"], "fresh client did not recover first author's draft")
    row = peer.call("list_sites", {"limit": 1})["sites"][0]
    check(row["site_id"] == key["site_id"] and row["site_url"] == stable and row["preview_url"] == out1["preview_url"], "site not discoverable with matching URLs")
    # Anonymous direct requests cannot fetch a private draft's HTML or its asset.
    anonymous.request("GET", out1["preview_url"]).expect(303, "anonymous draft preview")
    check(person.navigate(out1["preview_url"]).expect(200, "signed-in preview").body == draft1["text"].encode(), "preview content mismatch")
    image = image_fixture()
    digest = hashlib.sha256(image).hexdigest()
    prepared = peer.call("prepare_upload", {**key, "expected_revision": out1["revision"], "path": "assets/map.png",
                                           "size": len(image), "sha256": digest, "request_id": rid()})
    anonymous.request("PUT", prepared["upload_url"], b"x"*len(image)).expect(400, "wrong upload digest")
    check(peer.call("get_site", key)["revision"] == out1["revision"], "rejected bytes changed draft")
    raw = anonymous.request("PUT", prepared["upload_url"], image, {"Content-Type": "application/octet-stream"}).expect(200, "binary PUT")
    full = json.loads(raw.body)
    revision1 = full["revision"]
    release1 = peer.call("publish_site", {**key, "revision": revision1, "request_id": rid()})
    asset = urljoin(stable, "assets/map.png")
    anonymous.request("GET", asset).expect(303, "anonymous workspace asset")
    check(person.navigate(asset).expect(200, "signed-in workspace asset").body == image, "workspace asset differs after publication")
    second = peer.call("upload_files", {**key, "expected_revision": revision1, "files": [draft2], "request_id": rid()})
    check(person.navigate(stable).body == draft1["text"].encode(), "draft edit affected live release")
    release2 = peer.call("publish_site", {**key, "revision": second["revision"], "request_id": rid()})
    check(person.navigate(stable).body == draft2["text"].encode(), "publish left old release live")
    history = peer.call("get_site", {**key, "view": "releases", "limit": 1})
    check(len(history["releases"]) == 1 and history["next_cursor"], "release pagination missing cursor")
    page2 = peer.call("get_site", {**key, "view": "releases", "limit": 1, "cursor": history["next_cursor"]})
    check(page2["releases"][0]["release_id"] == release1["release_id"], "release history cursor skipped the older release")
    rolled = peer.call("rollback_site", {**key, "release_id": release1["release_id"], "expected_release_id": release2["release_id"], "request_id": rid()})
    check(rolled["site_url"] == stable and rolled["live_revision"] == revision1 and person.navigate(stable).body == draft1["text"].encode(), "rollback changed URL or served wrong release")
    public_args = {**key, "visibility": "public", "revision": revision1, "request_id": rid()}
    pending = peer.call("set_site_access", public_args, "approval_required")
    approval = pending["approval_url"]
    page = person.navigate(approval).expect(200, "public approval page")
    parser = Approval()
    parser.feed(page.body.decode())
    check(parser.csrf and revision1.encode() in page.body, "approval page lacks bound revision or CSRF input")
    form = {"Origin": control, "Content-Type": "application/x-www-form-urlencoded", "Accept": "text/html"}
    person.request("POST", approval, urlencode({"csrf": "invalid"}), form).expect(403, "invalid approval CSRF")
    anonymous.request("GET", asset).expect(303, "unapproved release")
    # Reasserting workspace access after review makes an already-open approval obsolete.
    peer.call("set_site_access", {**key, "visibility": "workspace", "revision": revision1, "request_id": rid()})
    person.request("POST", approval, urlencode({"csrf": parser.csrf}), form).expect(409, "superseded approval")
    anonymous.request("HEAD", asset).expect(303, "stale approval did not change the audience")
    fresh = peer.call("set_site_access", public_args, "approval_required")["approval_url"]
    page = person.navigate(fresh).expect(200, "renewed approval")
    renewed = Approval()
    renewed.feed(page.body.decode())
    check(renewed.csrf, "renewed approval missing CSRF")
    receipt = person.request("POST", fresh, urlencode({"csrf": renewed.csrf}), form).expect(200, "approved public release")
    check(receipt.headers.get("content-type", "").startswith("text/html") and stable.encode() in receipt.body
          and full["preview_url"].encode() in receipt.body and second["preview_url"].encode() not in receipt.body,
          "approval receipt should link to the reviewed live revision, even if the draft differs")
    public = anonymous.request("GET", asset).expect(200, "anonymous public asset")
    check(public.body == image and public.headers["etag"].strip('"') == digest, "served public bytes/ETag differ")
    head = anonymous.request("HEAD", asset).expect(200, "asset metadata")
    check(not head.body and head.headers["etag"] == public.headers["etag"]
          and int(head.headers["content-length"]) == len(image), "HEAD metadata differs from GET")
    old = {"If-None-Match": public.headers["etag"]}
    anonymous.request("HEAD", asset, headers=old).expect(304, "authorized conditional HEAD")
    anonymous.request("GET", asset, headers=old).expect(304, "fresh public cache")
    anonymous.request("GET", full["preview_url"]).expect(303, "published site's private preview")
    peer.call("set_site_access", {**key, "visibility": "offline", "revision": revision1, "request_id": rid()})
    withdrawn = anonymous.request("GET", asset, headers=old)
    # Login redirects deliberately mask whether an anonymous visitor named an offline or private host.
    check(withdrawn.status in {303, 404} and "etag" not in withdrawn.headers and withdrawn.body != image,
          "withdrawn asset still available to anonymous visitor with old ETag")
    person.request("GET", stable, headers=old).expect(404, "offline signed-in site")
    blocked = person.request("HEAD", asset, headers=old).expect(404, "withdrawn conditional HEAD")
    check("etag" not in blocked.headers, "withdrawn asset disclosed metadata")
    check(peer.call("get_site", key)["visibility"] == "offline", "offline change missing on readback")


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--binary", required=True, type=Path)
    args = p.parse_args()
    binary = args.binary.resolve(strict=True)
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        port = s.getsockname()[1]
    token = secrets.token_urlsafe(32)
    with tempfile.TemporaryDirectory(prefix="shoowcase-smoke-") as state:
        # Never inherit a production database URL, token, workspace or issuer.
        env = {k: v for k, v in os.environ.items() if not k.startswith("SHOOWCASE_")}
        env.update(SHOOWCASE_LISTEN=f"127.0.0.1:{port}", SHOOWCASE_DEV_TOKEN=token,
                   SHOOWCASE_PUBLIC="true", SHOOWCASE_STATE_DIR=state)
        with subprocess.Popen([binary, "-dev"], cwd=state, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL) as app:
            try:
                deadline = time.monotonic() + 12
                browser = Browser(port)
                while time.monotonic() < deadline and app.poll() is None:
                    try:
                        if browser.request("GET", f"http://localhost:{port}/healthz").status == 200:
                            break
                    except OSError:
                        pass
                    time.sleep(.05)
                else:
                    raise RuntimeError("isolated Shoowcase process failed to reach /healthz")
                exercise(port, token, subprocess.check_output([binary, "version"], text=True).strip())
            finally:
                app.terminate()
                try:
                    app.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    app.kill()
    print("PASS: two independent MCP clients, revision retry/conflict, scoped binary upload, workspace sign-in, publish/rollback, public approval/CSRF, withdrawal with old ETag")


if __name__ == "__main__":
    main()
