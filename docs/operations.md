# Run Shoowcase

## Production prerequisites

Provision a PostgreSQL database, a work identity provider, a control hostname and a separate registrable domain for uploaded content. Example: `publish.example.com` for control and `*.example-sites.net` for sites and previews. Configure wildcard DNS and wildcard TLS including preview hostnames. Send both to Shoowcase, preserving the original Host. Place TLS at the ingress and keep the Go listener and PostgreSQL off the public network. Do not serve site files from a public storage endpoint.

1. Create a browser OIDC client with the **exact** redirect URI `https://publish.example.com/auth/callback`. Configure the issuer to include a nonempty workspace ID and one or more of `viewer`, `editor`, `publisher`, `admin` in signed claims. The browser client ID and the resource audience must be different. Configure direct HTTPS URLs for discovery, signing keys and token exchange; Shoowcase does not follow redirects for these server requests. The user's browser can still follow the provider's ordinary sign-in redirects.
2. For clients using personal keys, members use **Agent keys** in their browser to issue an expiring credential. For MCP clients using OAuth, register/authorize the desired MCP clients with the issuer (or use an issuer with compatible client registration). The MCP resource URL is `https://publish.example.com/mcp`. Configure the issuer to return a signed JWT access token with an access-token type (`at+jwt`), matching audience and claims. Opaque access tokens and using an ID token as a bearer are unsupported. Shoowcase advertises protected-resource metadata; client registration and user assignments belong to the identity provider.
3. Supply a random session key using `shoowcase keygen` directly into a secret manager. All replicas share this key and the PostgreSQL database. Treat database credentials and the OIDC client secret as deployment secrets. Production startup requires TLS with PostgreSQL server identity verification on every connection target. Use `sslmode=verify-full`; use a restricted service account with access to its own database/schema and the DDL rights needed for first initialization. Keep a backup and rehearse restore before upgrades.
4. Install [the Kubernetes Helm chart](../charts/shoowcase/) or use [the Compose definition](../deploy/compose.yaml) with a pinned image digest. You can also run the release binary behind your ingress. Set resource limits and disable unneeded capabilities. Disable shared caching for all paths; do not configure a “cache everything” rule. Do not rewrite Origin headers to skip validation. Send no request query strings or headers to access logs.
5. Give an authorized agent the control URL ending in `/mcp`. Test OAuth discovery, create a team site, verify it in a different coworker's browser, then verify an unrelated workspace cannot read its HTML, styles or images. Public sharing must remain off unless you intend to offer it.

## Configuration

All names below have the prefix `SHOOWCASE_`.

| Name | Meaning |
| --- | --- |
| `CONTROL_URL` | Canonical HTTPS control origin, no path or trailing slash. |
| `SITES_DOMAIN` | Bare DNS suffix on a distinct registrable domain. Uses the control URL's scheme and port. |
| `DATABASE_URL` | PostgreSQL connection string. Use transport verification, e.g. `sslmode=verify-full`, with the appropriate trusted CA. |
| `ISSUER`, `CLIENT_ID`, `CLIENT_SECRET`, `CLIENT_AUTH_METHOD` | HTTPS issuer, registered browser client and token endpoint authentication. See [OIDC and PKCE](oidc.md) for discovery requirements and confidential or public client setup. |
| `OIDC_PROJECT_AUDIENCE` | Optional second audience in browser ID tokens (e.g. a provider project ID). If present in a token, its `azp` must match the browser client. |
| `OIDC_ACCOUNT_URL` | Optional HTTPS link to the identity provider’s self-service password settings. Shown under **Account** to users who signed in through the provider. |
| `AUDIENCE` | API token audience; defaults to control URL plus `/mcp`. |
| `LOCAL_WORKSPACE` | Enable username and password sign-in alongside OIDC for this exact workspace ID. Empty (default) disables password sign-in. |
| `PUBLIC_REGISTRATION` | Defaults to `false`. Administrators of the local workspace can create accounts with registration closed. Set `true` to allow visitors to register as readers; managed workspaces still require administrator provisioning. This switch is independent of public site sharing. |
| `OIDC_ADMIN_SUBJECT`, `OIDC_ADMIN_WORKSPACE` | Optional pair. Map this exact immutable subject of the configured issuer to an administrator in the specified workspace. The first verified browser sign-in enrolls this account. Use the subject assigned to the Shoowcase client, not an email address. To manage local accounts, use the same workspace as `LOCAL_WORKSPACE`. |
| `SESSION_KEY` | 32 random bytes encoded with URL-safe base64, no padding. Rotate across replicas together. |
| `WORKSPACE_CLAIM`, `ROLES_CLAIM`, `REQUIRED_SCOPE` | Defaults: `workspace`, `roles`, optional scope. For deployments with multiple APIs, set a dedicated publishing scope as well as the audience. |
| `LISTEN` | Defaults to `127.0.0.1:7878`. For containers use `:7878` behind the boundary described above. |
| `INTERACTIVE` | Exactly `true` or `false`; default true. Set false on all replicas to serve HTML with scripts disabled and withdraw the runtime URLs. Stored source and releases are preserved. Already-open pages should be closed. |
| `SIGNUP_URL` | Optional fixed HTTPS registration page at your identity provider. Without this, Create an account uses the usual work sign-in, followed by workspace enrollment. Configure the provider to return people to the control URL. |
| `PUBLIC` | Exactly `true` or `false`; default false. False disables anonymous site delivery, including formerly public sites. Change every replica and drain replicas with the old setting before considering the change complete. |
| `METRICS_WORKSPACE` | Optional exact operator workspace ID for the control host’s legacy `/metrics` endpoint. Unset disables it in production; local `-dev` defaults to its development workspace. |
| `STATE_DIR`, `DEV_TOKEN`, `DEV_SUBJECT`, `DEV_WORKSPACE` | For explicit `-dev` only. The default local state directory is `.shoowcase/`. |

