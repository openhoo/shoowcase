# Shoowcase: release scope and next milestones

Shoowcase helps a team turn a brief into a hosted site using the agent it already has. The first use case is a small onboarding site: publish a welcome page, checklist, and team image for coworkers, then change a start time from a fresh chat without changing the URL.

## What v0.1 implements

| Stage | What the author or visitor sees |
| --- | --- |
| Connect | The agent discovers eight tools through the organization's MCP endpoint. The production identity provider supplies the workspace and role. |
| Draft | The agent creates a workspace site, uploads HTML, CSS and raster images, and gets an immutable revision with a private preview. Another authorized agent can recover the files from the server without access to the original chat. |
| Publish | A publisher names a reviewed revision. A workspace member can sign in to the stable site URL. Public sharing is disabled by default; if enabled, the publisher must approve initial public access and subsequent public releases in a browser. |
| Maintain | A draft upload leaves the live version untouched. Conflicting edits return an error so a second author can reconcile them. Rolling back changes the live pointer while preserving the draft, address and audience. Taking a site offline preserves history. |

The host runs one Go service with two kinds of origins: a control hostname for authenticated agent and approval requests, and a wildcard content domain for previews and live sites. In production, PostgreSQL holds file bytes, manifests, site visibility, release pointers and audit events; the identity provider owns membership and roles. A release changes a database pointer in a transaction. The visitor's request never invokes a model or a tenant build process. The tradeoff is that delivery depends on the service and database, even for a public image.

The browser is a viewer and approval surface. The agent writes the files; there is no built-in chat model, drag-and-drop editor or site-specific application runtime. Start with the [onboarding example](../examples/onboarding/) and [publisher workflow](../skills/shoowcase-publish/SKILL.md). Site limits are 100 files and 8 MiB per revision. The [operations guide](operations.md) covers the remaining limits and the infrastructure the operator supplies.

## What to build next

These are proposed milestones, not release promises:

| Milestone | Deliverable and decision gate |
| --- | --- |
| Pilot the author journey | Observe five first-time authors at two teams. Aim for four to publish an acceptable workspace site within five minutes after connecting. Ask each to identify the audience, update one field from a fresh chat, and recover the previous version. Use the failures to choose the next UX change. |
| Make branded pages easier | If people repeatedly correct typography or layout, add versioned brand assets and a small template catalog. If they struggle to target one edit, pilot a click-to-select section in preview that passes a scoped edit back to their agent. Keep templates exportable as ordinary HTML and CSS. |
| Qualify the enterprise deployment | Test a real work identity provider and supported agent clients on staging, role revocation, cross-workspace access, multiple replicas, and withdrawal after public access. Rehearse a private restore and measure recovery time and tolerated data loss on a representative dataset. Agree operating targets before offering an SLA. |
| Extend hosting where measured | Review file sizes, database load and access needs with pilot teams. Add an object store or an edge cache only if measurements justify another dependency; test access withdrawal and stale caches before enabling it. Add custom domains or directory provisioning when a team has a concrete rollout need. |

Forms, user scripts and dynamic data would require a separate execution and permissions design. They should not be slipped into the static publishing path to satisfy a single demo.
