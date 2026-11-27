package web

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/openhoo/shoowcase/internal/ratelimit"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/openhoo/shoowcase/internal/auth"
	"github.com/openhoo/shoowcase/internal/mcpserver"
	"github.com/openhoo/shoowcase/internal/routes"
	"github.com/openhoo/shoowcase/internal/sites"
	"github.com/openhoo/shoowcase/internal/webui"
)

type Config struct {
	DisableApps                                                   bool
	ControlURL, SitesDomain, Version, SignupURL, MetricsWorkspace string
	OIDCAccountURL                                                string
	Key                                                           []byte
	Public                                                        bool
}
type App struct {
	archiveGate                     chan struct{}
	passive                         passiveDigests
	config                          Config
	origin                          *url.URL
	store                           *sites.Store
	auth                            *auth.Service
	control                         *routes.Control
	mcp                             http.Handler
	rate                            ratelimit.Window
	heavy                           chan struct{}
	requests, visits, bytes, denied atomic.Uint64
}
type whoKey struct{}

func New(c Config, db *sites.Store, a *auth.Service) (*App, error) {
	origin, zone, e := auth.CanonicalHosts(c.ControlURL, c.SitesDomain)
	if e != nil {
		return nil, e
	}
	c.ControlURL, c.SitesDomain = origin, zone
	if c.SignupURL != "" {
		signup, err := url.Parse(c.SignupURL)
		if err != nil || signup.Scheme != "https" || signup.Hostname() == "" || signup.User != nil || signup.Fragment != "" {
			return nil, errors.New("signup URL must be an absolute HTTPS URL without credentials or fragment")
		}
	}
	if c.OIDCAccountURL != "" {
		account, err := url.Parse(c.OIDCAccountURL)
		if err != nil || account.Scheme != "https" || account.Hostname() == "" || account.User != nil || strings.ContainsAny(c.OIDCAccountURL, " \\") {
			return nil, errors.New("OIDC account URL must be an absolute HTTPS URL without credentials")
		}
	}
	u, _ := url.Parse(origin)
	if len(c.Key) < 32 {
		return nil, errors.New("invalid control origin or signing key")
	}
	x := &App{config: c, origin: u, store: db, auth: a, heavy: make(chan struct{}, 8), archiveGate: make(chan struct{}, 2)}
	x.mcp = mcpserver.NewForHost(x.execute, u.Host, c.Version)
	x.control = x.controlRouter()
	return x, nil
}
func actor(i auth.Identity) sites.Actor {
	role := string(i.Role)
	if role == "viewer" {
		role = "reader"
	}
	return sites.Actor{Subject: i.Subject, Workspace: i.Workspace, Role: role}
}
func (a *App) siteURL(slug string) string {
	host := slug + "." + a.config.SitesDomain
	if p := a.origin.Port(); p != "" {
		host = net.JoinHostPort(host, p)
	}
	return a.origin.Scheme + "://" + host + "/"
}
func (a *App) decorate(r map[string]any) map[string]any {
	if r == nil {
		return nil
	}
	if rows, ok := r["sites"].([]map[string]any); ok {
		for _, row := range rows {
			a.decorate(row)
		}
	}
	if s, ok := r["site_id"].(string); ok && s != "" {
		draft, _ := r["revision"].(string)
		selected, _ := r["selected_revision"].(string)
		live, preview := a.siteLinks(s, draft, selected)
		r["site_url"] = live
		if preview != "" {
			r["preview_url"] = preview
		}
	}
	return r
}

type appError struct{ code, msg string }

func (e *appError) Error() string     { return e.msg }
func (e *appError) ErrorCode() string { return e.code }
func problem(code, msg string) error  { return &appError{code, msg} }
func errorCode(e error) string {
	var typed interface{ ErrorCode() string }
	if errors.As(e, &typed) {
		return typed.ErrorCode()
	}
	return ""
}
func (a *App) execute(ctx context.Context, tool string, args json.RawMessage) (map[string]any, error) {
	return withToolAccess(ctx, a, tool, func(ctx context.Context, who sites.Actor) (map[string]any, error) {
		return a.executeAs(ctx, who, tool, args)
	})
}