## Accounts and offboarding

Workspaces use their signed identity-provider claim as the tenant ID. In the default **identity** mode, existing OAuth clients and private-site readers can use their sign-in role before enrollment. **Managed** mode requires a local member record and grants a reader baseline, then applies group roles up to the IdP role and the member's individual limit. Only a verified administrator can self-enroll in managed mode. In **Workspace admin**, provision an exact IdP subject, manage groups, change the workspace's access mode and review the audit log. See [Tenant access](tenant-access.md) for the permission table and rollout steps. Personal keys require enrollment; each person may have ten active keys and no key grants admin access.

An existing personal key does not contact the identity provider on every request. A newer verified identity assertion updates the recorded provider role, but removing someone at the provider alone does not notify Shoowcase. For immediate offboarding, **suspend the member or revoke their keys in Shoowcase and remove access at the provider**. Another active workspace administrator can suspend an administrator immediately. No one can suspend themselves. Only the identity provider can change an administrator’s role; suspension and restoration are recorded in workspace activity. Limit OAuth token lifetimes and rotate the session signing key for emergency invalidation of existing sessions and signed links.

## Upgrade to schema 7

Take a restorable backup and stop all older writers **before** starting this version. Older running processes do not honor reserved addresses or coordinate with draft deletion. Rehearse the upgrade with the [restore drill](restore-drill.md). Schema 7 reserves existing site addresses and indexes file digests. Allow time and disk space to build this index. To downgrade, restore the pre-upgrade snapshot.

Workspace administrators can permanently delete never-published sites from **Sites**. This removes all their revisions and previews, releases their quota, and removes stored file contents when no other revision uses them. Site addresses stay reserved. Audit records and backups retain their normal lifetimes. A site with any release, including an offline site, keeps its history.

## Upgrade to schema 6

Schema 6 indexes release history by site, creation time and release ID. It does not change releases, site contents or permissions. Allow time and disk space for index creation on a large release history. Keep a pre-upgrade backup, drain older writers, and rehearse with the [restore drill](restore-drill.md) using the previous binary.

At startup, Goose applies each missing version in its own transaction on both databases. It updates the existing `sc_schema_migrations` table; there is no separate migration command. Old binaries refuse schema 6. To downgrade, restore the pre-upgrade snapshot before restarting the previous binary. If upgrading from schema 4 or earlier, follow the identity and quota checks below as well.

## Upgrade to schema 5

