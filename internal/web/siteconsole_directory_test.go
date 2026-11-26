package web

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/openhoo/shoowcase/internal/sites"
)

func TestDirectoryFiltersAndTotals(t *testing.T) {
	app, s := handlerFixture(t, filepath.Join(t.TempDir(), "directory.sqlite"), "team-a", true)
	who := sites.Actor{Subject: "tester", Workspace: "team-a", Role: "admin"}
	if _, err := s.EnrollAccount(t.Context(), who, "Editor"); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	for i := 0; i < 99; i++ {
		id := fmt.Sprintf("draft-%02d", i)
		args := map[string]any{"name": "Same name", "slug": id, "request_id": id}
		if i == 98 {
			args["name"] = "Flusswiese"
		}
		if _, err := s.Execute(ctx, who, "create_site", mustSiteJSON(t, args)); err != nil {
			t.Fatal(err)
		}
	}
	rev := seedSiteFor(t, s, who)
	_, err := s.Execute(ctx, who, "upload_files", mustSiteJSON(t, map[string]any{"site_id": "field", "expected_revision": rev, "request_id": "pending", "files": []map[string]string{{"path": "more.css", "text": "p{}"}}}))
	if err != nil {
		t.Fatal(err)
	}
	lookup := func(query string) directoryData {
		t.Helper()
		q, err := url.ParseQuery(query)
		if err != nil {
			t.Fatal(err)
		}
		res, err := app.directory(ctx, who, q)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	all := lookup("sort=name")
	if all.Total != 100 || all.Drafts != 99 || all.Published != 1 || all.Changes != 1 || all.MatchCount != 100 {
		t.Fatal("directory totals not global", all.Total, all.Drafts, all.Published, all.Changes, all.MatchCount)
	}
	names := all.Sites[2:]
	if !slices.IsSortedFunc(names, func(a, b directoryEntry) int { return strings.Compare(a.ID, b.ID) }) {
		t.Fatal("unstable sorting for matching names")
	}
	res := lookup("q=FIELD&state=changes&audience=public")
	if res.MatchCount != 1 || !res.Filtered || res.Sites[0].FileCount != 3 || res.Sites[0].State != "changes" || res.Total != 100 || res.Sites[0].Updated.IsZero() {
		t.Fatal("combined search did not match correct metadata", res)
	}
	for _, q := range []string{"q=notes", "q=dRaFt-98&state=draft", "q=Flusswiese&audience=workspace"} {
		if x := lookup(q); x.MatchCount != 1 {
			t.Fatal(q, x.MatchCount)
		}
	}
	if x := lookup("state=published"); x.MatchCount != 0 {
		t.Fatal("edits incorrectly listed in up-to-date state")
	}
	if x := lookup("q=missing"); x.MatchCount != 0 || x.Total != 100 {
		t.Fatal("empty result corrupted workspace totals")
	}
	sorted := lookup("sort=oldest").Sites
	if !slices.IsSortedFunc(sorted, func(a, b directoryEntry) int {
		if c := a.Updated.Compare(b.Updated); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	}) {
		t.Fatal("oldest sort incorrect")
	}
	app.config.Public = false
	if x := lookup("audience=public"); x.MatchCount != 0 {
		t.Fatal("public filter revealed disabled audience")
	}
	if x := lookup("audience=workspace"); x.MatchCount != 100 {
		t.Fatal("effective workspace audience omitted")
	}
	for _, q := range []string{"sort=invalid", "state=invalid", "audience=invalid", "q=" + strings.Repeat("a", 101)} {
		values, _ := url.ParseQuery(q)
		if _, e := app.directory(ctx, who, values); errorCode(e) != "invalid_argument" {
			t.Fatal("invalid filter accepted", q)
		}
	}
}

func mustSiteJSON(t *testing.T, m map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLegacyNewSlugSharesNoRouteWithCreationForm(t *testing.T) {
	app, _ := handlerFixture(t, filepath.Join(t.TempDir(), "new.sqlite"), "team-a", false)
	cookie, _ := joinMemberConsole(t, app)
	form := hitRoute(t, app, "GET", testControl+"/sites/new", cookie, nil)
	requireMemberStatus(t, form, 200)
	val := siteForm(t, form.Body.String(), "/sites")
	val.Set("name", "Legacy site")
	val.Set("slug", "new")
	saved := memberPost(t, app, cookie, "/sites", val)
	requireMemberStatus(t, saved, 303)
	if dst := saved.Header().Get("Location"); dst != "/sites/~new?notice=created" {
		t.Fatalf("site opens the form instead: %s", dst)
	}
	list := hitRoute(t, app, "GET", testControl+"/sites", cookie, nil)
	if !strings.Contains(list.Body.String(), `href="/sites/~new"`) {
		t.Fatal("directory lost link to existing site named new")
	}
	content := hitRoute(t, app, "GET", testControl+"/sites/~new/edit", cookie, nil)
	requireMemberStatus(t, content, 200)
	if siteForm(t, content.Body.String(), "/sites/~new/edit").Get("path") != "index.html" {
		t.Fatal("existing site cannot edit its homepage")
	}
}
