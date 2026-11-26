package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// MaxReturnPath limits the destination stored in short-lived OIDC cookies.
const MaxReturnPath = 1024

const authTTL = 5 * time.Minute

// Fresh random names let overlapping navigations finish independently. Refuse
// new flows if the requesting browser already has too many pending; cookies
// disappear on success or expiry. Also budget cookie bytes for reverse proxies.
const maxPendingFlows = 6
const maxFlowHeaderBytes = 6 << 10

func flowKind(kind, id string) string { return "flow-" + hash(id)[:22] + "-" + kind }

func (s *Service) beginFlow(w http.ResponseWriter, r *http.Request, kind, id, value string) bool {
	name := flowKind(kind, id)
	size := len(s.name(name)) + len(value) + 2
	count := 0
	for _, c := range r.Cookies() {
		if strings.HasPrefix(c.Name, s.name("flow-")) {
			count++
			size += len(c.Name) + len(c.Value) + 2
		}
	}
	if count >= maxPendingFlows || size > maxFlowHeaderBytes {
		w.Header().Set("Retry-After", "300")
		http.Error(w, "Too many unfinished sign-ins. Finish another sign-in or try again in five minutes.", http.StatusTooManyRequests)
		return false
	}
	s.cookie(w, name, value, int(authTTL.Seconds()))
	return true
}

// Readers and publishers re-check workspace claims at least every 15 minutes.
// Browser cookies never outlive the ID token used to mint them.
const sessionTTL = 15 * time.Minute

type destination struct {
	Host, Path, Binding string
	Exp                 int64
}
type login struct {
	State, Verifier, Nonce string
	Dest                   destination
	Exp                    int64
}
type handoff struct {
	Dest            destination
	Who             Identity
	Once, Family    string
	SessionExp, Exp int64
}

func (s *Service) consume(ctx context.Context, kind, key string, exp int64) bool {
	if key == "" || exp <= s.now().Unix() {
		return false
	}
	k := kind + ":" + hash(key)
	t := time.Unix(exp, 0)
	if s.cfg.Consume != nil {
		ok, e := s.cfg.Consume(ctx, k, t)
		return ok && e == nil
	}
	return s.consumeLocal(k, t)
}
func (s *Service) consumeLocal(k string, t time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.markers {
		if !v.After(s.now()) {
			delete(s.markers, k)
		}
	}
	if _, exists := s.markers[k]; exists || len(s.markers) >= 10000 {
		return false
	}
	s.markers[k] = t
	return true
}

