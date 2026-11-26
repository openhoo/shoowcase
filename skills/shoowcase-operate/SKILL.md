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


## Browser workspace and runtime

- Test both entry points against the same site. Make an editor change in the browser and check a stale agent write fails without losing the new revision. A failed ZIP import must add zero files. Verify accessible labels, focus, private/public state and a narrow screen in a real browser.
- Bind every personal key and derived ticket to its workspace, owner and current effective role. Recheck key validity at the mutation boundary. Exercise existing private sessions, upload links and approval links after suspension, revocation or role demotion. Never give a personal key administrative privileges. A restored database can revive old key records; revoke restored keys or reconcile later revocations before opening ingress.
- Keep uploaded JS as data inside the isolated VM. Native browser code is limited to the trusted bridge and worker. Treat every VM message as untrusted; avoid raw HTML, URLs, arbitrary property paths and host callbacks. Keep build inputs, compiler, upstream VM commit, licenses and distribution inventories pinned. Test a local interaction, CPU limits and absence of outbound requests. Plain content must still render without starting a worker.
- Run the workspace browser smoke (`uv run scripts/smoke-workspace.py --binary ./bin/shoowcase`) and runtime checks in `runtime/README.md` alongside the existing suite. For schema v3, drain older writers and confirm the issuer before the first upgraded process starts. Restore a pre-upgrade snapshot if rolling back.
- Follow [tenant access](../../docs/tenant-access.md) before switching a workspace to managed mode. Exercise group removal against active browser, MCP and key sessions, signed approvals and a previously valid private ETag. A member with no groups may still read; an unenrolled identity may no longer read even with a valid workspace claim.

## Quotas

- Before enabling group limits, assign existing project sites and verify that each author has an editor or publisher membership in the group. Under **Workspace admin → Quotas**, compare the workspace total with the group and unassigned totals. Try the last permitted site or revision, an over-limit request, and a retry of an earlier success.
- Schema 4 starts existing sites unassigned and backfills retained storage. Rehearse the upgrade on a restored database. Check that a site's full history moves when its budget group changes and that the workspace total stays constant.

## Grafana dashboards

- Start with the [illustrated operator guide](../../docs/grafana.md). Change the versioned dashboard JSON under `deploy/observability/dashboards/`; keep existing UIDs stable.
- For telemetry or panel changes, run `python3 scripts/check-grafana.py`. Verify the provisioned views with the local stack and a synthetic instance using `uv run scripts/capture-grafana.py`. Review the resulting screenshots before committing them.
