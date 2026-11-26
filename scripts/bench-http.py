#!/usr/bin/env python3
"""Measure authenticated 200 vs conditional 304 delivery on an isolated loopback instance."""
import argparse
import concurrent.futures
import hashlib
import http.client
import json
import math
import os
from pathlib import Path
import secrets
import socket
import subprocess
import sys
import tempfile
import threading
import time
from urllib.parse import urlsplit

sys.dont_write_bytecode = True
from smoke import Browser, MCP, check


def fixture():
    head = '<!doctype html><html lang="en"><head><title>Team notes</title></head><body><main><h1>Team notes</h1>'
    tail = '</main></body></html>'
    line = '<p>The onboarding team meets at nine on Monday.</p>'
    n = 32 * 1024 - len(head) - len(tail)
    return (head + line * (n // len(line)) + ' ' * (n % len(line)) + tail).encode()


def measure(port, host, cookies, payload, etag, conditional, total, workers):
    # Warm the DB and each persistent connection; exclude worker startup and warmup.
    started = [0.0]
    gate = threading.Barrier(workers, action=lambda: started.__setitem__(0, time.perf_counter()), timeout=15)

    def worker(index):
        samples = []
        conn = http.client.HTTPConnection("127.0.0.1", port, timeout=10)
        hdr = {"Host": host, "Cookie": cookies}
        if conditional:
            hdr["If-None-Match"] = etag

        def hit(measured):
            before = time.perf_counter()
            conn.request("GET", "/", headers=hdr)
            r = conn.getresponse()
            body = r.read()
            after = time.perf_counter()
            check(r.status == (304 if conditional else 200) and body == (b"" if conditional else payload)
                  and r.headers.get("ETag") == etag, "wrong status, body, or validator in measured path")
            if measured:
                samples.append((after-before)*1000)

        try:
            for _ in range(15):
                hit(False)
            gate.wait()
            for _ in range(total // workers + int(index < total % workers)):
                hit(True)
            return samples, time.perf_counter()
        except BaseException:
            gate.abort()
            raise
        finally:
            conn.close()

    with concurrent.futures.ThreadPoolExecutor(max_workers=workers) as pool:
        rows = list(pool.map(worker, range(workers)))
    ordered = sorted(x for samples, _ in rows for x in samples)
    sec = max(end for _, end in rows) - started[0]
    pct = lambda p: round(ordered[math.ceil(len(ordered)*p)-1], 3)
    return {"http_status": 304 if conditional else 200, "requests": total, "workers": workers,
            "seconds": round(sec, 3), "requests_per_second": round(total/sec, 1),
            "p50_ms": pct(0.5), "p95_ms": pct(0.95)}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--binary", required=True, type=Path)
    p.add_argument("--requests", type=int, default=2000, help="measured requests per status")
    p.add_argument("--concurrency", type=int, default=4)
    args = p.parse_args()
    if not (1 <= args.concurrency <= 32 and args.concurrency <= args.requests <= 100000):
        p.error("use 1–32 workers and between one request per worker and 100000 requests")
    binary = args.binary.resolve(strict=True)
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        port = s.getsockname()[1]
    with tempfile.TemporaryDirectory(prefix="shoowcase-bench-") as state:
        token = secrets.token_urlsafe(32)
        env = {k:v for k,v in os.environ.items() if not k.startswith("SHOOWCASE_")}
        env.update(SHOOWCASE_LISTEN=f"127.0.0.1:{port}", SHOOWCASE_STATE_DIR=state, SHOOWCASE_DEV_TOKEN=token)
        with subprocess.Popen([binary, "-dev"], cwd=state, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL) as app:
            try:
                end = time.monotonic() + 10
                person = Browser(port)
                while time.monotonic() < end and app.poll() is None:
                    try:
                        if person.request("GET", f"http://localhost:{port}/readyz").status == 200:
                            break
                    except OSError:
                        pass
                    time.sleep(.05)
                else:
                    raise RuntimeError("isolated server did not become ready")
                agent = MCP(port, token, "bench")
                site = agent.call("create_site", {"name": "Team notes", "request_id": "benchmark-create"})
                payload = fixture()
                r = agent.call("upload_files", {"site_id":site["site_id"], "expected_revision":site["revision"],
                             "files":[{"path":"index.html","text":payload.decode()}], "request_id":"benchmark-upload"})
                live = agent.call("publish_site", {"site_id":site["site_id"], "revision":r["revision"], "request_id":"benchmark-publish"})
                person.request("GET", live["site_url"]).expect(303, "anonymous baseline")
                sample = person.navigate(live["site_url"]).expect(200, "signed-in baseline")
                etag = sample.headers.get("etag")
                check(sample.body == payload and etag == '"' + hashlib.sha256(payload).hexdigest() + '"', "fixture did not round-trip")
                host = urlsplit(live["site_url"]).netloc
                header = "; ".join(f"{k}={v}" for k, v in person.cookies[host].items())
                check(bool(header), "missing private-site session")
                report = [measure(port, host, header, payload, etag, cond, args.requests, args.concurrency) for cond in (False, True)]
                print(json.dumps({"body_bytes":len(payload), "database":"SQLite", "cases":report}, indent=2))
            finally:
                app.terminate()
                try:
                    app.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    app.kill()


if __name__ == "__main__":
    main()
