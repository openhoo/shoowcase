package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openhoo/shoowcase/internal/auth"
	"github.com/openhoo/shoowcase/internal/sites"
)

const testControl = "http://studio.localhost:7878"
const testZone = "sites.localhost"
const devBearer = "test-only-long-local-agent-token-used-for-tests"
const testSite = "http://field.sites.localhost:7878"

var testKey = []byte("01234567890123456789012345678901")

func handlerFixture(t *testing.T, storePath, workspace string, public bool) (*App, *sites.Store) {
	t.Helper()
	return handlerIdentityFixture(t, storePath, "tester", workspace, public)
}
func handlerIdentityFixture(t *testing.T, storePath, subject, workspace string, public bool) (*App, *sites.Store) {
	t.Helper()
	s, e := sites.Open(context.Background(), "sqlite", storePath)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	a, e := auth.New(context.Background(), auth.Config{Dev: true, ControlURL: testControl, SitesDomain: testZone, SessionKey: base64.RawURLEncoding.EncodeToString(testKey), DevToken: devBearer, DevWorkspace: workspace, DevSubject: subject, Consume: s.Consume})
	if e != nil {
		t.Fatal(e)
	}
	app, e := New(Config{ControlURL: testControl, SitesDomain: testZone, Key: testKey, Public: public}, s, a)
	if e != nil {
		t.Fatal(e)
	}
	return app, s
}
func hitRoute(t *testing.T, app *App, method, url string, c *http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, url, nil)
	req.RemoteAddr = "127.0.0.1:45550"
	if c != nil {
		req.AddCookie(c)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)
	return w
}
func testCookie(t *testing.T, w *httptest.ResponseRecorder, suffix string) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if strings.HasSuffix(c.Name, suffix) && c.Value != "" {
			return c
		}
	}
	t.Fatal("authentication cookie missing")
	return nil
}

// Obtain both cookies through the actual scoped redirect flow, without minting
// a session in this test or bypassing its per-host audience.
func browserSessions(t *testing.T, a *App, site string) (control, content *http.Cookie) {
	t.Helper()
	step := hitRoute(t, a, "GET", site+"/auth/login?next=%2F", nil, nil)
	if step.Code != http.StatusSeeOther {
		t.Fatal(step.Code)
	}
	binding := testCookie(t, step, "binding")
	controlStep := hitRoute(t, a, "GET", step.Header().Get("Location"), nil, nil)
	if controlStep.Code != http.StatusSeeOther {
		t.Fatal(controlStep.Code)
	}
	control = testCookie(t, controlStep, "session")
	finish := hitRoute(t, a, "GET", controlStep.Header().Get("Location"), binding, nil)
	if finish.Code != http.StatusSeeOther {
		t.Fatal(finish.Code)
	}
	return control, testCookie(t, finish, "session")
}
func seedPublicSite(t *testing.T, s *sites.Store) string {
	t.Helper()
	return seedSiteFor(t, s, sites.Actor{Subject: "tester", Workspace: "team-a", Role: "publisher"})
}
func seedSiteFor(t *testing.T, s *sites.Store, user sites.Actor) string {
	t.Helper()
	ctx := context.Background()
	call := func(tool string, arg map[string]any) map[string]any {
		b, e := json.Marshal(arg)
		if e != nil {
			t.Fatal(e)
		}
		r, e := s.Execute(ctx, user, tool, b)
		if e != nil {
			t.Fatal(tool, e)
		}
		return r
	}
	r := call("create_site", map[string]any{"slug": "field", "name": "Field Notes", "request_id": "create"})
	r = call("upload_files", map[string]any{"site_id": "field", "request_id": "upload", "expected_revision": r["revision"], "files": []map[string]string{{"path": "index.html", "text": "<p>Release body</p>"}, {"path": "screen.css", "text": "p{color:green}"}}})
	rev := r["revision"].(string)
	call("publish_site", map[string]any{"site_id": "field", "revision": rev, "request_id": "publish"})
	access := map[string]any{"site_id": "field", "revision": rev, "visibility": "public", "request_id": "visibility"}
	b, _ := json.Marshal(access)
	_, err := s.Execute(ctx, user, "set_site_access", b)
	gen, ok := sites.ApprovalGeneration(err)
	if !ok {
		t.Fatal(err)
	}
	ctx = sites.WithApproval(ctx, gen)
	call("set_site_access", access)
	return rev
}

