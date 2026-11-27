#!/usr/bin/env python3
"""Exercise a real pg_dump/pg_restore on two new databases in a disposable local PostgreSQL container."""
import argparse
import contextlib
import json
import os
import secrets
import socket
import sqlite3
from dataclasses import dataclass, field
from html.parser import HTMLParser
import subprocess
import sys
import tempfile
import time
from pathlib import Path
from urllib.parse import quote, urlencode

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
    env = {k: v for k, v in os.environ.items() if not k.startswith(("SHOOWCASE_", "OTEL_"))}
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


def ledger(pg, db):
    result = pg.sql(db, "SELECT row_to_json(a) FROM sc_workspace_audit a ORDER BY workspace,seq;")
    return [json.loads(line) for line in result.splitlines()]


def audit_export(reader, port, after=0):
    page = reader.request("GET", f"http://localhost:{port}/api/audit?after={after}").expect(200, "tenant audit export")
    result = json.loads(page.body)
    events = result["events"]
    check(result["has_more"] is False, "fixture no longer fits a single audit page")
    check([e["sequence"] for e in events] == list(range(after + 1, after + len(events) + 1)),
          "restored ledger contains an unexpected sequence gap")
    check(result["next_after"] == after + len(events), "wrong tenant audit checkpoint")
    return result


SUPPORTED_PRIORS = {
    "0.1.1": 1, "0.2.0": 2, "0.3.2": 4, "0.6.0": 5, "0.9.0": 6,
    "0.10.0": 7, "0.10.1": 7, "0.10.2": 7, "0.10.3": 7, "0.10.4": 7, "0.10.5": 7, "0.10.6": 7,
}
CURRENT_SCHEMA = 9
MEMBER_SCHEMAS = {2, 4, 5, 6, 7, 8, 9}
MEMBER_NAME = "Restore rehearsal member"


class Inputs(HTMLParser):
    """Read only the form inputs required for the account/key rehearsal."""
    def __init__(self, page):
        super().__init__()
        self.values = {}
        self.feed(page.body.decode())

    def handle_starttag(self, tag, attrs):
        props = dict(attrs)
        if tag == "input":
            for key in ("csrf", "creation_token", "agent-secret"):
                if key in (props.get("name"), props.get("id")) and props.get("value"):
                    self.values[key] = props["value"]

    def value(self, key):
        check(key in self.values, f"missing {key} input")
        return self.values[key]


def submit(browser, control, path, values):
    return browser.request("POST", control + path, urlencode(values),
                           {"Origin": control, "Content-Type": "application/x-www-form-urlencoded"})


def enroll(browser, control):
    form = browser.navigate(control + "/account").expect(200, "enrollment")
    submit(browser, control, "/account", {"action": "enroll", "display_name": MEMBER_NAME,
                                         "csrf": Inputs(form).value("csrf")}).expect(303, "create member")


def issue_reader_key(browser, control):
    form = Inputs(browser.navigate(control + "/keys").expect(200, "key form"))
    receipt = submit(browser, control, "/keys",
                     {"action": "create", "scope": "reader", "name": "Rehearsal reader", "days": "1",
                      "csrf": form.value("csrf"), "creation_token": form.value("creation_token")})
    return Inputs(receipt.expect(200, "create reader key")).value("agent-secret")


def check_member_and_key(port, specimen, expected_release):
    control = f"http://localhost:{port}"
    page = Browser(port).navigate(control + "/account").expect(200, "saved membership")
    check(MEMBER_NAME.encode() in page.body and b"Your account" in page.body, "original local member missing")
    reader = MCP(port, specimen.reader_key, "restored-personal-key")
    check(reader.call("get_site", specimen.key)["release_id"] == expected_release,
          "existing personal key cannot read its original private site")
    reader.call("create_site", {"name": "Must stay read-only", "request_id": secrets.token_hex(16)}, "forbidden")


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
    reader_key: str = field(default="", repr=False)


