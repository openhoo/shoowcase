// Package auth authenticates Shoowcase editors, site readers and MCP clients.
// A deployment can use the same issuer for both flows, but an ID token is never
// accepted as an API access token.
package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/net/publicsuffix"
	"golang.org/x/oauth2"
)

type Role string

const (
	Viewer    Role = "viewer"
	Editor    Role = "editor"
	Publisher Role = "publisher"
	Admin     Role = "admin"
)

type Identity struct {
	Subject   string
	Workspace string
	Role      Role
	CSRF      string
}

type Config struct {
	ControlURL, SitesDomain, Issuer, ClientID, ClientSecret, Audience string
	// SessionKey is a random 32-byte AES key in unpadded URL-safe Base64.
	SessionKey, WorkspaceClaim, RolesClaim, RequiredScope string
	Dev                                                   bool
	DevWorkspace, DevSubject, DevToken                    string
	// Consume atomically inserts the key if absent until expiry. All replicas must
	// share a store. Production requires it; development defaults to memory.
	Consume    func(ctx context.Context, purposeKey string, expires time.Time) (bool, error)
	HTTPClient *http.Client
}

type Service struct {
	cfg                        Config
	control                    *url.URL
	sitesHost, sitesPort       string
	aead                       cipher.AEAD
	idVerifier, accessVerifier *oidc.IDTokenVerifier
	oauth                      oauth2.Config
	client                     *http.Client
	now                        func() time.Time
	mu                         sync.Mutex
	markers                    map[string]time.Time
}

var errInvalid = errors.New("invalid credentials")
var enc = base64.RawURLEncoding

func preciseHost(s string) bool {
	if len(s) == 0 || len(s) > 253 {
		return false
	}
	for _, l := range strings.Split(s, ".") {
		if len(l) == 0 || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for _, c := range l {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
func localName(s string) bool {
	return s == "localhost" || strings.HasSuffix(s, ".localhost") || net.ParseIP(s).IsLoopback()
}
func simpleURL(raw string) (*url.URL, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return nil, errors.New("invalid URL")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, errors.New("invalid URL scheme")
	}
	return u, nil
}

// CanonicalHosts returns the exact control origin and DNS suffix used by both
// auth and HTTP routing. Content shares the control origin's port. An explicit
// port in the DNS suffix is a configuration error, not a second port source.
func CanonicalHosts(control, sitesDomain string) (origin, zone string, err error) {
	u, e := simpleURL(control)
	if e != nil || (u.Path != "" && u.Path != "/") || u.RawPath != "" || strings.HasSuffix(u.Host, ":") {
		return "", "", errors.New("ControlURL must be an origin")
	}
	host := strings.ToLower(u.Hostname())
	if !preciseHost(host) && net.ParseIP(host) == nil {
		return "", "", errors.New("invalid control hostname")
	}
	port := u.Port()
	if port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return "", "", errors.New("invalid control port")
		}
		port = strconv.Itoa(n)
	}
	if u.Scheme == "https" && port == "443" || u.Scheme == "http" && port == "80" {
		port = ""
	}
	u.Host = host
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	}
	u.Path = ""
	zone = strings.ToLower(sitesDomain)
	if !preciseHost(zone) {
		return "", "", errors.New("SitesDomain must be a bare DNS suffix without a port or wildcard")
	}
	return u.String(), zone, nil
}