// All host-specific cookies and unfinished handoffs belonging to one browser
// sign-in share this family. Only its digest is persisted, until session expiry.
func (s *Service) familyActive(ctx context.Context, family string) bool {
	if family == "" {
		return false
	}
	key := "logout:" + hash(family)
	if s.cfg.IsConsumed != nil {
		spent, err := s.cfg.IsConsumed(ctx, key)
		return err == nil && !spent
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.markers[key].After(s.now())
}

func (s *Service) logout(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	v, ok := s.sessionCookie(r)
	if !ok || r.ParseForm() != nil || !s.validForm(r, v.Who, r.PostForm.Get("csrf")) {
		deny(w, 403)
		return
	}
	s.cookie(w, "session", "", -1)
	key := "logout:" + hash(v.Family)
	v.Who.CSRF = ""
	if s.cfg.RevokeSession != nil {
		if err := s.cfg.RevokeSession(r.Context(), key, time.Unix(v.Exp, 0), v.Who); err != nil {
			deny(w, 503)
			return
		}
	} else if !s.consumeLocal(key, time.Unix(v.Exp, 0)) && s.familyActive(r.Context(), v.Family) {
		deny(w, 503)
		return
	}
	topRedirect(w, r, "/")
}

func (s *Service) audit(ctx context.Context, who Identity, action string) bool {
	who.CSRF = ""
	return s.cfg.Audit == nil || s.cfg.Audit(ctx, who, action) == nil
}

func nextPath(raw string) (string, bool) {
	if raw == "" {
		return "/", true
	}
	if len(raw) > MaxReturnPath || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.Contains(raw, "\\") || strings.Contains(raw, "#") {
		return "", false
	}
	u, e := url.ParseRequestURI(raw)
	if e != nil || u.Host != "" || u.IsAbs() || strings.HasPrefix(u.Path, "//") || strings.Contains(u.Path, "\\") || strings.IndexFunc(u.Path, unicode.IsControl) >= 0 || strings.HasPrefix(u.Path, "/auth/") {
		return "", false
	}
	return raw, true
}
func (s *Service) destOK(d destination) bool {
	_, pathOK := nextPath(d.Path)
	return pathOK && d.Exp > s.now().Unix() && (s.isControl(d.Host) && d.Binding == "" || s.isSite(d.Host) && d.Binding != "")
}
func deny(w http.ResponseWriter, n int) { http.Error(w, http.StatusText(n), n) }
func topRedirect(w http.ResponseWriter, r *http.Request, u string) {
	http.Redirect(w, r, u, http.StatusSeeOther)
}
func (s *Service) Challenge(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource/mcp"`, s.control.String()))
}
func (s *Service) Handle(w http.ResponseWriter, r *http.Request) bool {
	var isMetadata bool
	switch r.URL.Path {
	case "/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp":
		isMetadata = true
	case "/auth/login", "/auth/callback", "/auth/finish", "/auth/logout":
	default:
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !s.HostAllowed(r) {
		deny(w, 404)
		return true
	}
	if isMetadata {
		if !s.isControl(r.Host) {
			deny(w, 404)
			return true
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			w.Header().Set("Allow", "GET, HEAD")
			deny(w, 405)
			return true
		}
		d := map[string]any{"resource": s.control.String() + "/mcp", "bearer_methods_supported": []string{"header"}}
		if !s.cfg.Dev {
			d["authorization_servers"] = []string{s.cfg.Issuer}
		}
		if s.cfg.RequiredScope != "" {
			d["scopes_supported"] = []string{s.cfg.RequiredScope}
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method != "HEAD" {
			_ = json.NewEncoder(w).Encode(d)
		}
		return true
	}
	if r.URL.Path == "/auth/logout" {
		if r.Method != "POST" {
			w.Header().Set("Allow", "POST")
			deny(w, 405)
			return true
		}
		s.logout(w, r)
		return true
	}
	if r.Method != "GET" {
		w.Header().Set("Allow", "GET")
		deny(w, 405)
		return true
	}
	switch r.URL.Path {
	case "/auth/login":
		s.login(w, r)
	case "/auth/callback":
		s.callback(w, r)
	case "/auth/finish":
		s.finish(w, r)
	}
	return true
}
func (s *Service) login(w http.ResponseWriter, r *http.Request) {
	if v := r.URL.Query().Get("ticket"); v != "" {
		var d destination
		if !s.isControl(r.Host) || !s.open("destination", v, &d) || !s.destOK(d) || !s.isSite(d.Host) {
			deny(w, 400)
			return
		}
		s.authorize(w, r, d)
		return
	}
	path, ok := nextPath(r.URL.Query().Get("next"))
	if !ok {
		deny(w, 400)
		return
	}
	d := destination{Host: strings.ToLower(r.Host), Path: path, Exp: s.now().Add(authTTL).Unix()}
	if s.isControl(r.Host) {
		s.authorize(w, r, d)
		return
	}
	if _, ok := s.Browser(r); ok {
		topRedirect(w, r, path)
		return
	}
	binding := random()
	d.Binding = hash(binding)
	t, e := s.seal("destination", d)
	if e != nil {
		deny(w, 500)
		return
	}
	if !s.beginFlow(w, r, "binding", d.Binding, binding) {
		return
	}
	topRedirect(w, r, s.control.String()+"/auth/login?ticket="+url.QueryEscape(t))
}
func (s *Service) authorize(w http.ResponseWriter, r *http.Request, d destination) {
	if v, ok := s.liveSession(r); ok {
		s.returnTo(w, r, d, v.Who, v.Family, v.Exp)
		return
	}
	if s.cfg.Dev {
		s.completeLogin(w, r, d, s.devIdentity(), s.now().Add(sessionTTL).Unix())
		return
	}
	tx := login{State: random(), Verifier: oauth2.GenerateVerifier(), Nonce: random(), Dest: d, Exp: s.now().Add(authTTL).Unix()}
	c, e := s.seal("transaction", tx)
	if e != nil || len(c) > 3750 {
		deny(w, 400)
		return
	}
	if !s.beginFlow(w, r, "oidc", tx.State, c) {
		return
	}
	topRedirect(w, r, s.oauth.AuthCodeURL(tx.State, oauth2.S256ChallengeOption(tx.Verifier), oidc.Nonce(tx.Nonce)))
}
func (s *Service) callback(w http.ResponseWriter, r *http.Request) {
	if !s.isControl(r.Host) || s.cfg.Dev {
		deny(w, 404)
		return
	}
	var tx login
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q["state"]) != 1 || len(q["code"]) > 1 || len(q["iss"]) > 1 || len(q["error"]) > 1 {
		s.rejectCallback(w, 400, "invalid_parameters")
		return
	}
	// RFC 9207: a provider advertising response issuer identification must
	// supply iss. Any iss, including from older providers, must match exactly.
	if (s.responseIssuer || q.Has("iss")) && q.Get("iss") != s.cfg.Issuer {
		s.rejectCallback(w, 400, "issuer_mismatch")
		return
	}
	state := q.Get("state")
	if len(state) != 43 { // 32 random bytes in unpadded base64url
		s.rejectCallback(w, 400, "invalid_state")
		return
	}
	name := flowKind("oidc", state)
	c, e := r.Cookie(s.name(name))
	if e != nil || !s.open("transaction", c.Value, &tx) || !s.destOK(tx.Dest) || !equal(state, tx.State) {
		s.rejectCallback(w, 400, "invalid_transaction")
		return
	}
	// Consume before exchange, across replicas, even if a transient exchange
	// error requires the author to start a new sign-in.
	if !s.consume(r.Context(), "oidc", tx.State, tx.Exp) {
		s.rejectCallback(w, 400, "transaction_unavailable")
		return
	}
	s.cookie(w, name, "", -1)
	if q.Get("code") == "" || len(q.Get("code")) > 4096 || q.Has("error") {
		s.rejectCallback(w, 400, "authorization_unsuccessful")
		return
	}
	ctx := s.oidcCtx(r.Context())
	tok, e := s.oauth.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(tx.Verifier))
	if e != nil {
		s.rejectCallback(w, 401, "exchange_failed")
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	t, e := s.idVerifier.Verify(ctx, raw)
	if e != nil || !equal(t.Nonce, tx.Nonce) {
		s.rejectCallback(w, 401, "invalid_id_token")
		return
	}
	var az struct {
		Authorized string `json:"azp"`
	}
	// No other ID token audiences are configured as trusted on this client.
	if t.Claims(&az) != nil || len(t.Audience) != 1 || t.Audience[0] != s.cfg.ClientID ||
		(az.Authorized != "" && az.Authorized != s.cfg.ClientID) ||
		(t.AccessTokenHash != "" && t.VerifyAccessToken(tok.AccessToken) != nil) {
		s.rejectCallback(w, 401, "invalid_token_binding")
		return
	}
	who, e := s.identity(t)
	if e != nil {
		s.rejectCallback(w, 403, "missing_identity")
		return
	}
	exp := min(t.Expiry.Unix(), s.now().Add(sessionTTL).Unix())
	s.completeLogin(w, r, tx.Dest, who, exp)
}
func (s *Service) completeLogin(w http.ResponseWriter, r *http.Request, d destination, who Identity, exp int64) {
	if !s.audit(r.Context(), who, "auth.login") {
		deny(w, 503)
		return
	}
	family := random()
	if !s.mint(w, s.control.Host, family, who, exp) {
		deny(w, 401)
		return
	}
	s.returnTo(w, r, d, who, family, exp)
}
func (s *Service) returnTo(w http.ResponseWriter, r *http.Request, d destination, who Identity, family string, exp int64) {
	if !s.destOK(d) {
		deny(w, 400)
		return
	}
	if s.isControl(d.Host) {
		topRedirect(w, r, d.Path)
		return
	}
	who.CSRF = ""
	h := handoff{Dest: d, Who: who, Family: family, Once: random(), SessionExp: exp, Exp: min(exp, s.now().Add(time.Minute).Unix())}
	t, e := s.seal("handoff", h)
	if e != nil {
		deny(w, 500)
		return
	}
	topRedirect(w, r, s.control.Scheme+"://"+d.Host+"/auth/finish?ticket="+url.QueryEscape(t))
}
func (s *Service) finish(w http.ResponseWriter, r *http.Request) {
	var h handoff
	if !s.isSite(r.Host) || !s.open("handoff", r.URL.Query().Get("ticket"), &h) || !s.destOK(h.Dest) || !strings.EqualFold(r.Host, h.Dest.Host) {
		deny(w, 400)
		return
	}
	name := flowKind("binding", h.Dest.Binding)
	c, e := r.Cookie(s.name(name))
	if e != nil || !equal(h.Dest.Binding, hash(c.Value)) || !s.familyActive(r.Context(), h.Family) || !s.consume(r.Context(), "handoff", h.Once, h.Exp) {
		deny(w, 400)
		return
	}
	s.cookie(w, name, "", -1)
	if !s.mint(w, h.Dest.Host, h.Family, h.Who, h.SessionExp) {
		deny(w, 401)
		return
	}
	topRedirect(w, r, h.Dest.Path)
}
