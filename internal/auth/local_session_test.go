package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/openhoo/shoowcase/internal/sites"
)

func TestPasswordSessionLifetimeAndAccountChanges(t *testing.T) {
	ctx := t.Context()
	store, err := sites.Open(ctx, "sqlite", filepath.Join(t.TempDir(), "sessions.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	admin := sites.Actor{Workspace: "team", Subject: "admin", Role: "admin"}
	if _, err := store.EnrollAccount(ctx, admin, "Administrator"); err != nil {
		t.Fatal(err)
	}
	const username = "password-user"
	const password = "test passphrase for sign-in"
	member, err := store.CreateLocalAccount(ctx, admin, username, password, "reader")
	if err != nil {
		t.Fatal(err)
	}
	s := service(t, Config{Dev: true, ControlURL: "http://studio.localhost", SitesDomain: "sites.localhost",
		LocalWorkspace: admin.Workspace, LocalAccounts: store, Consume: store.Consume,
		IsConsumed: store.IsConsumed,
		RevokeSession: func(ctx context.Context, key string, expires time.Time, who Identity) error {
			return store.RevokeSessionAndAudit(ctx, key, expires, sites.Actor{Workspace: who.Workspace, Subject: who.Subject, Role: "reader"})
		},
	})
	now := time.Now().Truncate(time.Second)
	s.now = func() time.Time { return now }
	login := func(pass string) *http.Cookie {
		t.Helper()
		page := hit(t, s, req("GET", s.cfg.ControlURL+"/auth/login?next=/account"), http.StatusOK)
		formCookie := getCookie(t, page, s.name("password-form"))
		body, err := io.ReadAll(page.Body)
		page.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		flow := regexp.MustCompile(`name="flow" value="([A-Za-z0-9_-]+)"`).FindSubmatch(body)
		if len(flow) != 2 {
			t.Fatal("sign-in form missing its flow binding")
		}
		post := httptest.NewRequest(http.MethodPost, s.cfg.ControlURL+"/auth/local", strings.NewReader(url.Values{
			"username": {username}, "password": {pass}, "flow": {string(flow[1])},
		}.Encode()))
		post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		post.Header.Set("Origin", s.cfg.ControlURL)
		post.RemoteAddr = "127.0.0.1:53306"
		post.AddCookie(formCookie)
		return getCookie(t, hit(t, s, post, http.StatusSeeOther), s.name("session"))
	}
	check := func(cookie *http.Cookie, want bool) Identity {
		t.Helper()
		who, ok := s.Browser(req("GET", s.cfg.ControlURL+"/account", cookie))
		if ok != want || ok && (who.Subject != member.Subject || who.Role != Viewer) {
			t.Fatalf("password session accepted: %t, want %t", ok, want)
		}
		return who
	}

	const passwordSeconds = 30 * 24 * 60 * 60
	cookie := login(password)
	if cookie.MaxAge != passwordSeconds {
		t.Fatalf("password cookie lasts %d seconds, want %d", cookie.MaxAge, passwordSeconds)
	}
	signedIn := now
	now = signedIn.Add((passwordSeconds - 1) * time.Second)
	check(cookie, true)
	now = signedIn.Add(passwordSeconds * time.Second)
	check(cookie, false)

	// Change the credentials while two new cookies are still live.
	cookie, second := login(password), login(password)
	now = now.Add(time.Hour)
	who := check(cookie, true)
	check(second, true)
	const replacement = "a new test password for sign-in"
	user := sites.Actor{Workspace: who.Workspace, Subject: who.Subject, Role: "reader"}
	if err := store.ChangeLocalPassword(sites.WithLocalSession(ctx, who.LocalGeneration), user, password, replacement); err != nil {
		t.Fatal(err)
	}
	check(cookie, false)
	check(second, false)
	current := login(replacement)
	check(current, true)
	if err := store.SetMember(ctx, admin, member.Subject, "", true, "", false); err != nil {
		t.Fatal(err)
	}
	check(current, false)
	if err := store.SetMember(ctx, admin, member.Subject, "", false, "", true); err != nil {
		t.Fatal(err)
	}
	check(current, false)
}
