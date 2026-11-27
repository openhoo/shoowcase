package web

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openhoo/shoowcase/internal/auth"
	"github.com/openhoo/shoowcase/internal/sites"
)

func TestManagedConsoleEnrollmentGuidance(t *testing.T) {
	app, db := handlerFixture(t, filepath.Join(t.TempDir(), "workspace.sqlite"), "engineering", false)
	admin := sites.Actor{Workspace: "engineering", Subject: "admin", Role: "admin"}
	if _, err := db.EnrollAccount(t.Context(), admin, "Workspace admin"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetWorkspaceSettings(t.Context(), admin, "Field team", "managed", 1); err != nil {
		t.Fatal(err)
	}
	viewer := auth.Identity{Workspace: admin.Workspace, Subject: "idp|guest&field", Role: auth.Viewer, IssuedAt: 100, CSRF: "form-token"}
	page := func(i auth.Identity, method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequestWithContext(t.Context(), method, testControl+path, nil)
		signedInPage(app, w, r, i)
		return w
	}
	for _, path := range []string{"/", "/sites", "/sites/sample/edit", "/keys", "/account", "/admin"} {
		w := page(viewer, "GET", path)
		if w.Code != 403 || !strings.Contains(w.Body.String(), "<h1>Workspace access required</h1>") ||
			!strings.Contains(w.Body.String(), "<code>idp|guest&amp;field</code>") ||
			!strings.Contains(w.Body.String(), "<strong>Field team</strong>") || strings.Contains(w.Body.String(), "Create your account") {
			t.Fatalf("%s: missing managed enrollment instructions (status %d)", path, w.Code)
		}
	}
	if w := page(viewer, "POST", "/account"); w.Code != 403 || !strings.Contains(w.Body.String(), "Workspace access required") {
		t.Fatalf("unenrolled POST: %d", w.Code)
	}
	freshAdmin := viewer
	freshAdmin.Subject = "backup"
	freshAdmin.Role = auth.Admin
	if w := page(freshAdmin, "GET", "/account"); w.Code != 200 || !strings.Contains(w.Body.String(), "Create your account") {
		t.Fatal("IdP admin cannot self-enroll")
	}
	other := viewer
	other.Workspace = "other"
	if w := page(other, "GET", "/account"); w.Code != 200 || !strings.Contains(w.Body.String(), "Create your account") || strings.Contains(w.Body.String(), "Field team") {
		t.Fatal("unrelated identity-mode workspace did not offer normal enrollment")
	}
	if err := db.ProvisionMember(t.Context(), admin, viewer.Subject, "Guest"); err != nil {
		t.Fatal(err)
	}
	if w := page(viewer, "GET", "/sites"); w.Code != 200 || !strings.Contains(w.Body.String(), "<h1>Sites</h1>") {
		t.Fatal("provisioning did not unlock the original page")
	}
	if err := db.SetMember(t.Context(), admin, viewer.Subject, "", true, "", false); err != nil {
		t.Fatal(err)
	}
	if w := page(viewer, "GET", "/account"); w.Code != 403 || !strings.Contains(w.Body.String(), "<h1>Account suspended</h1>") {
		t.Fatal("suspension rendered as missing enrollment")
	}
	db.Close()
	if w := page(viewer, "GET", "/account"); w.Code != 503 || strings.Contains(w.Body.String(), "<h1>Workspace access required</h1>") {
		t.Fatal("storage failure rendered as missing enrollment")
	}
}
