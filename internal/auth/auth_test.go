package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// The issuer serves real TLS discovery, JWKS and token exchange; every token is
// signed. No production validation option is relaxed for the test.
type testIssuer struct {
	*httptest.Server
	key       *rsa.PrivateKey
	mu        sync.Mutex
	codes     map[string][2]string
	exchanges int
}

type issuerProfile struct {
	metadata   func(map[string]any)
	idClaims   func(map[string]any)
	clientAuth func(*http.Request) bool
}

func issuer(t *testing.T) *testIssuer {
	return newIssuer(t, issuerProfile{})
}
func newIssuer(t *testing.T, profile issuerProfile) *testIssuer {
	t.Helper()
	k, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	f := &testIssuer{key: k, codes: make(map[string][2]string)}
	m := http.NewServeMux()
	jsonOut := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	m.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		metadata := map[string]any{"issuer": f.URL, "authorization_endpoint": f.URL + "/authorize", "token_endpoint": f.URL + "/token", "jwks_uri": f.URL + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}, "code_challenge_methods_supported": []string{"S256"}, "authorization_response_iss_parameter_supported": true}
		if profile.metadata != nil {
			profile.metadata(metadata)
		}
		jsonOut(w, metadata)
	})
	m.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &k.PublicKey, KeyID: "1", Algorithm: "RS256", Use: "sig"}}})
	})
	m.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("redirect_uri") != "https://studio.example.com/auth/callback" || q.Get("response_type") != "code" || q.Get("client_id") != "studio" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("nonce") == "" || q.Get("state") == "" {
			deny(w, 400)
			return
		}
		f.mu.Lock()
		code := random()
		f.codes[code] = [2]string{q.Get("nonce"), q.Get("code_challenge")}
		f.mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?"+url.Values{"code": {code}, "state": {q.Get("state")}, "iss": {f.URL}}.Encode(), 303)
	})
	m.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if profile.clientAuth != nil && !profile.clientAuth(r) {
			deny(w, 401)
			return
		}
		f.mu.Lock()
		f.exchanges++
		code := r.Form.Get("code")
		grant, ok := f.codes[code]
		delete(f.codes, code)
		f.mu.Unlock()
		nonce, challenge := grant[0], grant[1]
		if r.Method != "POST" || !ok || r.Form.Get("redirect_uri") != "https://studio.example.com/auth/callback" || challenge == "" || hash(r.Form.Get("code_verifier")) != challenge {
			deny(w, 400)
			return
		}
		v := f.claims("studio")
		v["nonce"] = nonce
		hash := sha256.Sum256([]byte("unused"))
		v["at_hash"] = enc.EncodeToString(hash[:16])
		if profile.idClaims != nil {
			profile.idClaims(v)
		}
		tok, e := f.token("JWT", v)
		if e != nil {
			deny(w, 500)
			return
		}
		jsonOut(w, map[string]any{"access_token": "unused", "token_type": "Bearer", "id_token": tok})
	})
	f.Server = httptest.NewTLSServer(m)
	t.Cleanup(f.Close)
	return f
}
func (f *testIssuer) claims(aud string) map[string]any {
	return map[string]any{"iss": f.URL, "aud": aud, "iat": time.Now().Add(-time.Second).Unix(), "exp": time.Now().Add(time.Hour).Unix(), "sub": "ada", "workspace": "operations", "roles": []string{"viewer", "publisher"}, "scope": "sites"}
}
func (f *testIssuer) token(typ string, c map[string]any) (string, error) {
	o := (&jose.SignerOptions{}).WithType(jose.ContentType(typ)).WithHeader("kid", "1")
	s, e := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: f.key}, o)
	if e != nil {
		return "", e
	}
	b, e := json.Marshal(c)
	if e != nil {
		return "", e
	}
	j, e := s.Sign(b)
	if e != nil {
		return "", e
	}
	return j.CompactSerialize()
}
func config(f *testIssuer) Config {
	var mu sync.Mutex
	seen := map[string]struct{}{}
	return Config{ControlURL: "https://studio.example.com", SitesDomain: "sites.example.net", Issuer: f.URL, ClientID: "studio", ClientSecret: "test-secret", Audience: "https://studio.example.com/mcp", SessionKey: random(), RequiredScope: "sites", HTTPClient: f.Client(), Audit: func(context.Context, Identity, string) error { return nil }, Consume: func(_ context.Context, id string, _ time.Time) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		if _, ok := seen[id]; ok {
			return false, nil
		}
		seen[id] = struct{}{}
		return true, nil
	}, RevokeSession: func(_ context.Context, key string, _ time.Time, who Identity) error {
		mu.Lock()
		defer mu.Unlock()
		seen[key] = struct{}{}
		return nil
	}, IsConsumed: func(_ context.Context, id string) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		_, ok := seen[id]
		return ok, nil
	}}
}
func service(t *testing.T, c Config) *Service {
	t.Helper()
	s, e := New(context.Background(), c)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func req(method, addr string, cs ...*http.Cookie) *http.Request {
	r := httptest.NewRequest(method, addr, nil)
	r.RemoteAddr = "127.0.0.1:53306"
	for _, c := range cs {
		r.AddCookie(c)
	}
	return r
}
func hit(t *testing.T, s *Service, r *http.Request, want int) *http.Response {
	t.Helper()
	w := httptest.NewRecorder()
	if !s.Handle(w, r) {
		t.Fatal("auth route missing")
	}
	v := w.Result()
	if v.StatusCode != want {
		t.Fatalf("%s: got status %d, want %d", r.URL.Path, v.StatusCode, want)
	}
	return v
}
func getCookie(t *testing.T, r *http.Response, name string) *http.Cookie {
	t.Helper()
	for _, c := range r.Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatal("missing cookie " + name)
	return nil
}

func flowCookie(t *testing.T, resp *http.Response, kind string) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if strings.HasSuffix(c.Name, "-"+kind) && c.Value != "" {
			return c
		}
	}
	t.Fatal("flow cookie missing: " + kind)
	return nil
}

