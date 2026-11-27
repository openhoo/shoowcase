# Contributing

Run the project in a fresh directory with `make smoke`. Please explain the user-visible change and how it was verified. Follow [AGENTS.md](AGENTS.md) and the [maintainer skill](skills/shoowcase-operate/SKILL.md) for changes to authorization, storage, delivery or MCP schemas.

Before submitting a pull request, run `make vet test smoke` and `make vuln`. For PostgreSQL-dependent changes, point `SHOOWCASE_TEST_POSTGRES_DSN` only at a disposable test database. Do not attach local credentials, private site content, approval URLs or database dumps to issues or test fixtures.

For Helm or release packaging changes, also run `make test-chart` with Helm and PyYAML installed. The `chart` CI job checks the packaged archive, validates the example against a Kubernetes API server, and runs a disposable install, upgrade and uninstall. The corresponding local command is `python3 scripts/smoke-helm.py --context k3d-shoowcase-ci --traefik` on a fresh k3d cluster of that name with Traefik and NetworkPolicy enabled.

The site directory is the pilot for typed SQL. Edit `internal/sites/query/directory.sql`, then run `make generate-sql` with sqlc v1.31.1. Commit the generated Go files. The generator reads the base schema and the group table definitions used by the startup migrations. The browser and MCP share the same directory query. It generates one `database/sql` query with numbered placeholders for both PostgreSQL and SQLite. Keep authorization and the directory read in the same transaction. Run `TestWorkspaceDirectoryMetadata` and `TestPersonalAndAdminSiteLists` against both drivers when changing the query.
