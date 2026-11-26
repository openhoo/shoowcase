#!/usr/bin/env python3
"""Start or stop the disposable loopback Grafana, Prometheus and OTLP/HTTP stack."""
import argparse
import json
import os
from pathlib import Path
import re
import secrets
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request

COMPOSE = Path(__file__).resolve().parents[1] / "deploy/observability/compose.yaml"
PORTS = {"GRAFANA": 13000, "PROMETHEUS": 19090, "OTLP": 24318}
BINDINGS = {"grafana": ("GRAFANA", "3000/tcp"), "prometheus": ("PROMETHEUS", "9090/tcp"),
            "collector": ("OTLP", "4318/tcp")}


def login_secret(directory):
    if os.name != "posix":
        raise ValueError("Use WSL for private generated credentials, or set SHOOWCASE_GRAFANA_PASSWORD_FILE")
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    if directory.is_symlink() or directory.stat().st_uid != os.getuid() or directory.stat().st_mode & 0o077:
        raise ValueError("Grafana's generated credential requires a private state directory")
    path = directory / "grafana-admin"
    try:
        # Container uid 472 must read the bind-mounted secret on Linux. The
        # containing directory (0700) keeps it private from other host users.
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o400)
    except FileExistsError:
        if path.is_symlink() or not path.is_file() or path.stat().st_uid != os.getuid():
            raise ValueError("unexpected Grafana secret file")
    else:
        with os.fdopen(fd, "w") as f:
            f.write(secrets.token_urlsafe(32) + "\n")
            os.fchmod(f.fileno(), 0o444)
    return path


def existing_project(engine, project):
    # Compose project names are global within an engine. Do not touch another
    # application's containers if the user accidentally chooses its name.
    ids = subprocess.check_output([engine, "ps", "-aq", "--filter",
                                   f"label=com.docker.compose.project={project}"], text=True).split()
    claimed = set()
    if ids:
        fields = "{{json .Config.Labels}}\t{{.State.Running}}\t{{json .NetworkSettings.Ports}}"
        for line in subprocess.check_output([engine, "inspect", "--format", fields, *ids], text=True).splitlines():
            raw_labels, running, raw_ports = line.split("\t")
            labels = json.loads(raw_labels)
            service = labels.get("com.docker.compose.service")
            ours = (labels.get("org.openhoo.shoowcase.observability") == "local" or
                    labels.get("com.docker.compose.project.config_files") == str(COMPOSE))
            if not ours or service not in BINDINGS:
                raise ValueError(f"Compose project {project} is already used by another stack")
            if running == "true":
                key, internal = BINDINGS[service]
                for entry in (json.loads(raw_ports) or {}).get(internal) or []:
                    if entry["HostIp"] == "127.0.0.1":
                        claimed.add((key, int(entry["HostPort"])))
    return bool(ids), claimed


def check_ports(ports, claimed):
    for name, port in ports.items():
        if (name, port) in claimed:
            continue
        with socket.socket() as sock:
            try:
                sock.bind(("127.0.0.1", port))
            except OSError as exc:
                raise RuntimeError(f"Cannot bind 127.0.0.1:{port}; set SHOOWCASE_{name}_PORT to a free port") from exc


def ready(grafana, prometheus):
    local = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    def read(port, path):
        with local.open(f"http://127.0.0.1:{port}{path}", timeout=2) as response:
            return json.load(response)
    end = time.monotonic() + 60
    while time.monotonic() < end:
        try:
            if (read(grafana, "/api/health").get("database") == "ok" and
                read(grafana, "/api/datasources/uid/shoowcase-prometheus").get("url") == "http://prometheus:9090" and
                any(t.get("health") == "up" and t.get("scrapePool") == "shoowcase-collector"
                    for t in read(prometheus, "/api/v1/targets?state=active")["data"]["activeTargets"])):
                return
        except (KeyError, OSError, ValueError, urllib.error.URLError):
            pass
        time.sleep(.4)
    raise RuntimeError("Grafana datasource or Prometheus collector scrape did not become ready within 60 seconds")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("up", "down", "status"))
    parser.add_argument("--engine", choices=("docker", "podman"), default="docker")
    parser.add_argument("--project", default="shoowcase-observability",
                        help="isolated Compose project (must start with shoowcase-)")
    args = parser.parse_args()
    if not re.fullmatch(r"shoowcase-[a-z0-9][a-z0-9-]{0,42}", args.project):
        parser.error("invalid Shoowcase project name")
    state = Path(os.getenv("XDG_STATE_HOME") or Path.home()/".local/state") / "shoowcase/observability" / args.engine / args.project
    env = os.environ.copy()
    ports = {k: int(env.get(f"SHOOWCASE_{k}_PORT", str(v))) for k, v in PORTS.items()}
    if len(set(ports.values())) != 3 or not all(1 <= p <= 65535 for p in ports.values()):
        raise ValueError("Grafana, Prometheus and OTLP require three distinct valid ports")
    previous, claimed = existing_project(args.engine, args.project)
    if args.action == "up":
        check_ports(ports, claimed)
    supplied = env.get("SHOOWCASE_GRAFANA_PASSWORD_FILE")
    if supplied:
        secret = Path(supplied).expanduser().resolve()
        if args.action == "up" and not secret.is_file():
            raise ValueError("provided Grafana admin secret is not a file")
    else:
        secret = login_secret(state) if args.action == "up" else state / "grafana-admin"
    env["SHOOWCASE_GRAFANA_PASSWORD_FILE"] = str(secret)
    prefix = [args.engine, "compose", "-p", args.project, "-f", str(COMPOSE)]
    def compose(*options):
        subprocess.run([*prefix, *options], env=env, check=True)
    def down():
        compose("down", "--volumes")
        if not supplied and secret.exists():
            secret.unlink()
            state.rmdir()
    if args.action == "up":
        try:
            compose("up", "-d")
            ready(ports["GRAFANA"], ports["PROMETHEUS"])
        except (OSError, RuntimeError, subprocess.CalledProcessError, KeyboardInterrupt):
            if not previous:
                down()
            raise
        print(f"Grafana: http://127.0.0.1:{ports['GRAFANA']}  (local viewer)")
        print(f"Prometheus: http://127.0.0.1:{ports['PROMETHEUS']}")
        print(f"OTLP/HTTP metrics: http://127.0.0.1:{ports['OTLP']}/v1/metrics")
    elif args.action == "down":
        down()
    else:
        compose("ps")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, RuntimeError, subprocess.CalledProcessError) as exc:
        print(str(exc), file=sys.stderr)
        sys.exit(1)