def seed(binary, dsn, token, state, with_member=False):
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
        specimen = Fixture(home, css, updated, key, write, first, draft2, second)
        if with_member:
            control = f"http://localhost:{port}"
            enroll(visitor, control)
            specimen.reader_key = issue_reader_key(visitor, control)
            check_member_and_key(port, specimen, specimen.second["release_id"])
        return specimen


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
        control = f"http://localhost:{port}"
        if specimen.reader_key:
            check_member_and_key(port, specimen, specimen.second["release_id"])
        else:
            enroll(reader, control)
        before = audit_export(reader, port)
        if specimen.reader_key:
            for action in ("account_created", "key_created"):
                check(sum(e["action"] == action and e["subject"] == "local-user" for e in before["events"]) == 1,
                      f"legacy {action} event was not preserved exactly once")
        site_events = [e for e in before["events"] if e.get("target") == specimen.key["site_id"]]
        def signature(e):
            return (e["action"], e.get("draft_revision"), e.get("live_revision", ""), e.get("release_id", ""))
        # Legacy site timestamps have millisecond resolution. Equal-time backfill
        # is deterministic, but random legacy IDs do not encode mutation order.
        revisions = [e for e in site_events if e["action"] == "create_site"]
        check(len(revisions) == 1 and revisions[0].get("draft_revision") and not revisions[0].get("live_revision"),
              "create event lost draft/live distinction")
        d1, l1 = specimen.first["live_revision"], specimen.first["release_id"]
        d2, l2 = specimen.draft2["revision"], specimen.second["release_id"]
        expected = [("upload_files", d1, "", ""), ("publish_site", d1, d1, l1),
                    ("upload_files", d2, d1, l1), ("publish_site", d2, d2, l2)]
        check(len(site_events) == 5 and sorted(signature(e) for e in site_events if e["action"] != "create_site") == sorted(expected),
              "restored or backfilled ledger lost a site operation, draft, live revision or release reference")
        replayed = mcp.call("upload_files", specimen.write)
        check(replayed["revision"] == specimen.draft2["revision"] and get_audit() == original_audit,
              "replayed mutation created an extra revision or audit record")
        check(audit_export(reader, port) == before, "replay changed the exported workspace ledger")
        rolled = mcp.call("rollback_site", {**specimen.key, "release_id": specimen.first["release_id"],
                          "expected_release_id": specimen.second["release_id"], "request_id": secrets.token_hex(16)})
        check(rolled["live_revision"] == d1, "restored rollback pointer mismatch")
        resume = audit_export(reader, port, before["next_after"])
        check(len(resume["events"]) == 1, "resumed export missed or duplicated the rollback")
        event = resume["events"][0]
        check(event["action"] == "rollback_site" and event.get("target") == specimen.key["site_id"] and
              event.get("draft_revision") == d2 and event.get("live_revision") == d1 and event.get("release_id") == l1,
              "rollback event did not distinguish the unchanged draft from the restored live revision")
        check(not audit_export(reader, port, resume["next_after"])["events"], "completed checkpoint returned duplicates")
        check(reader.navigate(found["site_url"]).body == specimen.home, "rollback did not restore original page bytes")
        check(mcp.call("get_site", {**specimen.key, "path": "index.html"})["file"]["text"].encode() == specimen.updated,
              "rollback altered unpublished draft")


def reject_downgrade(binary, dsn, token, state):
    env = {k: v for k, v in os.environ.items() if not k.startswith(("SHOOWCASE_", "OTEL_"))}
    env.update(SHOOWCASE_LISTEN="127.0.0.1:0", SHOOWCASE_DEV_TOKEN=token,
               SHOOWCASE_DATABASE_URL=dsn, SHOOWCASE_STATE_DIR=state, SHOOWCASE_PUBLIC="false")
    try:
        proc = subprocess.run([binary, "-dev"], env=env, cwd=state, capture_output=True, timeout=6)
    except subprocess.TimeoutExpired:
        raise RuntimeError("Older binary started on a migrated database") from None
    check(proc.returncode != 0 and b"cannot initialize database" in proc.stderr,
          "Older binary failed to reject the migrated database at startup")


