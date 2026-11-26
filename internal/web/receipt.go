package web

import (
	_ "embed"
	"html/template"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

func readMethod(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	w.Header().Set("Allow", "GET, HEAD")
	w.WriteHeader(http.StatusMethodNotAllowed)
	return false
}

// Browser navigation explicitly advertises HTML. Keep the JSON contract for
// tools that omit Accept or only send a wildcard.
func prefersHTML(accept string) bool {
	var html, json float64
	for item := range strings.SplitSeq(accept, ",") {
		media, params, err := mime.ParseMediaType(strings.TrimSpace(item))
		if err != nil {
			continue
		}
		q := 1.0
		if v, ok := params["q"]; ok {
			q, err = strconv.ParseFloat(v, 64)
			if err != nil || !(q >= 0 && q <= 1) {
				continue
			}
		}
		switch media {
		case "text/html":
			html = max(html, q)
		case "application/json":
			json = max(json, q)
		}
	}
	return html > 0 && html >= json
}

func (a *App) browserResult(w http.ResponseWriter, r *http.Request, raw map[string]any, title, message string) {
	res := a.decorate(raw)
	w.Header().Add("Vary", "Accept")
	if !prefersHTML(r.Header.Get("Accept")) {
		writeJSON(w, res)
		return
	}
	data := map[string]any{"Title": title, "Message": message, "Site": res["site_id"], "Preview": res["preview_url"], "Revision": res["revision"]}
	if r.URL.Path == "/approve" {
		data["SiteURL"] = res["site_url"]
		if live, _ := res["live_revision"].(string); live != "" {
			data["Revision"], data["Preview"] = live, a.siteURL("pv-"+live)
		}
	}
	render(w, receiptPage, data)
}

var receiptPage = template.Must(template.New("receipt").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><link rel="stylesheet" href="/ui.css"><title>{{.Title}} · Shoowcase</title><main><small>Shoowcase</small><h1>{{.Title}}</h1><p>{{.Message}}</p><dl><dt>Site</dt><dd>{{.Site}}</dd><dt>Revision</dt><dd><code>{{.Revision}}</code></dd></dl>{{if .Preview}}<p><a href="{{.Preview}}">Preview this revision</a></p>{{end}}{{if .SiteURL}}<p><a href="{{.SiteURL}}">Open the live site</a></p>{{end}}</main></html>`))

//go:embed control.css
var controlCSS string
