package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openhoo/shoowcase/internal/auth"
	"github.com/openhoo/shoowcase/internal/sites"
	"golang.org/x/net/html"
)

// Read actual form controls, including html/template's escaping. A digest or
// a secret-looking string elsewhere in the page is not a usable credential.
func memberControl(t *testing.T, page, attr, name string) string {
	t.Helper()
	z := html.NewTokenizer(strings.NewReader(page))
	for {
		tok := z.Next()
		if tok == html.ErrorToken {
			return ""
		}
		if tok != html.StartTagToken && tok != html.SelfClosingTagToken {
			continue
		}
		n := z.Token()
		if n.Data != "input" {
			continue
		}
		matched := false
		var value string
		for _, v := range n.Attr {
			if v.Key == attr && v.Val == name {
				matched = true
			}
			if v.Key == "value" {
				value = v.Val
			}
		}
		if matched {
			return value
		}
	}
}
func memberPost(t *testing.T, app *App, c *http.Cookie, path string, data url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, testControl+path, strings.NewReader(data.Encode()))
	req.RemoteAddr = "127.0.0.1:42355"
	req.AddCookie(c)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", testControl)
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)
	return w
}
func requireMemberStatus(t *testing.T, w *httptest.ResponseRecorder, code int) {
	t.Helper()
	if w.Code != code {
		t.Fatalf("expected HTTP %d, got %d", code, w.Code)
	}
}
func joinMemberConsole(t *testing.T, app *App) (*http.Cookie, string) {
	t.Helper()
	cookie, _ := browserSessions(t, app, testSite)
	get := hitRoute(t, app, "GET", testControl+"/account", cookie, nil)
	requireMemberStatus(t, get, 200)
	csrf := memberControl(t, get.Body.String(), "name", "csrf")
	if csrf == "" {
		t.Fatal("missing account form token")
	}
	w := memberPost(t, app, cookie, "/account", url.Values{"action": {"enroll"}, "display_name": {"Ada <Design>"}, "csrf": {csrf}})
	requireMemberStatus(t, w, 303)
	if w.Header().Get("Location") != "/sites" {
		t.Fatalf("unexpected enrollment redirect: %s", w.Header().Get("Location"))
	}
	return cookie, csrf
}

