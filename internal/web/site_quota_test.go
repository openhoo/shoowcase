package web

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openhoo/shoowcase/internal/auth"
	"github.com/openhoo/shoowcase/internal/sites"
)

// A signed upload can outlive a quota change. Check the actual MCP tool and
// ticket redemption on the HTTP boundary, including retry after capacity returns.
func TestSignedUploadUsesCurrentGroupBudget(t *testing.T) {
	app, store := handlerFixture(t, filepath.Join(t.TempDir(), "budget.sqlite"), "team-a", false)
	joinMemberConsole(t, app)
	ctx := t.Context()
	admin := sites.Actor{Subject: "tester", Workspace: "team-a", Role: "admin"}
	writer := sites.Actor{Subject: "writer", Workspace: "team-a", Role: "editor"}
	if _, err := store.EnrollAccount(ctx, writer, "Writer"); err != nil {
		t.Fatal(err)
	}
	group, err := store.CreateGroup(ctx, admin, "Atlas", "editor")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetGroupMember(ctx, admin, group.ID, writer.Subject, true, group.Version); err != nil {
		t.Fatal(err)
	}
	_, token, err := store.CreateKey(ctx, writer, "Atlas agent", "editor", 1)
	if err != nil {
		t.Fatal(err)
	}
	choices := keyTool(t, app, token, "list_site_groups", map[string]any{}, false)
	groups := choices["groups"].([]any)
	if len(groups) != 1 || choices["allow_unassigned"] != true {
		t.Fatalf("unexpected groups: %v", choices)
	}
	choice := groups[0].(map[string]any)
	site := keyTool(t, app, token, "create_site", map[string]any{"name": "Atlas", "group_id": choice["id"], "request_id": "create"}, false)
	if site["group_id"] != group.ID {
		t.Fatal("site was not assigned to the selected group")
	}
	file := []byte("<p>Atlas</p>")
	spec := uploadSpec{Site: site["site_id"].(string), Revision: site["revision"].(string), SHA: fmt.Sprintf("%x", sha256.Sum256(file)), Path: "index.html", Size: int64(len(file)), Request: "file"}
	ticket := keyTool(t, app, token, "prepare_upload", spec, false)["upload_url"].(string)
	put := func() int {
		req := httptest.NewRequest(http.MethodPut, ticket, bytes.NewReader(file))
		req.RemoteAddr = "127.0.0.1:45301"
		w := httptest.NewRecorder()
		app.ServeHTTP(w, req)
		return w.Code
	}
	limits := sites.QuotaLimits{Sites: 1, Bytes: 0}
	if err := store.SetQuota(ctx, admin, group.ID, limits, group.Version+1); err != nil {
		t.Fatal(err)
	}
	if status := put(); status != http.StatusTooManyRequests {
		t.Fatalf("capped upload returned %d", status)
	}
	current := keyTool(t, app, token, "get_site", map[string]string{"site_id": spec.Site}, false)
	if current["revision"] != spec.Revision {
		t.Fatal("failed upload changed the draft")
	}
	limits.Bytes = int64(len(file))
	if err := store.SetQuota(ctx, admin, group.ID, limits, group.Version+2); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if status := put(); status != http.StatusOK {
			t.Fatalf("exact retry at the limit returned %d", status)
		}
	}
	usage, err := store.Quotas(ctx, admin)
	if err != nil || len(usage.Groups) != 1 || usage.Groups[0].Usage.Bytes != limits.Bytes || usage.Workspace.Usage.Bytes != limits.Bytes {
		t.Fatal("signed upload was not charged exactly once", usage, err)
	}
	// A workspace editor removed from the owning group can still inspect the
	// site, but neither the browser nor MCP should offer an unusable upload.
	if err := store.SetGroupMember(ctx, admin, group.ID, writer.Subject, false, usage.Groups[0].Version); err != nil {
		t.Fatal(err)
	}
	current = keyTool(t, app, token, "get_site", map[string]string{"site_id": spec.Site}, false)
	reason, _ := current["storage_restriction"].(string)
	if reason == "" {
		t.Fatal("missing reason the draft is unavailable for editing")
	}
	spec.Revision = current["revision"].(string)
	if failure := keyTool(t, app, token, "prepare_upload", spec, true); failure["error"].(map[string]any)["code"] != "forbidden" {
		t.Fatal("expected a group membership denial")
	}
	for path, status := range map[string]int{"": http.StatusOK, "/edit": http.StatusForbidden} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, testControl+"/sites/"+spec.Site+path, nil)
		signedInPage(app, w, r, auth.Identity{Subject: writer.Subject, Workspace: writer.Workspace, Role: auth.Editor})
		if w.Code != status || !strings.Contains(w.Body.String(), reason) || strings.Contains(w.Body.String(), ">New file</a>") {
			t.Fatalf("site page %q: unexpected editing controls or missing explanation (status %d)", path, w.Code)
		}
	}
}
