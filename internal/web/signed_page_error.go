package web

import (
	"html/template"
	"net/http"

	"github.com/openhoo/shoowcase/internal/webui"
)

var signedErrorPage = template.Must(template.New("signed-error").Parse(`<main id="content" tabindex="-1"><h1>{{.Title}}</h1><p>{{.Message}}</p><a href="/">Back to workspace</a></main>`))

func signedDomainError(w http.ResponseWriter, r *http.Request, err error) {
	message, status := publicErrorDetails(err)
	signedPageError(w, r, message, status)
}

// Only browser navigation gets the HTML error page. In particular a raw PUT
// upload keeps its existing HTTP contract even if a client advertises HTML.
func signedPageError(w http.ResponseWriter, r *http.Request, message string, status int) {
	if r.Method == http.MethodGet || r.Method == http.MethodPost {
		w.Header().Add("Vary", "Accept")
		if prefersHTML(r.Header.Get("Accept")) {
			title := "Unable to review this release"
			if r.URL.Path == "/upload" {
				title = "Unable to upload this file"
			}
			webui.Public(w, status, title, "error", signedErrorPage, map[string]any{"Title": title, "Message": message})
			return
		}
	}
	http.Error(w, message, status)
}
