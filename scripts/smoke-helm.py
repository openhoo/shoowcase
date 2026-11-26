#!/usr/bin/env python3
"""Install, exercise, upgrade and remove the chart on an explicitly named disposable cluster."""
import argparse
import base64
import contextlib
import http.client
import json
import re
import secrets
import select
import socket
import ssl
import subprocess
import tempfile
import time
from pathlib import Path
from urllib.parse import parse_qs, urlsplit

ROOT = Path(__file__).resolve().parents[1]
CONTROL = "studio.example.com"
SITES = "sites.example.net"
SELECTOR = "app.kubernetes.io/instance=smoke"


def run(args, *, data=None, timeout=120):
    return subprocess.run(args, input=data, text=True, stdout=subprocess.PIPE,
                          check=True, timeout=timeout).stdout


def manifest(kube, ns, kind, name, **fields):
    # Pass these via stdin; secret values never appear in process arguments or logs.
    body = {"apiVersion": "v1", "kind": kind, "metadata": {"name": name}, **fields}
    run([*kube, "-n", ns, "create", "-f", "-"], data=json.dumps(body))


def openssl(*args):
    result = subprocess.run(["openssl", *args], capture_output=True, text=True, timeout=30)
    if result.returncode:
        raise RuntimeError(f"openssl {args[0]} failed: {result.stderr}")


def tls_secret(kube, ns, tmp, name, dns_name):
    key, req, ext, cert = (tmp / (name + suffix) for suffix in (".key", ".csr", ".ext", ".crt"))
    ext.write_text(f"subjectAltName=DNS:{dns_name}\nbasicConstraints=critical,CA:FALSE\n"
                   "keyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\n"
                   "authorityKeyIdentifier=keyid:always\nsubjectKeyIdentifier=hash\n")
    openssl("req", "-newkey", "rsa:2048", "-nodes", "-subj", f"/CN={dns_name}", "-keyout", key, "-out", req)
    openssl("x509", "-req", "-in", req, "-CA", tmp / "ca.crt", "-CAkey", tmp / "ca.key", "-CAcreateserial",
            "-days", "2", "-extfile", ext, "-out", cert)
    manifest(kube, ns, "Secret", name, type="kubernetes.io/tls",
             stringData={"tls.key": key.read_text(), "tls.crt": cert.read_text()})


def traefik_values(kube, app, deps, tmp):
    tls_secret(kube, app, tmp, "smoke-control-tls", CONTROL)
    tls_secret(kube, app, tmp, "smoke-content-tls", "*." + SITES)
    def peer(namespace, label, value):
        return {"namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": namespace}},
                "podSelector": {"matchLabels": {label: value}}}
    return {"ingress": {"enabled": True, "className": "traefik", "controlTLSSecret": "smoke-control-tls",
                        "sitesTLSSecret": "smoke-content-tls"},
            "networkPolicy": {"enabled": True, "ingressFrom": [peer("kube-system", "app.kubernetes.io/name", "traefik")],
                              "egress": [
                                  {"to": [peer("kube-system", "k8s-app", "kube-dns")],
                                   "ports": [{"port": 53, "protocol": proto} for proto in ("UDP", "TCP")]},
                                  {"to": [peer(deps, "fixture", "backends")],
                                   "ports": [{"port": port, "protocol": "TCP"} for port in (5432, 8443, 443)]}]}}


def fixtures(kube, app, deps, tmp):
    hostname = f"backends.{deps}.svc"
    issuer = f"https://{hostname}"
    ca, ca_key = tmp / "ca.crt", tmp / "ca.key"
    openssl("req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2",
            "-subj", "/CN=Shoowcase smoke CA", "-addext", "basicConstraints=critical,CA:TRUE",
            "-addext", "keyUsage=critical,keyCertSign,cRLSign", "-addext", "subjectKeyIdentifier=hash",
            "-keyout", ca_key, "-out", ca)
    tls_secret(kube, deps, tmp, "fixture-tls", hostname)
    password = secrets.token_urlsafe(24)
    manifest(kube, deps, "Secret", "fixture-password", stringData={"password": password})
    discover = {"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
                "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys",
                "code_challenge_methods_supported": ["S256"],
                "token_endpoint_auth_methods_supported": ["client_secret_basic"]}
    conf = ("server { listen 8443 ssl; ssl_certificate /tls/tls.crt; ssl_certificate_key /tls/tls.key; "
            "location = /.well-known/openid-configuration { default_type application/json; "
            f"return 200 '{json.dumps(discover)}'; }} location / {{ return 404; }} }}")
    manifest(kube, deps, "ConfigMap", "fixture-discovery", data={"default.conf": conf})
    run([*kube, "-n", deps, "apply", "-f", ROOT / "scripts/fixtures/helm-backends.yaml"])
    manifest(kube, app, "ConfigMap", "smoke-ca", data={"ca-bundle.pem": ca.read_text()})
    manifest(kube, app, "Secret", "smoke-credentials", stringData={
        "database-url": f"postgres://postgres:{password}@{hostname}/shoowcase?sslmode=verify-full"
                        "&sslrootcert=/etc/shoowcase/ca/ca-bundle.pem",
        "session-key": base64.urlsafe_b64encode(secrets.token_bytes(32)).rstrip(b"=").decode(),
        "client-secret": secrets.token_urlsafe(24)})
    run([*kube, "-n", deps, "wait", "--for=condition=Ready", "pod/backends", "--timeout=180s"], timeout=190)
    return {"config": {"controlHost": CONTROL, "sitesDomain": SITES,
                       "oidc": {"issuer": issuer, "clientId": "browser", "audience": "shoowcase-api"}},
            "caBundle": {"configMapName": "smoke-ca", "key": "ca-bundle.pem"},
            "existingSecret": {"name": "smoke-credentials"}}


