package web

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openhoo/shoowcase/internal/sites"
)

func TestTenantAuditExportAuthenticationAndScope(t *testing.T) {
	app, s := handlerFixture(t, filepath.Join(t.TempDir(), "audit.db"), "team-a", false)
	cookie, csrf := joinMemberConsole(t, app)
	who := sites.Actor{Workspace: "team-a", Subject: "tester", Role: "admin"}
	if _, err := s.EnrollAccount(t.Context(), sites.Actor{Workspace: "team-b", Subject: "other-private-person", Role: "admin"}, "Other"); err != nil {
		t.Fatal(err)
	}
	_, secret, err := s.CreateKey(t.Context(), who, "private-name", "publisher", 1)
	if err != nil {
		t.Fatal(err)
	}
	// A valid browser cookie must never override a rejected Authorization header.
	requireMemberStatus(t, hitRoute(t, app, "GET", testControl+"/api/audit", cookie, map[string]string{"Authorization": "Bearer " + secret}), 401)
	requireMemberStatus(t, hitRoute(t, app, "GET", testControl+"/api/audit", nil, nil), 401)
	requireMemberStatus(t, hitRoute(t, app, "GET", testControl+"/api/audit?workspace=team-b", cookie, nil), 400)
	requireMemberStatus(t, hitRoute(t, app, "GET", testControl+"/api/audit?after=1&after=0", cookie, nil), 400)
	requireMemberStatus(t, hitRoute(t, app, "POST", testControl+"/api/audit", cookie, nil), 405)
	for _, mode := range []string{"browser", "collector"} {
		c := cookie
		var h map[string]string
		if mode == "collector" {
			c = nil
			h = map[string]string{"Authorization": "Bearer " + devBearer}
		}
		w := hitRoute(t, app, "GET", testControl+"/api/audit", c, h)
		requireMemberStatus(t, w, 200)
		var report struct {
			Events []sites.AuditEvent
			Next   int64 `json:"next_after"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil || len(report.Events) == 0 || report.Next != report.Events[len(report.Events)-1].Sequence || strings.Contains(w.Body.String(), "other-private-person") {
			t.Fatal("invalid or unscoped export", err)
		}
	}
	// Group management uses the existing CSRF gate and never trusts an ID in a form.
	params := url.Values{"action": {"group-create"}, "name": {"Writers"}, "role": {"editor"}}
	requireMemberStatus(t, memberPost(t, app, cookie, "/admin", params), 403)
	params.Set("csrf", csrf)
	requireMemberStatus(t, memberPost(t, app, cookie, "/admin", params), 303)
	foreign := sites.Actor{Workspace: "team-b", Subject: "other-private-person", Role: "admin"}
	g, err := s.CreateGroup(t.Context(), foreign, "Foreign group", "editor")
	if err != nil {
		t.Fatal(err)
	}
	requireMemberStatus(t, hitRoute(t, app, "GET", testControl+"/admin?view=groups&group="+g.ID, cookie, nil), 404)
	requireMemberStatus(t, memberPost(t, app, cookie, "/admin", url.Values{"action": {"group-delete"}, "group": {g.ID}, "version": {"1"}, "confirm": {"yes"}, "csrf": {csrf}}), 404)
}
