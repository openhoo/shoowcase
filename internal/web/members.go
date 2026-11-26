package web

import (
	"crypto/rand"
	"embed"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/openhoo/shoowcase/internal/sites"
)

//go:embed templates/members*.html
var memberFiles embed.FS

var memberPages = template.Must(template.New("member-pages").Funcs(template.FuncMap{
	"role":            memberRole,
	"shortDate":       func(t time.Time) string { return t.UTC().Format("Jan 2, 2006") },
	"longDate":        func(t time.Time) string { return t.UTC().Format("Jan 2, 2006 at 15:04 UTC") },
	"isoDate":         func(t time.Time) string { return t.UTC().Format(time.RFC3339) },
	"eventName":       memberEventName,
	"maxQuotaSites":   func() int { return sites.MaxSitesPerWorkspace },
	"maxQuotaStorage": func() string { return formatQuotaBytes(sites.MaxWorkspaceManifestBytes) },
	"quotaBytes":      formatQuotaBytes,
	"limitState":      quotaLimitState,
}).ParseFS(memberFiles, "templates/members*.html"))

func memberRole(r string) string {
	switch r {
	case "reader":
		return "Viewer"
	case "editor":
		return "Editor"
	case "publisher":
		return "Publisher"
	case "admin":
		return "Admin"
	case "":
		return "Use sign-in role"
	default:
		return "Unknown role"
	}
}

type memberRow struct {
	sites.Account
	Manage   bool
	Ceilings []string
}

type memberKeyRow struct {
	sites.AgentKey
	State string
}

type memberPage struct {
	LocalAdmin, LocalUser                                 bool
	CSRF, Workspace, Subject, Name, Role, Endpoint        string
	Enrolled                                              bool
	NameValue, Scope, Days, Secret, Err, Notice, Creation string
	Scopes                                                []string
	Keys                                                  []memberKeyRow
	Members                                               []memberRow
	Events                                                []sites.AuditEvent
	Settings                                              sites.WorkspaceSettings
	Groups                                                []sites.Group
	Group                                                 *sites.Group
	GroupMembers                                          []string
	Quota                                                 quotaPage
	Stats                                                 sites.WorkspaceSummary
	Next, View                                            string
	Admin                                                 bool
}

func memberEventName(action string) string {
	switch action {
	case "access.denied":
		return "Access denied"
	case "group_created":
		return "Group created"
	case "group_updated":
		return "Group updated"
	case "group_deleted":
		return "Group deleted"
	case "group_member_added":
		return "Member added to group"
	case "group_member_removed":
		return "Member removed from group"
	case "local_account_created":
		return "Password account created"
	case "local_password_changed":
		return "Password changed"
	case "member_provisioned":
		return "Member provisioned"
	case "workspace_quota_changed":
		return "Workspace limits changed"
	case "group_quota_changed":
		return "Group limits changed"
	case "site_group_changed":
		return "Site group changed"
	case "workspace_access_changed":
		return "Workspace access mode changed"
	case "workspace_renamed":
		return "Workspace renamed"
	case "create_site":
		return "Site created"
	case "upload_files":
		return "Draft files changed"
	case "publish_site":
		return "Site published"
	case "set_site_access":
		return "Site audience changed"
	case "rollback_site":
		return "Release restored"
	case "auth.login":
		return "Browser sign-in"
	case "auth.logout":
		return "Browser sign-out"
	case "account_created":
		return "Account created"
	case "key_created":
		return "Agent key created"
	case "key_revoked":
		return "Agent key revoked"
	case "account_suspended":
		return "Member suspended"
	case "account_reactivated":
		return "Member restored"
	case "account_role_changed":
		return "Role limit changed"
	case "identity_role_updated":
		return "Sign-in role changed"
	default:
		return "Workspace access changed"
	}
}

// The identity's verified workspace and role are passed to storage. The store
// applies the member's current role limit for each operation, including writes.
func (a *App) membersConsole(w http.ResponseWriter, r *http.Request, c consoleContext) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, HEAD, POST")
		consoleError(w, c, http.StatusMethodNotAllowed, "Method not allowed", "Open this page in your browser.")
		return
	}
	p := memberPage{LocalAdmin: a.auth.LocalWorkspace() != "" && a.auth.LocalWorkspace() == c.Identity.Workspace, LocalUser: c.Identity.LocalGeneration > 0, CSRF: c.Identity.CSRF, Workspace: c.WorkspaceLabel, Subject: c.Actor.Subject, Name: c.Name, Role: c.Actor.Role, Enrolled: c.Enrolled, Endpoint: a.config.ControlURL + "/mcp"}
	switch r.URL.Path {
	case "/account":
		a.memberAccount(w, r, c, p)
	case "/keys":
		a.memberKeys(w, r, c, p)
	case "/admin":
		a.memberAdmin(w, r, c, p)
	default:
		consoleError(w, c, 404, "Page not found", "Check the address and try again.")
	}
}