func TestMemberConsoleAccountAndKeyLifecycle(t *testing.T) {
	app, s := handlerFixture(t, filepath.Join(t.TempDir(), "account.sqlite"), "team-a", false)
	c, csrf := joinMemberConsole(t, app)
	account := hitRoute(t, app, "GET", testControl+"/account", c, nil)
	requireMemberStatus(t, account, 200)
	if !strings.Contains(account.Body.String(), "Ada &lt;Design&gt;") {
		t.Fatal("account name was not rendered as text")
	}
	data := url.Values{"action": {"create"}, "name": {"<Design> agent"}, "scope": {"editor"}, "days": {"7"}}
	requireMemberStatus(t, memberPost(t, app, c, "/keys", data), 403)
	data.Set("csrf", csrf)
	form := hitRoute(t, app, "GET", testControl+"/keys", c, nil)
	requireMemberStatus(t, form, 200)
	data.Set("creation_token", memberControl(t, form.Body.String(), "name", "creation_token"))
	out := memberPost(t, app, c, "/keys", data)
	requireMemberStatus(t, out, 200)
	secret := memberControl(t, out.Body.String(), "id", "agent-secret")
	if secret == "" || sites.KeyID(secret) == "" || !strings.Contains(out.Header().Get("Cache-Control"), "no-store") || out.Header().Get("Location") != "" {
		t.Fatal("secret receipt is missing, cacheable or reusable via URL")
	}
	if !strings.Contains(out.Body.String(), "&lt;Design&gt; agent") {
		t.Fatal("key name not escaped")
	}
	repeated := memberPost(t, app, c, "/keys", data)
	requireMemberStatus(t, repeated, 409)
	if strings.Contains(repeated.Body.String(), secret) {
		t.Fatal("duplicate form disclosed the secret")
	}
	created, _, err := s.ListKeys(t.Context(), sites.Actor{Subject: "tester", Workspace: "team-a", Role: "admin"}, false, "")
	if err != nil || len(created) != 1 {
		t.Fatal("duplicate form minted another key")
	}
	who, e := s.ResolveKey(t.Context(), secret)
	if e != nil || who.Role != "editor" || who.Subject != "tester" || who.Workspace != "team-a" {
		t.Fatal("created credential did not resolve to the expected scoped owner")
	}
	for _, p := range []string{"/keys", "/account", "/admin?view=keys", "/admin?view=activity"} {
		get := hitRoute(t, app, "GET", testControl+p, c, nil)
		requireMemberStatus(t, get, 200)
		if strings.Contains(get.Body.String(), secret) {
			t.Fatalf("secret disclosed on %s", p)
		}
	}
	revoke := url.Values{"csrf": {csrf}, "action": {"revoke"}, "key_id": {sites.KeyID(secret)}}
	requireMemberStatus(t, memberPost(t, app, c, "/keys", revoke), 400)
	if _, e = s.ResolveKey(t.Context(), secret); e != nil {
		t.Fatal("unconfirmed request revoked a key")
	}
	revoke.Set("confirm", "yes")
	requireMemberStatus(t, memberPost(t, app, c, "/keys", revoke), 303)
	if _, e = s.ResolveKey(t.Context(), secret); e == nil {
		t.Fatal("revoked credential remains active")
	}
	get := hitRoute(t, app, "GET", testControl+"/admin?view=activity", c, nil)
	requireMemberStatus(t, get, 200)
	if !strings.Contains(get.Body.String(), "Agent key revoked") {
		t.Fatal("revocation missing from workspace activity")
	}
}
func TestMemberConsoleAdminActionsAndConflicts(t *testing.T) {
	app, s := handlerFixture(t, filepath.Join(t.TempDir(), "admin.sqlite"), "team-a", false)
	c, csrf := joinMemberConsole(t, app)
	bob := sites.Actor{Subject: "bob", Workspace: "team-a", Role: "editor"}
	if _, e := s.EnrollAccount(t.Context(), bob, "Bob Smith"); e != nil {
		t.Fatal(e)
	}
	_, secret, e := s.CreateKey(t.Context(), bob, "Bob’s agent", "editor", 7)
	if e != nil {
		t.Fatal(e)
	}
	other := sites.Actor{Subject: "other", Workspace: "team-b", Role: "publisher"}
	if _, e = s.EnrollAccount(t.Context(), other, "Other Team"); e != nil {
		t.Fatal(e)
	}
	_, outsider, e := s.CreateKey(t.Context(), other, "Other team agent", "reader", 30)
	if e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{"/admin", "/admin?view=keys", "/admin?view=activity"} {
		get := hitRoute(t, app, "GET", testControl+p, c, nil)
		requireMemberStatus(t, get, 200)
		if strings.Contains(get.Body.String(), "Other Team") || strings.Contains(get.Body.String(), "Other team agent") || strings.Contains(get.Body.String(), sites.KeyID(outsider)) {
			t.Fatalf("another workspace disclosed on %s", p)
		}
	}
	get := hitRoute(t, app, "GET", testControl+"/admin", c, nil)
	if !strings.Contains(get.Body.String(), "Manage Bob Smith") || strings.Contains(get.Body.String(), "Manage Ada") || strings.Contains(get.Body.String(), `<option value="publisher"`) {
		t.Fatal("member controls do not match role boundaries")
	}
	role := url.Values{"csrf": {csrf}, "action": {"role"}, "subject": {"bob"}, "ceiling": {"reader"}, "expected_ceiling": {""}, "expected_disabled": {"false"}}
	requireMemberStatus(t, memberPost(t, app, c, "/admin", role), 303)
	if actor, e := s.ResolveKey(t.Context(), secret); e != nil || actor.Role != "reader" {
		t.Fatal("role limit not applied to key")
	}
	// Admin B's original form still describes the pre-edit state.
	stale := url.Values{"csrf": {csrf}, "action": {"suspend"}, "subject": {"bob"}, "confirm": {"yes"}, "expected_ceiling": {""}, "expected_disabled": {"false"}}
	requireMemberStatus(t, memberPost(t, app, c, "/admin", stale), 409)
	if _, e = s.ResolveKey(t.Context(), secret); e != nil {
		t.Fatal("stale form revoked an agent key")
	}
	stale.Del("confirm")
	stale.Set("expected_ceiling", "reader")
	requireMemberStatus(t, memberPost(t, app, c, "/admin", stale), 400)
	stale.Set("confirm", "yes")
	requireMemberStatus(t, memberPost(t, app, c, "/admin", stale), 303)
	if _, e = s.ResolveKey(t.Context(), secret); e == nil {
		t.Fatal("suspension did not revoke keys")
	}
	stale.Set("expected_disabled", "true")
	stale.Set("action", "restore")
	requireMemberStatus(t, memberPost(t, app, c, "/admin", stale), 303)
	if _, e = s.ResolveKey(t.Context(), secret); e == nil {
		t.Fatal("restoration reactivated revoked key")
	}
	foreign := url.Values{"csrf": {csrf}, "action": {"revoke"}, "confirm": {"yes"}, "key_id": {sites.KeyID(outsider)}}
	requireMemberStatus(t, memberPost(t, app, c, "/admin", foreign), 404)
	if _, e = s.ResolveKey(t.Context(), outsider); e != nil {
		t.Fatal("another team's key was revoked")
	}
}