func TestBrowserSiteHandoffAndCSRF(t *testing.T) {
	f := issuer(t)
	cfg := config(f)
	revoke := cfg.RevokeSession
	var revocations int
	cfg.RevokeSession = func(ctx context.Context, key string, until time.Time, who Identity) error {
		if who.Subject != "ada" || who.Workspace != "operations" || who.CSRF != "" {
			t.Fatal("unverified logout actor")
		}
		revocations++
		return revoke(ctx, key, until, who)
	}
	s, second := service(t, cfg), service(t, cfg)
	site := "https://a.sites.example.net"
	ctl := cfg.ControlURL
	first := hit(t, s, req("GET", site+"/auth/login?next=%2Fhandbook%3Fmonth%3D1"), 303)
	binding := flowCookie(t, first, "binding")
	if !binding.Secure || binding.Domain != "" || !binding.HttpOnly || binding.SameSite != http.SameSiteLaxMode {
		t.Fatal("content binding not host-only secure")
	}
	login := hit(t, second, req("GET", first.Header.Get("Location")), 303)
	tx := flowCookie(t, login, "oidc")
	cl := *f.Client()
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	auth, e := cl.Get(login.Header.Get("Location"))
	if e != nil {
		t.Fatal(e)
	}
	auth.Body.Close()
	if auth.StatusCode != 303 {
		t.Fatalf("authorization: %v", auth.Status)
	}
	callback := auth.Header.Get("Location")
	hit(t, s, req("GET", strings.Replace(callback, "state=", "state=invalid", 1), tx), 400)
	redirect := hit(t, s, req("GET", callback, tx), 303)
	hit(t, second, req("GET", callback, tx), 400)
	f.mu.Lock()
	n := f.exchanges
	f.mu.Unlock()
	if n != 1 {
		t.Fatalf("replayed token exchange: %d", n)
	}
	controlCookie := getCookie(t, redirect, s.name("session"))
	if !strings.HasPrefix(redirect.Header.Get("Location"), site+"/auth/finish?ticket=") {
		t.Fatal("wrong content redirect")
	}
	handoff := redirect.Header.Get("Location")
	hit(t, s, req("GET", handoff), 400)
	hit(t, second, req("GET", strings.Replace(handoff, "a.sites.", "b.sites.", 1), binding), 400)
	finished := hit(t, second, req("GET", handoff, binding), 303)
	if finished.Header.Get("Location") != "/handbook?month=1" {
		t.Fatal("path lost")
	}
	siteCookie := getCookie(t, finished, s.name("session"))
	hit(t, s, req("GET", handoff, binding), 400)
	if _, ok := s.Browser(req("GET", site+"/", controlCookie)); ok {
		t.Fatal("control cookie crossed hosts")
	}
	if _, ok := s.Browser(req("GET", "https://b.sites.example.net/", siteCookie)); ok {
		t.Fatal("cookie crossed sibling hosts")
	}
	page := req("GET", ctl+"/", controlCookie)
	who, ok := s.Browser(page)
	if !ok || who.Subject != "ada" || who.Workspace != "operations" || who.Role != Publisher || who.CSRF == "" {
		t.Fatalf("identity: %+v", who)
	}
	post := req("POST", ctl+"/approve", controlCookie)
	if !s.ValidCSRF(post, who.CSRF) || s.ValidCSRF(page, who.CSRF) || s.ValidCSRF(post, "invalid") {
		t.Fatal("CSRF method/token validation")
	}
	post.Header.Set("Origin", site)
	if s.ValidCSRF(post, who.CSRF) {
		t.Fatal("cross-site origin accepted")
	}
	// Capture a still-pending handoff. Revoking its parent session must also
	// prevent finishing this sign-in on another content host.
	start := hit(t, s, req("GET", "https://b.sites.example.net/auth/login"), 303)
	pendingBinding := flowCookie(t, start, "binding")
	pending := hit(t, second, req("GET", start.Header.Get("Location"), controlCookie), 303)
	siteWho, ok := second.Browser(req("GET", site+"/", siteCookie))
	if !ok {
		t.Fatal("expected content session before logout")
	}
	signOut := httptest.NewRequest("POST", site+"/auth/logout", strings.NewReader(url.Values{"csrf": {siteWho.CSRF}}.Encode()))
	signOut.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	signOut.AddCookie(siteCookie)
	o := hit(t, s, signOut, 303)
	if revocations != 1 || getCookie(t, o, s.name("session")).MaxAge >= 0 {
		t.Fatal("session not cleared")
	}
	if _, ok := second.Browser(req("GET", site+"/", siteCookie)); ok {
		t.Fatal("content session remained valid after logout on another replica")
	}
	if _, ok := second.Browser(req("GET", ctl+"/", controlCookie)); ok {
		t.Fatal("old control session still usable after logout")
	}
	hit(t, second, req("GET", pending.Header.Get("Location"), pendingBinding), 400)
	bogus := *siteCookie
	bogus.Value = "AA" + bogus.Value
	if _, ok := s.Browser(req("GET", site+"/", &bogus)); ok {
		t.Fatal("modified cookie passed")
	}
	s.now = func() time.Time { return time.Now().Add(16 * time.Minute) }
	if _, ok := s.Browser(req("GET", site+"/", siteCookie)); ok {
		t.Fatal("expired session passed")
	}
}
func TestAccessTokens(t *testing.T) {
	f := issuer(t)
	cfg := config(f)
	s := service(t, cfg)
	check := func(t *testing.T, typ string, v map[string]any, ok bool) {
		t.Helper()
		token, e := f.token(typ, v)
		if e != nil {
			t.Fatal(e)
		}
		r := req("POST", cfg.ControlURL+"/mcp")
		r.Header.Set("Authorization", "Bearer "+token)
		w, e := s.Bearer(r)
		if ok != (e == nil) || ok && (w.Role != Publisher || w.Workspace != "operations" || w.Subject != "ada") {
			t.Fatalf("verified=%v want=%v: %v", e == nil, ok, e)
		}
	}
	check(t, "at+jwt", f.claims(cfg.Audience), true)
	check(t, "JWT", f.claims(cfg.Audience), false)
	for name, alter := range map[string]func(map[string]any){"wrong audience": func(c map[string]any) { c["aud"] = "studio" }, "wrong issuer": func(c map[string]any) { c["iss"] = "https://different.example" }, "expired": func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() }, "not yet valid": func(c map[string]any) { c["nbf"] = time.Now().Add(time.Minute).Unix() }, "unknown role": func(c map[string]any) { c["roles"] = "owner" }, "missing workspace": func(c map[string]any) { delete(c, "workspace") }, "no scope": func(c map[string]any) { delete(c, "scope") }} {
		t.Run(name, func(t *testing.T) { c := f.claims(cfg.Audience); alter(c); check(t, "at+jwt", c, false) })
	}
	other := issuer(t)
	tok, e := other.token("at+jwt", f.claims(cfg.Audience))
	if e != nil {
		t.Fatal(e)
	}
	r := req("POST", cfg.ControlURL+"/mcp")
	r.Header.Set("Authorization", "Bearer "+tok)
	if _, e = s.Bearer(r); e == nil {
		t.Fatal("foreign signature accepted")
	}
	r = req("GET", cfg.ControlURL+"/.well-known/oauth-protected-resource/mcp")
	r.Host = "foreign.example"
	hit(t, s, r, 404)
	m := hit(t, s, req("GET", cfg.ControlURL+"/.well-known/oauth-protected-resource/mcp"), 200)
	var d map[string]any
	if json.NewDecoder(m.Body).Decode(&d) != nil || d["resource"] != cfg.ControlURL+"/mcp" {
		t.Fatal("resource document incorrect")
	}
}
func TestDevelopmentBoundary(t *testing.T) {
	cfg := Config{Dev: true, ControlURL: "http://localhost:7878", SitesDomain: "localhost", DevToken: random()}
	s := service(t, cfg)
	ok := req("POST", cfg.ControlURL+"/mcp")
	ok.Header.Set("Authorization", "Bearer "+cfg.DevToken)
	if w, e := s.Bearer(ok); e != nil || w.Role != Admin {
		t.Fatal("explicit local bearer rejected")
	}
	for name, alter := range map[string]func(*http.Request){"remote": func(r *http.Request) { r.RemoteAddr = "192.0.2.1:5500" }, "forwarded": func(r *http.Request) { r.Header.Set("X-Forwarded-For", "127.0.0.1") }, "other host": func(r *http.Request) { r.Host = "other.example" }, "wrong port": func(r *http.Request) { r.Host = "localhost:9999" }} {
		t.Run(name, func(t *testing.T) { r := req("GET", cfg.ControlURL+"/auth/login"); alter(r); hit(t, s, r, 404) })
	}
	for _, n := range []string{"//other.example", "/%2fother.example", "/\\other.example", "https://other.example", "/auth/login"} {
		if _, ok := nextPath(n); ok {
			t.Fatalf("unsafe destination %q", n)
		}
	}
	cfg.SitesDomain = "sites.example.com"
	if _, e := New(context.Background(), cfg); e == nil {
		t.Fatal("non-loopback domain accepted in dev")
	}
}

