package web

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openhoo/shoowcase/internal/auth"
	"golang.org/x/net/html"
)

func publicModel(t *testing.T, doc string) map[string]any {
	t.Helper()
	n, err := html.Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	for s := range n.Descendants() {
		if s.Type != html.ElementNode || s.Data != "script" || s.FirstChild == nil {
			continue
		}
		for _, a := range s.Attr {
			if a.Key != "id" || a.Val != "shoowcase-page" {
				continue
			}
			var m struct {
				Kind string
				Page map[string]any
			}
			if err := json.Unmarshal([]byte(s.FirstChild.Data), &m); err != nil || m.Kind != "public" {
				t.Fatalf("invalid public model: %v", err)
			}
			return m.Page
		}
	}
	t.Fatal("public page model missing")
	return nil
}

func TestPublicControlPagesPreservePasswordFormsAndTenantIsolation(t *testing.T) {
	app, store := handlerFixture(t, filepath.Join(t.TempDir(), "public.db"), "team-a", true)
	s, err := auth.New(t.Context(), auth.Config{Dev: true, ControlURL: testControl, SitesDomain: testZone, SessionKey: base64.RawURLEncoding.EncodeToString(testKey), DevToken: devBearer, DevWorkspace: "team-a", DevSubject: "tester", LocalWorkspace: "team-a", PublicRegistration: true, LocalAccounts: store, Consume: store.Consume})
	if err != nil {
		t.Fatal(err)
	}
	app.auth = s
	get := func(path string) map[string]any {
		out := hitRoute(t, app, "GET", testControl+path, nil, nil)
		if out.Code != 200 || !strings.Contains(out.Header().Get("Cache-Control"), "no-store") {
			t.Fatalf("%s: %d", path, out.Code)
		}
		return publicModel(t, out.Body.String())
	}
	if p := get("/"); p["screen"] != "landing" || p["Signup"] != "/auth/register" {
		t.Fatal("landing did not use local registration settings")
	}
	for _, path := range []string{"/auth/login", "/auth/register"} {
		out := hitRoute(t, app, "GET", testControl+path, nil, nil)
		p := publicModel(t, out.Body.String())
		if out.Code != 200 || p["screen"] != "password" || p["Register"] != (path == "/auth/register") || p["Open"] != true || p["Flow"] == "" || p["Flow"] == nil {
			t.Fatal("password screen failed to bind the requested flow")
		}
		if out.Header().Get("Referrer-Policy") != "strict-origin" || !strings.Contains(out.Header().Get("Content-Security-Policy"), "form-action 'self'") {
			t.Fatal("password page lost form or referrer policy")
		}
		// The native form must submit the same server-issued intent as React.
		flow, ok := p["Flow"].(string)
		if !ok || flow == "" || memberControl(t, out.Body.String(), "name", "flow") != flow {
			t.Fatal("script-free password form lost the matching flow")
		}
	}
	for _, path := range []string{"/auth/local", "/auth/register", "/auth/login", "/console.js", "/ui.css"} {
		out := hitRoute(t, app, "GET", testSite+path, nil, nil)
		if strings.Contains(out.Body.String(), "shoowcase-page") || strings.Contains(out.Header().Get("Content-Security-Policy"), "script-src 'self'") {
			t.Fatalf("control interface served on tenant %s", path)
		}
	}
}

func TestSignedLinkErrorsNegotiateWithoutExposingRequest(t *testing.T) {
	app, _ := handlerFixture(t, filepath.Join(t.TempDir(), "actions.db"), "team-a", true)
	const sentinel = "expired-private-action-value"
	for _, tc := range []struct {
		method, path, accept string
		page                 bool
	}{
		{"GET", "/approve", "text/html", true}, {"GET", "/upload", "text/html", true}, {"POST", "/upload", "text/html", true},
		{"GET", "/approve", "application/json", false}, {"PUT", "/upload", "text/html", false},
	} {
		out := hitRoute(t, app, tc.method, testControl+tc.path+"?t="+sentinel, nil, map[string]string{"Accept": tc.accept})
		if out.Code != http.StatusBadRequest || strings.Contains(out.Body.String(), sentinel) {
			t.Fatal("wrong status or secret reflected")
		}
		if tc.page {
			if publicModel(t, out.Body.String())["screen"] != "error" || out.Header().Get("Referrer-Policy") != "strict-origin" {
				t.Fatal("signed link lost its protected error page")
			}
		} else if !strings.HasPrefix(out.Header().Get("Content-Type"), "text/plain") {
			t.Fatal("machine-facing error became HTML")
		}
	}
}