// Use a verified viewer identity context as delivered by the console router.
// The development IdP intentionally grants an admin role to every login.
func TestMemberConsoleRoleControls(t *testing.T) {
	app, s := handlerFixture(t, filepath.Join(t.TempDir(), "roles.sqlite"), "team-a", false)
	identity := auth.Identity{Subject: "a-viewer", Workspace: "team-a", Role: auth.Viewer, CSRF: "test-csrf"}
	who := actor(identity)
	if _, e := s.EnrollAccount(t.Context(), who, "Viewer"); e != nil {
		t.Fatal(e)
	}
	c := consoleContext{Actor: who, Identity: identity, Enrolled: true, Name: "Viewer"}
	run := func(path, method string, values url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, testControl+path, strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if e := req.ParseForm(); e != nil {
			t.Fatal(e)
		}
		w := httptest.NewRecorder()
		app.membersConsole(w, req, c)
		return w
	}
	requireMemberStatus(t, run("/admin", "GET", nil), 403)
	keys := run("/keys", "GET", nil)
	requireMemberStatus(t, keys, 200)
	if strings.Contains(keys.Body.String(), `value="publisher"`) || strings.Contains(keys.Body.String(), `value="editor"`) {
		t.Fatal("viewer offered a privileged scope")
	}
	requireMemberStatus(t, run("/keys", "POST", url.Values{"creation_token": {memberControl(t, keys.Body.String(), "name", "creation_token")}, "action": {"create"}, "scope": {"publisher"}, "days": {"7"}, "name": {"reader key"}}), 403)
	// The caller cannot create a credential for a different workspace or owner.
	keys = run("/keys", "GET", nil)
	issued := run("/keys", "POST", url.Values{"creation_token": {memberControl(t, keys.Body.String(), "name", "creation_token")}, "action": {"create"}, "scope": {"reader"}, "days": {"30"}, "name": {"reader key"}, "workspace": {"team-b"}, "subject": {"other"}})
	requireMemberStatus(t, issued, 200)
	receipt := memberControl(t, issued.Body.String(), "id", "agent-secret")
	issuedTo, e := s.ResolveKey(t.Context(), receipt)
	if e != nil || issuedTo != who {
		t.Fatal("posted owner or workspace changed credentials")
	}
}
