package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/openhoo/shoowcase/internal/sites"
)

type uploadSpec struct {
	Site     string `json:"site_id"`
	Revision string `json:"expected_revision"`
	Path     string `json:"path"`
	SHA      string `json:"sha256"`
	Size     int64  `json:"size"`
	Request  string `json:"request_id"`
}

const maxUpload = sites.MaxFileBytes

var hashFormat = regexp.MustCompile("^[0-9a-f]{64}$")

func (a *App) prepare(ctx context.Context, who sites.Actor, args json.RawMessage) (map[string]any, error) {
	if who.Role != "editor" && who.Role != "publisher" && who.Role != "admin" {
		return nil, problem("forbidden", "An editor role is required to upload files.")
	}
	var spec uploadSpec
	if json.Unmarshal(args, &spec) != nil || !hashFormat.MatchString(spec.SHA) || spec.Size < 1 || spec.Request == "" || len(spec.Request) > 128 {
		return nil, problem("invalid_argument", "Supply a relative path, SHA-256 hex, request_id, and size between 1 byte and 2 MiB.")
	}
	if _, err := sites.UploadTarget(spec.Path, spec.Size); err != nil {
		return nil, err
	}
	g, _ := json.Marshal(map[string]any{"site_id": spec.Site})
	site, e := a.store.Execute(ctx, who, "get_site", g)
	if e != nil {
		return nil, e
	}
	if site["revision"] != spec.Revision {
		return nil, problem("conflict", "Draft changed. Read the current revision and reapply.")
	}
	if reason, ok := site["storage_restriction"].(string); ok {
		return nil, problem("forbidden", reason)
	}
	expiry := time.Now().Add(5 * time.Minute)
	t := a.sign(ticket{Kind: "upload", KeyID: sourceKey(ctx), Who: who, Args: args, Expires: expiry.Unix()})
	link := a.config.ControlURL + "/upload?t=" + url.QueryEscape(t)
	return map[string]any{"upload_url": link, "browser_upload_url": link, "method": "PUT", "expires_at": expiry.UTC().Format(time.RFC3339), "size": spec.Size, "site_id": spec.Site}, nil
}

var uploadPage = template.Must(template.New("upload").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/ui.css"><title>Upload file · Shoowcase</title><main>
<h1>Upload a file to the draft</h1><p>Choose the original file your agent requested. Uploading it will not change the published site.</p>
<dl><dt>Site</dt><dd>{{.Site}}</dd><dt>Destination</dt><dd><code>{{.Path}}</code></dd><dt>Expected size</dt><dd>{{.Size}} bytes</dd></dl>
<details><summary>Verify the file checksum</summary><p>Expected SHA-256: <code>{{.SHA}}</code></p></details>
<form method="post" enctype="multipart/form-data"><input type="hidden" name="csrf" value="{{.CSRF}}"><label>Select file <input name="file" type="file" required></label><button>Upload to draft</button></form></main></html>`))

func (a *App) upload(w http.ResponseWriter, r *http.Request) {
	t, ok := a.ticket(r, "upload")
	if !ok {
		http.Error(w, "Expired or invalid upload", 400)
		return
	}
	var spec uploadSpec
	if json.Unmarshal(t.Args, &spec) != nil || spec.Size > maxUpload || spec.Size < 1 {
		http.Error(w, "Invalid upload", 400)
		return
	}
	ctx := r.Context()
	who := t.Who
	var err error
	var src io.Reader
	switch r.Method {
	case "GET", "POST":
		i, ok := a.auth.Browser(r)
		if !ok {
			a.login(w, r)
			return
		}
		who = actor(i)
		if who.Subject != t.Who.Subject || who.Workspace != t.Who.Workspace {
			http.Error(w, "Wrong account", 403)
			return
		}
		ctx, who, err = a.ticketActor(ctx, t, &i)
		if err != nil {
			writeError(w, err)
			return
		}
		if who.Role != "publisher" && who.Role != "editor" && who.Role != "admin" {
			http.Error(w, "Editor access is required for this upload", 403)
			return
		}
		if r.Method == "GET" {
			render(w, uploadPage, map[string]any{"Site": spec.Site, "Path": spec.Path, "Size": spec.Size, "SHA": spec.SHA, "CSRF": i.CSRF})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, spec.Size+24*1024)
		if r.ParseMultipartForm(maxUpload+24576) != nil {
			http.Error(w, "Invalid upload", 400)
			return
		}
		defer r.MultipartForm.RemoveAll()
		if !a.auth.ValidCSRF(r, r.PostForm.Get("csrf")) {
			http.Error(w, "Invalid form", 403)
			return
		}
		f, _, e := r.FormFile("file")
		if e != nil {
			http.Error(w, "Choose a file", 400)
			return
		}
		defer f.Close()
		src = f
	case "PUT":
		// The short-lived URL is a write capability for these exact bytes, site,
		// path and revision. It grants no read or publication permission.
		if o := r.Header.Get("Origin"); o != "" && o != a.config.ControlURL {
			http.Error(w, "Origin denied", 403)
			return
		}
		ctx, who, err = a.ticketActor(ctx, t, nil)
		if err != nil {
			writeError(w, err)
			return
		}
		src = http.MaxBytesReader(w, r.Body, spec.Size)
	default:
		w.Header().Set("Allow", "GET, POST, PUT")
		w.WriteHeader(405)
		return
	}
	b, e := io.ReadAll(io.LimitReader(src, spec.Size+1))
	h := sha256.Sum256(b)
	want, _ := hex.DecodeString(spec.SHA)
	if e != nil || int64(len(b)) != spec.Size || !bytes.Equal(h[:], want) {
		http.Error(w, "File size or SHA-256 does not match. The draft is unchanged. Choose the original file, or ask your agent for a new upload link for the changed file.", 400)
		return
	}
	change, _ := json.Marshal(map[string]any{"site_id": spec.Site, "expected_revision": spec.Revision, "request_id": spec.Request, "files": []map[string]string{{"path": spec.Path, "base64": base64.StdEncoding.EncodeToString(b)}}})
	if !a.config.Public {
		ctx = sites.WithPrivateDelivery(ctx)
	}
	result, e := a.store.Execute(ctx, who, "upload_files", change)
	if e != nil {
		writeError(w, e)
		return
	}
	if r.Method == "POST" {
		a.browserResult(w, r, result, "Upload complete", "Open the private preview to see the file in this revision. Uploading a file does not publish it.")
		return
	}
	writeJSON(w, a.decorate(result))
}
