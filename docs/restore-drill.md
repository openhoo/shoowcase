# Rehearse a PostgreSQL restore

Run this on a **disposable PostgreSQL container**, using an official PostgreSQL image with `psql`, `pg_dump` and `pg_restore` installed. Its database port must be published on `127.0.0.1`. The container's configured admin role must be able to create databases and roles. The script uses Python's standard library, the compiled Shoowcase binary, and Podman or Docker.

```sh
make build
python3 scripts/verify-recovery.py --binary ./bin/shoowcase \
  --engine podman --container shoowcase-test-postgres
```

For Docker, use `--engine docker --container <disposable-postgres-container-id>`. No database URL, password, or existing application database name is passed. The script creates a random database role and two databases belonging to it. It seeds the first through the real MCP endpoint, dumps it in PostgreSQL custom format, and restores into the newly created, empty second database with `pg_restore --exit-on-error --single-transaction`. At exit it drops only the databases and role it created. The archive is temporary. An interrupted process that cannot complete its cleanup may leave resources prefixed `sc_drill_` in the disposable service.

Add `--previous-binary /path/to/shoowcase-v0.10.5` to rehearse the upgrade from schema 7 to schema 9 on SQLite and PostgreSQL. The drill checks that the candidate can serve the original private release and that an existing reader key can still read but cannot create a site. The old binary must refuse the upgraded database and must serve the original release from the separately restored pre-upgrade snapshot. PostgreSQL uses a third temporary database for this check. To test the immediate predecessor, pass a binary built from schema 8 and add `--previous-schema 8`. The drill verifies the actual schema on both backends before migrating.

To rehearse a longer migration, seed with v0.9.0 (schema 6), v0.6.0 (schema 5), v0.3.2 (schema 4), v0.2.0 (schema 2) or v0.1.1 (schema 1). The drill verifies the actual starting and ending schema and checks that existing site addresses remain reserved. Member and key checks apply from schema 2 onwards.

The script publishes and edits the bundled onboarding example as a workspace site. After the restore it starts a fresh application process against the restored database; compares the HTML and CSS bytes, release identifiers and all original audit rows; verifies that an unauthenticated client still cannot fetch a protected asset; replays a prior request without introducing an extra write; and rolls the live page back without changing the newer draft.

For a production recovery, provision a separate PostgreSQL target and role with the original ownership or a reviewed ownership mapping, restore the encrypted backup into an empty database, and run the appropriate isolated application version against it. Verify representative private and public content, role bindings, audit history and a rollback after restore before moving delivery to that target. Before opening ingress, reconcile membership changes, account deletions and key revocations after the snapshot. A backup can restore an erased account, its credentials and published sites. Reapply those deletions in isolation. If you cannot reconstruct all key revocations, revoke all restored agent keys while offline. Rotate the signing key for restored sessions and tickets. Restore and check DNS/TLS, OIDC and the separately escrowed session key as needed; these are not PostgreSQL objects. The fixture drill does not test your production backup scheduler, storage provider, credentials, cluster failover, or time to restore a customer-sized database.