// Use a browser cookie jar so that the second response actually overwrites a
// cookie if its name and host are the same. Complete callbacks in reverse order.
func TestTwoSignInsFromDifferentTabs(t *testing.T) {
	f := issuer(t)
	cfg := config(f)
	first, second := service(t, cfg), service(t, cfg)
	jar, _ := cookiejar.New(nil)
	const site = "https://a.sites.example.net"
	get := func(s *Service, addr string, want int) *http.Response {
		u, err := url.Parse(addr)
		if err != nil {
			t.Fatal(err)
		}
		r := req("GET", addr, jar.Cookies(u)...)
		v := hit(t, s, r, want)
		jar.SetCookies(u, v.Cookies())
		return v
	}
	browser := *f.Client()
	browser.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	var callbacks []string
	for _, page := range []string{"/handbook", "/schedule"} {
		start := get(first, site+"/auth/login?next="+url.QueryEscape(page), 303)
		login := get(second, start.Header.Get("Location"), 303)
		auth, err := browser.Get(login.Header.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		auth.Body.Close()
		callbacks = append(callbacks, auth.Header.Get("Location"))
	}
	for _, i := range []int{1, 0} {
		res := get(first, callbacks[i], 303)
		finish := get(second, res.Header.Get("Location"), 303)
		if finish.Header.Get("Location") != []string{"/handbook", "/schedule"}[i] {
			t.Fatalf("wrong tab destination: %v", finish.Header.Get("Location"))
		}
	}
	for _, host := range []string{site, cfg.ControlURL} {
		u, _ := url.Parse(host)
		cookies := jar.Cookies(u)
		if len(cookies) != 1 || cookies[0].Name != first.name("session") {
			t.Fatalf("unfinished cookies after both sign-ins on %s", host)
		}
	}
}

func TestBoundUnfinishedSignIns(t *testing.T) {
	s := service(t, Config{Dev: true, ControlURL: "http://localhost:7878", SitesDomain: "localhost"})
	const addr = "http://site.localhost:7878/auth/login?next=/handbook"
	var active []*http.Cookie
	for i := 0; i < maxPendingFlows; i++ {
		res := hit(t, s, req("GET", addr, active...), 303)
		active = append(active, flowCookie(t, res, "binding"))
	}
	res := hit(t, s, req("GET", addr, active...), 429)
	if len(res.Cookies()) != 0 || res.Header.Get("Retry-After") == "" {
		t.Fatal("expected retry guidance and no new cookie")
	}
}

// The injected fixture client still trusts its private CA. Both discovery and
// JSON endpoints use the copy; HTTP redirect responses are returned as-is.
func TestOIDCBackendUsesDirectEndpoints(t *testing.T) {
	var calls int
	f := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Redirect(w, r, "/different-endpoint", http.StatusFound)
	}))
	defer f.Close()
	client := oidcHTTPClient(f.Client())
	got, err := client.Get(f.URL)
	if err != nil {
		t.Fatal(err)
	}
	got.Body.Close()
	if got.StatusCode != 302 || calls != 1 || f.Client().CheckRedirect != nil {
		t.Fatalf("redirect policy or original client changed, got %d calls, HTTP %d", calls, got.StatusCode)
	}
}

