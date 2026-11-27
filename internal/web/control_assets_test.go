package web

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/openhoo/shoowcase/internal/routes"
)

// Follow the links emitted by the real control page. This catches a mismatch
// between the build, the router and the bytes available to a cached browser.
func TestControlAssetsMatchTheirContentAddresses(t *testing.T) {
	app, _ := handlerFixture(t, filepath.Join(t.TempDir(), "ui.db"), "team-a", true)
	page := hitRoute(t, app, http.MethodGet, testControl+"/", nil, nil)
	requireMemberStatus(t, page, 200)
	if !strings.Contains(page.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("a cached page could retain old asset references")
	}
	for _, spec := range []struct{ alias, tag, mime string }{
		{"/ui.css", `<link rel="stylesheet" href="([^"]+)"`, "text/css"},
		{"/console.js", `<script defer src="([^"]+)"`, "text/javascript"},
	} {
		match := regexp.MustCompile(spec.tag).FindStringSubmatch(page.Body.String())
		if len(match) != 2 {
			t.Fatalf("page did not reference %s", spec.alias)
		}
		target := match[1]
		get := hitRoute(t, app, "GET", testControl+target, nil, nil)
		requireMemberStatus(t, get, 200)
		digest := fmt.Sprintf("%x", sha256.Sum256(get.Body.Bytes()))
		if target != "/ui/"+digest+spec.alias || !strings.HasPrefix(get.Header().Get("Content-Type"), spec.mime) || !strings.Contains(get.Header().Get("Cache-Control"), "immutable") {
			t.Fatalf("incorrect content address, type or cache policy for %s", spec.alias)
		}
		etag := get.Header().Get("ETag")
		if etag != `"`+digest+`"` || get.Body.Len() == 0 {
			t.Fatalf("incorrect validator or empty %s", spec.alias)
		}
		alias := hitRoute(t, app, "GET", testControl+spec.alias, nil, nil)
		if alias.Code != 200 || alias.Body.String() != get.Body.String() || alias.Header().Get("Cache-Control") != "no-cache" || alias.Header().Get("ETag") != etag {
			t.Fatalf("legacy URL is unavailable or could cache stale bytes for %s", spec.alias)
		}
		for _, path := range []string{spec.alias, target} {
			conditional := hitRoute(t, app, "GET", testControl+path, nil, map[string]string{"If-None-Match": etag})
			requireMemberStatus(t, conditional, 304)
			head := hitRoute(t, app, "HEAD", testControl+path, nil, nil)
			if head.Code != 200 || head.Header().Get("ETag") != etag || head.Body.Len() != 0 {
				t.Fatalf("wrong HEAD for %s", path)
			}
			requireMemberStatus(t, hitRoute(t, app, "POST", testControl+path, nil, nil), 405)
		}
		oldPath := "/ui/" + strings.Repeat("a", 64) + spec.alias
		stale := hitRoute(t, app, "GET", testControl+oldPath, nil, map[string]string{"If-None-Match": etag})
		if stale.Code != 404 || stale.Header().Get("ETag") != "" || !strings.Contains(stale.Header().Get("Cache-Control"), "no-store") {
			t.Fatalf("stale address served or cached the wrong version of %s", spec.alias)
		}
		req, err := http.NewRequest("GET", testControl+target, nil)
		if err != nil || routes.Label(req, true) != spec.alias {
			t.Fatalf("unstable telemetry label for %s", spec.alias)
		}
		tenant := hitRoute(t, app, "GET", testSite+target, nil, nil)
		if tenant.Code == 200 || tenant.Header().Get("ETag") == etag {
			t.Fatalf("control asset is available on tenant host: %s", spec.alias)
		}
	}
}
