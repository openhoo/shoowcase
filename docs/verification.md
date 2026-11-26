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

## v0.1.1 review

Three independent reviewers examined identity and browser sessions, storage and delivery, and the agent publishing workflow. They then cross-checked changes outside their original areas. The final pass exercised a real browser, the source example and a database written by the released v0.1.0 executable.

| Finding | Verified behavior in v0.1.1 |
| --- | --- |
| A second browser sign-in could interrupt the first. | Two tabs can complete sign-in in reverse order on different replicas. OIDC backend requests reject redirects; browser sign-in still works. |
| A signed public approval could remain usable after an intervening change. | Reasserting workspace access or moving the release away and back makes the old approval fail. Fresh approval still works; a completed request can be retried without another write. |
| The raw HTTP smoke passed forms which failed in Chrome. | Two official Python MCP clients and Chrome completed a signed upload, reviewed old and new previews, approved public sharing and rolled back to the original release. The live address, audience and current draft matched expectations. |
| A historical file read could return a preview of the latest draft. | The agent and the browser both saw the selected historical content. An approval receipt links to the reviewed revision even when the draft differs. |
| A site configured public could be described as public after the instance disabled sharing. | Tool responses report the effective audience and the stored site setting. Anonymous reads, including conditional HEAD requests with an old ETag, are gated. |
| HEAD used to load the entire file. | HEAD returns the same size, type and ETag as GET, checks access, and reads metadata without fetching blob bytes. |

**Dependency review.** The GitHub alerts for the previously pinned Go JOSE v4.1.4 covered JSON parsing, CBC-HMAC processing and RSA key parsing. v0.1.1 uses v4.1.9. The local reachable-code scan did not flag the older pin; reviewing package advisories alongside the scan caught it. The identity and browser tests passed with the updated dependency.