func pendingLogin(t *testing.T, f *testIssuer, s *Service) (callback *url.URL, flow *http.Cookie) {
	t.Helper()
	login := hit(t, s, req("GET", s.cfg.ControlURL+"/auth/login"), 303)
	browser := *f.Client()
	browser.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := browser.Get(login.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	callback, err = resp.Location()
	if err != nil {
		t.Fatal(err)
	}
	return callback, flowCookie(t, login, "oidc")
}

func TestCallbackResponseBinding(t *testing.T) {
	f := issuer(t)
	s := service(t, config(f))
	callback, flow := pendingLogin(t, f, s)
	for _, alter := range []func(url.Values){
		func(q url.Values) { q.Del("iss") },
		func(q url.Values) { q.Set("iss", "https://other.example") },
		func(q url.Values) { q.Add("iss", f.URL) },
		func(q url.Values) { q.Add("state", q.Get("state")) },
		func(q url.Values) { q.Add("code", q.Get("code")) },
	} {
		u := *callback
		q := u.Query()
		alter(q)
		u.RawQuery = q.Encode()
		hit(t, s, req("GET", u.String(), flow), 400)
	}
	f.mu.Lock()
	calls := f.exchanges
	f.mu.Unlock()
	if calls != 0 {
		t.Fatalf("unexpected exchanges: %d", calls)
	}
	hit(t, s, req("GET", callback.String(), flow), 303)
}

func TestDiscoveryAndIDTokenPolicy(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"no PKCE declaration":      func(m map[string]any) { delete(m, "code_challenge_methods_supported") },
		"plain only":               func(m map[string]any) { m["code_challenge_methods_supported"] = []string{"plain"} },
		"incompatible client auth": func(m map[string]any) { m["token_endpoint_auth_methods_supported"] = []string{"private_key_jwt"} },
	} {
		t.Run(name, func(t *testing.T) {
			f := newIssuer(t, issuerProfile{metadata: mutate})
			if _, err := New(context.Background(), config(f)); err == nil {
				t.Fatal("accepted incompatible discovery document")
			}
		})
	}
	for name, mutate := range map[string]func(map[string]any){
		"nonce":               func(m map[string]any) { m["nonce"] = "unrelated" },
		"azp":                 func(m map[string]any) { m["azp"] = "another-client" },
		"additional audience": func(m map[string]any) { m["aud"] = []string{"studio", "another-client"}; m["azp"] = "studio" },
		"at hash":             func(m map[string]any) { m["at_hash"] = "does-not-match" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newIssuer(t, issuerProfile{idClaims: mutate})
			s := service(t, config(f))
			u, flow := pendingLogin(t, f, s)
			res := hit(t, s, req("GET", u.String(), flow), 401)
			for _, c := range res.Cookies() {
				if c.Name == s.name("session") {
					t.Fatal("invalid ID token yielded a session")
				}
			}
		})
	}
	t.Run("legacy issuer response", func(t *testing.T) {
		f := newIssuer(t, issuerProfile{metadata: func(m map[string]any) { delete(m, "authorization_response_iss_parameter_supported") }, idClaims: func(m map[string]any) { delete(m, "at_hash") }})
		s := service(t, config(f))
		u, flow := pendingLogin(t, f, s)
		q := u.Query()
		q.Set("iss", "")
		u.RawQuery = q.Encode()
		hit(t, s, req("GET", u.String(), flow), 400)
		q.Del("iss")
		u.RawQuery = q.Encode()
		hit(t, s, req("GET", u.String(), flow), 303)
	})
}

