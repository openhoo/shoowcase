// Package webui renders the management frontend. It must not be used on tenant hosts.
package webui

import (
	"bytes"
	"encoding/json"
	"html"
	"net/http"

	"github.com/openhoo/shoowcase/internal/web/uiassets"
)

// Render pairs a React page model with script-free forms. native is a body
// fragment already rendered by html/template; refresh is the validated login
// continuation URL, if any. Normal navigation and form POSTs use real URLs.
func Render(w http.ResponseWriter, status int, title, kind string, page any, native []byte, refresh string) {
	model, err := json.Marshal(struct {
		Kind string `json:"kind"`
		Page any    `json:"page"`
	}{kind, page})
	if err != nil {
		http.Error(w, "Page unavailable", 500)
		return
	}
	var doc bytes.Buffer
	doc.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="color-scheme" content="light dark"><title>`)
	doc.WriteString(html.EscapeString(title))
	doc.WriteString(` · Shoowcase</title><script src="` + uiassets.Theme.Path + `"></script><link rel="stylesheet" href="` + uiassets.Style.Path + `"><script defer src="` + uiassets.Script.Path + `"></script>`)
	if refresh != "" {
		doc.WriteString(`<noscript><meta http-equiv="refresh" content="0;url=`)
		doc.WriteString(html.EscapeString(refresh))
		doc.WriteString(`"></noscript>`)
	}
	doc.WriteString(`</head><body><a class="skip" href="#content">Skip to content</a><div id="app"><div id="native-page">`)
	doc.Write(native)
	doc.WriteString(`</div></div><script type="application/json" id="shoowcase-page">`)
	// json.Marshal escapes HTML-sensitive characters, so source code and form
	// values stay inside this inert data block until React reads it.
	doc.Write(model)
	doc.WriteString(`</script></body></html>`)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; script-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	w.WriteHeader(status)
	_, _ = doc.WriteTo(w)
}
