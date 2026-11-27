package web

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"html/template"
	"mime"
	"net/http"
	"strings"

	"github.com/openhoo/shoowcase/internal/auth"
	"github.com/openhoo/shoowcase/internal/routes"
	"github.com/openhoo/shoowcase/internal/sites"
	"github.com/openhoo/shoowcase/internal/webui"
)

type consoleContext struct {
	Actor                sites.Actor
	Identity             auth.Identity
	Enrolled             bool
	Name, WorkspaceLabel string
}

//go:embed console.html
var consoleHTML string
var frame = template.Must(webui.NativeTemplate("console").Parse(consoleHTML))

// MarshalJSON limits the browser context to fields used by the control UI.
func (c consoleContext) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Enrolled             bool
		Name, WorkspaceLabel string
		Actor                sites.Actor
		Identity             struct{ Subject, CSRF string }
	}{c.Enrolled, c.Name, c.WorkspaceLabel, c.Actor, struct{ Subject, CSRF string }{c.Identity.Subject, c.Identity.CSRF}})
}

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
	webui.Render(w, status, title, "console", map[string]any{
		"title": title, "active": active, "view": tmpl.Name(), "context": c, "data": data,
	}, out.Bytes(), "")
}
func consoleError(w http.ResponseWriter, c consoleContext, status int, title, detail string) {
	consoleView(w, c, status, title, "", frame.Lookup("error-page"), map[string]string{"Title": title, "Detail": detail, "CSRF": c.Identity.CSRF})
}
func verified(ctx context.Context, i auth.Identity) context.Context {
	if i.LocalGeneration > 0 {
		return sites.WithLocalSession(ctx, i.LocalGeneration)
	}
	return sites.WithIdentityIssuedAt(ctx, i.IssuedAt)
}

func (a *App) console(w http.ResponseWriter, r *http.Request, route routes.Endpoint) {
	w.Header().Set("Referrer-Policy", "strict-origin")
	if r.Method != "GET" && r.Method != "HEAD" && r.Method != "POST" {
		w.Header().Set("Allow", "GET, HEAD, POST")
		w.WriteHeader(405)
		return
	}
	i, ok := a.auth.Browser(r)
	if !ok {
		if r.URL.Path == "/" && r.Method != "POST" {
			signup := a.config.SignupURL
			if a.auth.LocalWorkspace() != "" {
				signup = ""
				if a.auth.RegistrationOpen() {
					signup = "/auth/register"
				}
			} else if signup == "" {
				signup = "/auth/login?next=/account"
			}
			webui.Public(w, http.StatusOK, "Team sites", "landing", frame.Lookup("landing"), map[string]any{"Signup": signup})
			return
		}
		a.login(w, r)
		return
	}
	a.signedInConsole(w, r, i, route)
}

func (a *App) signedInConsole(w http.ResponseWriter, r *http.Request, i auth.Identity, route routes.Endpoint) {
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
			if r.URL.Path == "/quickstart" {
				dest = "/account?next=quickstart"
			}
		}
		http.Redirect(w, r, dest, http.StatusSeeOther)
		return
	}
	r = r.WithContext(context.WithValue(ctx, whoKey{}, c.Actor))
	if r.Method == "POST" {
		if !a.rate.Allow("form:"+i.Workspace+"\x00"+i.Subject, 120) {
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
			if r.URL.Path == "/admin" {
				// A page of 25 percent-encoded identity-provider subjects plus its review fields.
				limit = 32 << 10
			} else if r.URL.Path == "/sites" {
				// Up to 100 reviewed sites with their draft revision, live revision and release ID.
				limit = 32 << 10
			} else if strings.HasPrefix(r.URL.Path, "/sites/") && strings.HasSuffix(r.URL.Path, "/edit") {
				limit = 2 << 20
			} else if strings.HasPrefix(r.URL.Path, "/sites/") && strings.HasSuffix(r.URL.Path, "/delete") {
				// 100 paths of 200 bytes may be percent-encoded.
				limit = 64 << 10
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
	if route.Page != routes.Console {
		a.siteConsole(w, r, c, route)
		return
	}
	a.membersConsole(w, r, c)
}