1. Rehearse against a backup, then close ingress, drain all older writers and take a consistent database backup. Record the previous binary or image digest, issuer and deployment configuration.
2. Before starting any schema 5 process, check whether your identity provider has issued subjects beginning with `local:`. Schema 5 builds reserve that prefix for password accounts and reject it for OIDC, even when password sign-in is disabled. Resolve any conflicting identity and membership mappings before upgrading; changing only the display name does not fix a subject collision. Start with the same identity provider that issued existing workspace and subject IDs. Databases older than schema 3 pin their issuer on first startup and start in identity mode. Existing schema 3 tenants keep their settings, group grants and audit history.
3. Schema 5 adds a separate credential table for password accounts. Existing workspaces and OIDC members retain their data. When upgrading from before schema 4, the migration also sums all retained revisions per site. Allow time for this migration on larger databases. Check **Workspace admin → Quotas**: existing sites are unassigned, and workspace usage is subject to the previous limits. Assign sites to projects before enabling group limits.
4. Verify a private release, a preview and an existing member's effective rights. Issue a reader key, access a site via MCP, revoke it and confirm the same key is refused. Check audit history and public visibility before reopening ingress.
5. To downgrade, stop all new writers and restore the pre-upgrade backup before starting the old binary. Older binaries refuse schema 5. Reconcile subsequent writes, revocations, group assignments and quota changes.

## Upgrading from v0.1.0

Back up the database and record your current application digest and configuration. Drain v0.1.0 replicas from traffic and stop **all old writers** before starting v0.1.1. The new binary migrates existing sites on startup. An old binary cannot invalidate pending approvals when it changes a release or audience; do not mix versions during the migration. An old review link is rejected: have the publisher request a fresh link and review the revision again.

With public delivery disabled, `get_site` and `list_sites` report `visibility: workspace` for a site still configured public. Check `configured_visibility` before enabling the instance switch: turning it back on can expose those existing releases. To keep a site private across configuration changes, set its site audience to workspace. Verify member access and anonymous denial before returning traffic.

## Recovery, retention and monitoring

Use the [Grafana dashboards](grafana.md) to investigate HTTP traffic, MCP tool outcomes and database connection waits across service replicas.

Back up PostgreSQL as a single unit. It contains content, active release pointers, history, audit events and auth replay records. Protect backups as private site content. Use managed point-in-time recovery if your organization has an RPO. Choose an RPO/RTO, measure both on your deployment and record the results. An isolated, executable [restore drill](restore-drill.md) verifies a restored release with the application. Never run validation against a production restore destination.

After a disaster restore, keep the ingress private while reviewing publication and access changes since the snapshot. Set `PUBLIC=false` and rotate the session key before restoring traffic. Verify a private site denies a signed-out visitor and ensure no revoked public site is automatically re-exposed. Reconcile restored memberships with the identity provider. A restore may also revive an old password hash: keep affected local accounts suspended until you have reconciled their credentials. A snapshot can also revive revoked personal keys. While ingress is closed, reconcile every subsequent revocation, or revoke all restored keys (`UPDATE sc_agent_keys SET revoked=TRUE`) and have members issue fresh ones. Rotating the session key alone does not invalidate personal keys. Back up deployment configuration and externally managed secrets separately.

Control-host `/healthz` checks liveness and `/readyz` checks database connectivity. Both may be probed without a token using the configured Host. The legacy `/metrics` route returns process-wide counters. In production it is disabled (404) unless `SHOOWCASE_METRICS_WORKSPACE` names the operator workspace. Supply an IdP API token with effective admin rights in that exact workspace. Other tenant admins and personal keys cannot read these counters. OTLP export is configured independently. Monitor database storage/latency, request/error rates and memory at the proxy and process. Record site deliveries and transferred bytes without collecting URLs, tokens or document content. Use the authenticated [workspace audit export](audit.md) for access reviews and external retention. Set an OTLP/HTTP endpoint to enable [OpenTelemetry traces, metrics and logs](telemetry.md); outbound telemetry is disabled by default.

Each site can hold 100 files or 8 MiB per revision. A file is at most 2 MiB; HTML, CSS and JavaScript are capped at 256 KiB. Each site supports 500 revisions; the workspace supports 50,000 mutations. Administrators can set [site and storage quotas](tenant-access.md#set-project-budgets) within the service ceilings of 100 sites and 2 GiB per workspace. Quotas count all retained revisions, including unchanged files, even when the database deduplicates identical content. Taking a site offline does not free storage. History is not automatically pruned; do not remove individual SQL rows to free space.

## Supported operating envelope

Keep PostgreSQL close to the service. Production replicas share the database; replay, authorization and publication state do not depend on a sticky server session. The binary combines the management and delivery endpoints; a model outage does not affect hosting, but losing every binary or the database does. Use multiple replicas, a monitored HA database and load testing if you require higher availability. Confirm recoverability and SLOs on your own infrastructure before contracting them.
