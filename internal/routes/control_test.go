package routes

import (
	"net/http/httptest"
	"testing"
)

func TestControlAuthAndSiteRouteLabels(t *testing.T) {
	for _, tc := range []struct{ method, path, control, content string }{
		{"GET", "/auth/local", "/auth/local", "/{path...}"},
		{"GET", "/auth/register", "/auth/register", "/{path...}"},
		{"GET", "/auth/finish", "/auth/finish", "/auth/finish"},
		{"GET", "/sites/new", "/sites/new", "/{path...}"},
		{"GET", "/sites/private-team/edit?q=secret", "/sites/{site}/edit", "/{path...}"},
		{"POST", "/sites/private-team/edit", "/sites/{site}/edit", "/{path...}"},
		{"PUT", "/sites", "/sites", "/{path...}"},
		{"GET", "/sites/private-team/private-route", "", "/{path...}"},
		{"GET", "/sites//edit", "", "/{path...}"},
		{"GET", "/x/../auth/login", "", "/{path...}"},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		if got := Label(r, true); got != tc.control {
			t.Errorf("%s %s control label = %q, want %q", tc.method, tc.path, got, tc.control)
		}
		if got := Label(r, false); got != tc.content {
			t.Errorf("%s %s content label = %q, want %q", tc.method, tc.path, got, tc.content)
		}
	}
}
