package web

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openhoo/shoowcase/internal/sites"
	"golang.org/x/net/html"
)

// Capture defaults only from the form the user would submit. Different actions
// on a single page can carry different revisions and request IDs.
func siteForm(t *testing.T, page, action string) url.Values {
	t.Helper()
	z := html.NewTokenizer(strings.NewReader(page))
	values := url.Values{}
	selected := false
	for {
		switch tok := z.Next(); tok {
		case html.ErrorToken:
			t.Fatalf("missing form with action %s", action)
		case html.StartTagToken, html.SelfClosingTagToken:
			n := z.Token()
			if n.Data == "noscript" {
				z.NextIsNotRawText()
			}
			attrs := map[string]string{}
			for _, a := range n.Attr {
				attrs[a.Key] = a.Val
			}
			if n.Data == "form" {
				selected = attrs["action"] == action
			}
			if selected && n.Data == "input" && attrs["name"] != "" {
				values.Add(attrs["name"], attrs["value"])
			}
		case html.EndTagToken:
			if z.Token().Data == "form" && selected {
				if values.Get("csrf") == "" {
					t.Fatalf("missing CSRF field: %s", action)
				}
				return values
			}
		}
	}
}

func siteMultipart(t *testing.T, app *App, c *http.Cookie, path string, values url.Values, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for k, values := range values {
		if k == "file" {
			continue
		}
		for _, v := range values {
			if err := form.WriteField(k, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	writer, err := form.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Write(content); err != nil {
		t.Fatal(err)
	}
	if err = form.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", testControl+path, &body)
	req.RemoteAddr = "127.0.0.1:42000"
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Origin", testControl)
	req.AddCookie(c)
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)
	return w
}

func TestSiteConsoleManualWorkflow(t *testing.T) {
	app, _ := handlerFixture(t, filepath.Join(t.TempDir(), "sites.sqlite"), "team-a", true)
	c, _ := joinMemberConsole(t, app)
	page := func(path string) string {
		t.Helper()
		w := hitRoute(t, app, "GET", testControl+path, c, nil)
		requireMemberStatus(t, w, 200)
		return w.Body.String()
	}
	redirect := func(w *httptest.ResponseRecorder, loc string) {
		t.Helper()
		requireMemberStatus(t, w, 303)
		if w.Header().Get("Location") != loc {
			t.Fatalf("got redirect %s, want %s", w.Header().Get("Location"), loc)
		}
	}
	getForm := func(path, action string) url.Values { t.Helper(); return siteForm(t, page(path), action) }
	base := "/sites/field"
	f := getForm("/sites/new", "/sites")
	f.Set("name", "Field notebooks")
	f.Set("slug", "field")
	redirect(memberPost(t, app, c, "/sites", f), base+"?notice=created")
	if !strings.Contains(page(base), "No files yet") {
		t.Fatal("new site has no empty state")
	}
	newfile := getForm(base+"/edit", base+"/edit")
	if newfile.Get("path") != "index.html" {
		t.Fatal("missing suggested homepage path")
	}
	newfile.Set("source", "<p>Private draft</p>")
	redirect(memberPost(t, app, c, base+"/edit", newfile), base+"?notice=saved")
	redirect(memberPost(t, app, c, base+"/edit", newfile), base+"?notice=saved")
	level1 := getForm(base+"/edit?path=index.html", base+"/edit")
	duplicate := getForm(base+"/edit", base+"/edit")
	duplicate.Set("path", "index.html")
	duplicate.Set("source", "<p>Wrong file</p>")
	requireMemberStatus(t, memberPost(t, app, c, base+"/edit", duplicate), 409)
	if !strings.Contains(page(base+"/edit?path=index.html"), "Private draft") {
		t.Fatal("duplicate path overwrote existing file")
	}
	up := getForm(base+"/upload", base+"/upload")
	up.Set("path", "styles/theme.css")
	redirect(siteMultipart(t, app, c, base+"/upload", up, "site.css", []byte("p{color:red}")), base+"?notice=uploaded")
	level1.Set("source", "<p>Unsaved local edits</p>")
	stale := memberPost(t, app, c, base+"/edit", level1)
	requireMemberStatus(t, stale, 409)
	if !strings.Contains(stale.Body.String(), "Unsaved local edits") {
		t.Fatal("editor dropped text after conflict")
	}
	imp := getForm(base+"/import", base+"/import")
	bad := makeSiteZIP(t, zipFixtureEntry{"index.html", "<p>Should never appear</p>", 0}, zipFixtureEntry{"bad.png", "ordinary text", 0})
	requireMemberStatus(t, siteMultipart(t, app, c, base+"/import", imp, "bad.zip", bad), 400)
	if content := page(base + "/edit?path=index.html"); !strings.Contains(content, "Private draft") || strings.Contains(content, "Should never appear") {
		t.Fatal("failed ZIP changed draft")
	}
	imp = getForm(base+"/import", base+"/import")
	good := makeSiteZIP(t, zipFixtureEntry{"work/index.html", "<h1>Version one</h1>", 0}, zipFixtureEntry{"work/section.css", "h1{}", 0})
	redirect(siteMultipart(t, app, c, base+"/import", imp, "export.zip", good), base+"?notice=imported")
	first := getForm(base, base+"/publish")
	redirect(memberPost(t, app, c, base+"/publish", first), base+"?notice=published")
	_, contentCookie := browserSessions(t, app, testSite)
	live := hitRoute(t, app, "GET", testSite+"/", contentCookie, nil)
	if live.Code != 200 || live.Body.String() != "<h1>Version one</h1>" {
		t.Fatal("first publication did not deliver the imported homepage", live.Code)
	}
	del := getForm(base+"/delete?path=section.css", base+"/delete")
	requireMemberStatus(t, memberPost(t, app, c, base+"/delete", del), 400)
	del.Set("confirm", "remove")
	redirect(memberPost(t, app, c, base+"/delete", del), base+"?notice=removed")
	if w := hitRoute(t, app, "GET", testSite+"/section.css", contentCookie, nil); w.Code != 200 {
		t.Fatal("draft deletion changed live release")
	}
	edit := getForm(base+"/edit?path=index.html", base+"/edit")
	edit.Set("source", "<h1>Version two</h1>")
	redirect(memberPost(t, app, c, base+"/edit", edit), base+"?notice=saved")
	redirect(memberPost(t, app, c, base+"/publish", getForm(base, base+"/publish")), base+"?notice=published")
	if w := hitRoute(t, app, "GET", testSite+"/section.css", contentCookie, nil); w.Code != 404 {
		t.Fatal("removed file still live after publish")
	}
	if w := hitRoute(t, app, "GET", testSite+"/", contentCookie, nil); w.Body.String() != "<h1>Version two</h1>" {
		t.Fatal("second publish changed wrong content")
	}
	tok := html.NewTokenizer(strings.NewReader(page(base)))
	restore := ""
	for restore == "" {
		kind := tok.Next()
		if kind == html.ErrorToken {
			if tok.Err() != io.EOF {
				t.Fatal(tok.Err())
			}
			t.Fatal("missing link to previous release")
		}
		if kind != html.StartTagToken {
			continue
		}
		n := tok.Token()
		if n.Data == "noscript" {
			tok.NextIsNotRawText()
		}
		for _, attr := range n.Attr {
			if n.Data == "a" && attr.Key == "href" && strings.HasPrefix(attr.Val, base+"/rollback?release=") {
				restore = attr.Val
			}
		}
	}
	confirm := getForm(restore, base+"/rollback")
	redirect(memberPost(t, app, c, base+"/rollback", confirm), base+"?notice=restored")
	if w := hitRoute(t, app, "GET", testSite+"/", contentCookie, nil); w.Body.String() != "<h1>Version one</h1>" {
		t.Fatal("rollback did not restore old release")
	}
	if !strings.Contains(page(base+"/edit?path=index.html"), "Version two") {
		t.Fatal("rollback modified draft")
	}
	share := getForm(base, base+"/access")
	share.Set("visibility", "public")
	review := memberPost(t, app, c, base+"/access", share)
	requireMemberStatus(t, review, 303)
	if !strings.HasPrefix(review.Header().Get("Location"), testControl+"/approve?t=") {
		t.Fatal("public sharing needs signed review")
	}
	if w := hitRoute(t, app, "GET", testSite+"/", nil, nil); w.Code == 200 {
		t.Fatal("review request alone made site public")
	}
}

func TestSiteConsoleRoleAndWorkspace(t *testing.T) {
	db := filepath.Join(t.TempDir(), "policy.sqlite")
	app, s := handlerFixture(t, db, "team-a", false)
	c, _ := joinMemberConsole(t, app)
	seedPublicSite(t, s)
	reader, _ := handlerIdentityFixture(t, db, "reviewer", "team-a", false)
	admin := sites.Actor{Subject: "tester", Workspace: "team-a", Role: "admin"}
	if _, err := s.EnrollAccount(t.Context(), sites.Actor{Subject: "reviewer", Workspace: "team-a", Role: "editor"}, "Reviewer"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMember(t.Context(), admin, "reviewer", "reader", false, "", false); err != nil {
		t.Fatal(err)
	}
	readerCookie, _ := browserSessions(t, reader, testSite)
	keys := hitRoute(t, reader, "GET", testControl+"/keys", readerCookie, nil)
	requireMemberStatus(t, keys, 200)
	csrf := memberControl(t, keys.Body.String(), "name", "csrf")
	if csrf == "" {
		t.Fatal("missing CSRF token")
	}
	base := "/sites/field"
	view := hitRoute(t, reader, "GET", testControl+base, readerCookie, nil)
	requireMemberStatus(t, view, 200)
	if !strings.Contains(view.Body.String(), "index.html") || strings.Contains(view.Body.String(), "New file") {
		t.Fatal("read-only file list incorrect")
	}
	for _, path := range []string{base + "/edit?path=index.html", base + "/import", base + "/delete?path=index.html"} {
		requireMemberStatus(t, hitRoute(t, reader, "GET", testControl+path, readerCookie, nil), 403)
	}
	requireMemberStatus(t, memberPost(t, reader, readerCookie, base+"/publish", url.Values{"csrf": {csrf}}), 403)
	requireMemberStatus(t, memberPost(t, app, c, base+"/delete", url.Values{}), 403)
	other, _ := handlerIdentityFixture(t, db, "elsewhere", "team-b", false)
	otherCookie, _ := joinMemberConsole(t, other)
	requireMemberStatus(t, hitRoute(t, other, "GET", testControl+base, otherCookie, nil), 404)
}
