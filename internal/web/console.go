package web

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"html/template"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/openhoo/shoowcase/internal/auth"
	"github.com/openhoo/shoowcase/internal/sites"
)

type consoleContext struct {
	Actor                sites.Actor
	Identity             auth.Identity
	Enrolled             bool
	Name, WorkspaceLabel string
}

//go:embed console.html
var consoleHTML string
var frame = template.Must(template.New("console").Parse(consoleHTML))

//go:embed console.js
var consoleJS string

func consoleView(w http.ResponseWriter, c consoleContext, status int, title, active string, tmpl *template.Template, data any) {
	var out bytes.Buffer
	shell := struct {
		C             consoleContext
		Title, Active string
	}{c, title, active}
	if err := frame.ExecuteTemplate(&out, "start", shell); err != nil {
		http.Error(w, "Workspace unavailable", 500)
		return
	}
	if err := tmpl.Execute(&out, data); err != nil {
		http.Error(w, "Workspace unavailable", 500)
		return
	}
	if err := frame.ExecuteTemplate(&out, "end", shell); err != nil {
		http.Error(w, "Workspace unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; script-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	w.WriteHeader(status)
	_, _ = out.WriteTo(w)
}
func consoleError(w http.ResponseWriter, c consoleContext, status int, title, detail string) {
	consoleView(w, c, status, title, "", frame.Lookup("error-page"), map[string]string{"Title": title, "Detail": detail, "CSRF": c.Identity.CSRF})
}
func verified(ctx context.Context, i auth.Identity) context.Context {
	return sites.WithIdentityIssuedAt(ctx, i.IssuedAt)
}

func (a *App) console(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "strict-origin")
	if r.Method != "GET" && r.Method != "HEAD" && r.Method != "POST" {
		w.Header().Set("Allow", "GET, HEAD, POST")
		w.WriteHeader(405)
		return
	}
	i, ok := a.auth.Browser(r)
	if !ok {
		if r.URL.Path == "/" && r.Method != "POST" {
			render(w, frame.Lookup("landing"), map[string]string{"Signup": a.config.SignupURL})
			return
		}
		a.login(w, r)
		return
	}
	a.signedInConsole(w, r, i)
}

func (a *App) signedInConsole(w http.ResponseWriter, r *http.Request, i auth.Identity) {
	ctx := verified(r.Context(), i)
	c := consoleContext{Actor: actor(i), Identity: i, WorkspaceLabel: i.Workspace}
	m, enrolled, err := a.store.AccountFor(ctx, c.Actor)
	if err != nil {
		if enrolled && m.Disabled {
			consoleError(w, c, 403, "Account suspended", "Contact a workspace admin to restore access.")
			return
		}
		memberError(w, c, err)
		return
	}
	if m.WorkspaceDisplayName != "" {
		c.WorkspaceLabel = m.WorkspaceDisplayName
	}
	if !enrolled && m.RequiresProvisioning(ctx, c.Actor) {
		consoleView(w, c, http.StatusForbidden, "Workspace access required", "", frame.Lookup("provisioning-required"), c)
		return
	}
	if enrolled {
		c.Enrolled = true
		c.Actor.Role = m.Role
		c.Name = m.DisplayName
	}
	if r.URL.Path == "/" || !enrolled && r.URL.Path != "/account" {
		dest := "/sites"
		if !enrolled {
			dest = "/account"
		}
		http.Redirect(w, r, dest, http.StatusSeeOther)
		return
	}
	r = r.WithContext(context.WithValue(ctx, whoKey{}, c.Actor))
	if r.Method == "POST" {
		if !a.rate.allow("form:"+i.Workspace+"\x00"+i.Subject, 120) {
			w.Header().Set("Retry-After", "60")
			consoleError(w, c, 429, "Too many changes", "Wait a minute and try again.")
			return
		}
		media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		switch media {
		case "multipart/form-data":
			if !strings.HasPrefix(r.URL.Path, "/sites/") || !(strings.HasSuffix(r.URL.Path, "/upload") || strings.HasSuffix(r.URL.Path, "/import")) {
				consoleError(w, c, 415, "Unsupported form", "Use the file upload or ZIP import page.")
				return
			}
			select {
			case a.archiveGate <- struct{}{}:
				defer func() { <-a.archiveGate }()
			default:
				w.Header().Set("Retry-After", "2")
				consoleError(w, c, 429, "Uploads are busy", "Try again in a moment.")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
			err = r.ParseMultipartForm(10 << 20)
			if r.MultipartForm != nil {
				defer r.MultipartForm.RemoveAll()
			}
		case "application/x-www-form-urlencoded":
			limit := int64(16 << 10)
			if strings.HasPrefix(r.URL.Path, "/sites/") && strings.HasSuffix(r.URL.Path, "/edit") {
				limit = 2 << 20
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			err = r.ParseForm()
		default:
			consoleError(w, c, 415, "Unsupported form", "Open the form and try again.")
			return
		}
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				consoleError(w, c, 413, "File too large", "Choose a smaller file and try again.")
			} else {
				consoleError(w, c, 400, "Invalid form", "Reopen this page and submit the form again.")
			}
			return
		}
		if len(r.PostForm["csrf"]) != 1 || !a.auth.ValidCSRF(r, r.PostForm.Get("csrf")) {
			consoleError(w, c, 403, "Form expired", "Reload the page and try again.")
			return
		}
	}
	if r.URL.Path == "/sites" || strings.HasPrefix(r.URL.Path, "/sites/") {
		a.siteConsole(w, r, c)
		return
	}
	a.membersConsole(w, r, c)
}

func (a *App) controlScript(w http.ResponseWriter, r *http.Request) {
	if !readMethod(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	if r.Method == "GET" {
		_, _ = io.WriteString(w, consoleJS)
	}
}
