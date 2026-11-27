package web

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestGetSiteViewsAcrossMCPAndConsole(t *testing.T) {
	// The site was public; both access paths must report the instance's
	// effective private audience and link to the selected historical revision.
	app, db := handlerFixture(t, filepath.Join(t.TempDir(), "views.db"), "team-a", false)
	rev := seedPublicSite(t, db)
	ctl, _ := joinMemberConsole(t, app)
	get := func(args map[string]any) map[string]any {
		t.Helper()
		args["site_id"] = "field"
		return keyTool(t, app, devBearer, "get_site", args, false)
	}
	page := get(map[string]any{"limit": 1})
	files := page["files"].([]any)
	if len(files) != 1 || files[0].(map[string]any)["path"] != "index.html" || page["next_cursor"] != "index.html" {
		t.Fatalf("unexpected first manifest page: %v", page)
	}
	if page["visibility"] != "workspace" || page["configured_visibility"] != "public" {
		t.Fatalf("effective audience: %v", page)
	}
	next := get(map[string]any{"cursor": page["next_cursor"]})
	if next["next_cursor"] != "" || len(next["files"].([]any)) != 1 {
		t.Fatalf("unexpected final manifest page: %v", next)
	}

	keyTool(t, app, devBearer, "upload_files", map[string]any{
		"site_id": "field", "expected_revision": rev, "request_id": "update",
		"files": []map[string]string{{"path": "index.html", "text": "<p>New draft</p>"}},
	}, false)
	file := get(map[string]any{"path": "index.html", "revision": rev})
	content := file["file"].(map[string]any)
	if file["selected_revision"] != rev || file["revision"] == rev || file["preview_url"] != app.siteURL("pv-"+rev) || file["site_url"] != app.siteURL("field") {
		t.Fatalf("historical file: %v", file)
	}
	if content["text"] != "<p>Release body</p>" || content["base64"] != nil || file["next_cursor"] != nil || file["files"] != nil {
		t.Fatalf("named file wire shape: %v", file)
	}
	history := get(map[string]any{"view": "releases"})
	releases := history["releases"].([]any)
	if history["next_cursor"] != "" || history["selected_revision"] != nil || len(releases) != 1 || releases[0].(map[string]any)["revision"] != rev {
		t.Fatalf("release history: %v", history)
	}
	detail := hitRoute(t, app, "GET", testControl+"/sites/field", ctl, nil)
	if detail.Code != 200 || !strings.Contains(detail.Body.String(), app.siteURL("pv-"+file["revision"].(string))) || !strings.Contains(detail.Body.String(), "Public sharing is disabled") || !strings.Contains(detail.Body.String(), rev[:8]) {
		t.Fatalf("console omitted the current preview, audience or published revision: status %d", detail.Code)
	}
	invalid := keyTool(t, app, devBearer, "get_site", map[string]any{"site_id": "field", "limit": 0}, true)
	problem := invalid["error"].(map[string]any)
	if problem["code"] != "invalid_argument" || problem["action"] == "" {
		t.Fatalf("lost structured validation error: %v", problem)
	}
}
