#!/usr/bin/env python3
"""Exercise a real pg_dump/pg_restore on two new databases in a disposable local PostgreSQL container."""
import argparse
import contextlib
import json
import os
import secrets
import socket
import sqlite3
from dataclasses import dataclass
import subprocess
import sys
import tempfile
import time
from pathlib import Path
from urllib.parse import quote

sys.dont_write_bytecode = True
from smoke import Browser, MCP, check

REPO = Path(__file__).resolve().parents[1]


class Postgres:
    def __init__(self, engine, container):
        self.engine = engine
        self.container = container
        opts = subprocess.run([engine, "port", container, "5432/tcp"], check=True,
                              capture_output=True, text=True).stdout.splitlines()
        ports = [v.removeprefix("127.0.0.1:") for v in opts if v.startswith("127.0.0.1:")]
        check(len(ports) == 1 and ports[0].isdigit(), "publish disposable PostgreSQL port on 127.0.0.1")
        self.port = int(ports[0])

    def run(self, *args, stdin=None, stdout=subprocess.PIPE):
        # The database image owns the admin identity. Never expose the password to the host.
        cmd = [self.engine, "exec", "-i", self.container, "sh", "-ec",
               'export PGUSER="${POSTGRES_USER:-postgres}" PGPASSWORD="${POSTGRES_PASSWORD:-}"; exec "$@"',
               "pg-tool", *args]
        result = subprocess.run(cmd, stdin=stdin, stdout=stdout, stderr=subprocess.PIPE, timeout=60)
        check(result.returncode == 0, f"container {args[0]} failed (exit {result.returncode}); no secrets or archive printed")
        return result.stdout

    def sql(self, db, query):
        # SQL can contain the temporary test password; pass it on stdin, not the command line.
        with tempfile.TemporaryFile() as query_file:
            query_file.write(query.encode())
            query_file.seek(0)
            return self.run("psql", "-X", "-qAt", "-v", "ON_ERROR_STOP=1", "-d", db, stdin=query_file)


@contextlib.contextmanager
def server(binary, dsn, token, state):
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        port = s.getsockname()[1]
    env = {k: v for k, v in os.environ.items() if not k.startswith("SHOOWCASE_")}
    env.update(SHOOWCASE_LISTEN=f"127.0.0.1:{port}", SHOOWCASE_DEV_TOKEN=token,
               SHOOWCASE_DATABASE_URL=dsn, SHOOWCASE_STATE_DIR=state, SHOOWCASE_PUBLIC="false")
    with subprocess.Popen([binary, "-dev"], cwd=state, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL) as app:
        try:
            deadline = time.monotonic() + 12
            while app.poll() is None and time.monotonic() < deadline:
                try:
                    if Browser(port).request("GET", f"http://localhost:{port}/healthz").status == 200:
                        break
                except OSError:
                    pass
                time.sleep(.05)
            else:
                raise RuntimeError("Shoowcase could not start against the disposable database")
            yield port, MCP(port, token, "recovery-rehearsal")
        finally:
            app.terminate()
            try:
                app.wait(timeout=5)
            except subprocess.TimeoutExpired:
                app.kill()


def audit(pg, db):
    result = pg.sql(db, "SELECT row_to_json(a) FROM sc_audit a ORDER BY id;")
    return [json.loads(line) for line in result.splitlines()]


@dataclass
class Fixture:
    home: bytes
    css: bytes
    updated: bytes
    key: dict
    write: dict
    first: dict
    draft2: dict
    second: dict


def seed(binary, dsn, token, state):
    home = (REPO / "examples/onboarding/index.html").read_bytes()
    css = (REPO / "examples/onboarding/style.css").read_bytes()
    updated = home.replace(b"</main>", b"<p>Welcome back on day two.</p></main>")
    check(updated != home, "onboarding example needs a main section")
    request = lambda: secrets.token_hex(16)
    with server(binary, dsn, token, state) as (port, mcp):
        site = mcp.call("create_site", {"name": "Onboarding", "request_id": request()})
        key = {"site_id": site["site_id"]}
        draft = mcp.call("upload_files", {**key, "expected_revision": site["revision"], "request_id": request(),
            "files": [{"path": "index.html", "text": home.decode()}, {"path": "style.css", "text": css.decode()}]})
        first = mcp.call("publish_site", {**key, "revision": draft["revision"], "request_id": request()})
        write = {**key, "expected_revision": draft["revision"], "request_id": request(),
                 "files": [{"path": "index.html", "text": updated.decode()}]}
        draft2 = mcp.call("upload_files", write)
        second = mcp.call("publish_site", {**key, "revision": draft2["revision"], "request_id": request()})
        visitor = Browser(port)
        check(visitor.navigate(second["site_url"]).expect(200, "original site").body == updated, "wrong original release")
        check(visitor.navigate(second["site_url"] + "style.css").body == css, "missing original stylesheet")
    return Fixture(home, css, updated, key, write, first, draft2, second)


