package web

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/openhoo/shoowcase/internal/sites"
)

//go:embed siteconsole.html
var siteHTML string

var siteParts = template.Must(template.New("sites").Funcs(template.FuncMap{
	"humanBytes": func(n int) string {
		if n < 1024 {
			return fmt.Sprintf("%d B", n)
		}
		if n < 1<<20 {
			return fmt.Sprintf("%.1f KiB", float64(n)/1024)
		}
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	},
	"shortID": func(s string) string {
		if len(s) > 8 {
			return s[:8]
		}
		return s
	},
	"utcTime": func(s string) string {
		if t, e := time.Parse(time.RFC3339Nano, s); e == nil {
			return t.UTC().Format("02 Jan 2006, 15:04 UTC")
		}
		return s
	},
	"editable": func(p string) bool {
		e := strings.ToLower(path.Ext(p))
		return e == ".html" || e == ".css" || e == ".js"
	},
	"audience": func(s string) string {
		switch s {
		case "offline":
			return "Offline"
		case "public":
			return "Public"
		default:
			return "Workspace"
		}
	},
}).Parse(siteHTML))

type siteData struct {
	C                                                                   consoleContext
	Directory                                                           directoryData
	S, History                                                          map[string]any
	Base, Name, Slug, Path, Source, Rev, Request, Action, Notice, Error string
	CanEdit, CanPublish, AllowPublic, Existing, HasIndex                bool
}

func makeSiteData(c consoleContext) siteData {
	r := c.Actor.Role
	return siteData{C: c, Request: rand.Text(), CanEdit: r == "editor" || r == "publisher" || r == "admin", CanPublish: r == "publisher" || r == "admin"}
}

func siteView(w http.ResponseWriter, d siteData, status int, title, part string) {
	consoleView(w, d.C, status, title, "sites", siteParts.Lookup(part), d)
}
func siteCall(a *App, ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	b, e := json.Marshal(args)
	if e != nil {
		return nil, e
	}
	return a.execute(ctx, name, b)
}

func siteProblem(e error) (int, string) {
	switch errorCode(e) {
	case "invalid_argument":
		return 400, e.Error()
	case "forbidden", "unauthorized":
		return 403, "Your account does not have permission for this change."
	case "not_found":
		return 404, "This site, release, or file is unavailable in your workspace."
	case "conflict", "revision_conflict":
		return 409, e.Error()
	case "quota_exceeded", "rate_limited":
		return 429, e.Error()
	default:
		return 503, "Shoowcase could not complete the request. Try again shortly."
	}
}
func siteFailure(w http.ResponseWriter, c consoleContext, e error) {
	status, msg := siteProblem(e)
	consoleError(w, c, status, "Request could not be completed", msg)
}
func siteMethod(w http.ResponseWriter, c consoleContext, allowed string) {
	w.Header().Set("Allow", allowed)
	consoleError(w, c, 405, "Method not allowed", "Use the site controls to make changes.")
}
func siteRead(r *http.Request) bool { return r.Method == http.MethodGet || r.Method == http.MethodHead }

