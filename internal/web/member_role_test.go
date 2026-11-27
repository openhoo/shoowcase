package web

import (
	"encoding/base64"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openhoo/shoowcase/internal/auth"
	"github.com/openhoo/shoowcase/internal/sites"
)

func TestAdminChangesOnlyLocalSignInRoles(t *testing.T) {
	ctx := t.Context()
	app, store := handlerFixture(t, filepath.Join(t.TempDir(), "roles.sqlite"), "team-a", false)
	admin := sites.Actor{Workspace: "team-a", Subject: "tester", Role: "admin"}
	svc, err := auth.New(ctx, auth.Config{Dev: true, ControlURL: testControl, SitesDomain: testZone,
		SessionKey: base64.RawURLEncoding.EncodeToString(testKey), DevToken: devBearer, DevWorkspace: admin.Workspace,
		DevSubject: admin.Subject, LocalWorkspace: admin.Workspace, LocalAccounts: store, Consume: store.Consume})
	if err != nil {
		t.Fatal(err)
	}
	app.auth = svc
	if _, err = store.EnrollAccount(ctx, admin, "Workspace admin"); err != nil {
		t.Fatal(err)
	}
	login := hitRoute(t, app, "GET", testControl+"/auth/login?provider=oidc&next=/admin", nil, nil)
	requireMemberStatus(t, login, 303)
	cookie := testCookie(t, login, "session")
	m, err := store.CreateLocalAccount(ctx, admin, "local-member", "a long test passphrase", "reader")
	if err != nil {
		t.Fatal(err)
	}
	detail := "/admin?member=" + url.QueryEscape(m.Subject)
	form := hitRoute(t, app, "GET", testControl+detail, cookie, nil)
	requireMemberStatus(t, form, 200)
	data := url.Values{"action": {"local-role"}, "subject": {m.Subject}, "role": {"admin"},
		"expected_role": {"reader"}, "expected_ceiling": {""}, "expected_disabled": {"false"}}
	requireMemberStatus(t, memberPost(t, app, cookie, "/admin", data), 403)
	data.Set("csrf", memberControl(t, form.Body.String(), "name", "csrf"))
	updated := memberPost(t, app, cookie, "/admin", data)
	requireMemberStatus(t, updated, 303)
	page := hitRoute(t, app, "GET", testControl+updated.Header().Get("Location"), cookie, nil)
	requireMemberStatus(t, page, 200)
	if !strings.Contains(page.Body.String(), "Password account role saved.") {
		t.Fatal("role change did not return to the member with a confirmation")
	}
	data.Set("role", "editor")
	requireMemberStatus(t, memberPost(t, app, cookie, "/admin", data), 409)

	sso := sites.Actor{Workspace: admin.Workspace, Subject: "sso-user", Role: "editor"}
	if _, err = store.EnrollAccount(ctx, sso, "SSO user"); err != nil {
		t.Fatal(err)
	}
	data.Set("subject", sso.Subject)
	data.Set("expected_role", sso.Role)
	data.Set("role", "admin")
	requireMemberStatus(t, memberPost(t, app, cookie, "/admin", data), 403)
	data.Set("action", "role")
	data.Set("ceiling", "publisher")
	requireMemberStatus(t, memberPost(t, app, cookie, "/admin", data), 400)
	data.Set("ceiling", "reader")
	requireMemberStatus(t, memberPost(t, app, cookie, "/admin", data), 303)

	ssoAdmin := sites.Actor{Workspace: admin.Workspace, Subject: "sso-admin", Role: "admin"}
	if _, err = store.EnrollAccount(ctx, ssoAdmin, "SSO admin"); err != nil {
		t.Fatal(err)
	}
	memberURL := testControl + "/admin?member=" + url.QueryEscape(ssoAdmin.Subject)
	readAdmin := func(wantRole string) {
		t.Helper()
		page := hitRoute(t, app, "GET", memberURL, cookie, nil)
		requireMemberStatus(t, page, 200)
		body := page.Body.String()
		if !strings.Contains(body, "Current access: "+wantRole) || !strings.Contains(body, "Role limit for SSO admin") ||
			!strings.Contains(body, "Sign-in role: Admin.") || strings.Contains(body, `name="action" value="local-role"`) {
			t.Fatal("SSO admin detail must show the effective role and let another admin change only the local limit")
		}
	}

	readAdmin("Admin")
	data.Set("subject", ssoAdmin.Subject)
	data.Set("ceiling", "publisher")
	requireMemberStatus(t, memberPost(t, app, cookie, "/admin", data), 303)
	readAdmin("Publisher")
	data.Set("expected_ceiling", "publisher")
	data.Set("ceiling", "")
	requireMemberStatus(t, memberPost(t, app, cookie, "/admin", data), 303)
	readAdmin("Admin")
}