**Upgrade and recovery.** A v0.1.0 binary populated SQLite and PostgreSQL 16.15 separately. After upgrade, both retained the exact HTML and CSS, two releases and five audit events. Replaying an upload added no row; rollback restored the original page and left the current draft intact. A separate PostgreSQL dump-and-restore rehearsal verified the two releases, exact file bytes, five audit rows, private access and a new rollback. See the [upgrade procedure](operations.md#upgrading-from-v010).

**Repeat the browser check.** With a local Chrome installation, Python 3.11+ and uv, run:

```sh
make build
uv run scripts/smoke-browser.py --binary ./bin/shoowcase
```

CI installs the hashed test dependencies separately from the service. The browser runs on an isolated development instance with a disposable database; production identity-provider acceptance is a separate check.

**Performance sample.** On the local Apple M4 Max, a candidate binary serving an authenticated 32 KiB page from SQLite handled 5,000 GETs at concurrency four with p50 **0.246 ms** and p95 **0.508 ms**. Matching conditional requests returned 304 with p50 **0.206 ms** and p95 **0.357 ms**. This loopback sample excludes ingress, network and production PostgreSQL latency; use the [benchmark script and method](http-benchmark.md) to measure your own deployment.

**Published artifacts.** The [v0.1.1 release](https://github.com/openhoo/shoowcase/releases/tag/v0.1.1) was downloaded anonymously. Its macOS arm64 binary passed both smoke tests, including native Chrome upload and approval. The native archive, skills ZIP, checksum manifest and Linux image index had verified release provenance for commit `d73bd6154537b395d16244fc538d680dc5728ef0`. The public index contained both Linux architectures. A second local build matched the published checksums for all four native archives and both agent skills using the compression setting described in the [release instructions](../deploy/README.md).


## v0.2 workspace verification

The release candidate was reviewed in separate passes for identity and storage, browser authoring, and the isolated runtime. Reviewers then checked work outside their original area. The resulting regression cases include duplicate key submissions, access after revocation, simultaneous admin suspensions, stale edits, and a private script requested during a release change.

| Check | Observed result |
| --- | --- |
| Browser publishing | A real Chromium session enrolled a user, issued a scoped MCP key, imported a ZIP, uploaded and edited files, published the workshop tally privately and then publicly, and interacted with both versions. Revocation blocked a previously issued exact-byte upload and public approval. |
| Editorial workspace | Eight synthetic sites exercised searches by name/address, combined status and audience filters, sorting, zero results and reset. Keyboard and automated axe checks passed on the directory at 1440, 390 and 320 CSS pixels. The full workspace smoke also checks key, admin, editor and upload pages. Automated coverage does not establish accessibility conformance. |
| Runtime | A clean rebuild produced the same embedded bridge and worker. Real Chrome exercised input, keyboard clicks, maximum and empty scripts, unavailable browser globals, aggregate budgets and termination. The networking check observed only the fixture page and the two trusted runtime assets. |
| Storage and upgrade | The race suite ran on SQLite and PostgreSQL. An actual v0.1.1 binary seeded two releases and five audit events before migration. A separate restore retained private access, source bytes, release history and idempotent replay, and permitted rollback. The old executable rejects schema v2. |
| Delivery sample | On loopback SQLite, a 32 KiB private page at four concurrent clients returned 1,500 full reads at 0.69 ms p95 and 1,500 conditional responses at 0.57 ms p95. This short local run excludes TLS, ingress, internet and production database latency. |

To repeat the browser and machine checks, see the [operator skill](../skills/shoowcase-operate/SKILL.md), [runtime test commands](../runtime/README.md#rebuild-and-test) and [restore drill](restore-drill.md). To regenerate the README images from synthetic workspace data, run `uv run scripts/capture-workspace.py --binary ./bin/shoowcase --output docs/media` after building the binary and installing the locked runtime test dependencies.

On a trusted checkout, verify a published v0.2 or later release with `python3 scripts/verify-published.py TAG TRUSTED_COMMIT --out NEW_DIRECTORY`. This checks the public downloads, corresponding Git source, archives, dependency inventories, image contents on both Linux architectures and GitHub build provenance. It requires Python, Git, Go and an authenticated GitHub CLI for the provenance check; downloads use anonymous reads.

## Managed access, audit and telemetry (schema 3)

The feature was exercised locally with synthetic accounts and data on November 26, 2026.

| Area | What was verified |
| --- | --- |
| OIDC | A real Chrome session signed in to Keycloak 26.8.2 using required S256 and a confidential client. Shoowcase ran in production mode with HTTPS OIDC endpoints and PostgreSQL certificate verification. The signed workspace and role allowed the user to enroll and open the admin console. After sign-out the previous session could no longer read the audit export. The ledger contained one sign-in and one sign-out for the Keycloak subject. |
| Tenant access | The race suite ran against SQLite and PostgreSQL. It exercised tenant isolation, managed membership, group changes and stale forms, personal keys, private reads and the signed upload and approval paths. The workspace browser smoke exercised the administration views and audit export, including keyboard, axe and narrow-screen checks. |
| Recovery | The upgrade drill used binaries built from the v0.1.1 and v0.2.0 Git tags. Both SQLite and PostgreSQL 16.16 preserved private data and history. A v0.2.0 member’s existing reader key stayed read-only after upgrade and after restoring the pre-upgrade snapshot. The audit export resumed at its checkpoint and distinguished draft and live revisions after rollback. |
| OpenTelemetry | The OTLP tests decoded trace, metric and log payloads, checked correlation and rejected a synthetic private identifier in all three signals. A separate loopback run with the official OpenTelemetry Collector 0.166.0 accepted all three signals from the service. |
| Grafana | Grafana 13.3.2 displayed measurements from Prometheus 3.16.0 and Collector Contrib 0.166.0 in all 18 panels. In Chrome, switching between the three dashboards preserved the service, replica and time range in all six directions. |

## Reproduce the documentation screenshots

The [Grafana guide](grafana.md) includes three dashboards. `python3 scripts/check-grafana.py` evaluates their queries using Prometheus. The [browser capture](../scripts/capture-grafana.py) exercises the service against the local monitoring stack, checks the dashboard panels against actual Grafana data-source responses, and writes the three images. Follow the [local quickstart](grafana.md#open-the-dashboards-locally) to repeat it.

To refresh the [workspace access](tenant-access.md) and [audit](audit.md) screenshots, run `make build`, `npm ci --prefix runtime --ignore-scripts`, then `uv run scripts/smoke-workspace.py --binary ./bin/shoowcase --admin-media docs/media/admin`. The fixture uses its own SQLite database and synthetic members.

## Workspace and group quotas (schema 4)

The store race suite exercises concurrent writers against SQLite and PostgreSQL: only one can claim the last site or storage allowance. It also checks that moving a site transfers the sizes of all its revisions, failed writes leave usage unchanged, and pre-upgrade mutations can be replayed. The HTTP tests lower a group limit after an agent has obtained a signed upload link, verify that redeeming the link is refused, then retry after restoring capacity.

The workspace browser smoke creates a group limited to one site and 18 bytes, fills both limits, and checks that excess writes leave the original file intact. It then moves the site and verifies the updated usage and audit events. The [upgrade drill](restore-drill.md) also covers migration from v0.2.0 to schema 4 on both backends, including a PostgreSQL dump and restore.
