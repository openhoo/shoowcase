package web

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openhoo/shoowcase/internal/sites"
)

func TestSiteAndFileDeleteReviewBindsExactSelection(t *testing.T) {
	app, s := handlerFixture(t, filepath.Join(t.TempDir(), "selection.sqlite"), "team-a", true)
	c, csrf := joinMemberConsole(t, app)
	who := sites.Actor{Subject: "tester", Workspace: "team-a", Role: "admin"}
	call := func(tool string, args map[string]any) map[string]any {
		t.Helper()
		out, err := s.Execute(t.Context(), who, tool, mustSiteJSON(t, args))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	create := func(id string) string {
		return call("create_site", map[string]any{"slug": id, "name": id, "request_id": "create-" + id})["revision"].(string)
	}
	a, b := create("alpha"), create("beta")
	values := url.Values{"action": {"review-delete-sites"}, "site": {"alpha:" + a, "beta:" + b}}
	requireMemberStatus(t, memberPost(t, app, c, "/sites", values), 403)
	values.Set("csrf", csrf)
	review := memberPost(t, app, c, "/sites", values)
	requireMemberStatus(t, review, 200)
	if body := review.Body.String(); !strings.Contains(body, "alpha (alpha)") || !strings.Contains(body, "beta (beta)") {
		t.Fatal("targets absent from confirmation")
	}
	final := siteForm(t, review.Body.String(), "/sites")
	final.Set("confirm", "delete")
	final["site"] = []string{"alpha:" + a}
	requireMemberStatus(t, memberPost(t, app, c, "/sites", final), 400)
	final["site"] = values["site"]
	files := []map[string]string{{"path": "a.css", "text": "a{}"}, {"path": "b.css", "text": "b{}"}, {"path": "index.html", "text": "<p>Keep</p>"}}
	newRev := call("upload_files", map[string]any{"site_id": "beta", "expected_revision": b, "request_id": "files", "files": files})["revision"].(string)
	requireMemberStatus(t, memberPost(t, app, c, "/sites", final), 409)
	requireMemberStatus(t, hitRoute(t, app, "GET", testControl+"/sites/alpha", c, nil), 200)
	call("publish_site", map[string]any{"site_id": "beta", "revision": newRev, "request_id": "publish"})
	base := "/sites/beta/delete"
	fileReview := memberPost(t, app, c, base, url.Values{"csrf": {csrf}, "step": {"review"}, "revision": {newRev}, "path": {"a.css", "b.css"}})
	requireMemberStatus(t, fileReview, 200)
	fileFinal := siteForm(t, fileReview.Body.String(), base)
	fileFinal.Set("confirm", "remove")
	fileFinal["path"] = []string{"index.html", "b.css"}
	requireMemberStatus(t, memberPost(t, app, c, base, fileFinal), 400)
	fileFinal["path"] = []string{"a.css", "b.css"}
	saved := memberPost(t, app, c, base, fileFinal)
	requireMemberStatus(t, saved, 303)
	current, err := s.GetSite(t.Context(), who, sites.GetSiteQuery{Site: "beta", Limit: new(sites.MaxFiles)})
	if err != nil || current.Revision == newRev || len(current.Files) != 1 || !current.HasFile("index.html") {
		t.Fatal("selection did not update draft as reviewed", err)
	}
	requireMemberStatus(t, memberPost(t, app, c, base, fileFinal), 303)
	retry, err := s.GetSite(t.Context(), who, sites.GetSiteQuery{Site: "beta"})
	if err != nil || retry.Revision != current.Revision {
		t.Fatal("replay created a second revision", err)
	}
	if live, err := s.Fetch(t.Context(), "beta", "", "a.css", who); err != nil || string(live.Data) != "a{}" {
		t.Fatal("published release changed", err)
	}
}