func memberFailure(err error) (int, string, string) {
	switch errorCode(err) {
	case "invalid_argument":
		return 400, "Check the details", "Check the values you entered and try again."
	case "forbidden", "unauthorized":
		return 403, "Access denied", "You don’t have permission to do this in this workspace."
	case "not_found":
		return 404, "Item not found", "The item is no longer available in this workspace."
	case "conflict":
		return 409, "This item changed", "Reload the page to review its current settings before trying again."
	case "quota_exceeded":
		return 409, "Limit reached", "This workspace has reached its limit for this action."
	default:
		return 503, "Workspace unavailable", "The workspace couldn’t complete your request. Please try again."
	}
}

func memberError(w http.ResponseWriter, c consoleContext, err error) {
	n, title, detail := memberFailure(err)
	consoleError(w, c, n, title, detail)
}

func validMemberLabel(s string, max int) bool {
	return s != "" && utf8.ValidString(s) && utf8.RuneCountInString(s) <= max && !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) })
}

func (a *App) memberAccount(w http.ResponseWriter, r *http.Request, c consoleContext, p memberPage) {
	status := 200
	if r.Method == http.MethodPost {
		if r.PostForm.Get("action") == "change-password" {
			if !p.LocalUser {
				consoleError(w, c, 403, "Password managed elsewhere", "Use your work identity provider to change your password.")
				return
			}
			if !a.rate.Allow("password-change:"+c.Identity.Subject, 10) {
				w.Header().Set("Retry-After", "60")
				consoleError(w, c, 429, "Too many attempts", "Wait a minute before trying again.")
				return
			}
			err := a.store.ChangeLocalPassword(r.Context(), actor(c.Identity), r.PostForm.Get("current_password"), r.PostForm.Get("new_password"))
			if err != nil {
				switch errorCode(err) {
				case "unauthorized":
					p.Err = "The current password is incorrect."
					status = 400
				case "invalid_argument":
					p.Err = "Use a different password of at least 12 characters (up to 1024 bytes)."
					status = 400
				default:
					memberError(w, c, err)
					return
				}
			} else {
				consoleView(w, consoleContext{}, 200, "Password changed", "", frame.Lookup("password-changed"), nil)
				return
			}
			consoleView(w, c, status, "Your account", "account", memberPages.Lookup("account"), p)
			return
		}
		if r.PostForm.Get("action") != "enroll" {
			consoleError(w, c, 400, "Unknown action", "Reopen your account page and try again.")
			return
		}
		name := strings.TrimSpace(r.PostForm.Get("display_name"))
		p.NameValue = name
		if name == "" {
			name = c.Identity.Subject
			if !validMemberLabel(name, 80) {
				name = "Workspace member"
			}
		}
		if !validMemberLabel(name, 80) {
			p.Err = "Enter a display name of up to 80 characters, without line breaks."
			status = 400
		} else if _, err := a.store.EnrollAccount(r.Context(), actor(c.Identity), name); err != nil {
			memberError(w, c, err)
			return
		} else {
			http.Redirect(w, r, "/sites", http.StatusSeeOther)
			return
		}
	}
	title := "Your account"
	if !c.Enrolled {
		title = "Create your account"
	}
	consoleView(w, c, status, title, "account", memberPages.Lookup("account"), p)
}

func memberScopes(role string) []string {
	switch role {
	case "admin", "publisher":
		return []string{"reader", "editor", "publisher"}
	case "editor":
		return []string{"reader", "editor"}
	case "reader":
		return []string{"reader"}
	default:
		return nil
	}
}

// Keep a previously configured higher limit visible after an IdP downgrade.
// The store still caps effective authority at the current identity role.
func memberCeilings(m sites.Account) []string {
	allowed := memberScopes(m.IdentityRole)
	for _, v := range allowed {
		if m.Ceiling == v {
			return allowed
		}
	}
	if m.Ceiling != "" {
		return append(allowed, m.Ceiling)
	}
	return allowed
}

func memberKeyRows(keys []sites.AgentKey) []memberKeyRow {
	now := time.Now()
	rows := make([]memberKeyRow, 0, len(keys))
	for _, k := range keys {
		state := "Active"
		if k.Revoked {
			state = "Revoked"
		} else if !k.ExpiresAt.After(now) {
			state = "Expired"
		}
		rows = append(rows, memberKeyRow{k, state})
	}
	return rows
}

