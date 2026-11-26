# Release verification

These checks describe the v0.1 release candidate tested on November 26, 2026. For the runs against a particular commit or tag, see the repository's [CI](https://github.com/openhoo/shoowcase/actions/workflows/ci.yml) and [release](https://github.com/openhoo/shoowcase/actions/workflows/release.yml) history.

| Area | Verification |
| --- | --- |
| Agent interoperability | The official MCP Go SDK negotiated versions 2025-03-26, 2025-06-18, 2025-11-25 and 2026-07-28, discovered all eight tools and made a call. The HTTP smoke test uses two separate client instances and does not reuse chat state. |
| Publishing | Created a site, uploaded HTML, CSS and a binary asset, rejected an incorrect digest, published a revision, edited it from the second client, retried the same request and rejected a stale update. Rollback preserved the address and draft. |
| Identity and isolation | Tests exercised a local TLS OIDC issuer, browser callback, host-bound sessions, role and workspace isolation, token audience, one-time handoffs, CSRF and direct asset reads. The public-sharing test covered review and withdrawal, including a request with a previously valid ETag. |
| Storage and recovery | The race suite exercised both storage engines. A local PostgreSQL 16.15 backup restored into a new database; the application returned the exact HTML/CSS, both release IDs and all five fixture audit rows. Private reads remained protected, retrying the old mutation did not write twice and a new rollback succeeded. CI repeats the recovery test using PostgreSQL 17. |
| Dependencies and artifacts | Go module verification, vet, race detector and govulncheck run in CI. The release workflow builds four native archives, inventories their Go dependencies, publishes checksums and generates provenance. The Linux arm64 scratch image measured about 20.3 MB locally and ran its version command as UID 65532 with a read-only filesystem, all capabilities dropped and networking disabled. |

## Reproduce

Use Go 1.27.4 for release builds and Python 3 for the integration checks. Run PostgreSQL tests **only against a disposable test database**; backend test setup creates and drops test data. Do not point these commands at a working installation.

```sh
go mod verify
make vet
# Set SHOOWCASE_TEST_POSTGRES_DSN to include the PostgreSQL suite;
# without it the suite still runs SQLite and explicitly skips PostgreSQL.
make test vuln smoke
go test ./internal/sites -run '^$' -bench BenchmarkDelivery -benchtime=200ms
python3 scripts/bench-http.py --binary ./bin/shoowcase --requests 5000 --concurrency 4
```

Follow the [restore drill](restore-drill.md) to exercise the compiled binary against a backup. See [release operations](../deploy/README.md) for container checks and provenance verification.

## Performance and scope

For a 32 KiB page on an Apple M4 Max, a 200 ms store benchmark averaged 41.4 µs per SQLite full read and 27.5 µs for a matching ETag. Against local Podman PostgreSQL, the corresponding averages were 310 µs and 217 µs. These are time per **store call**; the PostgreSQL measurement includes its local container connection. A separate [HTTP benchmark](http-benchmark.md) includes HTTP handling, session validation, authorization and response transfer and records its own conditions and percentiles.

The tests use fixtures. They establish neither compatibility with a particular enterprise identity provider or agent product nor an availability target. Before exposing a deployment, qualify the actual client and IdP, DNS and wildcard TLS, multi-replica failover, sustained concurrent load and backup recovery on representative data. An independent security assessment, operational sign-off and any contractual SLO belong to the deployment.