def verify_restored(binary, dsn, token, state, specimen, original_audit, get_audit):
    with server(binary, dsn, token, state) as (port, mcp):
        found = mcp.call("get_site", specimen.key)
        check(found["visibility"] == "workspace" and found["release_id"] == specimen.second["release_id"], "restored pointer or audience changed")
        Browser(port).request("GET", found["site_url"] + "style.css").expect(303, "restored private asset")
        reader = Browser(port)
        check(reader.navigate(found["site_url"]).expect(200, "restored live page").body == specimen.updated, "restored live HTML bytes differ")
        check(reader.navigate(found["site_url"] + "style.css").body == specimen.css, "restored CSS bytes differ")
        history = mcp.call("get_site", {**specimen.key, "view": "releases"})["releases"]
        check({r["release_id"] for r in history} == {specimen.first["release_id"], specimen.second["release_id"]}, "restored release history incomplete")
        replayed = mcp.call("upload_files", specimen.write)
        check(replayed["revision"] == specimen.draft2["revision"] and get_audit() == original_audit,
              "replayed mutation created an extra revision or audit record")
        rolled = mcp.call("rollback_site", {**specimen.key, "release_id": specimen.first["release_id"],
                          "expected_release_id": specimen.second["release_id"], "request_id": secrets.token_hex(16)})
        check(rolled["live_revision"] == specimen.first["live_revision"], "restored rollback pointer mismatch")
        check(reader.navigate(found["site_url"]).body == specimen.home, "rollback did not restore original page bytes")
        check(mcp.call("get_site", {**specimen.key, "path": "index.html"})["file"]["text"].encode() == specimen.updated,
              "rollback altered unpublished draft")


def reject_downgrade(binary, dsn, token, state):
    env = {k: v for k, v in os.environ.items() if not k.startswith("SHOOWCASE_")}
    env.update(SHOOWCASE_LISTEN="127.0.0.1:0", SHOOWCASE_DEV_TOKEN=token,
               SHOOWCASE_DATABASE_URL=dsn, SHOOWCASE_STATE_DIR=state, SHOOWCASE_PUBLIC="false")
    try:
        proc = subprocess.run([binary, "-dev"], env=env, cwd=state, capture_output=True, timeout=6)
    except subprocess.TimeoutExpired:
        raise RuntimeError("Older binary started on a migrated database") from None
    check(proc.returncode != 0 and b"cannot initialize database" in proc.stderr,
          "Older binary failed to reject the migrated database at startup")


def verify_recovery_target(previous, dsn, token, state, specimen):
    # Actual service rollback means starting the prior binary on a pre-upgrade
    # snapshot, never pointing it at a database the candidate already migrated.
    with server(previous, dsn, token, state) as (port, mcp):
        recovered = mcp.call("get_site", specimen.key)
        check(recovered["release_id"] == specimen.second["release_id"] and recovered["revision"] == specimen.draft2["revision"],
              "Prior binary did not recover the saved release and draft")
        Browser(port).request("GET", recovered["site_url"]).expect(303, "pre-upgrade snapshot private access")
        check(Browser(port).navigate(recovered["site_url"]).body == specimen.updated, "pre-upgrade snapshot content differs")


def sqlite_upgrade(previous, candidate):
    token = secrets.token_urlsafe(32)
    with tempfile.TemporaryDirectory(prefix="shoowcase-sqlite-upgrade-") as state, tempfile.TemporaryDirectory(prefix="shoowcase-sqlite-backup-") as backup:
        specimen = seed(previous, "", token, state)
        with contextlib.closing(sqlite3.connect(Path(state)/"shoowcase.db")) as db, contextlib.closing(sqlite3.connect(Path(backup)/"shoowcase.db")) as saved:
            check(db.execute("SELECT max(version) FROM sc_schema_migrations").fetchone() == (1,), "SQLite did not start on schema 1")
            db.backup(saved)

        def sql(query):
            with contextlib.closing(sqlite3.connect(Path(state)/"shoowcase.db")) as db:
                return db.execute(query).fetchall()

        get_audit = lambda: sql("SELECT * FROM sc_audit ORDER BY id")
        before = get_audit()
        check(len(before) == 5, "expected five audit rows before migration")
        verify_restored(candidate, "", token, state, specimen, before, get_audit)
        check(sql("SELECT max(version) FROM sc_schema_migrations") == [(2,)], "wrong migrated SQLite schema")
        reject_downgrade(previous, "", token, state)
        verify_recovery_target(previous, "", token, backup, specimen)
        print("PASS: SQLite schema 1→2; preserved two releases, private content, audit and replay; release rollback, old-binary rejection and restoring the prior snapshot verified.")