func memberNext(path, view, cursor string) string {
	if cursor == "" {
		return ""
	}
	q := url.Values{"after": {cursor}}
	if view != "" {
		q.Set("view", view)
	}
	return path + "?" + q.Encode()
}

// Spending the signed form nonce before issuance guarantees that reloading a
// receipt, double-clicking, or replaying the POST at another replica cannot
// mint a second active key. If issuance fails, the user opens a fresh form.
func (a *App) spendKeyForm(w http.ResponseWriter, r *http.Request, c consoleContext) bool {
	t, ok := a.ticketValue(r.PostForm.Get("creation_token"), "create-key")
	if !ok || t.Who != c.Actor || t.Nonce == "" {
		consoleError(w, c, 409, "Key form expired", "Open Agent keys to get a new form.")
		return false
	}
	spent, err := a.store.Consume(r.Context(), "key-form:"+t.Nonce, time.Unix(t.Expires, 0))
	if err != nil {
		memberError(w, c, err)
		return false
	}
	if !spent {
		consoleError(w, c, 409, "Form already submitted", "Open Agent keys to check your existing keys. Revoke any key whose secret you did not save.")
		return false
	}
	return true
}

func (a *App) memberKeys(w http.ResponseWriter, r *http.Request, c consoleContext, p memberPage) {
	p.Scopes = memberScopes(c.Actor.Role)
	p.Scope = "reader"
	p.Days = "30"
	status := 200
	who := actor(c.Identity)
	if r.Method == http.MethodPost {
		switch r.PostForm.Get("action") {
		case "create":
			p.NameValue = strings.TrimSpace(r.PostForm.Get("name"))
			p.Scope = r.PostForm.Get("scope")
			p.Days = r.PostForm.Get("days")
			days, e := strconv.Atoi(p.Days)
			if !validMemberLabel(p.NameValue, 80) {
				p.Err = "Give the key a name of 1 to 80 characters, without line breaks."
			} else if e != nil || days < 1 || days > 90 {
				p.Err = "Choose an expiry between 1 and 90 days."
			} else {
				if !a.spendKeyForm(w, r, c) {
					return
				}
				_, secret, err := a.store.CreateKey(r.Context(), who, p.NameValue, p.Scope, days)
				if err != nil {
					memberError(w, c, err)
					return
				}
				p.Secret = secret
				// The key has already been created. Render its one-time receipt
				// without another database read that could fail and hide the secret.
				consoleView(w, c, 200, "Connect your agent", "keys", memberPages.Lookup("key-receipt"), p)
				return
			}
			status = 400
		case "revoke":
			if !a.memberRevoke(w, r, c) {
				return
			}
			http.Redirect(w, r, "/keys?done=revoked", 303)
			return
		default:
			consoleError(w, c, 400, "Unknown action", "Reopen the agent keys page and try again.")
			return
		}
	} else if r.URL.Query().Get("done") == "revoked" {
		p.Notice = "Key revoked."
	}
	keys, next, err := a.store.ListKeys(r.Context(), who, false, r.URL.Query().Get("after"))
	if err != nil {
		memberError(w, c, err)
		return
	}
	p.Keys = memberKeyRows(keys)
	p.Next = memberNext("/keys", "", next)
	p.Creation = a.sign(ticket{Kind: "create-key", Who: c.Actor, Nonce: rand.Text(), Expires: time.Now().Add(15 * time.Minute).Unix()})
	consoleView(w, c, status, "Agent keys", "keys", memberPages.Lookup("keys"), p)
}

func (a *App) memberRevoke(w http.ResponseWriter, r *http.Request, c consoleContext) bool {
	if r.PostForm.Get("confirm") != "yes" {
		consoleError(w, c, 400, "Confirm revocation", "Select the confirmation checkbox to revoke this key.")
		return false
	}
	if err := a.store.RevokeKey(r.Context(), actor(c.Identity), r.PostForm.Get("key_id")); err != nil {
		memberError(w, c, err)
		return false
	}
	return true
}