func New(ctx context.Context, cfg Config) (*Service, error) {
	origin, zone, e := CanonicalHosts(cfg.ControlURL, cfg.SitesDomain)
	if e != nil {
		return nil, e
	}
	u, _ := url.Parse(origin)
	cfg.ControlURL, cfg.SitesDomain = origin, zone
	port := u.Port()
	if cfg.WorkspaceClaim == "" {
		cfg.WorkspaceClaim = "workspace"
	}
	if cfg.RolesClaim == "" {
		cfg.RolesClaim = "roles"
	}
	if cfg.Dev {
		if !localName(u.Hostname()) || !localName(zone) {
			return nil, errors.New("development origins must use loopback")
		}
		if cfg.DevWorkspace == "" {
			cfg.DevWorkspace = "local"
		}
		if cfg.DevSubject == "" {
			cfg.DevSubject = "local-user"
		}
		if cfg.DevToken != "" && len(cfg.DevToken) < 32 {
			return nil, errors.New("development bearer token must be at least 32 characters")
		}
	} else {
		if u.Scheme != "https" || !preciseHost(u.Hostname()) {
			return nil, errors.New("production ControlURL must be HTTPS with a DNS hostname")
		}
		ctl, e1 := publicsuffix.EffectiveTLDPlusOne(u.Hostname())
		site, e2 := publicsuffix.EffectiveTLDPlusOne(zone)
		if e1 != nil || e2 != nil || ctl == site || localName(zone) || localName(u.Hostname()) {
			return nil, errors.New("content and control must use separate registrable domains")
		}
	}
	if cfg.Audience == "" {
		cfg.Audience = u.String() + "/mcp"
	}
	if !cfg.Dev && (cfg.Audience == cfg.ClientID || cfg.ClientID == "") {
		return nil, errors.New("configure distinct OIDC client ID and API audience")
	}
	key, e := enc.DecodeString(cfg.SessionKey)
	if cfg.Dev && cfg.SessionKey == "" {
		key = make([]byte, 32)
		_, e = io.ReadFull(rand.Reader, key)
	}
	if e != nil || len(key) != 32 {
		return nil, errors.New("SessionKey must be 32 random bytes encoded with base64url without padding")
	}
	b, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	a, e := cipher.NewGCM(b)
	if e != nil {
		return nil, e
	}
	s := &Service{cfg: cfg, control: u, sitesHost: zone, sitesPort: port, aead: a, now: time.Now, markers: make(map[string]time.Time), client: cfg.HTTPClient}
	if s.client == nil {
		s.client = &http.Client{Timeout: 12 * time.Second}
	}
	if !cfg.Dev && cfg.Consume == nil {
		return nil, errors.New("production requires a shared one-time token store")
	}
	if cfg.Dev {
		return s, nil
	}
	iss, e := simpleURL(cfg.Issuer)
	if e != nil || iss.Scheme != "https" {
		return nil, errors.New("OIDC issuer must be an HTTPS URL")
	}
	p, e := oidc.NewProvider(s.oidcCtx(ctx), cfg.Issuer)
	if e != nil {
		return nil, fmt.Errorf("discover OIDC issuer: %w", e)
	}
	var d struct {
		JWKS string `json:"jwks_uri"`
	}
	if e = p.Claims(&d); e != nil {
		return nil, e
	}
	for _, endpoint := range []string{p.Endpoint().AuthURL, p.Endpoint().TokenURL, d.JWKS} {
		x, e := simpleURL(endpoint)
		if e != nil || x.Scheme != "https" {
			return nil, errors.New("OIDC endpoints must be HTTPS")
		}
	}
	verifier := func(aud string) *oidc.IDTokenVerifier {
		return p.Verifier(&oidc.Config{ClientID: aud, SupportedSigningAlgs: []string{"RS256", "ES256"}, Now: func() time.Time { return s.now() }})
	}
	s.idVerifier = verifier(cfg.ClientID)
	s.accessVerifier = verifier(cfg.Audience)
	s.oauth = oauth2.Config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, RedirectURL: u.String() + "/auth/callback", Endpoint: p.Endpoint(), Scopes: []string{oidc.ScopeOpenID, "profile"}}
	return s, nil
}
func (s *Service) oidcCtx(ctx context.Context) context.Context {
	return oidc.ClientContext(ctx, s.client)
}
func (s *Service) isControl(host string) bool { return strings.EqualFold(host, s.control.Host) }
func (s *Service) isSite(host string) bool {
	// Avoid allowing userinfo, ports other than the configured port, an extra
	// subdomain, or any host supplied through proxy forwarding headers.
	h := strings.ToLower(host)
	suffix := "." + s.sitesHost
	if s.sitesPort != "" {
		suffix += ":" + s.sitesPort
	}
	l, ok := strings.CutSuffix(h, suffix)
	return ok && !strings.Contains(l, ".") && preciseHost(l)
}
func (s *Service) HostAllowed(r *http.Request) bool {
	if !s.isControl(r.Host) && !s.isSite(r.Host) {
		return false
	}
	if !s.cfg.Dev {
		return true
	}
	h, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil || !net.ParseIP(h).IsLoopback() {
		return false
	}
	for k := range r.Header {
		k = strings.ToLower(k)
		if k == "forwarded" || k == "x-real-ip" || strings.HasPrefix(k, "x-forwarded-") {
			return false
		}
	}
	return true
}