def rehearse(pg, binary, previous_binary=None):
    tag = "sc_drill_" + secrets.token_hex(8)
    # All SQL identifiers below are generated here and contain only [a-z0-9_].
    source, restored, user = tag + "_source", tag + "_restored", tag + "_user"
    rollback_db = tag + "_rollback" if previous_binary else None
    password = secrets.token_hex(32)
    token = secrets.token_urlsafe(32)
    created = []

    def dsn(db):
        return f"postgres://{user}:{quote(password, safe='')}@127.0.0.1:{pg.port}/{db}?sslmode=disable"

    try:
        pg.sql("postgres", f"CREATE ROLE {user} LOGIN PASSWORD '{password}';")
        created.append(("ROLE", user))
        for db in ((source, restored, rollback_db) if rollback_db else (source, restored)):
            pg.sql("postgres", f"CREATE DATABASE {db} OWNER {user};")
            created.append(("DATABASE", db))
        with tempfile.TemporaryDirectory(prefix="shoowcase-restore-") as state:
            specimen = seed(previous_binary or binary, dsn(source), token, state)
            if previous_binary:
                check(pg.sql(source, "SELECT MAX(version) FROM sc_schema_migrations").strip() == b"1", "PostgreSQL did not start on schema 1")
            original_audit = audit(pg, source)
            check(len(original_audit) == 5, "fixture expected five audited mutations")
            # Stream binary archive using native clients from exactly this PostgreSQL image.
            with tempfile.TemporaryFile() as archive:
                pg.run("pg_dump", "-Fc", "--no-acl", "-d", source, stdout=archive)
                archive_size = archive.tell()
                check(archive_size > 0, "empty pg_dump")
                archive.seek(0)
                # Same deployment/restored role exists; restore ownership as well as data.
                pg.run("pg_restore", "--exit-on-error", "--single-transaction", "--no-acl", "-d", restored, stdin=archive)
                if rollback_db:
                    archive.seek(0)
                    pg.run("pg_restore", "--exit-on-error", "--single-transaction", "--no-acl", "-d", rollback_db, stdin=archive)
            check(audit(pg, restored) == original_audit, "restored audit differs from original rows")
            verify_restored(binary, dsn(restored), token, state, specimen, original_audit, lambda: audit(pg, restored))
            if previous_binary:
                check(pg.sql(restored, "SELECT MAX(version) FROM sc_schema_migrations").strip() == b"2", "wrong migrated PostgreSQL schema")
                reject_downgrade(previous_binary, dsn(restored), token, state)
                verify_recovery_target(previous_binary, dsn(rollback_db), token, state, specimen)
                print("PASS: PostgreSQL schema 1→2; older binary rejected schema 2 and served the private site from a separately restored v1 snapshot.")
            rows = audit(pg, restored)
            check(len(rows) == 6 and sum(r["action"] == "rollback_site" for r in rows) == 1, "restored DB did not audit rollback")
            version = pg.sql("postgres", "SHOW server_version;").decode().strip()
            print(f"PASS: PostgreSQL {version}; {archive_size}-byte pg_dump/pg_restore; all 5 audit rows matched; two releases, private access, both asset files, request replay and post-restore rollback verified.")
    finally:
        for kind, name in reversed(created):
            pg.sql("postgres", f"DROP {kind} {name};")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--previous-binary", type=Path, help="seed data with v0.1.1 and verify SQLite and PostgreSQL upgrades plus binary rollback")
    parser.add_argument("--engine", choices=("podman", "docker"), default="podman")
    parser.add_argument("--container", required=True, help="disposable official PostgreSQL service, published on loopback")
    args = parser.parse_args()
    binary = args.binary.resolve(strict=True)
    previous = args.previous_binary.resolve(strict=True) if args.previous_binary else None
    if previous:
        check(subprocess.check_output([previous, "version"], text=True).strip().lstrip("v") == "0.1.1", "expected previous Shoowcase binary v0.1.1")
        sqlite_upgrade(previous, binary)
    rehearse(Postgres(args.engine, args.container), binary, previous)