func TestPublicSwitchOnRestartAndHostBoundSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sites.sqlite")
	first, s := handlerFixture(t, path, "team-a", true)
	rev := seedPublicSite(t, s)
	preview := "http://pv-" + rev + "." + testZone + ":7878"
	if w := hitRoute(t, first, "GET", preview+"/screen.css", nil, nil); w.Code != http.StatusSeeOther {
		t.Fatalf("public site exposed a draft asset: %d", w.Code)
	}
	previous := hitRoute(t, first, "GET", testSite+"/screen.css", nil, nil)
	tag := previous.Header().Get("ETag")
	if previous.Code != 200 || previous.Body.String() != "p{color:green}" || tag == "" {
		t.Fatal(previous.Code, previous.Body.String())
	}
	if !strings.Contains(previous.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("response permits stale shared caching")
	}
	if w := hitRoute(t, first, "GET", testSite+"/screen.css", nil, map[string]string{"If-None-Match": tag}); w.Code != 304 {
		t.Fatalf("conditional authorized read: %d", w.Code)
	}
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	// A new handler, new auth service and reopened database with the switch off.
	restarted, _ := handlerFixture(t, path, "team-a", false)
	for _, u := range []string{testSite + "/", testSite + "/screen.css", preview + "/screen.css"} {
		w := hitRoute(t, restarted, "GET", u, nil, map[string]string{"If-None-Match": tag})
		if w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/auth/login") || w.Header().Get("ETag") != "" || strings.Contains(w.Body.String(), "color:green") {
			t.Fatalf("private restart did not gate %s: %d", u, w.Code)
		}
	}
	ctl, site := browserSessions(t, restarted, testSite)
	if w := hitRoute(t, restarted, "GET", testSite+"/screen.css", ctl, nil); w.Code != http.StatusSeeOther {
		t.Fatalf("management cookie accepted on content host: %d", w.Code)
	}
	if w := hitRoute(t, restarted, "GET", testSite+"/screen.css", site, nil); w.Code != 200 || w.Body.String() != "p{color:green}" {
		t.Fatalf("workspace owner cannot read: %d", w.Code)
	}
	if w := hitRoute(t, restarted, "GET", preview+"/screen.css", site, nil); w.Code != http.StatusSeeOther {
		t.Fatalf("live-host cookie accepted on preview: %d", w.Code)
	}
	_, previewCookie := browserSessions(t, restarted, preview)
	if w := hitRoute(t, restarted, "GET", preview+"/screen.css", previewCookie, nil); w.Code != 200 {
		t.Fatalf("signed-in workspace preview: %d", w.Code)
	}
	foreign, _ := handlerFixture(t, path, "another-team", false)
	_, foreignCookie := browserSessions(t, foreign, testSite)
	if w := hitRoute(t, restarted, "GET", testSite+"/screen.css", foreignCookie, map[string]string{"If-None-Match": tag}); w.Code != 404 || w.Header().Get("ETag") != "" {
		t.Fatalf("foreign team read a cached private release: %d", w.Code)
	}
}

