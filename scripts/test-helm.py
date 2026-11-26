#!/usr/bin/env python3
"""Exercise Shoowcase's operator-facing Helm contract. Requires Helm and PyYAML."""
import json
import subprocess
import tempfile
import unittest
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]
CHART = ROOT / "charts/shoowcase"
EXAMPLE = CHART / "examples/production.yaml"
CONTROL = "studio.example.com"
SITES = "sites.example.net"
REQUIRED = {
    "config": {
        "controlHost": CONTROL,
        "sitesDomain": SITES,
        "oidc": {"issuer": "https://login.example.org", "clientId": "browser", "audience": "shoowcase-api"},
    },
    "existingSecret": {"name": "external-credentials"},
}


def helm(*args, input_values=None, check=True):
    result = subprocess.run(
        ["helm", *map(str, args)], cwd=ROOT, text=True, capture_output=True,
        input=json.dumps(input_values) if input_values is not None else None,
    )
    if check and result.returncode:
        raise AssertionError(f"{result.args}: {result.stdout}{result.stderr}")
    return result


def render(chart=CHART, *overrides):
    result = helm("template", "acceptance", chart, "-f", "-", *overrides, input_values=REQUIRED)
    return [doc for doc in yaml.safe_load_all(result.stdout) if doc]


def only(manifests, kind):
    matches = [doc for doc in manifests if doc["kind"] == kind]
    if len(matches) != 1:
        raise AssertionError(f"Expected one {kind}, found {len(matches)}")
    return matches[0]


class SmokeFixtureTest(unittest.TestCase):
    def test_pods_can_be_admitted_before_namespace_controllers_run(self):
        # kubectl apply submits each document in order. A fresh namespace can
        # still have no ServiceAccounts when the first Pod request arrives.
        accounts = set()
        fixture = ROOT / "scripts/fixtures/helm-backends.yaml"
        for doc in yaml.safe_load_all(fixture.read_text()):
            if doc["kind"] == "ServiceAccount":
                accounts.add(doc["metadata"]["name"])
            elif doc["kind"] == "Pod":
                account = doc["spec"].get("serviceAccountName", "default")
                self.assertIn(account, accounts, f'{doc["metadata"]["name"]} needs ServiceAccount {account}')


