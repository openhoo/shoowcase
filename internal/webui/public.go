package webui

import (
	"bytes"
	"html/template"
	"net/http"
)

// Public renders a standalone control-host screen and its native HTML fallback.
// Only pass fields that belong in the browser; never serialize an auth session,
// ticket structure, or submitted credentials.
func Public(w http.ResponseWriter, status int, title, screen string, t *template.Template, fields map[string]any) {
	var fallback bytes.Buffer
	if err := t.Execute(&fallback, fields); err != nil {
		http.Error(w, "Page unavailable", http.StatusInternalServerError)
		return
	}
	if fields == nil {
		fields = make(map[string]any)
	}
	fields["screen"] = screen
	var refresh string
	if screen == "continue" {
		refresh, _ = fields["Next"].(string)
	}
	Render(w, status, title, "public", fields, fallback.Bytes(), refresh)
}
