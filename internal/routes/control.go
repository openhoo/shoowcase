// Package routes defines the paths on the management host. The server and its
// telemetry both use this table; a tenant slug or unknown path is never a label.
package routes

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/openhoo/shoowcase/internal/web/uiassets"
)

type Endpoint struct {
	// Page selects the console section.
	Page    string
	Action  string // a registered site form action, possibly empty for the detail page
	Allowed string // nonempty only for an unsupported method on a known site page
}

const (
	Console     = "console"
	Site        = "site"
	NewSite     = "new-site"
	Directory   = "directory"
	UnknownSite = "unknown-site"
)

type Handlers struct {
	Health, Ready, MCP, Approve, Upload, CSS, Audit, Metrics http.HandlerFunc
	Redeem, Installer, Script, Auth                          http.HandlerFunc
	Console                                                  func(http.ResponseWriter, *http.Request, Endpoint)
}

type authPath uint8

const (
	otherPath authPath = iota
	discovery
	loginFlow
	password
)

var authPaths = map[string]authPath{
	"/.well-known/oauth-protected-resource": discovery, "/.well-known/oauth-protected-resource/mcp": discovery,
	"/auth/login": loginFlow, "/auth/callback": loginFlow, "/auth/finish": loginFlow, "/auth/logout": loginFlow,
	"/auth/local": password, "/auth/register": password,
}

// AuthPath identifies protocol discovery and browser authentication paths on
// both hosts. The auth handler decides which operations each host permits.
func AuthPath(path string) (known, metadata bool) {
	kind := authPaths[path]
	return kind != otherPath, kind == discovery
}

type registered struct {
	http.Handler
	label string
}

type originalURLKey struct{}

func (h registered) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only unusual escape sequences or literal dot segments need a different
	// URL for matching. Handlers must see the caller's exact return URL.
	if u, ok := r.Context().Value(originalURLKey{}).(*url.URL); ok {
		r.URL = u
	}
	h.Handler.ServeHTTP(w, r)
}

type Control struct {
	mux         *http.ServeMux
	siteMethods *http.ServeMux
	unknownSite http.Handler
}

// NewControl registers the same patterns for dispatch and telemetry. Methods
// for MCP, auth and uploads belong to their handlers (including their challenge
// and Allow headers). For site pages, match the method here but still pass
// method errors through the existing console login and CSRF checks.
func NewControl(h Handlers) *Control {
	page := func(e Endpoint) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { h.Console(w, r, e) }
	}
	m := &Control{mux: http.NewServeMux(), siteMethods: http.NewServeMux(), unknownSite: page(Endpoint{Page: UnknownSite})}
	add := func(pattern, label string, handler http.HandlerFunc) {
		m.mux.Handle(pattern, registered{handler, label})
	}
	for path, handler := range map[string]http.HandlerFunc{
		"/healthz": h.Health, "/readyz": h.Ready, "/mcp": h.MCP, "/approve": h.Approve, "/upload": h.Upload,
		"/ui.css": h.CSS, "/api/audit": h.Audit, "/metrics": h.Metrics, "/quickstart/redeem": h.Redeem,
		"/quickstart/install.sh": h.Installer, "/quickstart/install.ps1": h.Installer,
		"/quickstart/uninstall.sh": h.Installer, "/quickstart/uninstall.ps1": h.Installer, "/console.js": h.Script,
	} {
		add(path, path, handler)
	}
	// Only this binary's exact content hashes are valid. Reuse the stable
	// legacy labels so asset rebuilds do not add telemetry series.
	add(uiassets.Style.Path, "/ui.css", h.CSS)
	add(uiassets.Script.Path, "/console.js", h.Script)
	for path := range authPaths {
		add(path, path, h.Auth)
	}
	memberPage := page(Endpoint{Page: Console})
	add("/{$}", "/", memberPage)
	for _, path := range []string{"/keys", "/account", "/admin", "/quickstart"} {
		add(path, path, memberPage)
	}
	site := func(pattern, label, name, action, allow string) {
		// The fallback preserves our HTML method-error page after authentication.
		m.siteMethods.Handle(pattern, registered{page(Endpoint{Page: name, Action: action, Allowed: allow}), label})
		for _, method := range strings.Split(allow, ", ") {
			if method != "HEAD" { // ServeMux's GET also matches HEAD.
				add(method+" "+pattern, label, page(Endpoint{Page: name, Action: action}))
			}
		}
	}
	site("/sites", "/sites", Directory, "", "GET, HEAD, POST")
	site("/sites/{$}", "/sites", Directory, "", "GET, HEAD, POST")
	site("/sites/new", "/sites/new", NewSite, "", "GET, HEAD")
	site("/sites/{site}", "/sites/{site}", Site, "", "GET, HEAD")
	site("/sites/{site}/{$}", "/sites/{site}", Site, "", "GET, HEAD")
	for _, op := range []string{"edit", "delete", "upload", "import", "rollback", "publish", "access"} {
		allow := "GET, HEAD, POST"
		if op == "publish" || op == "access" {
			allow = "POST"
		}
		path := "/sites/{site}/" + op
		site(path, path, Site, op, allow)
	}
	// A method mismatch still needs session and form validation before the
	// console reports 405. Other unmatched /sites/ paths use its normal 404.
	m.mux.Handle("/sites", m.siteMethods)
	m.mux.Handle("/sites/{unknown...}", m.siteMethods)
	m.siteMethods.Handle("/sites/{unknown...}", registered{m.unknownSite, ""})
	return m
}

// Dispatch to the exact decoded path: the previous host router never sent
// ServeMux's automatic path-cleaning redirects. Query strings can contain
// signed tickets, and the request used by the selected handler must stay intact.
func matchRequest(r *http.Request) *http.Request {
	p := r.URL.Path
	if p == "" || p[0] != '/' || strings.Contains(p, "//") {
		return nil
	}
	if r.URL.RawPath == "" && !strings.HasSuffix(p, "/.") && !strings.HasSuffix(p, "/..") && !strings.Contains(p, "/./") && !strings.Contains(p, "/../") {
		return r
	}
	// A percent-encoded slash in URL.Path is a separator. Quote literal dot
	// segments for matching so the mux cannot clean them or redirect to a
	// different endpoint. Keep other request data (including the raw query).
	u := *r.URL
	u.RawPath = ""
	segments := strings.Split(u.EscapedPath(), "/")
	for i, s := range segments {
		switch s {
		case ".":
			segments[i] = "%2e"
		case "..":
			segments[i] = "%2e%2e"
		}
	}
	u.RawPath = strings.Join(segments, "/")
	q := r.WithContext(context.WithValue(r.Context(), originalURLKey{}, r.URL))
	q.URL = &u
	return q
}

func (m *Control) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if q := matchRequest(r); q != nil {
		m.mux.ServeHTTP(w, q)
	} else if strings.HasPrefix(r.URL.Path, "/sites/") {
		m.unknownSite.ServeHTTP(w, r)
	} else {
		http.NotFound(w, r)
	}
}

var labels = NewControl(Handlers{})

func Label(r *http.Request, control bool) string {
	if !control {
		// Only the auth handoff has a named surface on tenant hosts. In particular,
		// /auth/local and /auth/register are control-host operations.
		if authPaths[r.URL.Path] == loginFlow {
			return r.URL.Path
		}
		return "/{path...}"
	}
	q := matchRequest(r)
	if q == nil {
		return ""
	}
	h, _ := labels.mux.Handler(q)
	if h == labels.siteMethods {
		h, _ = labels.siteMethods.Handler(q)
	}
	entry, _ := h.(registered)
	return entry.label
}
