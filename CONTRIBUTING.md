# Contributing

Run the project in a fresh directory with `make smoke`. Please explain the user-visible change and how it was verified. Follow [AGENTS.md](AGENTS.md) and the [maintainer skill](skills/shoowcase-operate/SKILL.md) for changes to authorization, storage, delivery or MCP schemas.

Before submitting a pull request, run `make vet test smoke` and `make vuln`. For PostgreSQL-dependent changes, point `SHOOWCASE_TEST_POSTGRES_DSN` only at a disposable test database. Do not attach local credentials, private site content, approval URLs or database dumps to issues or test fixtures.

For Helm or release packaging changes, also run `make test-chart` with Helm and PyYAML installed. The `chart` CI job checks the packaged archive, validates the example against a Kubernetes API server, and runs a disposable install, upgrade and uninstall. The corresponding local command is `python3 scripts/smoke-helm.py --context k3d-shoowcase-ci --traefik` on a fresh k3d cluster of that name with Traefik and NetworkPolicy enabled.
