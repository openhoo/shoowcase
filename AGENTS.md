# Working with Shoowcase

Shoowcase is a static site host controlled through MCP. For authoring an actual site, use [shoowcase-publish](skills/shoowcase-publish/SKILL.md). For changes to the service, its hosting, or its authentication, use [shoowcase-operate](skills/shoowcase-operate/SKILL.md).

Keep the MCP contract in `internal/mcpserver` aligned with the domain behavior in `internal/sites` and the HTTP entry points in `internal/web`. The publishing skill describes the experience promised to agents; update it when accepted inputs or the release workflow change. Do not add agent-specific branches to the protocol.

A draft revision is immutable. Changing draft files, changing the live release, and changing who can see the site are separate operations. Preview and delivery hosts serve tenant-authored content; only the control host accepts agent credentials and approval forms.
