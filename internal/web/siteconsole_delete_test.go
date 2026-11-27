package web

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	requireMemberStatus(t, memberPost(t, app, c, "/sites", final), 400) // no confirmation
	final.Set("confirm", "delete")
	proof := final.Get("proof")
	final.Set("proof", app.selectionProof(consoleContext{Actor: who}, "delete-sites", final["site"]))
	requireMemberStatus(t, memberPost(t, app, c, "/sites", final), 400) // obsolete draft-only proof kind
	final.Set("proof", proof)
	reviewed := final["site"]
	if len(reviewed) != 2 {
		t.Fatalf("review targets: %v", reviewed)
	}
	final["site"] = reviewed[:1]
	requireMemberStatus(t, memberPost(t, app, c, "/sites", final), 400)
	final["site"] = reviewed
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

// Exercise the public and private delivery hosts, existing browser sessions and
// real signed write links. A receipt for a former release cannot revive it.
func TestDeletedSiteRevokesDeliveryAndSignedActions(t *testing.T) {
	app, store := handlerFixture(t, filepath.Join(t.TempDir(), "public.sqlite"), "team-a", true)
	control, csrf := joinMemberConsole(t, app)
	admin := sites.Actor{Subject: "tester", Workspace: "team-a", Role: "admin"}
	releaseRev := seedSiteFor(t, store, admin)
	_, key, err := store.CreateKey(t.Context(), admin, "Agent", "publisher", 1)
	if err != nil {
		t.Fatal(err)
	}
	keyTool(t, app, key, "set_site_access", map[string]any{"site_id": "field", "revision": releaseRev, "visibility": "workspace", "request_id": "go-private"}, false)
	public := func(request string) map[string]any {
		return keyTool(t, app, key, "set_site_access", map[string]any{"site_id": "field", "revision": releaseRev, "visibility": "public", "request_id": request}, true)
	}
	doneApproval, unusedApproval := public("go-public"), public("other-public")
	redeem := func(approval map[string]any) *httptest.ResponseRecorder {
		return memberPost(t, app, control, strings.TrimPrefix(approval["approval_url"].(string), testControl), url.Values{"csrf": {csrf}})
	}
	requireMemberStatus(t, redeem(doneApproval), 200)

	data := "p{color:blue}"
	upload := func(request string) string {
		return keyTool(t, app, key, "prepare_upload", uploadSpec{Site: "field", Revision: releaseRev, SHA: fmt.Sprintf("%x", sha256.Sum256([]byte(data))), Path: "screen.css", Size: int64(len(data)), Request: request}, false)["upload_url"].(string)
	}
	doneUpload, unusedUpload := upload("change-css"), upload("other-css")
	put := func(u string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PUT", u, strings.NewReader(data))
		req.RemoteAddr = "127.0.0.1:45300"
		w := httptest.NewRecorder()
		app.ServeHTTP(w, req)
		return w
	}
	requireMemberStatus(t, put(doneUpload), 200)
	draft, err := store.GetSite(t.Context(), admin, sites.GetSiteQuery{Site: "field"})
	if err != nil || draft.Revision == releaseRev {
		t.Fatal("missing new preview", err)
	}
	type host struct {
		url, etag string
		cookie    *http.Cookie
	}
	var hosts []host
	for _, hostURL := range []string{testSite, app.siteURL("pv-" + releaseRev), app.siteURL("pv-" + draft.Revision)} {
		_, session := browserSessions(t, app, strings.TrimSuffix(hostURL, "/"))
		target := strings.TrimSuffix(hostURL, "/") + "/screen.css"
		get := hitRoute(t, app, "GET", target, session, nil)
		requireMemberStatus(t, get, 200)
		if get.Header().Get("ETag") == "" || !strings.Contains(get.Header().Get("Cache-Control"), "no-store") {
			t.Fatalf("missing private cache policy or ETag on %s", target)
		}
		hosts = append(hosts, host{target, get.Header().Get("ETag"), session})
	}
	requireMemberStatus(t, hitRoute(t, app, "GET", hosts[0].url, nil, nil), 200)
	detail := hitRoute(t, app, "GET", testControl+"/sites/field", control, nil)
	requireMemberStatus(t, detail, 200)
	first := url.Values{"csrf": {csrf}, "action": {"review-delete-sites"}, "site": {siteSelectionValue("field", draft.Revision)}}
	review := memberPost(t, app, control, "/sites", first)
	requireMemberStatus(t, review, 200)
	if !strings.Contains(review.Body.String(), "Field Notes (field) · Live (Public)") {
		t.Fatal("review omits active public audience")
	}
	confirm := siteForm(t, review.Body.String(), "/sites")
	confirm.Set("confirm", "delete")
	done := memberPost(t, app, control, "/sites", confirm)
	requireMemberStatus(t, done, 303)
	if done.Header().Get("Location") != "/sites?notice=sites-deleted" {
		t.Fatalf("unexpected redirect %s", done.Header().Get("Location"))
	}
	requireMemberStatus(t, hitRoute(t, app, "GET", testControl+"/sites/field", control, nil), 404)
	for _, h := range hosts {
		for _, method := range []string{"GET", "HEAD"} {
			for _, conditional := range []string{"", h.etag} {
				headers := map[string]string{"If-None-Match": conditional}
				for _, auth := range []bool{false, true} {
					var cookie *http.Cookie
					expect := http.StatusSeeOther
					if auth {
						cookie, expect = h.cookie, http.StatusNotFound
					}
					res := hitRoute(t, app, method, h.url, cookie, headers)
					if res.Code != expect || res.Header().Get("ETag") != "" || strings.Contains(res.Body.String(), "color:") {
						t.Fatalf("%s %s (authenticated %v, conditional %v): HTTP %d", method, h.url, auth, conditional != "", res.Code)
					}
				}
			}
		}
	}
	for _, link := range []string{doneUpload, unusedUpload} {
		requireMemberStatus(t, hitRoute(t, app, "GET", link, control, nil), 404)
		requireMemberStatus(t, put(link), 404)
	}
	for _, spec := range []map[string]any{doneApproval, unusedApproval} {
		requireMemberStatus(t, hitRoute(t, app, "GET", spec["approval_url"].(string), control, nil), 404)
		requireMemberStatus(t, redeem(spec), 404)
		requireApprovalDenied(t, app, key, spec["approval_token"], "not_found")
	}
}
