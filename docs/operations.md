# Run Shoowcase

## Production prerequisites

Provision a PostgreSQL database, a work identity provider, a control hostname and a separate registrable domain for uploaded content. Example: `publish.example.com` for control and `*.example-sites.net` for sites and previews. Configure wildcard DNS and wildcard TLS including preview hostnames. Send both to Shoowcase, preserving the original Host. Place TLS at the ingress and keep the Go listener and PostgreSQL off the public network. Do not serve site files from a public storage endpoint.

1. Create a browser OIDC client with the **exact** redirect URI `https://publish.example.com/auth/callback`. Configure the issuer to include a nonempty workspace ID and one or more of `viewer`, `editor`, `publisher`, `admin` in signed claims. The browser client ID and the resource audience must be different. Configure direct HTTPS URLs for discovery, signing keys and token exchange; Shoowcase does not follow redirects for these server requests. The user's browser can still follow the provider's ordinary sign-in redirects.
2. For clients using personal keys, members use **Agent keys** in their browser to issue an expiring credential. For MCP clients using OAuth, register/authorize the desired MCP clients with the issuer (or use an issuer with compatible client registration). The MCP resource URL is `https://publish.example.com/mcp`. Configure the issuer to return a signed JWT access token with an access-token type (`at+jwt`), matching audience and claims. Opaque access tokens and using an ID token as a bearer are unsupported. Shoowcase advertises protected-resource metadata; client registration and user assignments belong to the identity provider.
3. Supply a random session key using `shoowcase keygen` directly into a secret manager. All replicas share this key and the PostgreSQL database. Treat database credentials and the OIDC client secret as deployment secrets. Production startup requires TLS with PostgreSQL server identity verification on every connection target. Use `sslmode=verify-full`; use a restricted service account with access to its own database/schema and the DDL rights needed for first initialization. Keep a backup and rehearse restore before upgrades.
4. Use [the Compose definition](../deploy/compose.yaml) with a pinned image digest or run the release binary behind your ingress. Set resource limits and disable unneeded capabilities. Disable shared caching for all paths; do not configure a “cache everything” rule. Do not rewrite Origin headers to skip validation. Send no request query strings or headers to access logs.
5. Give an authorized agent the control URL ending in `/mcp`. Test OAuth discovery, create a team site, verify it in a different coworker's browser, then verify an unrelated workspace cannot read its HTML, styles or images. Public sharing must remain off unless you intend to offer it.

## Configuration

All names below have the prefix `SHOOWCASE_`.

| Name | Meaning |
| --- | --- |
| `CONTROL_URL` | Canonical HTTPS control origin, no path or trailing slash. |
| `SITES_DOMAIN` | Bare DNS suffix on a distinct registrable domain. Uses the control URL's scheme and port. |
| `DATABASE_URL` | PostgreSQL connection string. Use transport verification, e.g. `sslmode=verify-full`, with the appropriate trusted CA. |
| `ISSUER`, `CLIENT_ID`, `CLIENT_SECRET` | HTTPS OIDC issuer, browser client and secret. |
| `AUDIENCE` | API token audience; defaults to control URL plus `/mcp`. |
| `SESSION_KEY` | 32 random bytes encoded with URL-safe base64, no padding. Rotate across replicas together. |
| `WORKSPACE_CLAIM`, `ROLES_CLAIM`, `REQUIRED_SCOPE` | Defaults: `workspace`, `roles`, optional scope. For deployments with multiple APIs, set a dedicated publishing scope as well as the audience. |
| `LISTEN` | Defaults to `127.0.0.1:7878`. For containers use `:7878` behind the boundary described above. |
| `INTERACTIVE` | Exactly `true` or `false`; default true. Set false on all replicas to serve HTML with scripts disabled and withdraw the runtime URLs. Stored source and releases are preserved. Already-open pages should be closed. |
| `SIGNUP_URL` | Optional fixed HTTPS registration page at your identity provider. Without this, Create an account uses the usual work sign-in, followed by workspace enrollment. Configure the provider to return people to the control URL. |
| `PUBLIC` | Exactly `true` or `false`; default false. False disables anonymous site delivery, including formerly public sites. Change every replica and drain replicas with the old setting before considering the change complete. |
| `STATE_DIR`, `DEV_TOKEN`, `DEV_SUBJECT`, `DEV_WORKSPACE` | For explicit `-dev` only. The default local state directory is `.shoowcase/`. |

## Accounts and offboarding

