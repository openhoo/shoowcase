package web

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkAccountPasswordSettings(t *testing.T) {
	app, store := handlerFixture(t, filepath.Join(t.TempDir(), "password.sqlite"), "team-a", false)
	cookie, csrf := joinMemberConsole(t, app)
	const settings = "https://id.example.test/account?tab=profile#security"
	for _, dest := range []string{"", settings} {
		cfg := app.config
		cfg.OIDCAccountURL = dest
		configured, err := New(cfg, store, app.auth)
		if err != nil {
			t.Fatal(err)
		}
		page := hitRoute(t, configured, "GET", testControl+"/account?oidc_account_url=https://unexpected.example", cookie, nil)
		requireMemberStatus(t, page, 200)
		body := page.Body.String()
		if strings.Contains(body, "unexpected.example") || strings.Contains(body, `type="password"`) {
			t.Fatal("work account rendered an unconfigured destination or a local password field")
		}
		if dest == "" && !strings.Contains(body, "work account’s security settings") {
			t.Fatal("missing instructions for an unconfigured provider")
		}
		if strings.Contains(body, `href="`+settings+`"`) != (dest != "") {
			t.Fatal("work account did not use its configured settings link")
		}
	}
	requireMemberStatus(t, memberPost(t, app, cookie, "/account", url.Values{
		"action": {"change-password"}, "csrf": {csrf}, "current_password": {"old example password"}, "new_password": {"new example password"},
	}), 403)
	for _, dest := range []string{"http://id.example.test/me", "//id.example.test/me", "/me", "https://user:pass@id.example.test/me", "https:///me", "javascript:alert(1)"} {
		cfg := app.config
		cfg.OIDCAccountURL = dest
		if _, err := New(cfg, store, app.auth); err == nil {
			t.Errorf("accepted invalid account URL %q", dest)
		}
	}
}
