package web

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openhoo/shoowcase/internal/sites"
)

func TestQuotaConsoleLimitsAndSiteAssignment(t *testing.T) {
	app, store := handlerFixture(t, filepath.Join(t.TempDir(), "quotas.db"), "team-a", false)
	cookie, csrf := joinMemberConsole(t, app)
	admin := sites.Actor{Subject: "tester", Workspace: "team-a", Role: "admin"}
	seedSiteFor(t, store, admin)
	g, err := store.CreateGroup(t.Context(), admin, "Design <Web>", "editor")
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) string {
		t.Helper()
		w := hitRoute(t, app, "GET", testControl+path, cookie, nil)
		requireMemberStatus(t, w, 200)
		return w.Body.String()
	}
	quotas := "/admin?view=quotas"
	page := get(quotas + "&site=field")
	for _, label := range []string{"Quotas", "Workspace quotas", "Design &lt;Web&gt;", "1 site · ", "Group for Field Notes", "Workspace limit"} {
		if !strings.Contains(page, label) {
			t.Fatalf("missing quota information %q", label)
		}
	}
	if memberControl(t, page, "id", "storage-limit-workspace") != "2" {
		t.Fatal("default 2 GiB limit missing from form")
	}
	post := func(f url.Values, code int) string {
		t.Helper()
		w := memberPost(t, app, cookie, "/admin", f)
		requireMemberStatus(t, w, code)
		return w.Body.String()
	}
	workspace := url.Values{"action": {"workspace-quota"}, "version": {"1"}, "site_limit": {"2"}, "storage_limit": {"513"}, "storage_unit": {"KiB"}}
	post(workspace, 403)
	workspace.Set("csrf", csrf)
	post(workspace, 303)
	post(workspace, 409)
	page = get(quotas)
	if memberControl(t, page, "id", "storage-limit-workspace") != "513" || !strings.Contains(page, "of 513 KiB") {
		t.Fatal("workspace limit or usage not shown after redirect")
	}
	group := url.Values{"csrf": {csrf}, "action": {"group-quota"}, "version": {"1"}, "group": {g.ID}, "site_limit": {""}, "storage_limit": {"1"}, "storage_unit": {"B"}}
	post(group, 303)
	assignment := url.Values{"csrf": {csrf}, "action": {"site-group"}, "version": {"1"}, "site": {"field"}, "group": {g.ID}}
	if msg := post(assignment, 409); !strings.Contains(msg, "storage limit") || !strings.Contains(msg, "Back to quotas") {
		t.Fatal("failed transfer needs a storage-specific reason and a way back")
	}
	group.Set("version", "2")
	group.Set("storage_limit", "")
	delete(group, "site_limit")
	post(group, 400) // Missing is not an instruction to clear a ceiling.
	group.Set("site_limit", "1")
	post(group, 303)
	post(assignment, 303)
	assignment.Set("group", "")
	post(assignment, 409)
	page = get(quotas + "&site=field")
	if !strings.Contains(page, "0 sites · 0 B") || !strings.Contains(page, "At limit") || !strings.Contains(page, `value="`+g.ID+`" selected`) {
		t.Fatal("usage, full group or saved assignment not visible")
	}
	if msg := post(url.Values{"csrf": {csrf}, "action": {"group-delete"}, "group": {g.ID}, "version": {"3"}, "confirm": {"yes"}}, 409); !strings.Contains(msg, "Reassign") || !strings.Contains(msg, "Back to quotas") {
		t.Fatal("deletion with assigned sites should explain how to move them")
	}
	if !strings.Contains(get("/admin?view=activity"), "Site group changed") {
		t.Fatal("missing audit label for assignment")
	}
	// A group form with no target may never modify workspace limits.
	group.Del("group")
	post(group, 400)
	workspace.Set("version", "2")
	for _, v := range []string{"9223372036854775807", "-1", "0.5"} {
		workspace.Set("storage_limit", v)
		post(workspace, 400)
	}
}

func TestQuotaConsoleScopesGroupsAndSitesToWorkspace(t *testing.T) {
	app, store := handlerFixture(t, filepath.Join(t.TempDir(), "tenants.db"), "team-a", false)
	c, csrf := joinMemberConsole(t, app)
	other := sites.Actor{Subject: "other", Workspace: "team-b", Role: "admin"}
	if _, err := store.EnrollAccount(t.Context(), other, "Other"); err != nil {
		t.Fatal(err)
	}
	seedSiteFor(t, store, other)
	g, err := store.CreateGroup(t.Context(), other, "Foreign Project", "editor")
	if err != nil {
		t.Fatal(err)
	}
	page := hitRoute(t, app, "GET", testControl+"/admin?view=quotas", c, nil)
	requireMemberStatus(t, page, 200)
	if strings.Contains(page.Body.String(), "Foreign Project") || strings.Contains(page.Body.String(), "Field Notes") {
		t.Fatal("quota overview disclosed another workspace")
	}
	requireMemberStatus(t, hitRoute(t, app, "GET", testControl+"/admin?view=quotas&site=field", c, nil), 404)
	requireMemberStatus(t, memberPost(t, app, c, "/admin", url.Values{"csrf": {csrf}, "action": {"group-quota"}, "group": {g.ID}, "version": {"1"}, "site_limit": {"0"}, "storage_limit": {"0"}, "storage_unit": {"B"}}), 404)
	requireMemberStatus(t, memberPost(t, app, c, "/admin", url.Values{"csrf": {csrf}, "action": {"site-group"}, "site": {"field"}, "group": {""}, "version": {"1"}}), 404)
}