func (a *App) siteConsole(w http.ResponseWriter, r *http.Request, c consoleContext) {
	d := makeSiteData(c)
	d.AllowPublic = a.config.Public
	if r.URL.Path == "/sites/new" {
		if !siteRead(r) {
			siteMethod(w, c, "GET, HEAD")
			return
		}
		if !d.CanEdit {
			siteFailure(w, c, problem("forbidden", ""))
			return
		}
		siteView(w, d, 200, "New site", "new-site")
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/sites"), "/")
	if len(parts) == 1 || (len(parts) == 2 && parts[1] == "") {
		if r.Method == http.MethodPost {
			if !d.CanEdit {
				siteFailure(w, c, problem("forbidden", ""))
				return
			}
			d.Name, d.Slug = strings.TrimSpace(r.PostForm.Get("name")), strings.TrimSpace(r.PostForm.Get("slug"))
			args := map[string]any{"name": d.Name, "request_id": r.PostForm.Get("request_id")}
			if d.Slug != "" {
				args["slug"] = d.Slug
			}
			out, e := siteCall(a, r.Context(), "create_site", args)
			if e == nil {
				http.Redirect(w, r, directoryHref(out["site_id"].(string))+"?notice=created", 303)
				return
			}
			status, msg := siteProblem(e)
			d.Error = msg
			siteView(w, d, status, "New site", "new-site")
			return
		}
		if !siteRead(r) {
			siteMethod(w, c, "GET, HEAD, POST")
			return
		}
		var e error
		d.Directory, e = a.directory(r.Context(), c.Actor, r.URL.Query())
		if e != nil {
			siteFailure(w, c, e)
			return
		}
		siteView(w, d, 200, "Sites", "list")
		return
	}
	if len(parts) > 3 || parts[0] != "" || parts[1] == "" {
		http.NotFound(w, r)
		return
	}
	id := parts[1]
	if id == "~new" {
		id = "new"
	}
	d.Base = directoryHref(id)
	action := ""
	if len(parts) == 3 {
		action = parts[2]
	}
	switch action {
	case "", "edit", "delete", "upload", "import", "publish", "access", "rollback":
	default:
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost && action != "" {
		a.sitePost(w, r, d, id, action)
		return
	}
	if !siteRead(r) {
		siteMethod(w, c, "GET, HEAD")
		return
	}
	if action == "publish" || action == "access" {
		siteMethod(w, c, "POST")
		return
	}
	if action != "" && !d.CanEdit {
		siteFailure(w, c, problem("forbidden", ""))
		return
	}
	args := map[string]any{"site_id": id, "limit": 100}
	d.Path = r.URL.Query().Get("path")
	if action == "delete" && d.Path == "" {
		consoleError(w, c, 400, "Choose a file", "Open the site and select a file to remove.")
		return
	}
	if action == "edit" && d.Path != "" {
		args["path"] = d.Path
		d.Existing = true
	}
	var e error
	d.S, e = siteCall(a, r.Context(), "get_site", args)
	if e != nil {
		siteFailure(w, c, e)
		return
	}
	d.Rev = d.S["revision"].(string)
	d.Action = action
	switch action {
	case "edit":
		if f, ok := d.S["file"].(map[string]any); ok {
			s, editable := f["text"].(string)
			if !editable {
				consoleError(w, c, 400, "Choose an HTML, CSS or JavaScript file", "Use Upload file to replace an image.")
				return
			}
			d.Source = s
		} else {
			d.Path = "index.html"
			d.Source = "<!doctype html>\n<html lang=\"en\">\n<head>\n  <meta charset=\"utf-8\">\n  <meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n  <title>Welcome</title>\n</head>\n<body>\n  <main><h1>Welcome</h1></main>\n</body>\n</html>\n"
			if manifestHas(d.S, "index.html") {
				d.Path, d.Source = "", ""
			}
		}
		siteView(w, d, 200, "File editor", "edit")
	case "rollback":
		if !d.CanPublish {
			siteFailure(w, c, problem("forbidden", ""))
			return
		}
		release := r.URL.Query().Get("release")
		rev, err := a.store.ReleaseRevision(r.Context(), c.Actor, id, release)
		if err != nil {
			siteFailure(w, c, err)
			return
		}
		d.S["restore_id"] = release
		d.S["restore_preview"] = a.siteURL("pv-" + rev)
		d.Rev = rev
		siteView(w, d, 200, "Restore release", "restore")
	case "delete":
		if !manifestHas(d.S, d.Path) {
			siteFailure(w, c, problem("not_found", ""))
			return
		}
		siteView(w, d, 200, "Remove file", "delete")
	case "upload", "import":
		siteView(w, d, 200, siteUploadTitle(action), "upload")
	default:
		d.HasIndex = manifestHas(d.S, "index.html")
		d.Notice = map[string]string{"created": "Site created. Add a homepage to get started.", "saved": "File saved to the draft.", "removed": "File removed from the draft.", "uploaded": "File uploaded to the draft.", "imported": "ZIP imported into the draft.", "published": "Draft published.", "access": "Site access updated.", "restored": "Release restored."}[r.URL.Query().Get("notice")]
		args = map[string]any{"site_id": id, "view": "releases", "limit": 10}
		if cursor := r.URL.Query().Get("releases"); cursor != "" {
			args["cursor"] = cursor
		}
		d.History, e = siteCall(a, r.Context(), "get_site", args)
		if e != nil {
			siteFailure(w, c, e)
			return
		}
		siteView(w, d, 200, d.S["name"].(string), "detail")
	}
}

// The store's manifest concrete type is private. Decode this small list of
// metadata without requesting file contents merely to discover filenames.
func manifestHas(item map[string]any, name string) bool {
	b, _ := json.Marshal(item["files"])
	var out []struct{ Path string }
	_ = json.Unmarshal(b, &out)
	for _, file := range out {
		if file.Path == name {
			return true
		}
	}
	return false
}
func siteUploadTitle(action string) string {
	if action == "import" {
		return "Import ZIP"
	}
	return "Upload file"
}

func (a *App) sitePost(w http.ResponseWriter, r *http.Request, d siteData, id, action string) {
	management := action == "publish" || action == "access" || action == "rollback"
	if !d.CanEdit || (management && !d.CanPublish) {
		siteFailure(w, d.C, problem("forbidden", ""))
		return
	}
	f := r.PostForm
	d.Action, d.Rev, d.Path, d.Source = action, f.Get("revision"), f.Get("path"), f.Get("source")
	d.Existing = f.Get("existing") == "yes"
	args := map[string]any{"site_id": id, "request_id": f.Get("request_id")}
	tool, notice := "upload_files", "saved"
	var e error
	switch action {
	case "edit":
		d.Source = strings.ReplaceAll(d.Source, "\r\n", "\n")
		ct, err := sites.UploadTarget(d.Path, int64(len(d.Source)))
		e = err
		if e == nil && !strings.HasPrefix(ct, "text/") {
			e = badFile("The editor accepts HTML, CSS and JavaScript files.")
		}
		if e == nil && !utf8.ValidString(d.Source) {
			e = badFile("Use UTF-8 for the file contents.")
		}
		if e == nil && !d.Existing {
			// Check the immutable revision the form opened, so retrying an
			// already successful New file submission remains idempotent.
			var original map[string]any
			original, e = siteCall(a, r.Context(), "get_site", map[string]any{"site_id": id, "revision": d.Rev, "limit": sites.MaxFiles})
			if e == nil && manifestHas(original, d.Path) {
				e = problem("conflict", "A file with this path already exists. Open it from the file list to edit it, or choose another path.")
			}
		}
		args["expected_revision"] = d.Rev
		args["files"] = []map[string]string{{"path": d.Path, "text": d.Source}}
	case "delete":
		if f.Get("confirm") != "remove" {
			e = badFile("Confirm the file to remove it.")
		}
		args["expected_revision"] = d.Rev
		args["files"] = []consoleFile{}
		args["delete_paths"] = []string{d.Path}
		notice = "removed"
	case "upload", "import":
		var files []consoleFile
		files, e = consoleUpload(r, action)
		args["expected_revision"] = d.Rev
		args["files"] = files
		if action == "upload" {
			notice = "uploaded"
		} else {
			notice = "imported"
		}
	case "publish":
		tool = "publish_site"
		args["revision"] = d.Rev
		notice = "published"
	case "access":
		tool = "set_site_access"
		args["revision"] = d.Rev
		args["visibility"] = f.Get("visibility")
		notice = "access"
	case "rollback":
		tool = "rollback_site"
		args["release_id"] = f.Get("release")
		args["expected_release_id"] = f.Get("current")
		notice = "restored"
	}
	var out map[string]any
	if e == nil {
		out, e = siteCall(a, r.Context(), tool, args)
	}
	if e == nil {
		http.Redirect(w, r, d.Base+"?notice="+notice, 303)
		return
	}
	if errorCode(e) == "approval_required" {
		if link, ok := out["approval_url"].(string); ok {
			http.Redirect(w, r, link, 303)
			return
		}
	}
	status, msg := siteProblem(e)
	d.Error = msg
	if status == 400 || status == 409 || status == 429 || (status == 503 && action == "edit") {
		// Preserve the user’s draft text and old edit token on a conflict. Never
		// silently resubmit against a newer revision. The user can compare and
		// copy their work before opening the current file.
		d.S = map[string]any{"name": id}
		switch action {
		case "edit":
			siteView(w, d, status, "File editor", "edit")
			return
		case "delete":
			siteView(w, d, status, "Remove file", "delete")
			return
		case "upload", "import":
			siteView(w, d, status, siteUploadTitle(action), "upload")
			return
		}
	}
	consoleError(w, d.C, status, "Change was not applied", msg)
}

func consoleUpload(r *http.Request, action string) ([]consoleFile, error) {
	if r.MultipartForm == nil || len(r.MultipartForm.File["file"]) != 1 || len(r.MultipartForm.File) != 1 {
		return nil, badFile("Choose one file to upload.")
	}
	h := r.MultipartForm.File["file"][0]
	limit := int64(sites.MaxFileBytes)
	if action == "import" {
		limit = maxSiteArchive
	}
	if h.Size > limit {
		return nil, badFile("The selected file exceeds the upload limit.")
	}
	src, e := h.Open()
	if e != nil {
		return nil, badFile("The file could not be opened.")
	}
	defer src.Close()
	b, e := boundedBytes(src, limit)
	if e != nil {
		return nil, e
	}
	if action == "import" {
		return consoleZIP(b)
	}
	dest := strings.TrimSpace(r.PostForm.Get("path"))
	if dest == "" {
		dest = h.Filename
	}
	if _, e = sites.UploadTarget(dest, int64(len(b))); e != nil {
		return nil, e
	}
	return []consoleFile{{dest, base64.StdEncoding.EncodeToString(b)}}, nil
}