class ChartTest(unittest.TestCase):
    def test_private_install(self):
        docs = render()
        self.assertFalse({"Secret", "ClusterRole", "ClusterRoleBinding", "RoleBinding", "Ingress", "HorizontalPodAutoscaler"} &
                         {doc["kind"] for doc in docs})
        deploy = only(docs, "Deployment")["spec"]
        service = only(docs, "Service")["spec"]
        budget = only(docs, "PodDisruptionBudget")["spec"]
        selector = deploy["selector"]["matchLabels"]
        self.assertTrue(selector)
        self.assertEqual(service["selector"], selector)
        self.assertEqual(budget["selector"]["matchLabels"], selector)
        self.assertLessEqual(selector.items(), deploy["template"]["metadata"]["labels"].items())
        self.assertEqual(deploy["replicas"], 2)
        self.assertEqual(deploy["strategy"]["type"], "Recreate")
        self.assertEqual(budget["maxUnavailable"], 1)
        self.assertEqual(service.get("type", "ClusterIP"), "ClusterIP")

        pod = deploy["template"]["spec"]
        self.assertFalse(pod["automountServiceAccountToken"])
        app, = pod["containers"]
        self.assertRegex(app["image"], r"^ghcr\.io/openhoo/shoowcase@sha256:[0-9a-f]{64}$")
        ctx = {**pod["securityContext"], **app["securityContext"]}
        self.assertTrue(ctx["runAsNonRoot"])
        self.assertEqual(ctx["seccompProfile"]["type"], "RuntimeDefault")
        self.assertTrue(ctx["readOnlyRootFilesystem"])
        self.assertFalse(ctx["allowPrivilegeEscalation"])
        self.assertIn("ALL", ctx["capabilities"]["drop"])
        self.assertTrue({"cpu", "memory"} <= app["resources"]["requests"].keys())

        ports = {p["name"]: p["containerPort"] for p in app["ports"]}
        def port_number(port):
            return ports.get(port, port)
        self.assertEqual(port_number(service["ports"][0]["targetPort"]), 7878)
        for probe, path in (("startupProbe", "/healthz"), ("livenessProbe", "/healthz"), ("readinessProbe", "/readyz")):
            with self.subTest(probe=probe):
                get = app[probe]["httpGet"]
                self.assertEqual(get["path"], path)
                self.assertEqual(port_number(get["port"]), 7878)
                self.assertIn({"name": "Host", "value": CONTROL}, get["httpHeaders"])

        env = {item["name"]: item for item in app["env"]}
        self.assertEqual(env["SHOOWCASE_CONTROL_URL"]["value"], "https://" + CONTROL)
        self.assertEqual(env["SHOOWCASE_SITES_DOMAIN"]["value"], SITES)
        self.assertEqual(env["SHOOWCASE_PUBLIC"]["value"], "false")
        self.assertEqual(env["SHOOWCASE_METRICS_WORKSPACE"]["value"], "")
        self.assertEqual(env["SHOOWCASE_PUBLIC_REGISTRATION"]["value"], "false")
        for key in ["LOCAL_WORKSPACE", "OIDC_ADMIN_SUBJECT", "OIDC_ADMIN_WORKSPACE"]:
            self.assertEqual(env["SHOOWCASE_" + key]["value"], "")
        self.assertNotIn("OTEL_EXPORTER_OTLP_ENDPOINT", env)
        self.assertNotIn("OTEL_EXPORTER_OTLP_HEADERS", env)
        for var, key in {"DATABASE_URL": "database-url", "CLIENT_SECRET": "client-secret", "SESSION_KEY": "session-key"}.items():
            ref = env["SHOOWCASE_" + var]["valueFrom"]["secretKeyRef"]
            self.assertEqual((ref["name"], ref["key"]), ("external-credentials", key))

    def test_routed_install(self):
        peer = {"namespaceSelector": {"matchLabels": {"owner": "gateway"}}}
        rule = {"to": [{"ipBlock": {"cidr": "192.0.2.10/32"}}], "ports": [{"protocol": "TCP", "port": 5432}]}
        docs = render(CHART, "--set", "ingress.enabled=true,ingress.className=enterprise",
                      "--set", "ingress.controlTLSSecret=control-cert,ingress.sitesTLSSecret=wildcard-cert",
                      "--set", "networkPolicy.enabled=true,updateStrategy=RollingUpdate",
                      "--set-json", "networkPolicy.ingressFrom=" + json.dumps([peer]),
                      "--set-json", "networkPolicy.egress=" + json.dumps([rule]),
                      "--set", "caBundle.configMapName=enterprise-ca,caBundle.key=roots.pem",
                      "--set", "existingSecret.keys.databaseUrl=dsn")
        service = only(docs, "Service")
        routes = {}
        tls = {}
        for ingress in (doc["spec"] for doc in docs if doc["kind"] == "Ingress"):
            self.assertEqual(ingress["ingressClassName"], "enterprise")
            self.assertNotIn("defaultBackend", ingress)
            for entry in ingress["tls"]:
                for host in entry["hosts"]:
                    tls[host] = entry["secretName"]
            for entry in ingress["rules"]:
                path, = entry["http"]["paths"]
                self.assertEqual((path["pathType"], path["path"]), ("Prefix", "/"))
                routes[entry["host"]] = path["backend"]["service"]["name"]
        self.assertEqual(tls, {CONTROL: "control-cert", "*." + SITES: "wildcard-cert"})
        self.assertEqual(routes, {host: service["metadata"]["name"] for host in tls})

        policy = only(docs, "NetworkPolicy")["spec"]
        self.assertEqual(policy["podSelector"]["matchLabels"], service["spec"]["selector"])
        self.assertEqual(set(policy["policyTypes"]), {"Ingress", "Egress"})
        self.assertEqual(policy["ingress"][0]["from"], [peer])
        self.assertEqual(policy["egress"], [rule])
        deploy = only(docs, "Deployment")["spec"]
        self.assertEqual(deploy["strategy"]["type"], "RollingUpdate")
        pod = deploy["template"]["spec"]
        app, = pod["containers"]
        env = {item["name"]: item for item in app["env"]}
        self.assertEqual(env["SHOOWCASE_DATABASE_URL"]["valueFrom"]["secretKeyRef"]["key"], "dsn")
        ca_file = Path(env["SSL_CERT_FILE"]["value"])
        # Check that the named ConfigMap key reaches the file the binary will open.
        for vol in pod["volumes"]:
            if vol.get("configMap", {}).get("name") == "enterprise-ca":
                item, = vol["configMap"]["items"]
                self.assertEqual(item["key"], "roots.pem")
                mount, = [m for m in app["volumeMounts"] if m["name"] == vol["name"]]
                self.assertTrue(mount["readOnly"])
                mounted = Path(mount["mountPath"])
                self.assertEqual(mounted if mount.get("subPath") == item["path"] else mounted / item["path"], ca_file)
                break
        else:
            self.fail("CA ConfigMap was not mounted")

    def test_public_oidc_client_and_operator_telemetry(self):
        docs = render(CHART, "--set", "config.oidc.clientAuthMethod=none,config.metricsWorkspace=ops",
                      "--set", "telemetry.endpoint=https://collector.example.org:4318",
                      "--set", "telemetry.headersSecret.name=collector-credentials,telemetry.headersSecret.key=headers")
        app, = only(docs, "Deployment")["spec"]["template"]["spec"]["containers"]
        env = {item["name"]: item for item in app["env"]}
        self.assertEqual(env["SHOOWCASE_CLIENT_AUTH_METHOD"]["value"], "none")
        self.assertNotIn("SHOOWCASE_CLIENT_SECRET", env)
        self.assertEqual(env["SHOOWCASE_METRICS_WORKSPACE"]["value"], "ops")
        self.assertEqual(env["OTEL_EXPORTER_OTLP_ENDPOINT"]["value"], "https://collector.example.org:4318")
        self.assertEqual(env["OTEL_EXPORTER_OTLP_HEADERS"]["valueFrom"]["secretKeyRef"],
                         {"name": "collector-credentials", "key": "headers"})

    def test_password_accounts_alongside_oidc(self):
        for registration in ("true", "false"):
            with self.subTest(registration=registration):
                docs = render(CHART, "--set", "config.localAccounts.workspace=team",
                              "--set", "config.localAccounts.publicRegistration=" + registration,
                              "--set-string", "config.oidc.adminSubject=0123456,config.oidc.adminWorkspace=team")
                app, = only(docs, "Deployment")["spec"]["template"]["spec"]["containers"]
                env = {item["name"]: item.get("value") for item in app["env"]}
                self.assertEqual(env["SHOOWCASE_ISSUER"], "https://login.example.org")
                self.assertEqual(env["SHOOWCASE_LOCAL_WORKSPACE"], "team")
                self.assertEqual(env["SHOOWCASE_PUBLIC_REGISTRATION"], registration)
                self.assertEqual(env["SHOOWCASE_OIDC_ADMIN_SUBJECT"], "0123456")
                self.assertEqual(env["SHOOWCASE_OIDC_ADMIN_WORKSPACE"], "team")

    def test_autoscaling(self):
        docs = render(CHART, "--set", "autoscaling.enabled=true,autoscaling.minReplicas=3",
                      "--set", "autoscaling.maxReplicas=7,autoscaling.targetCPUUtilizationPercentage=65")
        deploy = only(docs, "Deployment")
        self.assertNotIn("replicas", deploy["spec"], "Helm upgrades must not reset HPA-managed replicas")
        hpa = only(docs, "HorizontalPodAutoscaler")["spec"]
        self.assertEqual((hpa["minReplicas"], hpa["maxReplicas"]), (3, 7))
        self.assertEqual(hpa["scaleTargetRef"], {"apiVersion": "apps/v1", "kind": "Deployment",
                                                "name": deploy["metadata"]["name"]})
        metric, = hpa["metrics"]
        self.assertEqual(metric, {"type": "Resource", "resource": {"name": "cpu", "target": {
            "type": "Utilization", "averageUtilization": 65}}})

    def test_rejects_invalid_installs(self):
        for args in ([], ["--set-string", "image.digest=latest"], ["--set", "replicaCount=0"],
                     ["--set", "ingress.enabled=true"], ["--set-string", "config.publicSharing=fales"],
                     ["--set", "config.oidc.audience=browser"],
                     ["--set", "config.localAccounts.publicRegistration=true"],
                     ["--set", "config.oidc.adminSubject=me"],
                     ["--set", "config.oidc.adminWorkspace=team"],
                     ["--set", "config.oidc.adminWorkspace=team,config.oidc.adminSubject=local:user"],
                     ["--set", "config.oidc.clientAuthMethod=private_key_jwt"],
                     ["--set", "telemetry.endpoint=http://collector.example.org"],
                     ["--set", "telemetry.headersSecret.name=credentials"], ["--set", "config.controlHost=" + SITES],
                     ["--set", "autoscaling.enabled=true,autoscaling.minReplicas=8,autoscaling.maxReplicas=2"],
                     ["--set", "config.oidc.clientSecret=unmanaged"], ["--set", "existingSecret.name="]):
            with self.subTest(flags=args):
                source = ["-f", "-"] if args else []
                result = helm("template", "invalid", CHART, *source, *args, input_values=REQUIRED, check=False)
                self.assertNotEqual(result.returncode, 0, "unexpected success")
                self.assertFalse(result.stdout.strip(), "invalid install must not emit resource manifests")

    def test_operator_example(self):
        helm("lint", "--strict", CHART, "-f", EXAMPLE)
        helm("template", "production", CHART, "-f", EXAMPLE)

    def test_release_archive(self):
        with tempfile.TemporaryDirectory() as temp:
            version = "1.2.3"
            reference = "ghcr.io/openhoo/shoowcase@sha256:" + "1" * 64
            subprocess.run([ROOT / "scripts/package-chart.sh", "v" + version, reference, temp],
                           cwd=ROOT, check=True)
            archive = Path(temp) / ("shoowcase-" + version + ".tgz")
            meta = yaml.safe_load(helm("show", "chart", archive).stdout)
            self.assertEqual((meta["version"], meta["appVersion"]), (version, "v" + version))
            deploy = only(render(archive), "Deployment")["spec"]
            # Kubernetes rejects upgrades that change a Deployment selector.
            self.assertEqual(deploy["selector"], only(render(), "Deployment")["spec"]["selector"])
            app, = deploy["template"]["spec"]["containers"]
            self.assertEqual(app["image"], reference)


if __name__ == "__main__":
    unittest.main(verbosity=2)