func TestControlEndpointsRequireControlHostAndCorrectCredentials(t *testing.T) {
	app, _ := handlerFixture(t, filepath.Join(t.TempDir(), "test.sqlite"), "team-a", false)
	ctl, site := browserSessions(t, app, testSite)
	for _, c := range []*http.Cookie{nil, ctl, site} {
		w := hitRoute(t, app, "POST", testControl+"/mcp", c, nil)
		if w.Code != 401 || !strings.Contains(w.Header().Get("WWW-Authenticate"), testControl+"/.well-known/oauth-protected-resource/mcp") {
			t.Fatalf("control endpoint must advertise OAuth, got %d", w.Code)
		}
	}
	protected := hitRoute(t, app, "GET", testControl+"/.well-known/oauth-protected-resource/mcp", nil, nil)
	var info struct{ Resource string }
	if protected.Code != 200 || json.Unmarshal(protected.Body.Bytes(), &info) != nil || info.Resource != testControl+"/mcp" {
		t.Fatal("OAuth discovery does not lead to MCP")
	}
	authz := map[string]string{"Authorization": "Bearer " + devBearer}
	if w := hitRoute(t, app, "GET", testControl+"/metrics", nil, authz); w.Code != 200 || !strings.Contains(w.Body.String(), "shoowcase_site_responses_total") {
		t.Fatalf("authorized control metrics: %d", w.Code)
	}
	for _, tc := range []struct {
		method, path string
		want         int
	}{{"POST", "/mcp", 405}, {"GET", "/.well-known/oauth-protected-resource/mcp", 404}, {"GET", "/metrics", 404}, {"GET", "/approve", 404}, {"GET", "/upload", 404}} {
		w := hitRoute(t, app, tc.method, testSite+tc.path, site, authz)
		if w.Code != tc.want || w.Header().Get("WWW-Authenticate") != "" {
			t.Fatalf("content host routed management endpoint %s: %d", tc.path, w.Code)
		}
	}
	w := hitRoute(t, app, "POST", testControl+"/mcp", nil, map[string]string{"Authorization": "Bearer " + devBearer, "Origin": testSite})
	if w.Code != 403 {
		t.Fatal("browser origin check", w.Code)
	}
	w = hitRoute(t, app, "GET", "http://unconfigured.example/healthz", nil, nil)
	if w.Code != http.StatusMisdirectedRequest {
		t.Fatal("unconfigured host", w.Code)
	}
}