// A provider may offer both secret methods but register an individual client
// for only one. Do not probe other methods on a failed one-time code.
func TestRegisteredClientAuthentication(t *testing.T) {
	for _, method := range []string{"none", "client_secret_basic", "client_secret_post"} {
		t.Run(method, func(t *testing.T) {
			f := newIssuer(t, issuerProfile{
				metadata: func(m map[string]any) {
					m["token_endpoint_auth_methods_supported"] = []string{"client_secret_basic", "client_secret_post", "none"}
				},
				clientAuth: func(r *http.Request) bool {
					id, secret, basic := r.BasicAuth()
					if method == "client_secret_basic" {
						return basic && id == "studio" && secret == "test-secret" && !r.Form.Has("client_secret")
					}
					if basic || r.Form.Get("client_id") != "studio" {
						return false
					}
					if method == "none" {
						return !r.Form.Has("client_secret")
					}
					return r.Form.Get("client_secret") == "test-secret"
				},
			})
			cfg := config(f)
			cfg.ClientAuthMethod = method
			if method == "none" {
				cfg.ClientSecret = ""
			}
			s := service(t, cfg)
			u, flow := pendingLogin(t, f, s)
			hit(t, s, req("GET", u.String(), flow), 303)
		})
	}
}

func TestSessionDomainAndAuditFailure(t *testing.T) {
	f := issuer(t)
	cfg := config(f)
	var failAudit, failRevoke, failRead bool
	var records []string
	cfg.Audit = func(_ context.Context, who Identity, action string) error {
		if who.Subject != "ada" || who.Workspace != "operations" || who.CSRF != "" {
			t.Fatal("unverified identity or CSRF in audit")
		}
		if failAudit {
			return errors.New("audit unavailable")
		}
		records = append(records, action)
		return nil
	}
	revoke := cfg.RevokeSession
	read := cfg.IsConsumed
	cfg.RevokeSession = func(ctx context.Context, key string, exp time.Time, i Identity) error {
		if failRevoke {
			return errors.New("shared store unavailable")
		}
		err := revoke(ctx, key, exp, i)
		if err == nil {
			records = append(records, "auth.logout")
		}
		return err
	}
	cfg.IsConsumed = func(ctx context.Context, key string) (bool, error) {
		if failRead {
			return false, errors.New("shared store unavailable")
		}
		return read(ctx, key)
	}
	s := service(t, cfg)
	u, flow := pendingLogin(t, f, s)
	failAudit = true
	for _, c := range hit(t, s, req("GET", u.String(), flow), 503).Cookies() {
		if c.Name == s.name("session") {
			t.Fatal("login bypassed audit persistence")
		}
	}
	failAudit = false
	u, flow = pendingLogin(t, f, s)
	res := hit(t, s, req("GET", u.String(), flow), 303)
	cookie := getCookie(t, res, s.name("session"))
	who, ok := s.Browser(req("GET", cfg.ControlURL, cookie))
	if !ok {
		t.Fatal("session missing")
	}
	changed := cfg
	changed.RolesClaim = "other_roles"
	other := service(t, changed)
	if _, ok := other.Browser(req("GET", cfg.ControlURL, cookie)); ok {
		t.Fatal("old session accepted after changing trusted claims")
	}
	if string(other.ActionKey()) == string(s.ActionKey()) {
		t.Fatal("old action tickets accepted after claim configuration changed")
	}
	if s.IdentityRealm() != f.URL {
		t.Fatal("production issuer not bound")
	}
	dev := service(t, Config{Dev: true, ControlURL: "http://localhost", SitesDomain: "localhost", Issuer: f.URL})
	if dev.IdentityRealm() != "urn:shoowcase:development" {
		t.Fatal("dev mode accepted production identity realm")
	}
	logout := func(status int) *http.Response {
		post := httptest.NewRequest("POST", cfg.ControlURL+"/auth/logout", strings.NewReader(url.Values{"csrf": {who.CSRF}}.Encode()))
		post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		post.AddCookie(cookie)
		return hit(t, s, post, status)
	}
	failRevoke = true
	for _, c := range logout(503).Cookies() {
		if c.Name == s.name("session") {
			t.Fatal("failed sign-out discarded or replaced the cookie needed to retry")
		}
	}
	if len(records) != 1 || records[0] != "auth.login" {
		t.Fatalf("unexpected committed events %v", records)
	}
	failRead = true
	if _, ok := s.Browser(req("GET", cfg.ControlURL, cookie)); ok {
		t.Fatal("session accepted with unavailable revocation reads")
	}
	failRevoke, failRead = false, false
	if getCookie(t, logout(303), s.name("session")).MaxAge >= 0 {
		t.Fatal("successful retry did not remove the session cookie")
	}
	if _, ok := s.Browser(req("GET", cfg.ControlURL, cookie)); ok {
		t.Fatal("session still valid after successful sign-out")
	}
	if len(records) != 2 || records[1] != "auth.logout" {
		t.Fatalf("unexpected committed events after retry: %v", records)
	}
}