func (s *Service) seal(kind string, v any) (string, error) {
	p, e := json.Marshal(v)
	if e != nil {
		return "", e
	}
	n := make([]byte, s.aead.NonceSize())
	if _, e = io.ReadFull(rand.Reader, n); e != nil {
		return "", e
	}
	return enc.EncodeToString(s.aead.Seal(n, n, p, []byte(kind))), nil
}
func (s *Service) open(kind, token string, v any) bool {
	if len(token) == 0 || len(token) > 8192 {
		return false
	}
	b, e := enc.DecodeString(token)
	n := s.aead.NonceSize()
	if e != nil || len(b) < n+s.aead.Overhead() {
		return false
	}
	data, e := s.aead.Open(nil, b[:n], b[n:], []byte(kind))
	return e == nil && json.Unmarshal(data, v) == nil
}
func random() string {
	b := make([]byte, 32)
	if _, e := io.ReadFull(rand.Reader, b); e != nil {
		panic("system RNG unavailable")
	}
	return enc.EncodeToString(b)
}
func equal(a, b string) bool {
	return a != "" && b != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func hash(v string) string { h := sha256.Sum256([]byte(v)); return enc.EncodeToString(h[:]) }

func (s *Service) name(kind string) string {
	if s.control.Scheme == "https" {
		return "__Host-shoowcase-" + kind
	}
	return "shoowcase-dev-" + kind
}
func (s *Service) cookie(w http.ResponseWriter, kind, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: s.name(kind), Value: value, Path: "/", HttpOnly: true, Secure: s.control.Scheme == "https", SameSite: http.SameSiteLaxMode, MaxAge: age})
}

type session struct {
	Host string
	Exp  int64
	Who  Identity
}

func (s *Service) mint(w http.ResponseWriter, host string, who Identity, exp int64) bool {
	if exp <= s.now().Unix() {
		return false
	}
	who.CSRF = random()
	b, e := s.seal("session", session{strings.ToLower(host), exp, who})
	if e != nil || len(b) > 3750 {
		return false
	}
	s.cookie(w, "session", b, int(exp-s.now().Unix()))
	return true
}
func (s *Service) Browser(r *http.Request) (Identity, bool) {
	if !s.HostAllowed(r) {
		return Identity{}, false
	}
	c, e := r.Cookie(s.name("session"))
	var v session
	if e != nil || !s.open("session", c.Value, &v) || !strings.EqualFold(v.Host, r.Host) || v.Exp <= s.now().Unix() {
		return Identity{}, false
	}
	return v.Who, true
}

// ValidCSRF checks a token shown in a browser form against the authenticated
// session. Recheck the user's publisher role at the operation itself.
func (s *Service) ValidCSRF(r *http.Request, token string) bool {
	if r.Method == "GET" || r.Method == "HEAD" || r.Method == "OPTIONS" {
		return false
	}
	who, ok := s.Browser(r)
	return ok && equal(who.CSRF, token) && (r.Header.Get("Origin") == "" || r.Header.Get("Origin") == s.control.Scheme+"://"+strings.ToLower(r.Host)) && r.Header.Get("Sec-Fetch-Site") != "cross-site"
}