func (a *App) memberAdmin(w http.ResponseWriter, r *http.Request, c consoleContext, p memberPage) {
	if c.Actor.Role != "admin" {
		a.auditDenied(r.Context(), c.Actor, "admin")
		consoleError(w, c, 403, "Access denied", "Only workspace admins can open workspace administration.")
		return
	}
	p.Admin = true
	who := actor(c.Identity)
	ctx := r.Context()
	if r.Method == http.MethodPost {
		action := r.PostForm.Get("action")
		if action == "create-local" {
			if !p.LocalAdmin {
				consoleError(w, c, 403, "Local accounts are disabled", "Contact your instance operator.")
				return
			}
			_, err := a.store.CreateLocalAccount(ctx, who, r.PostForm.Get("username"), r.PostForm.Get("password"), r.PostForm.Get("role"))
			if err != nil {
				if e, ok := err.(*sites.Error); ok && (e.Code == "conflict" || e.Code == "invalid_argument") {
					n, _, _ := memberFailure(e)
					consoleError(w, c, n, "Account could not be created", e.Message)
					return
				}
				memberError(w, c, err)
				return
			}
			http.Redirect(w, r, "/admin?done=create-local", 303)
			return
		}
		if a.quotaMutation(w, r, c, action) || a.tenantMutation(w, r, c, action) {
			return
		}
		if action == "revoke" {
			if !a.memberRevoke(w, r, c) {
				return
			}
			http.Redirect(w, r, "/admin?view=keys&done=revoked", 303)
			return
		}
		if action != "suspend" && action != "restore" && action != "role" {
			consoleError(w, c, 400, "Unknown action", "Reopen workspace administration and try again.")
			return
		}
		expected, ok := r.PostForm["expected_ceiling"]
		before := r.PostForm.Get("expected_disabled")
		if !ok || len(expected) != 1 || (before != "false" && before != "true") {
			consoleError(w, c, 400, "Missing member settings", "Reload workspace administration and try again.")
			return
		}
		ceiling, disabled := expected[0], before == "true"
		switch action {
		case "role":
			value, ok := r.PostForm["ceiling"]
			if !ok || len(value) != 1 {
				consoleError(w, c, 400, "Choose a role limit", "Select a role limit for this member.")
				return
			}
			ceiling = value[0]
		case "suspend":
			if r.PostForm.Get("confirm") != "yes" {
				consoleError(w, c, 400, "Confirm suspension", "Select the confirmation checkbox to suspend this member and revoke their keys.")
				return
			}
			disabled = true
		case "restore":
			disabled = false
		}
		if err := a.store.SetMember(ctx, who, r.PostForm.Get("subject"), ceiling, disabled, expected[0], before == "true"); err != nil {
			memberError(w, c, err)
			return
		}
		http.Redirect(w, r, "/admin?done="+action, 303)
		return
	}
	p.Notice = map[string]string{"create-local": "Password account created. The member can sign in now.", "workspace-quota": "Workspace limits saved.", "group-quota": "Group limits saved.", "site-group": "Site assignment saved.", "provision": "Member added.", "workspace-save": "Workspace settings saved.", "group-create": "Group created.", "group-update": "Group updated.", "group-delete": "Group deleted.", "group-add-member": "Member added to group.", "group-remove-member": "Member removed from group.", "revoked": "Key revoked.", "suspend": "Member suspended. Their keys have been revoked.", "restore": "Member restored. Previously revoked keys remain inactive.", "role": "Role limit saved."}[r.URL.Query().Get("done")]
	var err error
	p.Stats, err = a.store.WorkspaceStats(ctx, who)
	if err != nil {
		memberError(w, c, err)
		return
	}
	p.View = r.URL.Query().Get("view")
	after := r.URL.Query().Get("after")
	switch p.View {
	case "", "members":
		p.View = "members"
		entries, next, err := a.store.ListAccounts(ctx, who, after)
		if err != nil {
			memberError(w, c, err)
			return
		}
		for _, v := range entries {
			p.Members = append(p.Members, memberRow{v, v.Subject != who.Subject, memberCeilings(v)})
		}
		p.Next = memberNext("/admin", "members", next)
	case "keys":
		keys, next, err := a.store.ListKeys(ctx, who, true, after)
		if err != nil {
			memberError(w, c, err)
			return
		}
		p.Keys = memberKeyRows(keys)
		p.Next = memberNext("/admin", "keys", next)
	case "quotas":
		if !a.quotaView(w, r, c, &p) {
			return
		}
	case "settings":
		p.Settings, err = a.store.WorkspaceSettings(ctx, who)
		if err != nil {
			memberError(w, c, err)
			return
		}
	case "groups":
		if !a.groupView(w, r, c, &p) {
			return
		}
	case "activity":
		cursor, ok := auditCursor(r.URL.Query(), "after")
		if !ok {
			consoleError(w, c, 400, "Invalid page", "Open Activity to see the most recent changes.")
			return
		}
		var more bool
		p.Events, more, err = a.store.AuditPage(ctx, who, cursor, false, 25)
		if more {
			p.Next = memberNext("/admin", "activity", strconv.FormatInt(p.Events[len(p.Events)-1].Sequence, 10))
		}
		if err != nil {
			memberError(w, c, err)
			return
		}
	default:
		consoleError(w, c, 404, "View not found", "Open workspace administration to choose a view.")
		return
	}
	consoleView(w, c, 200, "Workspace admin", "admin", memberPages.Lookup("admin"), p)
}
