# Shoowcase management interface

The control host renders React pages from request-scoped Go view models. React owns the workspace, account, administration, sign-in and approval screens. The browser submits native forms to the existing Go endpoints. They continue to validate the session, CSRF token, permissions and selected revisions. Script-free browsers get working HTML forms. Files published on a tenant host never load this application.

From this directory, run `npm ci --ignore-scripts` and `npm run build`. Commit the generated JS and CSS in `internal/web/uiassets`, plus the license file and dependency inventory. CI rebuilds them against the lockfile. The Go binary and OCI image do not require Node or a frontend CDN.

Shared components in `src/components/ui` are vendored from the official shadcn/ui New York v4 registry (`https://ui.shadcn.com/r/styles/new-york-v4/`). They use the local class merge helper and the theme in `src/styles.css`. Use Lucide icons alongside text or give icon-only controls an accessible name. For a new screen, add the React view and keep its no-script form equivalent. Only pass browser-visible fields in the page model.