func claimString(m map[string]json.RawMessage, k string) (string, bool) {
	var v string
	e := json.Unmarshal(m[k], &v)
	return v, e == nil && nonEmpty(v)
}
func nonEmpty(s string) bool {
	if strings.TrimSpace(s) == "" || len(s) > 255 {
		return false
	}
	for _, c := range s {
		if c < ' ' || c == 127 {
			return false
		}
	}
	return true
}
func (s *Service) identity(t *oidc.IDToken) (Identity, error) {
	var m map[string]json.RawMessage
	if e := t.Claims(&m); e != nil {
		return Identity{}, errInvalid
	}
	workspace, ok := claimString(m, s.cfg.WorkspaceClaim)
	if !ok || !nonEmpty(t.Subject) {
		return Identity{}, errInvalid
	}
	// Unlike expiration (verified by go-oidc), check nbf without clock leeway.
	var nbf int64
	if raw, ok := m["nbf"]; ok && (json.Unmarshal(raw, &nbf) != nil || nbf > s.now().Unix()) {
		return Identity{}, errInvalid
	}
	if t.IssuedAt.IsZero() || t.IssuedAt.After(s.now().Add(time.Minute)) {
		return Identity{}, errInvalid
	}
	var roles []string
	if v, ok := claimString(m, s.cfg.RolesClaim); ok {
		roles = []string{v}
	} else if json.Unmarshal(m[s.cfg.RolesClaim], &roles) != nil {
		return Identity{}, errInvalid
	}
	best := 0
	var role Role
	ranks := map[string]int{"viewer": 1, "editor": 2, "publisher": 3, "admin": 4}
	for _, v := range roles {
		if rank := ranks[v]; rank > best {
			best = rank
			role = Role(v)
		}
	}
	if best == 0 {
		return Identity{}, errInvalid
	}
	return Identity{Subject: t.Subject, Workspace: workspace, Role: role}, nil
}
func (s *Service) Bearer(r *http.Request) (Identity, error) {
	if !s.isControl(r.Host) || !s.HostAllowed(r) {
		return Identity{}, errInvalid
	}
	h := r.Header.Values("Authorization")
	if len(h) != 1 {
		return Identity{}, errInvalid
	}
	f := strings.Fields(h[0])
	if len(f) != 2 || !strings.EqualFold(f[0], "Bearer") || len(f[1]) > 24576 {
		return Identity{}, errInvalid
	}
	if s.cfg.Dev {
		if !equal(f[1], s.cfg.DevToken) {
			return Identity{}, errInvalid
		}
		return s.devIdentity(), nil
	}
	head, _, ok := strings.Cut(f[1], ".")
	var hdr struct {
		Typ string `json:"typ"`
	}
	b, e := enc.DecodeString(head)
	if !ok || e != nil || json.Unmarshal(b, &hdr) != nil || !(strings.EqualFold(hdr.Typ, "at+jwt") || strings.EqualFold(hdr.Typ, "application/at+jwt")) {
		return Identity{}, errInvalid
	}
	t, e := s.accessVerifier.Verify(s.oidcCtx(r.Context()), f[1])
	if e != nil {
		return Identity{}, errInvalid
	}
	if s.cfg.RequiredScope != "" {
		var v struct {
			Scope string `json:"scope"`
		}
		if t.Claims(&v) != nil {
			return Identity{}, errInvalid
		}
		yes := false
		for _, sc := range strings.Fields(v.Scope) {
			if sc == s.cfg.RequiredScope {
				yes = true
			}
		}
		if !yes {
			return Identity{}, errInvalid
		}
	}
	return s.identity(t)
}
func (s *Service) devIdentity() Identity {
	return Identity{Subject: s.cfg.DevSubject, Workspace: s.cfg.DevWorkspace, Role: Admin}
}
