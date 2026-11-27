package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openhoo/shoowcase/internal/sites"
)

// Authenticated requests cross the actual MCP HTTP handler so that dropping a
// key ID on the way to the SDK, store, upload or approval invalidates this test.
func keyHTTP(t *testing.T, app *App, host string, bearers []string, tool string, args any) *httptest.ResponseRecorder {
	t.Helper()
	wire, e := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
	if e != nil {
		t.Fatal(e)
	}
	req := httptest.NewRequest("POST", host+"/mcp", bytes.NewReader(wire))
	req.RemoteAddr = "127.0.0.1:45012"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2025-11-25")
	for _, v := range bearers {
		req.Header.Add("Authorization", "Bearer "+v)
	}
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)
	return w
}

func keyTool(t *testing.T, app *App, token, tool string, args any, expectError bool) map[string]any {
	t.Helper()
	w := keyHTTP(t, app, testControl, []string{token}, tool, args)
	var payload struct {
		Result struct {
			IsError           bool
			StructuredContent map[string]any
		}
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &payload) != nil || payload.Result.StructuredContent == nil || payload.Result.IsError != expectError {
		t.Fatalf("unexpected %s RPC response (status %d)", tool, w.Code)
	}
	return payload.Result.StructuredContent
}

func TestRevokedPersonalKeyCannotRedeemExistingTickets(t *testing.T) {
	a, s := handlerFixture(t, filepath.Join(t.TempDir(), "tickets.db"), "team-a", true)
	ctl, _ := joinMemberConsole(t, a)
	ctx := t.Context()
	owner := sites.Actor{Subject: "tester", Workspace: "team-a", Role: "admin"}
	rev := seedSiteFor(t, s, owner)
	change, _ := json.Marshal(map[string]any{"site_id": "field", "revision": rev, "visibility": "workspace", "request_id": "make-private"})
	if _, e := s.Execute(ctx, owner, "set_site_access", change); e != nil {
		t.Fatal(e)
	}
	key, secret, e := s.CreateKey(ctx, owner, "Studio", "publisher", 3)
	if e != nil {
		t.Fatal(e)
	}
	for _, tt := range []struct {
		host   string
		tokens []string
		expect int
	}{
		{testControl, []string{secret, devBearer}, 401}, {testControl, []string{devBearer, secret}, 401},
		{testControl, []string{key.ID}, 401}, {testSite, []string{secret}, 405}, {"http://unknown.invalid", []string{secret}, 421},
	} {
		w := keyHTTP(t, a, tt.host, tt.tokens, "list_sites", map[string]any{})
		if w.Code != tt.expect {
			t.Fatalf("credential routing got %d want %d", w.Code, tt.expect)
		}
	}
	pending := keyTool(t, a, secret, "set_site_access", map[string]any{"site_id": "field", "revision": rev, "visibility": "public", "request_id": "new-public"}, true)
	review := pending["approval_url"].(string)
	agentApproval := map[string]any{"approval_token": pending["approval_token"]}
	src := []byte("p{color:blue}")
	put := keyTool(t, a, secret, "prepare_upload", uploadSpec{Site: "field", Revision: rev, SHA: fmt.Sprintf("%x", sha256.Sum256(src)), Path: "pending.css", Size: int64(len(src)), Request: "signed-upload"}, false)["upload_url"].(string)
	show := hitRoute(t, a, "GET", review, ctl, nil)
	csrf := memberControl(t, show.Body.String(), "name", "csrf")
	if show.Code != 200 || csrf == "" || hitRoute(t, a, "GET", put, ctl, nil).Code != 200 {
		t.Fatal("valid key action unavailable")
	}
	if e := s.RevokeKey(ctx, owner, key.ID); e != nil {
		t.Fatal(e)
	}
	if w := keyHTTP(t, a, testControl, []string{secret}, "approve_public_change", agentApproval); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked agent could still use its approval: HTTP %d", w.Code)
	}
	for _, u := range []string{review, put} {
		if w := hitRoute(t, a, "GET", u, ctl, nil); w.Code != 403 {
			t.Fatalf("revoked key action GET returned %d", w.Code)
		}
	}
	if w := memberPost(t, a, ctl, strings.TrimPrefix(review, testControl), url.Values{"csrf": {csrf}}); w.Code != 403 {
		t.Fatal("revoked key approved", w.Code)
	}
	req := httptest.NewRequest("PUT", put, bytes.NewReader(src))
	req.RemoteAddr = "127.0.0.1:45301"
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatalf("revoked key raw upload returned %d", w.Code)
	}
	w = keyHTTP(t, a, testControl, []string{secret}, "list_sites", map[string]any{})
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := hitRoute(t, a, "GET", testSite+"/", nil, nil); w.Code != 303 {
		t.Fatal("unexpected anonymous visibility", w.Code)
	}
	state, e := s.Execute(ctx, owner, "get_site", json.RawMessage(`{"site_id":"field"}`))
	if e != nil || state["revision"] != rev || state["visibility"] != "workspace" {
		t.Fatal("rejected ticket changed site", e)
	}

	// A new, correctly scoped key can still finish both workflows. The old
	// tickets stay invalid even though this key has the same human owner.
	_, fresh, e := s.CreateKey(ctx, owner, "Replacement", "publisher", 1)
	if e != nil {
		t.Fatal(e)
	}
	requireApprovalDenied(t, a, fresh, agentApproval["approval_token"], "forbidden")
	url2 := keyTool(t, a, fresh, "prepare_upload", uploadSpec{Site: "field", Revision: rev, SHA: fmt.Sprintf("%x", sha256.Sum256(src)), Path: "pending.css", Size: int64(len(src)), Request: "fresh-upload"}, false)["upload_url"].(string)
	upload := httptest.NewRequest("PUT", url2, bytes.NewReader(src))
	upload.RemoteAddr = "127.0.0.1:45303"
	putOK := httptest.NewRecorder()
	a.ServeHTTP(putOK, upload)
	if putOK.Code != 200 {
		t.Fatal("replacement key upload", putOK.Code)
	}
	review2 := keyTool(t, a, fresh, "set_site_access", map[string]any{"site_id": "field", "revision": rev, "visibility": "public", "request_id": "fresh-public"}, true)["approval_url"].(string)
	if w := memberPost(t, a, ctl, strings.TrimPrefix(review2, testControl), url.Values{"csrf": {csrf}}); w.Code != 200 {
		t.Fatal("replacement key approval", w.Code)
	}
	if w := hitRoute(t, a, "GET", testSite+"/", nil, nil); w.Code != 200 {
		t.Fatal("expected approved release to be public", w.Code)
	}
	if w := hitRoute(t, a, "GET", review, ctl, nil); w.Code != 403 {
		t.Fatal("replacement key reactivated old ticket", w.Code)
	}

}

