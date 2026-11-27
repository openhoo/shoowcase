package web

import (
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openhoo/shoowcase/internal/sites"
)

func requireApprovalDenied(t *testing.T, app *App, credential string, token any, code string) {
	t.Helper()
	result := keyTool(t, app, credential, "approve_public_change", map[string]any{"approval_token": token}, true)
	failure, _ := result["error"].(map[string]any)
	if failure["code"] != code {
		t.Fatalf("expected %s from approve_public_change; got %v", code, failure["code"])
	}
}

func TestMCPPublicApprovalChecksCurrentPublisherAndCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "approvals.db")
	app, store := handlerFixture(t, path, "team-a", true)
	admin := sites.Actor{Workspace: "team-a", Subject: "tester", Role: "admin"}
	writer := sites.Actor{Workspace: "team-a", Subject: "writer", Role: "publisher"}
	ctx := t.Context()
	for _, who := range []sites.Actor{admin, writer} {
		if _, err := store.EnrollAccount(ctx, who, who.Subject); err != nil {
			t.Fatal(err)
		}
	}
	newKey := func(who sites.Actor, name string) string {
		t.Helper()
		_, secret, err := store.CreateKey(ctx, who, name, "publisher", 1)
		if err != nil {
			t.Fatal(err)
		}
		return secret
	}
	first, second := newKey(writer, "first agent"), newKey(writer, "second agent")
	adminKey := newKey(admin, "admin agent")
	rev := seedSiteFor(t, store, writer)
	keyTool(t, app, first, "set_site_access", map[string]string{"site_id": "field", "revision": rev, "visibility": "workspace", "request_id": "restrict"}, false)
	request := func(credential string, requestID string) map[string]any {
		t.Helper()
		result := keyTool(t, app, credential, "set_site_access", map[string]string{"site_id": "field", "revision": rev, "visibility": "public", "request_id": requestID}, true)
		failure, _ := result["error"].(map[string]any)
		if failure["code"] != "approval_required" {
			t.Fatalf("expected pending approval; got %v", failure["code"])
		}
		return result
	}
	keyPending := request(first, "key-share")
	keyToken, ok := keyPending["approval_token"].(string)
	if !ok || keyToken == "" {
		t.Fatal("missing MCP approval token")
	}
	// The same key owner has two publisher keys. Neither the other key nor
	// the owner's direct sign-in may redeem the first key's request.
	adminKeyToken := request(adminKey, "admin-agent-share")["approval_token"]
	requireApprovalDenied(t, app, second, keyToken, "forbidden")
	requireApprovalDenied(t, app, devBearer, adminKeyToken, "forbidden")
	events, _, err := store.AuditPage(ctx, admin, 0, false, 10)
	if err != nil {
		t.Fatal(err)
	}
	var foundDenial bool
	for _, event := range events {
		if event.Action == "access.denied" && event.Target == "approve_public_change" && event.CredentialID == sites.KeyID(second) && event.Subject == writer.Subject {
			foundDenial = true
		}
	}
	if !foundDenial {
		t.Fatal("denied agent approval missing from workspace audit")
	}

	// With direct sign-in, the subject AND workspace must still match. A key
	// belonging to that same signed-in account is a different credential.
	directPending := request(devBearer, "direct-share")
	directToken := directPending["approval_token"]
	otherSubject, _ := handlerIdentityFixture(t, path, "peer", admin.Workspace, true)
	otherWorkspace, _ := handlerIdentityFixture(t, path, admin.Subject, "team-b", true)
	requireApprovalDenied(t, otherSubject, devBearer, directToken, "forbidden")
	requireApprovalDenied(t, otherWorkspace, devBearer, directToken, "forbidden")
	requireApprovalDenied(t, app, adminKey, directToken, "forbidden")

	disabled, _ := handlerFixture(t, path, admin.Workspace, false)
	requireApprovalDenied(t, disabled, first, keyToken, "forbidden")
	requireApprovalDenied(t, app, first, "invalid."+keyToken, "invalid_argument")
	browserURL, err := url.Parse(keyPending["approval_url"].(string))
	if err != nil {
		t.Fatal("invalid browser approval URL")
	}
	requireApprovalDenied(t, app, first, browserURL.Query().Get("t"), "invalid_argument")
	if w := hitRoute(t, app, "GET", testControl+"/approve?t="+url.QueryEscape(keyToken), nil, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("browser accepted an MCP token: HTTP %d", w.Code)
	}
	issued, ok := app.ticketValue(keyToken, "mcp-approval")
	if !ok {
		t.Fatal("expected a signed MCP approval")
	}
	issued.Expires = time.Now().Add(-time.Minute).Unix()
	requireApprovalDenied(t, app, first, app.sign(issued), "invalid_argument")

	if err := store.SetMember(ctx, admin, writer.Subject, "editor", false, "", false); err != nil {
		t.Fatal(err)
	}
	requireApprovalDenied(t, app, first, keyToken, "forbidden")
	if got := hitRoute(t, app, "GET", testSite+"/", nil, nil); got.Code != http.StatusSeeOther {
		t.Fatal("rejected approval changed anonymous access")
	}
	applied := keyTool(t, app, devBearer, "approve_public_change", map[string]any{"approval_token": directToken}, false)
	if applied["visibility"] != "public" || applied["live_revision"] != rev {
		t.Fatal("matching publisher did not share the requested revision")
	}
	if got := hitRoute(t, app, "GET", testSite+"/", nil, nil); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "Release body") {
		t.Fatal("agent-approved release unavailable anonymously")
	}
}