func (a *App) executeAs(ctx context.Context, who sites.Actor, tool string, args json.RawMessage) (map[string]any, error) {
	if tool == "prepare_upload" {
		return a.prepare(ctx, who, args)
	}
	if tool == "approve_public_change" {
		return a.approvePublicChange(ctx, who, args)
	}
	if tool == "set_site_access" && !a.config.Public {
		var v struct{ Visibility string }
		_ = json.Unmarshal(args, &v)
		if v.Visibility == "public" {
			return nil, problem("forbidden", "Public sharing is disabled on this instance.")
		}
	}
	r, e := a.store.Execute(ctx, who, tool, args)
	if generation, ok := sites.ApprovalGeneration(e); ok && a.config.Public {
		t := ticket{Kind: "approval", KeyID: sourceKey(ctx), Generation: generation, Who: who, Tool: tool, Args: args, Expires: time.Now().Add(5 * time.Minute).Unix()}
		browserURL := a.config.ControlURL + "/approve?t=" + url.QueryEscape(a.sign(t))
		t.Kind = "mcp-approval"
		return map[string]any{"approval_url": browserURL, "approval_token": a.sign(t), "status": "approval_required"}, e
	}
	return a.decorate(r), e
}

// Apply the signed operation with the current MCP caller's authority. The store
// rechecks that authority and the publication generation under its workspace lock.
func (a *App) approvePublicChange(ctx context.Context, who sites.Actor, args json.RawMessage) (map[string]any, error) {
	if !a.config.Public {
		return nil, problem("forbidden", "Public sharing is disabled on this instance.")
	}
	var input map[string]string
	if json.Unmarshal(args, &input) != nil || len(input) != 1 || input["approval_token"] == "" {
		return nil, problem("invalid_argument", "Supply only the approval_token returned for the public change.")
	}
	t, ok := a.ticketValue(input["approval_token"], "mcp-approval")
	if !ok || t.Generation < 1 {
		return nil, problem("invalid_argument", "Invalid or expired approval token. Request the public change again.")
	}
	if who.Subject != t.Who.Subject || who.Workspace != t.Who.Workspace || sourceKey(ctx) != t.KeyID {
		return nil, problem("forbidden", "Use the same publisher account and credential that requested this change.")
	}
	switch t.Tool {
	case "publish_site", "rollback_site":
	case "set_site_access":
		var access struct{ Visibility string }
		if json.Unmarshal(t.Args, &access) != nil || access.Visibility != "public" {
			return nil, problem("invalid_argument", "Approval token does not identify a public change.")
		}
	default:
		return nil, problem("invalid_argument", "Approval token does not identify a public change.")
	}
	r, err := a.store.Execute(sites.WithApproval(ctx, t.Generation), who, t.Tool, t.Args)
	return a.decorate(r), err
}

type ticket struct {
	Kind       string
	Who        sites.Actor
	Tool       string
	Args       json.RawMessage
	Expires    int64
	Generation int64  `json:",omitempty"`
	KeyID      string `json:",omitempty"`
	Nonce      string `json:",omitempty"`
}

