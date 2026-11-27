# Shoowcase: release scope and next milestones

Shoowcase helps a team turn a brief into a hosted site using the agent it already has. The first use case is a small onboarding site: publish a welcome page, checklist, and team image for coworkers, then change a start time from a fresh chat without changing the URL.

## Publishing workflow

| Stage | What the author or visitor sees |
| --- | --- |
| Connect | The agent discovers the publishing tools through the organization's MCP endpoint. The production identity provider supplies the workspace and role. |
| Draft | The agent creates a workspace site, uploads HTML, CSS and raster images, and gets an immutable revision with a private preview. Another authorized agent can recover the files from the server without access to the original chat. |
| Publish | A publisher names a reviewed revision. A workspace member can sign in to the stable site URL. Public sharing is disabled by default; if enabled, agents with publisher access can complete public changes over MCP, and publishers using the console can review them in the browser. |
| Maintain | A draft upload leaves the live version untouched. Conflicting edits return an error so a second author can reconcile them. Rolling back changes the live pointer while preserving the draft, address and audience. Taking a site offline preserves history. |

The host runs one Go service with two kinds of origins: a control hostname for authenticated agent and approval requests, and a wildcard content domain for previews and live sites. In production, PostgreSQL holds file bytes, manifests, site visibility, release pointers and audit events; the identity provider verifies the workspace and sets the maximum role. In managed workspaces, local membership and group grants determine what a person can do within that limit. A release changes a database pointer in a transaction. The visitor's request never invokes a model or a tenant build process. The tradeoff is that delivery depends on the service and database, even for a public image.

People can also create, edit, import and publish sites from the browser. Both interfaces go through the same draft and release operations. The identity provider authenticates people; a local account links a verified identity to workspace settings, scoped agent keys, role limits and suspension. Start with the [browser walkthrough](../README.md#publish-from-your-browser).

HTML and CSS need no page runtime. For interactive pages, the server binds the local script from the same authorized revision to a nonce-bearing first-party bridge. A separate interpreter inside a dedicated browser worker executes that source. Guests can update text and controls through a bounded message interface; the host supplies no network primitive. See [the runtime contract](../runtime/README.md) and [workshop tally](../examples/counter/).

## What to build next

These are proposed milestones, not release promises:

| Milestone | Deliverable and decision gate |
| --- | --- |
| Pilot the author journey | Observe five first-time authors at two teams. Aim for four to publish an acceptable workspace site within five minutes after connecting. Ask each to identify the audience, update one field from a fresh chat, and recover the previous version. Use the failures to choose the next UX change. |
| Make branded pages easier | If people repeatedly correct typography or layout, add versioned brand assets and a small template catalog. If they struggle to target one edit, pilot a click-to-select section in preview that passes a scoped edit back to their agent. Keep templates exportable as ordinary HTML and CSS. |
| Qualify the enterprise deployment | Test a real work identity provider and supported agent clients on staging, role revocation, cross-workspace access, multiple replicas, and withdrawal after public access. Rehearse a private restore and measure recovery time and tolerated data loss on a representative dataset. Agree operating targets before offering an SLA. |
| Extend hosting where measured | Review file sizes, database load and access needs with pilot teams. Add an object store or an edge cache only if measurements justify another dependency; test access withdrawal and stale caches before enabling it. Add custom domains or directory provisioning when a team has a concrete rollout need. |
