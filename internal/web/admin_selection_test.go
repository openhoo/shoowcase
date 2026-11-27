package web

import (
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/openhoo/shoowcase/internal/sites"
)

// Exercise the two HTTP stages separately: a review is read-only and the
// confirmation must describe the same targets, operation and actor.
func TestAdminSelectionConfirmsExactMembers(t *testing.T) {
	app, store := handlerFixture(t, filepath.Join(t.TempDir(), "bulk.sqlite"), "team-a", false)
	c, csrf := joinMemberConsole(t, app)
	ctx := t.Context()
	admin := sites.Actor{Workspace: "team-a", Subject: "tester", Role: "admin"}
	var secrets []string
	for _, subject := range []string{"one", "two", "other-admin", "foreign"} {
		a := sites.Actor{Subject: subject, Workspace: admin.Workspace, Role: "editor"}
		if subject == "other-admin" {
			a.Role = "admin"
		}
		if subject == "foreign" {
			a.Workspace = "team-b"
		}
		if _, err := store.EnrollAccount(ctx, a, subject+" display"); err != nil {
			t.Fatal(err)
		}
		_, secret, err := store.CreateKey(ctx, a, "fixture", "reader", 1)
		if err != nil {
			t.Fatal(err)
		}
		secrets = append(secrets, secret)
	}
	post := func(f url.Values, status int) string {
		t.Helper()
		resp := memberPost(t, app, c, "/admin", f)
		requireMemberStatus(t, resp, status)
		return resp.Body.String()
	}
	f := url.Values{"action": {"review-selection"}, "operation": {"suspend-members"}, "selected": {"one", "other-admin"}}
	post(f, 403)
	f.Set("csrf", csrf)
	review := func() string {
		t.Helper()
		f.Set("action", "review-selection")
		body := post(f, 200)
		token := memberControl(t, body, "name", "review")
		if token == "" || !strings.Contains(body, "other-admin display") || !strings.Contains(body, "one display") {
			t.Fatal("review did not identify every member")
		}
		f.Set("action", "apply-selection")
		f.Set("review", token)
		return token
	}
	review()
	if _, err := store.ResolveKey(ctx, secrets[0]); err != nil {
		t.Fatal("review revoked a key")
	}
	post(f, 409) // missing explicit confirmation
	f.Set("confirm", "yes")
	f["selected"] = []string{"two", "other-admin"}
	post(f, 409)
	f["selected"] = []string{"one", "other-admin"}
	if err := store.SetMember(ctx, admin, "one", "reader", false, "", false); err != nil {
		t.Fatal(err)
	}
	post(f, 409) // stale role limit; no selection may be applied
	if _, err := store.ResolveKey(ctx, secrets[2]); err != nil {
		t.Fatal("partial revocation")
	}
	review()
	post(f, 303)
	post(f, 409)
	for _, i := range []int{0, 2} {
		if _, err := store.ResolveKey(ctx, secrets[i]); err == nil {
			t.Fatal("selected key still active")
		}
	}
	if _, err := store.ResolveKey(ctx, secrets[1]); err != nil {
		t.Fatal("unselected key revoked")
	}
	f.Set("operation", "restore-members")
	review()
	post(f, 303)
	for _, i := range []int{0, 2} {
		if _, err := store.ResolveKey(ctx, secrets[i]); err == nil {
			t.Fatal("restored account revived a key")
		}
	}
	f.Set("action", "review-selection")
	f.Set("operation", "suspend-members")
	for _, tc := range []struct {
		ids    []string
		status int
	}{
		{[]string{"one", "tester"}, 403}, {[]string{"one", "foreign"}, 404}, {[]string{"two", "two"}, 400}, {nil, 400},
	} {
		f["selected"] = tc.ids
		post(f, tc.status)
	}
	viewer := sites.Actor{Workspace: "team-a", Subject: "two", Role: "editor"}
	if _, err := store.ReviewAdminSelection(ctx, viewer, sites.AdminSelection{Action: "suspend-members", IDs: []string{"one"}}); err == nil {
		t.Fatal("non-admin could review member actions")
	}
}

func TestAdminSelectionGroupAtomicityAndRoster(t *testing.T) {
	app, s := handlerFixture(t, filepath.Join(t.TempDir(), "groups.sqlite"), "team-a", false)
	c, csrf := joinMemberConsole(t, app)
	a := sites.Actor{Subject: "tester", Workspace: "team-a", Role: "admin"}
	ctx := t.Context()
	for _, id := range []string{"one", "two"} {
		if err := s.ProvisionMember(ctx, a, id, id); err != nil {
			t.Fatal(err)
		}
	}
	makeGroup := func(name string) sites.Group {
		t.Helper()
		g, err := s.CreateGroup(ctx, a, name, "editor")
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	first, budget := makeGroup("Unassigned"), makeGroup("Assigned")
	seedSiteFor(t, s, a)
	if err := s.AssignSiteGroup(ctx, a, "field", budget.ID, 1); err != nil {
		t.Fatal(err)
	}
	form := func(op, group string, ids ...string) url.Values {
		return url.Values{"action": {"review-selection"}, "csrf": {csrf}, "group": {group}, "operation": {op}, "selected": ids}
	}
	review := func(f url.Values) url.Values {
		t.Helper()
		resp := memberPost(t, app, c, "/admin", f)
		requireMemberStatus(t, resp, 200)
		token := memberControl(t, resp.Body.String(), "name", "review")
		if token == "" {
			t.Fatal("no review")
		}
		f.Set("action", "apply-selection")
		f.Set("review", token)
		f.Set("confirm", "yes")
		return f
	}
	both := review(form("delete-groups", "", first.ID, budget.ID))
	resp := memberPost(t, app, c, "/admin", both)
	requireMemberStatus(t, resp, 409)
	if !strings.Contains(resp.Body.String(), "Reassign") {
		t.Fatal("missing useful resolution")
	}
	groups, err := s.ListGroups(ctx, a)
	if err != nil || len(groups) != 2 {
		t.Fatal("assigned group caused partial deletion")
	}
	for i, id := range []string{"one", "two"} {
		if err := s.SetGroupMember(ctx, a, first.ID, id, true, int64(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	remove := review(form("remove-members", first.ID, "one", "two"))
	if err := s.UpdateGroup(ctx, a, first.ID, "Renamed", "editor", 3); err != nil {
		t.Fatal(err)
	}
	requireMemberStatus(t, memberPost(t, app, c, "/admin", remove), 409)
	if members, err := s.GroupMembers(ctx, a, first.ID); err != nil || !slices.Equal(members, []string{"one", "two"}) {
		t.Fatal("stale review changed roster")
	}
	remove = review(form("remove-members", first.ID, "one", "two"))
	requireMemberStatus(t, memberPost(t, app, c, "/admin", remove), 303)
	if members, err := s.GroupMembers(ctx, a, first.ID); err != nil || len(members) != 0 {
		t.Fatal("roster did not commit")
	}
	for _, id := range []string{"one", "two"} {
		member, err := s.WorkspaceAccount(ctx, a, id)
		if err != nil || member.Disabled {
			t.Fatal("group removal changed member account")
		}
	}
	third := makeGroup("Third")
	both = review(form("delete-groups", "", first.ID, third.ID))
	requireMemberStatus(t, memberPost(t, app, c, "/admin", both), 303)
	groups, err = s.ListGroups(ctx, a)
	if err != nil || len(groups) != 1 || groups[0].ID != budget.ID {
		t.Fatal("wrong groups deleted")
	}
}
