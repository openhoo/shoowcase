package auth

import (
	"context"
	_ "embed"
	"errors"
	"html/template"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/openhoo/shoowcase/internal/sites"
	"github.com/openhoo/shoowcase/internal/webui"
)

// LocalAccounts stores credentials separately from identity-provider assertions.
type LocalAccounts interface {
	AuthenticateLocal(context.Context, string, string, string) (sites.Actor, int64, error)
	RegisterLocalAccount(context.Context, string, string, string) (sites.Account, error)
	ValidateLocalSession(context.Context, sites.Actor, int64) error
}

func (s *Service) LocalWorkspace() string { return s.cfg.LocalWorkspace }
func (s *Service) RegistrationOpen() bool { return s.cfg.PublicRegistration }
func reservedSubject(subject string) bool {
	return strings.HasPrefix(subject, sites.LocalSubjectPrefix)
}

func (s *Service) identityActive(ctx context.Context, i Identity) bool {
	if i.LocalGeneration == 0 {
		return !reservedSubject(i.Subject)
	}
	return reservedSubject(i.Subject) && s.cfg.LocalWorkspace != "" && i.Workspace == s.cfg.LocalWorkspace &&
		s.cfg.LocalAccounts.ValidateLocalSession(ctx, sites.Actor{Workspace: i.Workspace, Subject: i.Subject}, i.LocalGeneration) == nil
}

//go:embed password.html
var passwordHTML string
var passwordContinue = template.Must(template.New("continue").Parse(`<main><h1>Signed in</h1><a href="{{.Next}}">Continue to your site</a></main>`))

var passwordPage = template.Must(template.New("password").Parse(passwordHTML))

type passwordIntent struct {
	Dest           destination
	Nonce, Binding string
	Register       bool
}

func (s *Service) passwordForm(w http.ResponseWriter, r *http.Request, d destination, register bool, status int, message string) {
	binding, err := r.Cookie(s.name("password-form"))
	if err != nil || len(binding.Value) != 43 {
		binding = &http.Cookie{Value: random()}
	}
	s.cookie(w, "password-form", binding.Value, int(authTTL.Seconds()))
	intent, err := s.seal("password-intent", passwordIntent{d, random(), hash(binding.Value), register})
	ticket, e := s.seal("destination", d)
	if err != nil || e != nil {
		deny(w, 500)
		return
	}
	q := url.Values{"ticket": {ticket}}
	back := "/auth/login?" + q.Encode()
	signup := "/auth/register?" + q.Encode()
	q.Set("provider", "oidc")
	// Browsers send Origin: null on form POSTs from a no-referrer page.
	// Send only the origin (never a flow ticket) so same-origin CSRF checks work.
	w.Header().Set("Referrer-Policy", "strict-origin")
	title := "Sign in"
	if register {
		title = "Create an account"
	}
	webui.Public(w, status, title, "password", passwordPage, map[string]any{"Register": register, "Open": s.cfg.PublicRegistration, "Flow": intent, "Message": message, "OIDC": "/auth/login?" + q.Encode(), "Signup": signup, "Back": back})
}

func (s *Service) passwordRoute(w http.ResponseWriter, r *http.Request) {
	register := r.URL.Path == "/auth/register"
	if !s.isControl(r.Host) || s.cfg.LocalWorkspace == "" || register && !s.cfg.PublicRegistration {
		deny(w, 404)
		return
	}
	if register && r.Method == http.MethodGet {
		d := destination{Host: s.control.Host, Path: "/sites", Exp: s.now().Add(authTTL).Unix()}
		if ticket := r.URL.Query().Get("ticket"); ticket != "" && (!s.open("destination", ticket, &d) || !s.destOK(d)) {
			deny(w, 400)
			return
		}
		if _, ok := s.liveSession(r); ok {
			topRedirect(w, r, "/account")
			return
		}
		s.passwordForm(w, r, d, true, 200, "")
		return
	}
	if r.Method != http.MethodPost {
		allow := "POST"
		if register {
			allow = "GET, POST"
		}
		w.Header().Set("Allow", allow)
		deny(w, 405)
		return
	}
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/x-www-form-urlencoded" {
		deny(w, 415)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if r.ParseForm() != nil || len(r.PostForm["flow"]) != 1 || len(r.PostForm["username"]) != 1 || len(r.PostForm["password"]) != 1 {
		deny(w, 400)
		return
	}
	var p passwordIntent
	b, err := r.Cookie(s.name("password-form"))
	if err != nil || !s.open("password-intent", r.PostForm.Get("flow"), &p) || !s.destOK(p.Dest) || p.Register != register ||
		!s.validForm(r, Identity{CSRF: p.Binding}, hash(b.Value)) {
		http.Error(w, "This form expired. Open the sign-in page again.", 403)
		return
	}
	username, password := r.PostForm.Get("username"), r.PostForm.Get("password")
	// Bound guesses per username and total work per replica. Ignore forwarding
	// headers: an installation may use more than one ingress hop.
	if !s.passwordRate.Allow("all", 180) || !s.passwordRate.Allow("user:"+hash(strings.ToLower(strings.TrimSpace(username))), 10) {
		w.Header().Set("Retry-After", "60")
		deny(w, 429)
		return
	}
	if !s.consume(r.Context(), "password-intent", p.Nonce, p.Dest.Exp) {
		deny(w, 403)
		return
	}
	var a sites.Actor
	var generation int64
	if register {
		var m sites.Account
		m, err = s.cfg.LocalAccounts.RegisterLocalAccount(r.Context(), s.cfg.LocalWorkspace, username, password)
		if err == nil {
			a = sites.Actor{Subject: m.Subject, Workspace: m.Workspace, Role: m.IdentityRole}
			generation = 1
		}
	} else {
		a, generation, err = s.cfg.LocalAccounts.AuthenticateLocal(r.Context(), s.cfg.LocalWorkspace, username, password)
	}
	if err != nil {
		status, message := 503, "Sign-in is temporarily unavailable. Try again shortly."
		var problem *sites.Error
		if errors.As(err, &problem) {
			switch problem.Code {
			case "unauthorized", "forbidden":
				status, message = 401, "The username or password is incorrect."
				if register {
					status, message = 403, "Registration is closed for this workspace."
				}
			case "invalid_argument", "conflict", "quota_exceeded":
				status, message = 400, "Could not create this account. Check the username and password or contact your administrator."
			case "rate_limited":
				status, message = 429, "Too many attempts. Please wait a minute."
				w.Header().Set("Retry-After", "60")
			}
		}
		s.passwordForm(w, r, p.Dest, register, status, message)
		return
	}
	role, ok := map[string]Role{"reader": Viewer, "editor": Editor, "publisher": Publisher}[a.Role]
	if !ok || a.Workspace != s.cfg.LocalWorkspace || !reservedSubject(a.Subject) || generation < 1 {
		deny(w, 403)
		return
	}
	i := Identity{Subject: a.Subject, Workspace: a.Workspace, Role: role, IssuedAt: s.now().Unix(), LocalGeneration: generation}
	s.completeLogin(w, r, p.Dest, i, s.now().Add(sessionTTL).Unix())
}