@contextlib.contextmanager
def forward(kube, target, remote):
    with subprocess.Popen([*kube, "port-forward", "--address=127.0.0.1", target, f":{remote}"],
                          stdout=subprocess.PIPE, text=True) as proc:
        try:
            if not select.select([proc.stdout], [], [], 20)[0]:
                raise RuntimeError("port-forward did not start")
            line = proc.stdout.readline()
            match = re.search(r"Forwarding from 127\.0\.0\.1:(\d+)", line)
            if not match:
                raise RuntimeError(f"port-forward failed: {line}")
            yield int(match.group(1))
        finally:
            proc.terminate()


def probe(port, path, status, host=CONTROL, *, header=None):
    c = http.client.HTTPConnection("127.0.0.1", port, timeout=6)
    try:
        c.request("GET", path, headers={"Host": host})
        r = c.getresponse()
        if r.status != status:
            raise RuntimeError(f"GET {host}{path}: expected {status}, received {r.status}")
        body = r.read()
        return r.getheader(header, "") if header else body
    finally:
        c.close()


def https_probe(port, host, path, status, tls):
    # Use the loopback forwarding socket with the real DNS name for SNI,
    # certificate hostname verification and HTTP Host.
    with socket.create_connection(("127.0.0.1", port), timeout=7) as raw:
        with tls.wrap_socket(raw, server_hostname=host) as sock:
            conn = http.client.HTTPConnection(host)
            conn.sock = sock
            try:
                conn.request("GET", path)
                response = conn.getresponse()
                body = response.read()
                if response.status != status:
                    raise RuntimeError(f"HTTPS {host}{path}: expected {status}, got {response.status}")
                return body
            finally:
                conn.close()


def check_traefik(kube, tmp, fixtures_k, service):
    ctl = [*kube, "-n", "kube-system"]
    run([*ctl, "wait", "--for=create", "deployment/traefik", "--timeout=140s"], timeout=150)
    run([*ctl, "rollout", "status", "deployment/traefik", "--timeout=140s"], timeout=150)
    with forward(ctl, "svc/traefik", 443) as port:
        tls = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
        tls.minimum_version = ssl.TLSVersion.TLSv1_2
        tls.load_verify_locations(cafile=str(tmp / "ca.crt"))
        # Wait for the controller's asynchronous routing and certificate configuration.
        deadline = time.monotonic() + 90
        while True:
            try:
                if https_probe(port, CONTROL, "/readyz", 200, tls).strip() != b"ready":
                    raise RuntimeError("control host did not reach Shoowcase")
                bridge = https_probe(port, "demo." + SITES, "/auth/runtime/bridge.js", 200, tls)
                if b"/auth/runtime/worker.js" not in bridge:
                    raise RuntimeError("wildcard host did not reach the content runtime")
                break
            except (OSError, http.client.HTTPException, RuntimeError):
                if time.monotonic() >= deadline:
                    raise
                time.sleep(1)
        https_probe(port, "demo." + SITES, "/.well-known/oauth-protected-resource/mcp", 404, tls)
    # The same Service that the ingress can reach must refuse direct connections
    # from a pod in the fixtures namespace. Distinguish a kubectl failure from denial.
    result = run([*fixtures_k, "exec", "backends", "-c", "postgres", "--", "bash", "-c",
                  """if timeout 5 bash -c 'exec 3<>/dev/tcp/"$1"/"$2"' _ "$1" "$2"; then
                       echo reachable; else echo denied; fi""", "_", service["clusterIP"],
                  str(service["ports"][0]["port"])])
    if result.strip() != "denied":
        raise RuntimeError("NetworkPolicy allowed a non-ingress pod to reach Shoowcase")
    print("Passed: control and wildcard HTTPS with separate verified certificates; "
          "restricted ingress and dependency egress; direct fixture-to-app connection denied.")


