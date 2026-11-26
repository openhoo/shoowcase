# Shoowcase on Kubernetes

Run the Shoowcase service behind your HTTPS gateway, using your PostgreSQL database and OIDC provider. The chart starts two replicas of the released image pinned by SHA-256. It creates a ClusterIP Service, a dedicated ServiceAccount and a disruption budget. The pods run as UID/GID 65532 with a read-only filesystem, no capabilities and no Kubernetes API token. Public site sharing and Ingress are off by default. There are no chart dependencies, cluster-wide permissions or database volumes.

## Prepare an installation

1. Choose **two separate registrable domains**: for example, `studio.example.com` for control and `*.sites.example.net` for previews and published content. Two subdomains of `example.com` are insufficient. Set up the exact and wildcard DNS records and certificates. Your gateway must preserve the incoming Host and Origin, redirect HTTP to HTTPS, and reject unknown hosts. Disable shared caching. Exclude query strings, credentials, cookies and bodies from logs. Set request size, timeout and rate limits at the gateway; allow the upload sizes your teams use.
2. Provision PostgreSQL with verified TLS and a database owner able to initialize and migrate the Shoowcase schema. Configure and rehearse backup/restore. Register an OIDC authorization-code client with callback `https://studio.example.com/auth/callback`. Its discovery document must advertise PKCE S256. By default the chart uses a confidential client and Shoowcase selects a supported secret method. To match the client registration, set `config.oidc.clientAuthMethod` to `client_secret_basic`, `client_secret_post`, or `none` (public client, no secret). See [OIDC and PKCE](https://github.com/openhoo/shoowcase/blob/main/docs/oidc.md). Give the MCP API its own audience, distinct from the browser client ID. All replicas must reach the same database and issuer. See the [identity and access contract](https://github.com/openhoo/shoowcase/blob/main/docs/operations.md#production-prerequisites).
3. Create the release namespace (the commands below use `shoowcase`). Have your secret manager create a Kubernetes Secret there, for example `shoowcase-credentials`. The chart only references it. **Do not put raw credentials in values files or pass them through Helm.** By default the Secret needs:

   | Secret key | Value |
   | --- | --- |
   | `database-url` | PostgreSQL connection string with `sslmode=verify-full` and server hostname. |
   | `client-secret` | Confidential browser client secret from your issuer. Omit for `clientAuthMethod: none`. |
   | `session-key` | One shared key from `shoowcase keygen`: 32 random bytes in unpadded base64url. Store the output without a trailing newline. |

4. Copy [examples/production.yaml](examples/production.yaml) to `my-values.yaml`. Replace the example hostnames, issuer, audience and Secret names. The example enables TLS Ingress and NetworkPolicy: set your actual IngressClass, TLS Secrets, and network rules before installation. A private platform gateway can route directly to the ClusterIP instead; set `ingress.enabled: false` and configure the same two hosts there.

## Install and verify

From the repository root:

```sh
helm lint --strict charts/shoowcase -f my-values.yaml
helm template shoowcase charts/shoowcase -n shoowcase -f my-values.yaml |
  kubectl -n shoowcase apply --dry-run=server --validate=strict -f -
helm upgrade --install shoowcase charts/shoowcase -n shoowcase -f my-values.yaml --wait --timeout 5m
kubectl -n shoowcase get deployment,pods,svc,pdb -l app.kubernetes.io/instance=shoowcase
```

For a **release containing the chart**, download `shoowcase-X.Y.Z.tgz`, `chart.txt` and `SHA256SUMS` from that release. Verify the archive with `gh attestation verify shoowcase-X.Y.Z.tgz --repo openhoo/shoowcase` and its matching checksum. Use the archive path in place of `charts/shoowcase` in the commands above. To install from OCI, verify `chart.txt` with `gh attestation verify chart.txt --repo openhoo/shoowcase`. Then run `gh attestation verify "oci://$(cat chart.txt)" --repo openhoo/shoowcase` and use `"oci://$(cat chart.txt)"` as the chart argument for `helm template` or `helm upgrade --install`. Lint the downloaded archive locally. The OCI manifest digest, chart archive checksum and application image digest identify three different objects. Published charts default to the exact image from the same release. See the [release guide](https://github.com/openhoo/shoowcase/blob/main/deploy/README.md).

Probe the control origin after deployment: `/healthz` tests the process, `/readyz` also checks PostgreSQL, and `/.well-known/oauth-protected-resource/mcp` must advertise your control URL. The legacy control-host `/metrics` returns 404 by default. Set `config.metricsWorkspace` to the exact operator workspace ID to enable it. A scraper must send the control Host and an IdP access token with effective admin rights in that workspace; personal keys and other tenant admins cannot scrape it. Test the real issuer login and MCP client, publish a **private** site and check both its HTML and assets as a teammate, a signed-out visitor and a separate workspace before opening traffic.

## Configuration and day-two operations

| Area | How to operate it |
| --- | --- |
| Image and scheduling | Override `image.repository` and `image.digest` together for an internal mirror; use `imagePullSecrets` if needed. Size `resources` for your workload. Default spreading prefers different nodes; choose hard node/zone constraints only when the cluster can satisfy them. |
| Scaling | Set `replicaCount` for fixed capacity. To scale by CPU, enable `autoscaling.enabled`, set minimum/maximum replicas and a CPU utilization target, and provide working Kubernetes resource metrics. Budget database connections and apply aggregate rate limits as replicas increase. |
| Ingress and network policy | With `networkPolicy.enabled: true`, empty peer/rule lists deny traffic in that direction on a CNI that enforces NetworkPolicy. Allow your gateway to reach TCP 7878; allow the resolver (TCP and UDP), PostgreSQL and all issuer endpoints. Adapt selectors for NodeLocal DNS, host-networked gateways or a managed database. Kubernetes NetworkPolicy does not track a DNS name's changing addresses. |
| Telemetry | To export traces, metrics and logs, set `telemetry.endpoint` to an HTTPS OTLP/HTTP base URL and allow the collector through your egress policy. For collector authentication, reference an existing Secret with `telemetry.headersSecret.name` and `.key` (default `otlp-headers`); the key contains the SDK’s encoded `OTEL_EXPORTER_OTLP_HEADERS` value. With no endpoint the app sends no telemetry. See [OpenTelemetry](https://github.com/openhoo/shoowcase/blob/main/docs/telemetry.md). |
| Private PKI | Set `caBundle.configMapName` to an existing ConfigMap with the complete trusted PEM bundle under `caBundle.key`. The chart mounts it read-only at `/etc/shoowcase/ca/ca-bundle.pem`. For PostgreSQL, include `sslrootcert=/etc/shoowcase/ca/ca-bundle.pem` in the DSN; the OIDC client uses `SSL_CERT_FILE`. Restart pods after changing trust. |
| Secret rotation | Update the external Secret and restart all pods; environment variables are read at startup. You can change a non-secret rotation identifier in `podAnnotations` to trigger this through Helm. Rotate the session key across all replicas in one maintenance window; sessions and outstanding tickets will be invalidated. |
| Upgrades | The default `Recreate` strategy stops old pods before new ones migrate the database. **Expect downtime.** Take a restorable backup and rehearse each schema change. Verify the original issuer before the first startup on an existing database: the service binds the database to that issuer. Use `RollingUpdate` only when versions, keys and settings can safely overlap. A disruption budget limits voluntary evictions; it cannot prevent a Recreate rollout or the loss of a node. |
| Restore and rollback | Helm rollback changes Kubernetes resources; it cannot undo a database migration. If an older binary cannot read the new schema, close ingress, stop all writers (including autoscaling), restore a compatible backup and matching secrets, and reconcile later revocations before reopening. Follow the [restore drill](https://github.com/openhoo/shoowcase/blob/main/docs/restore-drill.md). |
| Removing Shoowcase | `helm uninstall shoowcase -n shoowcase` removes chart-owned resources. The external database, credentials, TLS Secrets and CA ConfigMap remain with their owners. |

See [values.yaml](values.yaml) for every setting and [values.schema.json](values.schema.json) for validation. The chart declares Kubernetes 1.25 or newer; CI exercises a specific Kubernetes version and Helm version, not every combination.
