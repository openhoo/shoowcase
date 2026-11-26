package web

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openhoo/shoowcase/internal/auth"
	"github.com/openhoo/shoowcase/internal/mcpserver"
	"github.com/openhoo/shoowcase/internal/sites"
)

type Config struct {
	DisableApps                                 bool
	ControlURL, SitesDomain, Version, SignupURL string
	Key                                         []byte
	Public                                      bool
}
type App struct {
	archiveGate                     chan struct{}
	passive                         passiveDigests
	config                          Config
	origin                          *url.URL
	store                           *sites.Store
	auth                            *auth.Service
	mcp                             http.Handler
	rate                            limiter
	heavy                           chan struct{}
	requests, visits, bytes, denied atomic.Uint64
}
type whoKey struct{}
type limiter struct {
	mu     sync.Mutex
	counts map[string]int
	window int64
}

func (l *limiter) allow(key string, max int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := time.Now().Unix() / 60
	if w != l.window || l.counts == nil {
		l.counts = make(map[string]int)
		l.window = w
	}
	if _, ok := l.counts[key]; !ok && len(l.counts) >= 65536 {
		return false
	}
	if l.counts[key] >= max {
		return false
	}
	l.counts[key]++
	return true
}
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
	u, _ := url.Parse(origin)
	if len(c.Key) < 32 {
		return nil, errors.New("invalid control origin or signing key")
	}
	x := &App{config: c, origin: u, store: db, auth: a, heavy: make(chan struct{}, 8), archiveGate: make(chan struct{}, 2)}
	x.mcp = mcpserver.NewForHost(x.execute, u.Host, c.Version)
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
		r["site_url"] = a.siteURL(s)
		rev, _ := r["selected_revision"].(string)
		if rev == "" {
			rev, _ = r["revision"].(string)
		}
		if rev != "" {
			r["preview_url"] = a.siteURL("pv-" + rev)
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
	if !a.config.Public {
		ctx = sites.WithPrivateDelivery(ctx)
	}
	who, _ := ctx.Value(whoKey{}).(sites.Actor)
	if who.Subject == "" {
		return nil, problem("unauthorized", "Sign in to Shoowcase.")
	}
	if !a.rate.allow("agent:"+who.Workspace+"\x00"+who.Subject, 300) {
		return nil, problem("rate_limited", "Wait a minute before retrying.")
	}
	if tool == "prepare_upload" {
		return a.prepare(ctx, who, args)
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
		token := a.sign(ticket{Kind: "approval", KeyID: sourceKey(ctx), Generation: generation, Who: who, Tool: tool, Args: args, Expires: time.Now().Add(5 * time.Minute).Unix()})
		return map[string]any{"approval_url": a.config.ControlURL + "/approve?t=" + url.QueryEscape(token), "status": "approval_required"}, e
	}
	return a.decorate(r), e
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
	if !a.auth.HostAllowed(r) {
		http.Error(w, "Unknown host", http.StatusMisdirectedRequest)
		return
	}
	host := strings.ToLower(r.Host)
	if host == a.origin.Host && r.URL.Path == "/healthz" {
		if !readMethod(w, r) {
			return
		}
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "ok\n")
		return
	}
	if host == a.origin.Host && r.URL.Path == "/readyz" {
		if !readMethod(w, r) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if a.store.Ping(ctx) != nil {
			http.Error(w, "storage unavailable", 503)
			return
		}
		_, _ = io.WriteString(w, "ready\n")
		return
	}
	if a.auth.Handle(w, r) {
		return
	}
	if host == a.origin.Host && (r.URL.Path == "/mcp" || r.URL.Path == "/upload" || strings.HasPrefix(r.URL.Path, "/sites/")) && (r.Method == "POST" || r.Method == "PUT") {
		select {
		case a.heavy <- struct{}{}:
			defer func() { <-a.heavy }()
		default:
			w.Header().Set("Retry-After", "2")
			http.Error(w, "Server busy; retry shortly", 429)
			return
		}
	}
	if host != a.origin.Host {
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
	switch r.URL.Path {
	case "/mcp":
		// Browser clients must originate on the management origin. Native MCP clients omit Origin.
		if o := r.Header.Get("Origin"); o != "" && o != a.config.ControlURL {
			http.Error(w, "Origin denied", 403)
			return
		}
		who, ctx, e := a.agent(r)
		if e != nil {
			a.denied.Add(1)
			a.agentError(w, e)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 12<<20)
		a.mcp.ServeHTTP(w, r.WithContext(context.WithValue(ctx, whoKey{}, who)))
	case "/approve":
		// Browsers derive the Origin of a form POST from the page referrer policy.
		// Send only the control origin, never the signed query or path.
		w.Header().Set("Referrer-Policy", "strict-origin")
		a.approve(w, r)
	case "/upload":
		w.Header().Set("Referrer-Policy", "strict-origin")
		a.upload(w, r)
	case "/ui.css":
		if !readMethod(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = io.WriteString(w, controlCSS)
	case "/metrics":
		if !readMethod(w, r) {
			return
		}
		i, e := a.auth.Bearer(r)
		if e != nil {
			a.auth.Challenge(w)
			http.Error(w, "Authentication required", http.StatusUnauthorized)
			return
		}
		principal, err := a.store.LimitActor(verified(r.Context(), i), actor(i))
		if err != nil {
			writeError(w, err)
			return
		}
		if principal.Role != "admin" {
			http.Error(w, "Forbidden", 403)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "shoowcase_requests_total %d\nshoowcase_site_responses_total %d\nshoowcase_site_bytes_total %d\nshoowcase_denials_total %d\n", a.requests.Load(), a.visits.Load(), a.bytes.Load(), a.denied.Load())
	case "/", "/sites", "/keys", "/account", "/admin":
		a.console(w, r)
	case "/console.js":
		a.controlScript(w, r)
	default:
		if strings.HasPrefix(r.URL.Path, "/sites/") {
			a.console(w, r)
			return
		}
		http.NotFound(w, r)
	}
}
func render(w http.ResponseWriter, t *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if e := t.Execute(w, data); e != nil {
		slog.Error("template rendering failed")
	}
}

// Long signed actions can exceed a safe OIDC state-cookie size. Leave the
// exact request in this tab and authenticate separately; no opaque action is
// copied into a cookie, returned through the identity provider or cached per replica.
var separateSignInPage = template.Must(template.New("signin").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><link rel="stylesheet" href="/ui.css"><title>Sign in to continue · Shoowcase</title><main><h1>Sign in to continue</h1><p>Keep this page open. <a href="/auth/login" target="_blank" rel="noopener noreferrer">Sign in in a new tab</a> with the account that requested this action, then close that tab and reload this page. You can then review the original request.</p><p>If the link expires, request a new one from your agent.</p></main></html>`))

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if strings.EqualFold(r.Host, a.origin.Host) && (r.URL.Path == "/approve" || r.URL.Path == "/upload") && len(r.URL.RequestURI()) > auth.MaxReturnPath {
		render(w, separateSignInPage, nil)
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
		user, err = a.store.LimitActor(verified(r.Context(), who), actor(who))
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

var approvalPage = template.Must(template.New("approval").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><link rel="stylesheet" href="/ui.css"><title>Review public release · Shoowcase</title><main><h1>Review public release</h1><p>Approval makes this version available to anyone who knows the site address. The current version remains unchanged until you approve.</p><dl><dt>Site</dt><dd>{{.Site}}</dd><dt>Version</dt><dd><code>{{.Revision}}</code></dd><dt>Action</dt><dd>{{.Tool}}</dd></dl>{{if .Preview}}<p><a href="{{.Preview}}" target="_blank" rel="noreferrer">Review this version</a></p>{{end}}<form method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><button>Approve public release</button></form></main></html>`))