def verify_prior_reads(previous, dsn, token, state, specimen, *, rolled_back=False):
    release = specimen.first if rolled_back else specimen.second
    body = specimen.home if rolled_back else specimen.updated
    with server(previous, dsn, token, state) as (port, mcp):
        if specimen.reader_key:
            check_member_and_key(port, specimen, release["release_id"])
        recovered = mcp.call("get_site", specimen.key)
        check(recovered["release_id"] == release["release_id"] and recovered["revision"] == specimen.draft2["revision"],
              "Prior binary did not recover the expected release and draft")
        Browser(port).request("GET", recovered["site_url"]).expect(303, "prior binary private access")
        check(Browser(port).navigate(recovered["site_url"]).body == body, "prior binary served different content")


def verify_prior_database(previous, dsn, token, state, specimen, previous_schema):
    if previous_schema < CURRENT_SCHEMA:
        reject_downgrade(previous, dsn, token, state)
        return "prior binary rejected the newer schema"
    # The candidate rolled back the live release on this database. A compatible
    # prior binary must read that decision without reverting the newer draft.
    verify_prior_reads(previous, dsn, token, state, specimen, rolled_back=True)
    return "prior binary served the candidate's rolled-back release with the newer draft intact"


def sqlite_upgrade(previous, candidate, previous_schema):
    token = secrets.token_urlsafe(32)
    with tempfile.TemporaryDirectory(prefix="shoowcase-sqlite-upgrade-") as state, tempfile.TemporaryDirectory(prefix="shoowcase-sqlite-backup-") as backup:
        specimen = seed(previous, "", token, state, with_member=previous_schema in MEMBER_SCHEMAS)
        with contextlib.closing(sqlite3.connect(Path(state)/"shoowcase.db")) as db, contextlib.closing(sqlite3.connect(Path(backup)/"shoowcase.db")) as saved:
            check(db.execute("SELECT max(version) FROM sc_schema_migrations").fetchone() == (previous_schema,), f"SQLite did not start on schema {previous_schema}")
            db.backup(saved)

        def sql(query):
            with contextlib.closing(sqlite3.connect(Path(state)/"shoowcase.db")) as db:
                return db.execute(query).fetchall()

        get_audit = lambda: sql("SELECT * FROM sc_audit ORDER BY id")
        before = get_audit()
        check(len(before) == 5, "expected five audit rows before migration")
        verify_restored(candidate, "", token, state, specimen, before, get_audit)
        check(sql("SELECT max(version) FROM sc_schema_migrations") == [(CURRENT_SCHEMA,)], "wrong migrated SQLite schema")
        check(sql("SELECT count(*) FROM sc_sites s WHERE NOT EXISTS (SELECT 1 FROM sc_reserved_sites r WHERE r.id=s.id)") == [(0,)], "SQLite failed to reserve an existing site address")
        check(sql("SELECT count(*) FROM sc_deleted_identities") == [(0,)], "SQLite migration marked a previously active identity as deleted")
        prior = verify_prior_database(previous, "", token, state, specimen, previous_schema)
        verify_prior_reads(previous, "", token, backup, specimen)
        print(f"PASS: SQLite schema {previous_schema}→{CURRENT_SCHEMA}; preserved two releases, private content, workspace audit revisions, cursor and replay; {prior}; pre-upgrade snapshot restored.")


