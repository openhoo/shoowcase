---
name: shoowcase-operate
description: Build, review, upgrade, or operate the Shoowcase service. Covers its MCP contract, publication and access controls, database migrations, and release verification.
---

# Build and operate Shoowcase

Run the commands below from the Shoowcase repository. For authoring a site on an existing instance, use the `shoowcase-publish` skill. Use disposable databases and synthetic site content for tests.

## Preserve these contracts

- **Agent API:** Keep the schemas in `internal/mcpserver` aligned with `internal/sites` and `internal/web`. Derive the workspace and actor from the authenticated request. A fresh MCP client must be able to discover the site, read the draft and continue an edit. Retrying the same mutation as the same actor must not write twice; a stale revision must not overwrite a newer draft.
- **Publication:** Draft edits, live releases and audience changes are separate. Rollback keeps the stable URL and leaves the draft alone. A public change requires the requesting publisher's browser approval for the exact operation, revision and publication state. After a conflict, read the current site and request a fresh approval. Test with two publishers.
- **Delivery:** Keep control and content on separate registrable domains. Authorize each HTML and asset request before returning bytes, headers or a cached response. Test anonymous and unrelated-workspace access to previews and private releases; include `HEAD` and a previously valid ETag after withdrawal. Keep signed URLs, credentials, site contents and database connection strings out of logs.

## Upgrade and restore

1. Follow `docs/operations.md`. Rehearse on a restored copy: create a site with the previous release, retain the release IDs and a mutation request, then start the candidate on the same data. Verify file bytes, private access, history, replay and rollback. Use the separate PostgreSQL restore drill in `docs/restore-drill.md` to test the backup itself.
2. For the upgrade from v0.1.0, drain old replicas and stop old writers before starting the candidate. Old binaries do not advance the publication generation that invalidates outstanding approvals. Startup applies the migration transactionally; if it reports a newer schema, stop and check the binary version. Keep a restorable pre-upgrade backup.
3. Keep public sharing disabled during recovery. Apply `SHOOWCASE_PUBLIC=false` to every replica and drain the previous configuration. `visibility` is the effective audience; `configured_visibility` is retained. Review all sites still configured as public before enabling public delivery again.

## Collect release evidence

- Run `go mod verify` and `make vet test vuln smoke`. Set `SHOOWCASE_TEST_POSTGRES_DSN` to a **disposable** PostgreSQL database for the race suite; otherwise it skips that backend.
- With Chrome installed, run `uv run scripts/smoke-browser.py --binary ./bin/shoowcase` after `make smoke`. This separate test uses two Python MCP SDK clients and a real browser to exercise the file picker, preview, public approval and rollback.
- Record the exact commit, backends, commands and results. Verify the downloaded binary archive's checksum and provenance or the OCI digest and provenance using `deploy/README.md`. Keep local fixture results distinct from deployment evidence: qualify the actual agent, IdP roles, wildcard DNS/TLS, replica failover and restore before declaring that deployment ready.
