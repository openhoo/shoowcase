---
name: shoowcase-operate
description: Build, test, or operate a Shoowcase instance, including the MCP transport, tenant isolation, publication state, and deployment configuration.
---

# Build and operate Shoowcase

Work in the Shoowcase repository. For site content changes on a running instance use the publishing skill instead. Start with the repository README, `docs/operations.md` for production identity, TLS and configuration, and `docs/design.md` for release scope. Use a temporary database for experiments; an operator's existing sites are not test fixtures.

## Changes and evidence

- **Transport:** `internal/mcpserver` defines discoverable schemas and delegates calls using the authenticated HTTP request context. Keep it stateless: no process-local editor session, no user-selected workspace, and no access to arbitrary server paths or URLs. Exercise changes using a real Streamable HTTP client; the tests in this package cover discovery and the older initialize handshake.
- **Persistence and authorization:** `internal/sites` owns role checks, immutable revisions, quotas, mutation replay and the live release. Test a stale write and a request from another workspace. Scope request replay to the same actor and workspace. Failed publish or rollback operations must leave the live release intact. Check both supported database engines when changing SQL or transaction boundaries; report which ones actually ran.
- **Browser and hosting:** `internal/auth` owns identity, and `internal/web` routes control requests, approval, previews, and site content. Protect the control origin separately from generated HTML. Verify that anonymous visitors cannot read workspace or draft content, including through a previously issued URL. Public changes need browser approval bound to the exact actor and operation; a link by itself is not approval.
- **Before calling a deployment ready:** exercise the configured identity provider and roles, wildcard DNS/TLS for site and preview hosts, publish/read/rollback, and a backup restored into a separate database. For the runnable PostgreSQL rehearsal and what it proves, see the repository’s `docs/restore-drill.md`. Include a private-site check after restore. A database connection, local smoke test or deployment manifest alone cannot establish those properties.

Run `go test ./...` in the repo and use `go test -race ./...` when changing concurrent operations, transport, or release handling. Use `make smoke` for the binary’s agent-to-browser publish flow. Set `SHOOWCASE_TEST_POSTGRES_DSN` to a disposable database when a change needs PostgreSQL coverage; the default run may skip that backend. Give the operator the tested command and distinguish local, staging and live evidence. Keep bearer credentials, signed approval/upload URLs, uploaded bytes and database connection strings out of diagnostics.