func TestSuspensionClosesExistingDeliverySessions(t *testing.T) {
	a, s := handlerFixture(t, filepath.Join(t.TempDir(), "audience.db"), "team-a", false)
	ctx := t.Context()
	admin := sites.Actor{Subject: "ops", Workspace: "team-a", Role: "admin"}
	member := sites.Actor{Subject: "tester", Workspace: "team-a", Role: "admin"}
	rev := seedSiteFor(t, s, member)
	preview := strings.TrimSuffix(a.siteURL("pv-"+rev), "/")
	_, content := browserSessions(t, a, testSite)
	_, snapshot := browserSessions(t, a, preview)
	if _, e := s.EnrollAccount(ctx, member, "Tester"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.EnrollAccount(ctx, admin, "Ops"); e != nil {
		t.Fatal(e)
	}
	etag := hitRoute(t, a, "GET", testSite+"/screen.css", content, nil).Header().Get("ETag")
	if etag == "" {
		t.Fatal("not visible before suspension")
	}
	if e := s.SetMember(ctx, admin, member.Subject, "", true, "", false); e != nil {
		t.Fatal(e)
	}
	for _, target := range []struct {
		base   string
		cookie *http.Cookie
	}{{testSite, content}, {preview, snapshot}} {
		for _, method := range []string{"GET", "HEAD"} {
			w := hitRoute(t, a, method, target.base+"/screen.css", target.cookie, map[string]string{"If-None-Match": etag})
			if (w.Code != 403 && w.Code != 404) || w.Header().Get("ETag") != "" || strings.Contains(w.Body.String(), "color:green") {
				t.Fatalf("suspended session %s returned %d with ETag=%t", method, w.Code, w.Header().Get("ETag") != "")
			}
		}
	}
}

func TestConsolePeerAdminOffboarding(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "administrators.db")
	a, _ := handlerIdentityFixture(t, dbPath, "alice", "team-a", false)
	b, _ := handlerIdentityFixture(t, dbPath, "bob", "team-a", false)
	alice, csrf := joinMemberConsole(t, a)
	bob, bCSRF := joinMemberConsole(t, b)
	get := hitRoute(t, a, "GET", testControl+"/admin?member=bob", alice, nil)
	if get.Code != 200 || !strings.Contains(get.Body.String(), "Suspend member") || !strings.Contains(get.Body.String(), "Save role limit") {
		t.Fatal("unexpected peer admin controls", get.Code)
	}
	values := url.Values{"csrf": {csrf}, "action": {"suspend"}, "subject": {"bob"}, "confirm": {"yes"}, "expected_ceiling": {""}, "expected_disabled": {"false"}}
	if w := memberPost(t, a, alice, "/admin", values); w.Code != 303 {
		t.Fatal("suspend peer admin", w.Code)
	}
	values.Set("action", "restore")
	values.Set("expected_disabled", "true")
	values.Set("csrf", bCSRF)
	if w := memberPost(t, b, bob, "/admin", values); w.Code != 403 {
		t.Fatal("suspended peer reactivated own account", w.Code)
	}
	values.Set("csrf", csrf)
	values.Set("action", "role")
	values.Set("ceiling", "reader")
	if w := memberPost(t, a, alice, "/admin", values); w.Code != 303 {
		t.Fatal("could not limit the suspended peer admin", w.Code)
	}
	values.Set("expected_ceiling", "reader")
	values.Set("action", "restore")
	if w := memberPost(t, a, alice, "/admin", values); w.Code != 303 {
		t.Fatal("restoring peer admin", w.Code)
	}
	if w := hitRoute(t, b, "GET", testControl+"/admin", bob, nil); w.Code != 403 {
		t.Fatal("restoring the member bypassed the role limit", w.Code)
	}
	values.Set("action", "role")
	values.Set("ceiling", "")
	values.Set("expected_disabled", "false")
	if w := memberPost(t, a, alice, "/admin", values); w.Code != 303 {
		t.Fatal("removing the peer admin's role limit", w.Code)
	}
	if w := hitRoute(t, b, "GET", testControl+"/admin", bob, nil); w.Code != 200 {
		t.Fatal("restored admin cannot sign in", w.Code)
	}
}
