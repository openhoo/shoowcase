# Rehearse a PostgreSQL restore

Run this on a **disposable PostgreSQL container**, using an official PostgreSQL image with `psql`, `pg_dump` and `pg_restore` installed. Its database port must be published on `127.0.0.1`. The container's configured admin role must be able to create databases and roles. The script uses Python's standard library, the compiled Shoowcase binary, and Podman or Docker.

```sh
make build
python3 scripts/verify-recovery.py --binary ./bin/shoowcase \
  --engine podman --container shoowcase-test-postgres
```

For Docker, use `--engine docker --container <disposable-postgres-container-id>`. No database URL, password, or existing application database name is passed. The script creates a random database role and two databases belonging to it. It seeds the first through the real MCP endpoint, dumps it in PostgreSQL custom format, and restores into the newly created, empty second database with `pg_restore --exit-on-error --single-transaction`. At exit it drops only the databases and role it created. The archive is temporary. An interrupted process that cannot complete its cleanup may leave resources prefixed `sc_drill_` in the disposable service.

Add `--previous-binary /path/to/shoowcase-v0.10.1` to test a patch upgrade on schema 7. The drill starts the previous binary again after the candidate changes the live release. It verifies that this binary can serve the changed release and still see the newer draft. It separately restores the pre-upgrade snapshot and checks the original release there. The drill also accepts v0.10.0 for this check.

To test an actual schema migration, seed with v0.9.0 (schema 6), v0.6.0 (schema 5), v0.3.2 (schema 4), v0.2.0 (schema 2) or v0.1.1 (schema 1). The older binary must refuse the newer schema and start successfully on the pre-upgrade snapshot. Both kinds of rehearsal check the actual starting and ending schema and that every existing site address remains reserved. From source schema 2 onwards, a member's reader key must still read its private release and must remain unable to create sites. PostgreSQL uses a third temporary database to test the pre-upgrade snapshot.

The script publishes and edits the bundled onboarding example as a workspace site. After the restore it starts a fresh application process against the restored database; compares the HTML and CSS bytes, release identifiers and all original audit rows; verifies that an unauthenticated client still cannot fetch a protected asset; replays a prior request without introducing an extra write; and rolls the live page back without changing the newer draft.

For a production recovery, provision a separate PostgreSQL target and role with the original ownership or a reviewed ownership mapping, restore the encrypted backup into an empty database, and run the appropriate isolated application version against it. Verify representative private and public content, role bindings, audit history and a rollback after restore before moving delivery to that target. Before opening ingress, reconcile membership changes and all key revocations after the snapshot, or revoke all restored agent keys while offline. Rotate the signing key for restored sessions and tickets. Restore and check DNS/TLS, OIDC and the separately escrowed session key as needed; these are not PostgreSQL objects. The fixture drill does not test your production backup scheduler, storage provider, credentials, cluster failover, or time to restore a customer-sized database.