def rehearse(pg, binary, previous_binary=None, previous_schema=None):
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
            specimen = seed(previous_binary or binary, dsn(source), token, state, with_member=previous_schema in MEMBER_SCHEMAS)
            if previous_binary:
                check(int(pg.sql(source, "SELECT MAX(version) FROM sc_schema_migrations")) == previous_schema,
                      f"PostgreSQL did not start on schema {previous_schema}")
            original_audit = audit(pg, source)
            original_ledger = None if previous_schema in (1, 2) else ledger(pg, source)
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
            check(audit(pg, restored) == original_audit, "restored site audit differs from original rows")
            if original_ledger is not None:
                check(original_ledger and ledger(pg, restored) == original_ledger,
                      "restored tenant ledger differs from source rows or checkpoints")
            verify_restored(binary, dsn(restored), token, state, specimen, original_audit, lambda: audit(pg, restored))
            if previous_binary:
                check(int(pg.sql(restored, "SELECT MAX(version) FROM sc_schema_migrations")) == CURRENT_SCHEMA, "wrong migrated PostgreSQL schema")
                check(int(pg.sql(restored, "SELECT count(*) FROM sc_sites s WHERE NOT EXISTS (SELECT 1 FROM sc_reserved_sites r WHERE r.id=s.id)")) == 0, "PostgreSQL failed to reserve an existing site address")
                check(int(pg.sql(restored, "SELECT count(*) FROM sc_deleted_identities")) == 0, "PostgreSQL migration marked a previously active identity as deleted")
                prior = verify_prior_database(previous_binary, dsn(restored), token, state, specimen, previous_schema)
                verify_prior_reads(previous_binary, dsn(rollback_db), token, state, specimen)
                print(f"PASS: PostgreSQL schema {previous_schema}→{CURRENT_SCHEMA}; {prior}; prior binary also served the private site from a separately restored snapshot.")
            rows = audit(pg, restored)
            check(len(rows) == 6 and sum(r["action"] == "rollback_site" for r in rows) == 1, "restored DB did not audit rollback")
            version = pg.sql("postgres", "SHOW server_version;").decode().strip()
            print(f"PASS: PostgreSQL {version}; {archive_size}-byte pg_dump/pg_restore; five site events matched; tenant audit cursor, draft and live revisions, private access, request replay and rollback verified.")
    finally:
        for kind, name in reversed(created):
            pg.sql("postgres", f"DROP {kind} {name};")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--previous-binary", type=Path, help="seed with a supported prior release, then verify SQLite/PostgreSQL upgrade and rollback")
    parser.add_argument("--previous-schema", type=int, choices=(8,), help="expected schema for a schema 8 build whose version is not listed; checked against both seeded databases")
    parser.add_argument("--engine", choices=("podman", "docker"), default="podman")
    parser.add_argument("--container", required=True, help="disposable official PostgreSQL service, published on loopback")
    args = parser.parse_args()
    def checked_binary(path, option):
        if not path.is_file() or not os.access(path, os.X_OK):
            parser.error(f"{option} must point to an executable file: {path}")
        return path.resolve()

    binary = checked_binary(args.binary, "--binary")
    previous = checked_binary(args.previous_binary, "--previous-binary") if args.previous_binary else None
    previous_schema = None
    if args.previous_schema and not previous:
        parser.error("--previous-schema requires --previous-binary")
    if previous:
        try:
            version = subprocess.check_output([previous, "version"], stderr=subprocess.DEVNULL, text=True, timeout=5).strip().removeprefix("v")
        except (OSError, UnicodeError, subprocess.SubprocessError):
            parser.error("--previous-binary did not report a Shoowcase version")
        if version in SUPPORTED_PRIORS and args.previous_schema and SUPPORTED_PRIORS[version] != args.previous_schema:
            parser.error("--previous-schema disagrees with the known release")
        if version not in SUPPORTED_PRIORS and not args.previous_schema:
            parser.error("--previous-binary must be a supported Shoowcase release: " + ", ".join("v" + v for v in SUPPORTED_PRIORS))
        previous_schema = SUPPORTED_PRIORS.get(version, args.previous_schema)
        sqlite_upgrade(previous, binary, previous_schema)
    rehearse(Postgres(args.engine, args.container), binary, previous, previous_schema)
    if previous_schema in MEMBER_SCHEMAS:
        print(f"PASS: On both backends the v{version} member and personal reader key survived the upgrade and snapshot rollback; the same key read the private site and was denied a write.")
