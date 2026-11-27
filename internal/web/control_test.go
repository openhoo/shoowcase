package web

import (
	"net/http"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/openhoo/shoowcase/internal/auth"
	"github.com/openhoo/shoowcase/internal/routes"
)

// Some policy tests inject a verified identity instead of minting an OIDC
// cookie. Select the page with the same method and path table as the server.
func signedInPage(app *App, w http.ResponseWriter, r *http.Request, i auth.Identity) {
	m := routes.NewControl(routes.Handlers{Console: func(w http.ResponseWriter, r *http.Request, e routes.Endpoint) { app.signedInConsole(w, r, i, e) }})
	m.ServeHTTP(w, r)
}

func TestControlRoutingKeepsHostAndFormSemantics(t *testing.T) {
	app, _ := handlerFixture(t, filepath.Join(t.TempDir(), "routes.sqlite"), "team-a", false)
	c, _ := joinMemberConsole(t, app)
	for _, tc := range []struct {
		method, path, allow string
		status              int
	}{
		{"PUT", "/sites", "GET, HEAD, POST", 405},
		{"PUT", "/sites/", "GET, HEAD, POST", 405},
		{"GET", "/sites/sample/publish", "POST", 405},
		{"HEAD", "/sites/sample/access", "POST", 405},
		{"GET", "/sites/sample/surprise", "", 404},
		{"GET", "/sites//edit", "", 404},
		{"GET", "/x/../healthz", "", 404},
		{"GET", "//healthz", "", 404},
		{"GET", "/healthz/", "", 404},
		{"POST", "/healthz", "GET, HEAD", 405},
		{"GET", "/healthz", "", 200},
		{"HEAD", "/sites%2Fnew", "", 200},
	} {
		w := hitRoute(t, app, tc.method, testControl+tc.path, c, nil)
		if w.Code != tc.status || w.Header().Get("Allow") != tc.allow || w.Header().Get("Location") != "" {
			t.Errorf("%s %s: status %d, Allow %q, Location %q", tc.method, tc.path, w.Code, w.Header().Get("Allow"), w.Header().Get("Location"))
		}
	}
	// Authenticated form validation precedes any route-specific 405.
	for _, path := range []string{"/sites/new", "/sites/sample"} {
		f := siteForm(t, hitRoute(t, app, "GET", testControl+"/sites/new", c, nil).Body.String(), "/sites")
		w := memberPost(t, app, c, path, f)
		if w.Code != 405 || w.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("POST %s: %d, Allow %q", path, w.Code, w.Header().Get("Allow"))
		}
		f.Set("csrf", "wrong")
		if w = memberPost(t, app, c, path, f); w.Code != 403 {
			t.Errorf("POST %s invalid CSRF: %d", path, w.Code)
		}
		w = hitRoute(t, app, "POST", testControl+path, nil, nil)
		if w.Code != 303 {
			t.Errorf("anonymous POST %s: %d", path, w.Code)
		}
	}
	// The mux matches the decoded path, but sign-in must retain the original
	// escaping and query so the user returns to the exact requested page.
	for _, path := range []string{"/sites%2fnew?x=y%2fz", "/sites/%2e/edit?x=a%2fb"} {
		w := hitRoute(t, app, "GET", testControl+path, nil, nil)
		want := "/auth/login?next=" + url.QueryEscape(path)
		if w.Code != 303 || w.Header().Get("Location") != want {
			t.Errorf("anonymous GET %s: %d Location %q, want %q", path, w.Code, w.Header().Get("Location"), want)
		}
	}
	for _, path := range []string{"/sites//edit", "/sites/x/no-such-action"} {
		w := hitRoute(t, app, "GET", testControl+path, nil, nil)
		if w.Code != 303 {
			t.Errorf("anonymous GET %s: %d", path, w.Code)
		}
	}
}
