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
	return quickReceiptFrom(t, page, "setup-command")
}

func quickReceiptFrom(t *testing.T, page, id string) string {
	t.Helper()
	z := html.NewTokenizer(strings.NewReader(page))
	for {
		switch z.Next() {
		case html.ErrorToken:
			t.Fatalf("no setup receipt %s", id)
		case html.StartTagToken:
			n := z.Token()
			if n.Data == "noscript" {
				z.NextIsNotRawText()
			}
			if n.Data == "textarea" {
				for _, a := range n.Attr {
					if a.Key == "id" && a.Val == id && z.Next() == html.TextToken {
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

func TestQuickstartPromptToAgent(t *testing.T) {
	app, db := handlerFixture(t, filepath.Join(t.TempDir(), "prompt.sqlite"), "team-a", false)
	c, csrf := joinMemberConsole(t, app)
	get := hitRoute(t, app, http.MethodGet, testControl+"/quickstart", c, nil)
	requireMemberStatus(t, get, http.StatusOK)
	if page := get.Body.String(); !strings.Contains(page, "Get setup prompt") ||
		!strings.Contains(page, "/quickstart?setup=manual") || strings.Contains(page, `name="client"`) {
		t.Fatal("the default Quickstart should offer one prompt without requiring a client choice")
	}
	data := url.Values{"creation_token": {memberControl(t, get.Body.String(), "name", "creation_token")}, "setup": {"prompt"}}
	requireMemberStatus(t, memberPost(t, app, c, "/quickstart", data), http.StatusForbidden)
	data.Set("csrf", csrf)
	started := time.Now().Truncate(time.Second)
	out := memberPost(t, app, c, "/quickstart", data)
	requireMemberStatus(t, out, http.StatusOK)
	requireMemberStatus(t, memberPost(t, app, c, "/quickstart", data), http.StatusConflict)
	if strings.Contains(out.Body.String(), sites.MCPKeyPrefix) || out.Header().Get("Location") != "" ||
		!strings.Contains(out.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("prompt disclosed a persistent key or was cacheable or redirected")
	}
	prompt := quickReceiptFrom(t, out.Body.String(), "setup-prompt")
	var code string
	for line := range strings.SplitSeq(prompt, "\n") {
		if value, ok := strings.CutPrefix(line, "One-time setup key (valid for 10 minutes): "); ok {
			code = value
		}
	}
	if code == "" || !strings.Contains(prompt, testControl+"/mcp") || !strings.Contains(prompt, "list_sites") || strings.Contains(out.Body.String(), `id="setup-command"`) {
		t.Fatal("expected a single prompt with the setup key, installer instructions and a read-only check")
	}
	secret, expiry, ok := app.openSetupCode(code, "cursor")
	if !ok || expiry.Before(started.Add(quickSetupTTL)) || expiry.After(time.Now().Add(quickSetupTTL)) {
		t.Fatal("prompt was not usable by the chosen client for ten minutes")
	}
	owner := sites.Actor{Workspace: "team-a", Subject: "tester", Role: "admin"}
	keys, _, err := db.ListKeys(t.Context(), owner, false, "")
	if err != nil || len(keys) != 1 || keys[0].Name != "Quickstart agent" || keys[0].Scope != "publisher" || keys[0].ExpiresAt.Sub(keys[0].CreatedAt) != 30*24*time.Hour {
		t.Fatal("the registered agent should get an account-scoped, revocable 30-day key")
	}
	w := hitRoute(t, app, http.MethodPost, testControl+"/mcp", nil, map[string]string{"Authorization": "Bearer " + code})
	requireMemberStatus(t, w, http.StatusUnauthorized)
	if redeemResult(app, testControl, "unsupported", code).Code != http.StatusUnauthorized ||
		redeemResult(app, testSite, "cursor", code).Code == http.StatusOK {
		t.Fatal("setup key could be redeemed by an unknown client or on a content host")
	}
	replica, err := New(app.config, db, app.auth)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 2)
	wg.Go(func() { results <- redeemResult(app, testControl, "cursor", code) })
	wg.Go(func() { results <- redeemResult(replica, testControl, "codex", code) })
	wg.Wait()
	close(results)
	accepted := 0
	for w := range results {
		if w.Code == http.StatusUnauthorized {
			continue
		}
		requireMemberStatus(t, w, http.StatusOK)
		var v struct{ Token string }
		if json.Unmarshal(w.Body.Bytes(), &v) != nil || v.Token != secret {
			t.Fatal("redemption did not return the expected local connector key")
		}
		accepted++
	}
	if accepted != 1 {
		t.Fatalf("accepted %d redemptions across different clients and replicas", accepted)
	}
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
			get := hitRoute(t, app, "GET", testControl+"/quickstart?setup=manual", c, nil)
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

func TestQuickstartKeepsCompanyProxyChoice(t *testing.T) {
	app, _ := handlerFixture(t, filepath.Join(t.TempDir(), "proxy.sqlite"), "team-a", false)
	c, csrf := joinMemberConsole(t, app)
	for _, platform := range []string{"windows", "unix"} {
		get := hitRoute(t, app, "GET", testControl+"/quickstart?setup=manual", c, nil)
		form := url.Values{"csrf": {csrf}, "creation_token": {memberControl(t, get.Body.String(), "name", "creation_token")},
			"company_proxy": {"on"}, "platform": {platform}, "scope": {"reader"}, "days": {"7"}}
		bad := memberPost(t, app, c, "/quickstart", form)
		requireMemberStatus(t, bad, 400)
		if !strings.Contains(bad.Body.String(), `aria-describedby="proxy-help" checked`) {
			t.Fatal("invalid form lost the company proxy selection")
		}
		form.Set("creation_token", memberControl(t, bad.Body.String(), "name", "creation_token"))
		form.Set("client", "vscode")
		out := memberPost(t, app, c, "/quickstart", form)
		requireMemberStatus(t, out, 200)
		cmd := quickCommandFrom(t, out.Body.String())
		if !strings.Contains(cmd, "HTTPS_PROXY") || !strings.Contains(cmd, "SHOOWCASE_CA_FILE") ||
			!strings.Contains(cmd, "/quickstart/install."+map[string]string{"windows": "ps1", "unix": "sh"}[platform]) {
			t.Fatal("did not render the selected platform’s proxy command")
		}
	}
}

func TestQuickstartUninstallDownloads(t *testing.T) {
	app, _ := handlerFixture(t, filepath.Join(t.TempDir(), "setup.sqlite"), "team-a", false)
	c, _ := joinMemberConsole(t, app)
	page := hitRoute(t, app, "GET", testControl+"/quickstart?setup=manual", c, nil)
	requireMemberStatus(t, page, 200)
	for _, name := range []string{"uninstall.sh", "uninstall.ps1"} {
		path := "/quickstart/" + name
		if !strings.Contains(page.Body.String(), path) {
			t.Fatalf("missing uninstall command for %s", name)
		}
		want, err := installers.ReadFile("quickstartassets/" + name)
		if err != nil {
			t.Fatal(err)
		}
		download := hitRoute(t, app, "GET", testControl+path, nil, nil)
		if download.Code != 200 || download.Body.String() != string(want) ||
			!strings.HasPrefix(download.Header().Get("Content-Type"), "text/plain") {
			t.Fatalf("did not serve the standalone %s", name)
		}
		head := hitRoute(t, app, "HEAD", testControl+path, nil, nil)
		if head.Code != 200 || head.Body.Len() != 0 {
			t.Fatalf("unexpected HEAD for %s", name)
		}
		if hitRoute(t, app, "POST", testControl+path, nil, nil).Code != 405 ||
			hitRoute(t, app, "GET", testSite+path, nil, nil).Code == 200 {
			t.Fatalf("served %s outside the control download route", name)
		}
	}
}

func TestQuickstartNameConflictAndRecovery(t *testing.T) {
	app, _ := handlerFixture(t, filepath.Join(t.TempDir(), "setup.sqlite"), "team-a", false)
	c, csrf := joinMemberConsole(t, app)
	submit := func(name string) *httptest.ResponseRecorder {
		form := hitRoute(t, app, "GET", testControl+"/quickstart?setup=manual", c, nil)
		return memberPost(t, app, c, "/quickstart", url.Values{
			"csrf": {csrf}, "creation_token": {memberControl(t, form.Body.String(), "name", "creation_token")},
			"client": {"codex"}, "platform": {"unix"}, "scope": {"reader"}, "days": {"7"}, "name": {name},
		})
	}
	requireMemberStatus(t, submit("Codex"), 200)
	rejected := submit("  CODEX  ")
	requireMemberStatus(t, rejected, 409)
	if body := rejected.Body.String(); !strings.Contains(body, "You already have a key with this name.") ||
		!strings.Contains(body, `value="CODEX"`) || !strings.Contains(body, `id="quick-name-error"`) ||
		strings.Contains(body, `id="setup-command"`) {
		t.Fatal("duplicate should reopen the form with the name and without a setup command")
	}
	automatic := submit("")
	requireMemberStatus(t, automatic, 200)
	if !strings.Contains(automatic.Body.String(), "<strong>Codex (2)</strong>") {
		t.Fatal("setup receipt did not show the chosen unique name")
	}
}