def smoke(context, chart, traefik=False):
    if not re.fullmatch(r"(k3d|kind)-shoowcase-[a-z0-9-]+", context):
        raise ValueError("use an explicit k3d-shoowcase-* or kind-shoowcase-* disposable context")
    kube = ["kubectl", "--context", context]
    ns = "smoke-" + secrets.token_hex(5)
    dep_ns = ns + "-deps"
    fixtures_k = [*kube, "-n", dep_ns]
    k = [*kube, "-n", ns]
    helm = ["helm", "--kube-context", context, "-n", ns]
    def get(kind):
        return json.loads(run([*k, "get", kind, "-l", SELECTOR, "-o", "json"]))["items"]
    def sql(query):
        return run([*fixtures_k, "exec", "-i", "backends", "-c", "postgres", "--", "psql",
                    "-U", "postgres", "-d", "postgres", "-v", "ON_ERROR_STOP=1"], data=query)
    def check_ready():
        pods = get("pods")
        if len(pods) != 2 or not all(p["status"]["containerStatuses"][0]["ready"] for p in pods):
            raise RuntimeError("expected two ready Shoowcase replicas")
        return {p["metadata"]["uid"] for p in pods}
    with tempfile.TemporaryDirectory() as directory, contextlib.ExitStack() as cleanup:
        tmp = Path(directory)
        for name in (dep_ns, ns):
            run([*kube, "create", "namespace", name])
            cleanup.callback(run, [*kube, "delete", "namespace", name, "--wait=false"])
        run([*kube, "label", "namespace", ns, "pod-security.kubernetes.io/enforce=restricted"])
        try:
            values = tmp / "values.json"
            settings = fixtures(kube, ns, dep_ns, tmp)
            if traefik:
                settings.update(traefik_values(kube, ns, dep_ns, tmp))
            values.write_text(json.dumps(settings))
            common = ["smoke", chart, "-f", values, "--wait", "--timeout=4m"]
            run([*helm, "install", *common], timeout=260)
            old_pods = check_ready()
            [service] = get("svc")
            if traefik:
                check_traefik(kube, tmp, fixtures_k, service["spec"])
            with forward(k, "svc/" + service["metadata"]["name"], service["spec"]["ports"][0]["port"]) as port:
                probe(port, "/healthz", 200)
                probe(port, "/readyz", 200)
                probe(port, "/healthz", 421, "wrong.example.org")
                probe(port, "/metrics", 404)
                location = urlsplit(probe(port, "/auth/login", 303, header="Location"))
                auth = parse_qs(location.query)
                expected = settings["config"]["oidc"]["issuer"] + "/authorize"
                if (location._replace(query="").geturl() != expected
                        or auth.get("client_id") != ["browser"]
                        or auth.get("redirect_uri") != [f"https://{CONTROL}/auth/callback"]
                        or auth.get("code_challenge_method") != ["S256"]
                        or not re.fullmatch(r"[A-Za-z0-9_-]{43}", auth.get("code_challenge", [""])[0])):
                    raise RuntimeError("browser login did not send the expected PKCE challenge to the issuer")
                resource = json.loads(probe(port, "/.well-known/oauth-protected-resource/mcp", 200))
                if resource["resource"] != "https://" + CONTROL + "/mcp":
                    raise RuntimeError("unexpected MCP resource URL")
                sql("ALTER DATABASE shoowcase ALLOW_CONNECTIONS false;\n"
                    "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='shoowcase';")
                try:
                    probe(port, "/readyz", 503)
                    probe(port, "/healthz", 200)
                finally:
                    sql("ALTER DATABASE shoowcase ALLOW_CONNECTIONS true;")
                probe(port, "/readyz", 200)
            run([*helm, "upgrade", *common, "--set", "config.metricsWorkspace=smoke-operators"], timeout=260)
            if old_pods & check_ready():
                raise RuntimeError("upgrade did not replace all original pods")
            with forward(k, "svc/" + service["metadata"]["name"], service["spec"]["ports"][0]["port"]) as port:
                probe(port, "/metrics", 401)
            run([*helm, "uninstall", "smoke", "--wait", "--timeout=2m"], timeout=130)
            if get("deployments") or get("svc"):
                raise RuntimeError("workload still present after uninstall")
            run([*k, "get", "secret", "smoke-credentials", "-o", "name"])
            print("Passed: two restricted replicas, TLS dependencies, Host routing, OIDC PKCE redirect, "
                  "metrics disabled by default and guarded after upgrade, database outage/recovery, "
                  "and uninstall with external Secret retained.")
        except Exception:
            for args in ([*fixtures_k, "get", "pods"], [*k, "get", "pods"], [*k, "get", "events"]):
                subprocess.run(args, check=False, timeout=20)
            raise


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--context", required=True)
    parser.add_argument("--chart", default=str(ROOT / "charts/shoowcase"), help="chart directory or package")
    parser.add_argument("--traefik", action="store_true", help="test k3s Traefik ingress and enforced NetworkPolicy")
    args = parser.parse_args()
    smoke(args.context, args.chart, args.traefik)
