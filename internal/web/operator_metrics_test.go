package web

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openhoo/shoowcase/internal/sites"
)

func TestInstanceMetricsRequireOperatorWorkspace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.sqlite")
	operator, db := handlerFixture(t, path, "operator", false)
	customer, _ := handlerFixture(t, path, "customer", false)
	authz := map[string]string{"Authorization": "Bearer " + devBearer}
	check := func(app *App, want int) {
		t.Helper()
		w := hitRoute(t, app, "GET", testControl+"/metrics?workspace=operator", nil, authz)
		if w.Code != want || strings.Contains(w.Body.String(), "shoowcase_requests_total") != (want == http.StatusOK) {
			t.Fatalf("status %d, want %d; unexpected access to process counters", w.Code, want)
		}
	}
	operator.config.MetricsWorkspace = ""
	check(operator, http.StatusNotFound)
	operator.config.MetricsWorkspace = "operator"
	customer.config.MetricsWorkspace = "operator"
	check(operator, http.StatusOK)
	check(customer, http.StatusForbidden)
	target := sites.Actor{Workspace: "operator", Subject: "tester", Role: "admin"}
	peer := sites.Actor{Workspace: "operator", Subject: "other-admin", Role: "admin"}
	for _, actor := range []sites.Actor{target, peer} {
		if _, e := db.EnrollAccount(t.Context(), actor, actor.Subject); e != nil {
			t.Fatal(e)
		}
	}
	if e := db.SetMember(t.Context(), peer, target.Subject, "", true, "", false); e != nil {
		t.Fatal(e)
	}
	check(operator, http.StatusForbidden)
}

func TestWorkspaceDisplayNameOnPublishingDesk(t *testing.T) {
	app, db := handlerFixture(t, filepath.Join(t.TempDir(), "studio.sqlite"), "engineering", false)
	admin := sites.Actor{Workspace: "engineering", Subject: "tester", Role: "admin"}
	if _, e := db.EnrollAccount(t.Context(), admin, "Operator"); e != nil {
		t.Fatal(e)
	}
	if e := db.SetWorkspaceSettings(t.Context(), admin, "Field & Service", "identity", 1); e != nil {
		t.Fatal(e)
	}
	ctl, _ := browserSessions(t, app, testSite)
	w := hitRoute(t, app, "GET", testControl+"/sites", ctl, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Field &amp; Service</p><h1>Sites</h1>") {
		t.Fatal("the tenant display name is missing from the sites heading")
	}
	w = hitRoute(t, app, "GET", testControl+"/admin?view=settings", ctl, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Permanent workspace ID: <code>engineering</code>") {
		t.Fatal("display name replaced the permanent identity in settings")
	}
}