The first sign-in offers enrollment; people cannot choose their workspace or grant themselves roles. Existing v0.1 OAuth clients and workspace readers can continue without a console account. In **Admin**, a workspace admin can review members and active keys, reduce a member’s role, suspend another member, including another administrator, or revoke a key. The provider manages administrator roles. Each enrolled person can have up to ten active keys; none grant admin access. The UI shows the newest 25 activity entries; export events to your own protected audit destination if you need longer retention.

An existing personal key does not contact the identity provider on every request. A newer verified identity assertion updates the recorded provider role, but removing someone at the provider alone does not notify Shoowcase. For immediate offboarding, **suspend the member or revoke their keys in Shoowcase and remove access at the provider**. Another active workspace administrator can suspend an administrator immediately. No one can suspend themselves. Only the identity provider can change an administrator’s role; suspension and restoration are recorded in workspace activity. Limit OAuth token lifetimes and rotate the session signing key for emergency invalidation of existing sessions and signed links.

## Upgrade to the browser workspace (v0.2)

1. Rehearse against a backup, then close ingress, stop all v0.1.x writers, and take a consistent database backup. Record the previous binary or image digest and deployment configuration.
2. Start the v0.2 binary. Startup migrates to schema v2, preserving sites, drafts, releases and audiences and creating the account and key tables. Do not mix binaries across this migration.
3. Verify the existing private live URL and a preview, enroll a member, issue a viewer key, read a site through MCP, revoke the key, and check the same key is refused. Verify public visibility settings before reopening ingress.
4. To downgrade, stop all new writers and restore the pre-upgrade backup before starting the old binary. Downgrading the binary alone cannot reopen schema v2; writes after the snapshot need separate reconciliation.

## Upgrading from v0.1.0

Back up the database and record your current application digest and configuration. Drain v0.1.0 replicas from traffic and stop **all old writers** before starting v0.1.1. The new binary migrates existing sites on startup. An old binary cannot invalidate pending approvals when it changes a release or audience; do not mix versions during the migration. An old review link is rejected: have the publisher request a fresh link and review the revision again.

With public delivery disabled, `get_site` and `list_sites` report `visibility: workspace` for a site still configured public. Check `configured_visibility` before enabling the instance switch: turning it back on can expose those existing releases. To keep a site private across configuration changes, set its site audience to workspace. Verify member access and anonymous denial before returning traffic.

## Recovery, retention and monitoring

Back up PostgreSQL as a single unit. It contains content, active release pointers, history, audit events and auth replay records. Protect backups as private site content. Use managed point-in-time recovery if your organization has an RPO. Choose an RPO/RTO, measure both on your deployment and record the results. An isolated, executable [restore drill](restore-drill.md) verifies a restored release with the application. Never run validation against a production restore destination.

After a disaster restore, keep the ingress private while reviewing publication and access changes since the snapshot. Set `PUBLIC=false` and rotate the session key before restoring traffic. Verify a private site denies a signed-out visitor and ensure no revoked public site is automatically re-exposed. Reconcile restored memberships with the identity provider. A snapshot can also revive revoked personal keys. While ingress is closed, reconcile every subsequent revocation, or revoke all restored keys (`UPDATE sc_agent_keys SET revoked=TRUE`) and have members issue fresh ones. Rotating the session key alone does not invalidate personal keys. Back up deployment configuration and externally managed secrets separately.

Control-host `/healthz` checks liveness and `/readyz` checks database connectivity. Both may be probed without a token using the configured Host. `/metrics` requires an admin API token. Monitor database storage/latency, request/error rates and memory at the proxy and process. Record site deliveries and transferred bytes without collecting URLs, tokens or document content. Read the workspace audit feed directly from `sc_audit` with a separate read-only reporting role; limit each report to the intended workspace. The release includes no outbound telemetry.

Each site can hold 100 files or 8 MiB per revision. A file is at most 2 MiB; HTML, CSS and JavaScript are capped at 256 KiB. Workspaces have 100 sites, 500 revisions per site, 2 GiB of cumulative manifest sizes, and 50,000 mutations. Identical file bytes are deduplicated while quota charging counts retained revision sizes. At a limit, inspect usage and archive records under your retention policy; this release does not automatically prune history. Do not remove individual SQL rows to free space. Taking a site offline affects delivery without destroying history.

## Supported operating envelope

Keep PostgreSQL close to the service. Production replicas share the database; replay, authorization and publication state do not depend on a sticky server session. The binary combines the management and delivery endpoints; a model outage does not affect hosting, but losing every binary or the database does. Use multiple replicas, a monitored HA database and load testing if you require higher availability. Confirm recoverability and SLOs on your own infrastructure before contracting them.
