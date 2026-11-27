package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openhoo/shoowcase/internal/sites"
	"golang.org/x/net/html"
)

func quickCommandFrom(t *testing.T, page string) string {
	t.Helper()
	z := html.NewTokenizer(strings.NewReader(page))
	for {
		switch z.Next() {
		case html.ErrorToken:
			t.Fatal("no setup command")
		case html.StartTagToken:
			n := z.Token()
			if n.Data == "textarea" {
				for _, a := range n.Attr {
					if a.Key == "id" && a.Val == "setup-command" && z.Next() == html.TextToken {
						return string(z.Text())
					}
				}
			}
		}
	}
}

func redeemResult(app *App, host, client, code string) *httptest.ResponseRecorder {
	buf, _ := json.Marshal(map[string]string{"client": client, "code": code})
	req := httptest.NewRequest("POST", host+"/quickstart/redeem", strings.NewReader(string(buf)))
	req.RemoteAddr = "127.0.0.1:42355"
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)
	return w
}

func TestQuickstartBrowserToAgent(t *testing.T) {
	app, db := handlerFixture(t, filepath.Join(t.TempDir(), "setup.sqlite"), "team-a", false)
	c, csrf := joinMemberConsole(t, app)
	replica, err := New(app.config, db, app.auth)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ id, platform string }{{"codex", "unix"}, {"cursor", "windows"}, {"copilot", "unix"}, {"claude", "windows"}, {"copilot-app", "unix"}, {"vscode", "windows"}, {"vscode-local", "unix"}, {"gemini", "windows"}, {"opencode", "unix"}, {"stdio", "windows"}} {
		t.Run(test.id, func(t *testing.T) {
			get := hitRoute(t, app, "GET", testControl+"/quickstart", c, nil)
			requireMemberStatus(t, get, 200)
			data := url.Values{"creation_token": {memberControl(t, get.Body.String(), "name", "creation_token")}, "client": {test.id}, "platform": {test.platform}, "scope": {"publisher"}, "days": {"7"}}
			requireMemberStatus(t, memberPost(t, app, c, "/quickstart", data), 403)
			data.Set("csrf", csrf)
			out := memberPost(t, app, c, "/quickstart", data)
			requireMemberStatus(t, out, 200)
			if strings.Contains(out.Body.String(), sites.MCPKeyPrefix) || !strings.Contains(out.Header().Get("Cache-Control"), "no-store") {
				t.Fatal("receipt contained a persistent key or allowed caching")
			}
			requireMemberStatus(t, memberPost(t, app, c, "/quickstart", data), 409)
			cmd := quickCommandFrom(t, out.Body.String())
			parts := strings.Split(cmd, "'")
			if len(parts) < 3 {
				t.Fatal("missing code in installer command")
			}
			code := parts[len(parts)-2]
			if cmd != quickCommand(testControl, test.id, test.platform, code) {
				t.Fatal("incorrect terminal, agent or control origin")
			}
			other := "cursor"
			if test.id == "copilot-app" || test.id == "vscode" {
				other = "copilot" // Same local settings, but not the same setup code.
			}
			if test.id == other {
				other = "codex"
			}
			if w := redeemResult(app, testControl, other, code); w.Code != 401 {
				t.Fatalf("wrong client: got HTTP %d, want 401", w.Code)
			}
			if redeemResult(app, testSite, test.id, code).Code == 200 {
				t.Fatal("content host accepted setup")
			}
			var wg sync.WaitGroup
			results := make(chan *httptest.ResponseRecorder, 2)
			for _, server := range []*App{app, replica} {
				wg.Go(func() { results <- redeemResult(server, testControl, test.id, code) })
			}
			wg.Wait()
			close(results)
			accepted := 0
			for w := range results {
				if w.Code == 401 {
					continue
				}
				requireMemberStatus(t, w, http.StatusOK)
				accepted++
				var v struct{ Token string }
				if json.Unmarshal(w.Body.Bytes(), &v) != nil || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
					t.Fatal("invalid CLI response")
				}
				a, err := db.ResolveKey(t.Context(), v.Token)
				if err != nil || a.Subject != "tester" || a.Workspace != "team-a" || a.Role != "publisher" {
					t.Fatal("incorrect authority")
				}
			}
			if accepted != 1 {
				t.Fatalf("accepted %d redemptions", accepted)
			}
		})
	}
}

func TestSetupCodeExpiryAndRevocation(t *testing.T) {
	app, db := handlerFixture(t, filepath.Join(t.TempDir(), "setup.sqlite"), "team-a", false)
	_, _ = joinMemberConsole(t, app)
	owner := sites.Actor{Workspace: "team-a", Subject: "tester", Role: "admin"}
	k, secret, err := db.CreateKey(t.Context(), owner, "revocable", "reader", 7)
	if err != nil {
		t.Fatal(err)
	}
	expired := app.setupCode(secret, "codex", time.Now().Add(-time.Second))
	if w := redeemResult(app, testControl, "codex", expired); w.Code != 401 || strings.Contains(w.Body.String(), secret) {
		t.Fatal("expired setup accepted")
	}
	code := app.setupCode(secret, "codex", time.Now().Add(10*time.Minute))
	if err := db.RevokeKey(t.Context(), owner, k.ID); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{code, "invalid"} {
		w := redeemResult(app, testControl, "codex", s)
		if w.Code != 401 || strings.Contains(w.Body.String(), secret) {
			t.Fatal("invalid setup disclosed credential")
		}
	}
}