func (a *App) sign(t ticket) string {
	b, _ := json.Marshal(t)
	mac := hmac.New(sha256.New, a.config.Key)
	mac.Write([]byte("shoowcase/ticket/v1\x00"))
	mac.Write(b)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (a *App) ticket(r *http.Request, kind string) (ticket, bool) {
	return a.ticketValue(r.URL.Query().Get("t"), kind)
}
func (a *App) ticketValue(s, kind string) (ticket, bool) {
	var t ticket
	if len(s) > 16384 {
		return t, false
	}
	p, sig, ok := strings.Cut(s, ".")
	if !ok {
		return t, false
	}
	b, e := base64.RawURLEncoding.DecodeString(p)
	if e != nil {
		return t, false
	}
	h, e := base64.RawURLEncoding.DecodeString(sig)
	mac := hmac.New(sha256.New, a.config.Key)
	mac.Write([]byte("shoowcase/ticket/v1\x00"))
	mac.Write(b)
	if e != nil || !hmac.Equal(h, mac.Sum(nil)) || json.Unmarshal(b, &t) != nil || t.Kind != kind || t.Expires <= time.Now().Unix() {
		return t, false
	}
	return t, true
}
func headers(w http.ResponseWriter) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Cache-Control", "private, no-store")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
}
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.requests.Add(1)
	headers(w)
	if a.origin.Scheme == "https" {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
	}
	// Only a direct request for the exact delivery apex redirects. Discard the
	// path and query; neither credentials nor tenant paths belong on the control host.
	if a.origin.Scheme == "https" && strings.EqualFold(r.Host, a.config.SitesDomain) {
		if readMethod(w, r) {
			http.Redirect(w, r, a.config.ControlURL+"/", http.StatusFound)
		}
		return
	}
	if !a.auth.HostAllowed(r) {
		http.Error(w, "Unknown host", http.StatusMisdirectedRequest)
		return
	}
	if !strings.EqualFold(r.Host, a.origin.Host) {
		if a.auth.Handle(w, r) {
			return
		}
		if strings.HasPrefix(r.URL.Path, "/auth/runtime/") {
			if a.config.DisableApps {
				http.NotFound(w, r)
				return
			}
			serveRuntime(w, r)
			return
		}
		a.deliver(w, r)
		return
	}
	if (r.URL.Path == "/mcp" || r.URL.Path == "/upload" || strings.HasPrefix(r.URL.Path, "/sites/")) && (r.Method == "POST" || r.Method == "PUT") {
		select {
		case a.heavy <- struct{}{}:
			defer func() { <-a.heavy }()
		default:
			w.Header().Set("Retry-After", "2")
			http.Error(w, "Server busy; retry shortly", 429)
			return
		}
	}
	a.control.ServeHTTP(w, r)
}

// Long signed actions can exceed a safe OIDC state-cookie size. Leave the
// exact request in this tab and authenticate separately; no opaque action is
// copied into a cookie, returned through the identity provider or cached per replica.
var separateSignInPage = template.Must(template.New("signin").Parse(`<main><h1>Sign in to continue</h1><p>Keep this page open. <a href="/auth/login" target="_blank" rel="noopener noreferrer">Sign in in a new tab</a> with the account that requested this action, then close that tab and reload this page. You can then review the original request.</p><p>If the link expires, request a new one from your agent.</p></main>`))

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if strings.EqualFold(r.Host, a.origin.Host) && (r.URL.Path == "/approve" || r.URL.Path == "/upload") && len(r.URL.RequestURI()) > auth.MaxReturnPath {
		webui.Public(w, http.StatusOK, "Sign in to continue", "separate-sign-in", separateSignInPage, nil)
		return
	}
	http.Redirect(w, r, "/auth/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
}
func (a *App) deliver(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(405)
		return
	}
	host := strings.ToLower(r.Host)
	if h, _, e := net.SplitHostPort(host); e == nil {
		host = h
	}
	slug, ok := strings.CutSuffix(host, "."+a.config.SitesDomain)
	if !ok || slug == "" || strings.Contains(slug, ".") {
		http.NotFound(w, r)
		return
	}
	who, logged := a.auth.Browser(r)
	var user sites.Actor
	if logged {
		var err error
		r = r.WithContext(verified(r.Context(), who))
		user, err = a.store.LimitActor(r.Context(), actor(who))
		if err != nil {
			writeError(w, err)
			return
		}
	}
	revision := ""
	var e error
	if strings.HasPrefix(slug, "pv-") {
		revision = strings.TrimPrefix(slug, "pv-")
		if !logged {
			a.login(w, r)
			return
		}
		slug, e = a.store.PreviewSlug(r.Context(), revision, user)
		if e != nil {
			if errorCode(e) == "" {
				http.Error(w, "Preview temporarily unavailable", http.StatusServiceUnavailable)
			} else {
				http.NotFound(w, r)
			}
			return
		}
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" || strings.HasSuffix(path, "/") {
		path += "index.html"
	}
	etag := r.Header.Get("If-None-Match")
	digest := ""
	if len(etag) == 66 && etag[0] == '"' && etag[65] == '"' {
		digest = etag[1:65]
	}
	ctx := r.Context()
	if !a.config.Public {
		ctx = sites.WithPrivateDelivery(ctx)
	}
	f, policy, e := a.representation(ctx, r.Method, slug, revision, path, user, digest)
	if e != nil {
		if !logged && (errorCode(e) == "not_found" || errorCode(e) == "unauthorized" || errorCode(e) == "authentication_required" || errorCode(e) == "forbidden") {
			a.login(w, r)
			return
		}
		if errorCode(e) == "" {
			slog.Error("site fetch failed")
			http.Error(w, "Site temporarily unavailable", 503)
			return
		}
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Security-Policy", policy)
	h.Set("X-DNS-Prefetch-Control", "off")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("Content-Type", f.ContentType)
	if f.Digest != "" {
		h.Set("ETag", fmt.Sprintf("%q", f.Digest))
	}
	a.visits.Add(1)
	if f.NotModified {
		w.WriteHeader(304)
		return
	}
	h.Set("Content-Length", fmt.Sprint(f.Size))
	if r.Method == "GET" {
		n, _ := w.Write(f.Data)
		a.bytes.Add(uint64(n))
	}
}