func TestPinnedOIDCAdministrator(t *testing.T) {
	for _, tc := range []struct {
		subject string
		want    int
	}{{"owner-id", 303}, {"another-user", 403}, {"local:owner-id", 403}} {
		t.Run(tc.subject, func(t *testing.T) {
			f := newIssuer(t, issuerProfile{idClaims: func(m map[string]any) {
				delete(m, "workspace")
				delete(m, "roles")
				m["sub"] = tc.subject
				m["email"] = "owner@example.com"
			}})
			cfg := config(f)
			cfg.OIDCAdminSubject = "owner-id"
			cfg.OIDCAdminWorkspace = "team"
			s := service(t, cfg)
			callback, flow := pendingLogin(t, f, s)
			res := hit(t, s, req("GET", callback.String(), flow), tc.want)
			if tc.want == 303 {
				i, ok := s.Browser(req("GET", cfg.ControlURL+"/admin", getCookie(t, res, s.name("session"))))
				if !ok || i.Role != Admin || i.Subject != "owner-id" || i.Workspace != "team" || i.LocalGeneration != 0 {
					t.Fatalf("wrong pinned identity: %+v", i)
				}
			}
		})
	}
}

func TestProjectAudienceOnBrowserCallback(t *testing.T) {
	for _, tc := range []struct {
		name, azp string
		audience  []string
		status    int
	}{
		{"one explicitly trusted project", "studio", []string{"project-id", "studio"}, 303},
		{"no authorized client", "", []string{"studio", "project-id"}, 401},
		{"different authorized client", "other", []string{"studio", "project-id"}, 401},
		{"different project", "studio", []string{"studio", "other"}, 401},
		{"additional client", "studio", []string{"studio", "project-id", "other"}, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newIssuer(t, issuerProfile{idClaims: func(m map[string]any) {
				m["aud"] = tc.audience
				if tc.azp != "" {
					m["azp"] = tc.azp
				}
			}})
			cfg := config(f)
			cfg.OIDCProjectAudience = "project-id"
			s := service(t, cfg)
			callback, flow := pendingLogin(t, f, s)
			hit(t, s, req("GET", callback.String(), flow), tc.status)
		})
	}
}