func (a *App) approve(w http.ResponseWriter, r *http.Request) {
	if !a.config.Public {
		http.NotFound(w, r)
		return
	}
	t, ok := a.ticket(r, "approval")
	if !ok || t.Generation < 1 {
		http.Error(w, "Expired or invalid approval; ask your agent for a new link", 400)
		return
	}
	i, ok := a.auth.Browser(r)
	if !ok {
		a.login(w, r)
		return
	}
	who := actor(i)
	if who.Subject != t.Who.Subject || who.Workspace != t.Who.Workspace {
		http.Error(w, "Wrong account for this approval", 403)
		return
	}
	ctx, who, err := a.ticketActor(r.Context(), t, &i)
	if err != nil {
		writeError(w, err)
		return
	}
	if who.Role != "publisher" && who.Role != "admin" {
		http.Error(w, "Publisher access is required for this approval", 403)
		return
	}
	var v struct {
		Site     string `json:"site_id"`
		Revision string `json:"revision"`
		Release  string `json:"release_id"`
	}
	if json.Unmarshal(t.Args, &v) != nil {
		http.Error(w, "Invalid approval", 400)
		return
	}
	if r.Method == "GET" {
		review := v.Revision
		if review == "" && v.Release != "" {
			var e error
			review, e = a.store.ReleaseRevision(ctx, who, v.Site, v.Release)
			if e != nil {
				writeError(w, e)
				return
			}
		}
		preview := ""
		if review != "" {
			preview = a.siteURL("pv-" + review)
		}
		render(w, approvalPage, map[string]string{"Site": v.Site, "Revision": review, "Preview": preview, "Tool": t.Tool, "CSRF": i.CSRF})
		return
	}
	if r.Method != "POST" {
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if r.ParseForm() != nil || !a.auth.ValidCSRF(r, r.PostForm.Get("csrf")) {
		http.Error(w, "Invalid form", 403)
		return
	}
	res, e := a.store.Execute(sites.WithApproval(ctx, t.Generation), who, t.Tool, t.Args)
	if e != nil {
		writeError(w, e)
		return
	}
	a.browserResult(w, r, res, "Approval recorded", "This receipt identifies the release you approved. The site address always opens the current live release.")
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, e error) {
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
		http.Error(w, "Service temporarily unavailable", 503)
		return
	}
	http.Error(w, c+": "+e.Error(), s)
}