var approvalPage = template.Must(template.New("approval").Parse(`<main><h1>Review public release</h1><p>Approval makes this version available to anyone who knows the site address. The current version remains unchanged until you approve.</p><dl><dt>Site</dt><dd>{{.Site}}</dd><dt>Version</dt><dd><code>{{.Revision}}</code></dd><dt>Action</dt><dd>{{.Tool}}</dd></dl>{{if .Preview}}<p><a href="{{.Preview}}" target="_blank" rel="noreferrer">Review this version</a></p>{{end}}<form method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><button>Approve public release</button></form></main>`))

func (a *App) approve(w http.ResponseWriter, r *http.Request) {
	if !a.config.Public {
		http.NotFound(w, r)
		return
	}
	t, ok := a.ticket(r, "approval")
	if !ok || t.Generation < 1 {
		signedPageError(w, r, "Expired or invalid approval; ask your agent for a new link", 400)
		return
	}
	i, ok := a.auth.Browser(r)
	if !ok {
		a.login(w, r)
		return
	}
	who := actor(i)
	if who.Subject != t.Who.Subject || who.Workspace != t.Who.Workspace {
		signedPageError(w, r, "Wrong account for this approval", 403)
		return
	}
	ctx, who, err := a.ticketActor(r.Context(), t, &i)
	if err != nil {
		signedDomainError(w, r, err)
		return
	}
	if who.Role != "publisher" && who.Role != "admin" {
		signedPageError(w, r, "Publisher access is required for this approval", 403)
		return
	}
	var v struct {
		Site     string `json:"site_id"`
		Revision string `json:"revision"`
		Release  string `json:"release_id"`
	}
	if json.Unmarshal(t.Args, &v) != nil {
		signedPageError(w, r, "Invalid approval", 400)
		return
	}
	if r.Method == "GET" {
		review := v.Revision
		if review == "" && v.Release != "" {
			var e error
			review, e = a.store.ReleaseRevision(ctx, who, v.Site, v.Release)
			if e != nil {
				signedDomainError(w, r, e)
				return
			}
		}
		preview := ""
		if review != "" {
			preview = a.siteURL("pv-" + review)
		}
		webui.Public(w, http.StatusOK, "Review public release", "approve", approvalPage, map[string]any{"Site": v.Site, "Revision": review, "Preview": preview, "Tool": t.Tool, "CSRF": i.CSRF})
		return
	}
	if r.Method != "POST" {
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if r.ParseForm() != nil || !a.auth.ValidCSRF(r, r.PostForm.Get("csrf")) {
		signedPageError(w, r, "Invalid form", 403)
		return
	}
	res, e := a.store.Execute(sites.WithApproval(ctx, t.Generation), who, t.Tool, t.Args)
	if e != nil {
		signedDomainError(w, r, e)
		return
	}
	a.browserResult(w, r, res, "Approval recorded", "This receipt identifies the release you approved. The site address always opens the current live release.")
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func publicErrorDetails(e error) (string, int) {
	c := errorCode(e)
	s := 400
	switch c {
	case "forbidden", "approval_required", "unauthorized":
		s = 403
	case "not_found":
		s = 404
	case "conflict", "revision_conflict":
		s = 409
	case "quota_exceeded", "rate_limited":
		s = 429
	case "":
		return "Service temporarily unavailable", http.StatusServiceUnavailable
	}
	return c + ": " + e.Error(), s
}

func writeError(w http.ResponseWriter, e error) {
	message, status := publicErrorDetails(e)
	http.Error(w, message, status)
}
