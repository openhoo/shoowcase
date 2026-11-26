# Shoowcase

[![CI](https://github.com/openhoo/shoowcase/actions/workflows/ci.yml/badge.svg)](https://github.com/openhoo/shoowcase/actions/workflows/ci.yml)

Give your agent one MCP connection and ask: **“Make an onboarding site for my team.”** It can upload ordinary HTML, CSS and images, return a private preview, and publish at a stable address. Open a fresh chat a week later: the new agent can find the site, read its files, change one page and roll back a release.

A single Go binary serves every site. The host needs no per-site process, build container, JavaScript runtime, Git provider, or model account. You author the site in your connected agent; the browser displays previews, live sites and approval forms. Shoowcase is self-hosted. An organization's deployed instance supplies the MCP URL.

## Connect an agent

In a client that supports remote MCP over Streamable HTTP, add the instance's **control URL followed by `/mcp`**, then sign in with your work account. For example, a deployment at `https://publish.example.com` offers `https://publish.example.com/mcp`. OAuth client registration and the correct API audience must already be configured at your organization's identity provider; see [operations](docs/operations.md). The server advertises its tools. No skill is required to discover or call them.

Try these in order:

1. “Create and publish a simple onboarding site for my workspace. Return the URL and tell me who can open it.”
2. In a new chat: “Find the onboarding site, read its current homepage, and change the start time to 10:00, and publish that revision. Keep the URL and audience.”
3. “Show the release history. Restore the previous release.”
4. “Make the site public.” If public sharing is enabled, the publisher gets a review link. Authorization happens in the browser for the exact release; nothing goes public from receiving a link.

## Run locally

Build from source with Go 1.26 or later:

```sh
make build
./bin/shoowcase -dev
```

For a packaged build, use a matching archive from [Releases](https://github.com/openhoo/shoowcase/releases), verify the archive against its release's `SHA256SUMS`, extract it and start `shoowcase -dev`.

Connect to `http://localhost:7878/mcp` with an Authorization bearer token from `.shoowcase/agent-token`. Use your client's secret store for the token. Local site and preview addresses are under `*.localhost:7878`. Development uses SQLite in `.shoowcase/`, signs local visitors in as one test user and refuses non-loopback requests. Never expose this mode through a tunnel or proxy. For local public-sharing tests, set `SHOOWCASE_PUBLIC=true` before startup.

## What agents can do

| Tool | Behavior |
| --- | --- |
| `create_site`, `list_sites` | Create an unpublished workspace site or find existing sites across conversations. |
| `get_site` | Read a manifest, one file at a selected revision, or paginated release history. |
| `upload_files` | Replace named files or explicitly remove paths. Unmentioned files stay in the draft. |
| `prepare_upload` | Create a five-minute URL to upload exact bytes by size and SHA-256, or choose the file in a browser. |
| `publish_site` | Publish the current, named draft revision at the site's stable URL. |
| `set_site_access` | Set the audience to workspace, public or offline. Never changes from an ordinary file edit. |
| `rollback_site` | Point the live URL to a selected release while preserving current access policy and draft. |

Mutation requests use stable request IDs. A stale editor gets a conflict, and retrying the same completed request does not duplicate its effect. Public means anyone with the URL can open it; the host asks search engines not to index sites. HTML, CSS, PNG, JPEG and GIF are supported; scripts, forms, external embeds and server-side code are outside the supported content profile. [A starter site](examples/onboarding/) is included.

## Agent skills

- **[Publish and maintain sites](skills/shoowcase-publish/SKILL.md)** — for agents using an instance. Includes revision checks, fresh-chat handoff and public-release review.
- **[Build and operate Shoowcase](skills/shoowcase-operate/SKILL.md)** — for agents changing the project or running the service. [AGENTS.md](AGENTS.md) routes repository tasks.

Install by copying the appropriate skill directory from a reviewed release into your agent's skills directory. For Codex that can be `~/.codex/skills/`. Skills document the workflow; the server enforces access.

## Production and verification

Use PostgreSQL, an HTTPS identity provider, and TLS ingress for the control host and the wildcard site domain. The two origins must be on separate registrable domains. Site bytes, manifests, audit records and replay state are stored in PostgreSQL. Back up deployment configuration and secrets separately; those are also needed to recover the installation. This release uses PostgreSQL for content-addressed files; it does not require an object-store account.

See the [release scope and roadmap](docs/design.md), [deployment and recovery guide](docs/operations.md), [security model](SECURITY.md), [performance and test evidence](docs/verification.md), and [OCI deployment](deploy/compose.yaml). No production SLA is implied by a passing build.

Licensed under [Apache-2.0](LICENSE), including the agent skills.