func TestCanonicalHostsUsedForRoutingAndGeneratedLinks(t *testing.T) {
	ctl := "http://STUDIO.LocalHost:7878/"
	zone := "SITES.LocalHost"
	cfg := auth.Config{Dev: true, ControlURL: ctl, SitesDomain: zone}
	identity, err := auth.New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	s, err := sites.Open(context.Background(), "sqlite", filepath.Join(t.TempDir(), "hosts.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	app, err := New(Config{ControlURL: ctl, SitesDomain: zone, Public: true, Key: testKey}, s, identity)
	if err != nil {
		t.Fatal(err)
	}
	seedPublicSite(t, s)
	if got := app.siteURL("field"); got != testSite+"/" {
		t.Fatalf("noncanonical generated content URL: %s", got)
	}
	w := hitRoute(t, app, "GET", testControl+"/healthz", nil, nil)
	if w.Code != 200 {
		t.Fatalf("normalized control host failed: %d", w.Code)
	}
	w = hitRoute(t, app, "GET", "http://FIELD.SITES.LOCALHOST:7878/screen.css", nil, nil)
	if w.Code != 200 || w.Body.String() != "p{color:green}" {
		t.Fatalf("uppercase site host failed: %d", w.Code)
	}
	for _, domain := range []string{"sites.localhost:7878", "*.sites.localhost", ".sites.localhost"} {
		cfg.SitesDomain = domain
		if _, err := auth.New(context.Background(), cfg); err == nil {
			t.Fatalf("accepted invalid DNS suffix: %s", domain)
		}
		if _, err := New(Config{ControlURL: ctl, SitesDomain: domain, Key: testKey}, s, identity); err == nil {
			t.Fatalf("web accepted invalid DNS suffix: %s", domain)
		}
	}
	orig, _, err := auth.CanonicalHosts("https://STUDIO.EXAMPLE.COM:443/", "SITES.EXAMPLE.NET")
	if err != nil || orig != "https://studio.example.com" {
		t.Fatalf("canonical HTTPS default port: %s, %v", orig, err)
	}
}

func TestLongActionContinuesWithoutSendingTicketToLogin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "action.sqlite")
	subject := strings.Repeat("u", 250)
	workspace := strings.Repeat("w", 250)
	app, s := handlerIdentityFixture(t, path, subject, workspace, true)
	user := sites.Actor{Subject: subject, Workspace: workspace, Role: "publisher"}
	rev := seedSiteFor(t, s, user)
	args := map[string]any{"site_id": "field", "revision": rev, "visibility": "workspace", "request_id": "private-test"}
	b, _ := json.Marshal(args)
	if _, e := s.Execute(context.Background(), user, "set_site_access", b); e != nil {
		t.Fatal(e)
	}
	wrong, _ := handlerFixture(t, path, "another-team", true)
	wrongLogin := hitRoute(t, wrong, "GET", testControl+"/auth/login", nil, nil)
	wrongCookie := testCookie(t, wrongLogin, "session")
	for _, kind := range []string{"approval", "upload"} {
		var a map[string]any
		var route string
		tkn := ticket{Kind: kind, Who: user, Expires: time.Now().Add(time.Minute).Unix()}
		if kind == "approval" {
			a = map[string]any{"site_id": "field", "revision": rev, "visibility": "public", "request_id": strings.Repeat("r", 128)}
			route = "/approve"
			tkn.Tool = "set_site_access"
		} else {
			a = map[string]any{"site_id": "field", "expected_revision": rev, "path": "b.png", "size": 3, "sha256": strings.Repeat("0", 64), "request_id": "asset"}
			route = "/upload"
		}
		// Legitimate long identity claims can push the signed URL beyond the
		// OIDC cookie return-path budget. The link contains no extraneous parameters.
		raw, _ := json.Marshal(a)
		if kind == "approval" {
			_, err := s.Execute(t.Context(), user, tkn.Tool, raw)
			var ok bool
			tkn.Generation, ok = sites.ApprovalGeneration(err)
			if !ok {
				t.Fatal(err)
			}
		}
		tkn.Args = raw
		token := app.sign(tkn)
		link := testControl + route + "?t=" + url.QueryEscape(token)
		if len(strings.TrimPrefix(link, testControl)) <= auth.MaxReturnPath {
			t.Fatal("fixture does not exceed OIDC return path limit")
		}
		w := hitRoute(t, app, "GET", link, nil, nil)
		if w.Code != 200 || w.Header().Get("Location") != "" || !strings.Contains(w.Body.String(), `href="/auth/login"`) || strings.Contains(w.Body.String(), token) {
			t.Fatalf("%s did not preserve original action in its own tab: %d", kind, w.Code)
		}
		w = hitRoute(t, app, "GET", link, wrongCookie, nil)
		if w.Code != 403 {
			t.Fatalf("%s allowed the wrong workspace: %d", kind, w.Code)
		}
		login := hitRoute(t, app, "GET", testControl+"/auth/login", nil, nil)
		cookie := testCookie(t, login, "session")
		w = hitRoute(t, app, "GET", link, cookie, nil)
		marker := "Review public release"
		if kind == "upload" {
			marker = "Upload file"
		}
		if w.Code != 200 || !strings.Contains(w.Body.String(), marker) {
			t.Fatalf("unable to reopen original %s after sign in: %d", kind, w.Code)
		}
	}
	if w := hitRoute(t, app, "GET", testSite+"/", nil, nil); w.Code != http.StatusSeeOther {
		t.Fatal("reviewing approval unexpectedly published content")
	}
}

func TestLegacyApprovalMustBeRenewed(t *testing.T) {
	a, _ := handlerFixture(t, filepath.Join(t.TempDir(), "legacy.sqlite"), "team-a", true)
	// A correctly signed v0.1.0 ticket has no publication generation.
	legacy := a.sign(ticket{Kind: "approval", Who: sites.Actor{Subject: "tester", Workspace: "team-a", Role: "publisher"}, Tool: "set_site_access", Args: json.RawMessage(`{"site_id":"field","visibility":"public","revision":"old","request_id":"share"}`), Expires: time.Now().Add(time.Minute).Unix()})
	w := hitRoute(t, a, "GET", testControl+"/approve?t="+url.QueryEscape(legacy), nil, nil)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "new link") || w.Header().Get("Location") != "" {
		t.Fatal("obsolete approval accepted or initiated a sign-in", w.Code)
	}
}

func TestPreviewDatabaseFailureIsTemporary(t *testing.T) {
	a, s := handlerFixture(t, filepath.Join(t.TempDir(), "preview.sqlite"), "team-a", true)
	rev := seedPublicSite(t, s)
	preview := a.siteURL("pv-" + rev)
	_, session := browserSessions(t, a, strings.TrimSuffix(preview, "/"))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	w := hitRoute(t, a, "GET", preview, session, nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("preview storage unavailable: got %d", w.Code)
	}
}
