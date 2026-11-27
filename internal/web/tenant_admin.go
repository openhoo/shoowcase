package web

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/openhoo/shoowcase/internal/auth"
	"github.com/openhoo/shoowcase/internal/sites"
)

// Keep denied actions observable without allowing a single actor to flood a
// tenant's permanent journal. Limits are per minute per process.
func (a *App) auditDenied(ctx context.Context, who sites.Actor, op string) {
	if !a.rate.Allow("audit-denied-user:"+who.Workspace+"\x00"+who.Subject, 6) || !a.rate.Allow("audit-denied-workspace:"+who.Workspace, 60) {
		return
	}
	if err := a.store.RecordDenied(ctx, who, op); err != nil {
		slog.Warn("could not persist authorization denial")
	}
}

func adminLocation(view, group, done string) string {
	q := url.Values{"view": {view}}
	if group != "" {
		q.Set("group", group)
	}
	if done != "" {
		q.Set("done", done)
	}
	return "/admin?" + q.Encode()
}

func (a *App) tenantMutation(w http.ResponseWriter, r *http.Request, c consoleContext, action string) bool {
	if a.adminSelection(w, r, c, action) {
		return true
	}
	f := r.PostForm
	who, ctx := actor(c.Identity), r.Context()
	group, view := f.Get("group"), "groups"
	version, parseErr := strconv.ParseInt(f.Get("version"), 10, 64)
	var err error
	switch action {
	case "group-update", "group-delete", "group-add-member", "workspace-save":
		if parseErr != nil || version < 1 {
			consoleError(w, c, 400, "Missing version", "Reload this form before making changes.")
			return true
		}
	}
	switch action {
	case "provision":
		view = "members"
		err = a.store.ProvisionMember(ctx, who, f.Get("subject"), f.Get("display_name"))
	case "workspace-save":
		view = "settings"
		err = a.store.SetWorkspaceSettings(ctx, who, f.Get("display_name"), f.Get("mode"), version)
	case "group-create":
		var g sites.Group
		g, err = a.store.CreateGroup(ctx, who, f.Get("name"), f.Get("role"))
		group = g.ID
	case "group-update":
		err = a.store.UpdateGroup(ctx, who, group, f.Get("name"), f.Get("role"), version)
	case "group-delete":
		if f.Get("confirm") != "yes" {
			consoleError(w, c, 400, "Confirm deletion", "Confirm that this group and its grants should be removed.")
			return true
		}
		err = a.store.DeleteGroup(ctx, who, group, version)
		group = ""
	case "group-add-member":
		err = a.store.SetGroupMember(ctx, who, group, f.Get("subject"), true, version)
	default:
		return false
	}
	if err != nil {
		if e, ok := err.(*sites.Error); action == "group-delete" && ok && e.Code == "conflict" {
			quotaFormError(w, c, http.StatusConflict, "Group could not be deleted", e.Message)
			return true
		}
		memberError(w, c, err)
		return true
	}
	http.Redirect(w, r, adminLocation(view, group, action), http.StatusSeeOther)
	return true
}

func (a *App) groupView(w http.ResponseWriter, r *http.Request, c consoleContext, p *memberPage) bool {
	ctx, who := r.Context(), actor(c.Identity)
	var err error
	p.Settings, err = a.store.WorkspaceSettings(ctx, who)
	if err == nil {
		p.Groups, err = a.store.ListGroups(ctx, who)
	}
	if err != nil {
		memberError(w, c, err)
		return false
	}
	if selected := r.URL.Query().Get("group"); selected != "" {
		for i := range p.Groups {
			if p.Groups[i].ID == selected {
				p.Group = &p.Groups[i]
				break
			}
		}
		if p.Group == nil {
			consoleError(w, c, 404, "Group not found", "Choose a group in this workspace.")
			return false
		}
		p.GroupMembers, err = a.store.GroupMembers(ctx, who, selected)
		if err != nil {
			memberError(w, c, err)
			return false
		}
		pageGroupRoster(p, r.URL.Query().Get("after"))
		return true
	}
	d, ok := parseAdminDirectory(w, r, c, false)
	if !ok {
		return false
	}
	p.Directory = d
	p.Groups = p.Directory.groups(p.Groups)
	return true
}

func pageGroupRoster(p *memberPage, after string) {
	const pageSize = 25
	all := p.GroupMembers
	p.Directory.Total = len(all)
	start := 0
	if after != "" {
		start = slices.Index(all, after) + 1
	}
	end := min(start+pageSize, len(all))
	urlFor := func(cursor string) string {
		q := url.Values{"group": {p.Group.ID}, "view": {"groups"}}
		if cursor != "" {
			q.Set("after", cursor)
		}
		return "/admin?" + q.Encode()
	}
	if end < len(all) {
		p.Directory.Next = urlFor(all[end-1])
	}
	if start > 0 {
		prev := ""
		if start > pageSize {
			prev = all[start-pageSize-1]
		}
		p.Directory.Prev = urlFor(prev)
	}
	p.GroupMembers = all[start:end]
}

func auditCursor(q url.Values, name string) (int64, bool) {
	if !q.Has(name) {
		return 0, true
	}
	if len(q[name]) != 1 {
		return 0, false
	}
	v, err := strconv.ParseInt(q.Get(name), 10, 64)
	return v, err == nil && v >= 0
}

// The collector supplies an IdP access token; the same read-only endpoint is
// available to a signed-in administrator. Never accept a tenant selector.
func (a *App) exportAudit(w http.ResponseWriter, r *http.Request) {
	if !readMethod(w, r) {
		return
	}
	var i auth.Identity
	if len(r.Header.Values("Authorization")) > 0 {
		var err error
		i, err = a.auth.Bearer(r)
		if err != nil {
			a.auth.Challenge(w)
			http.Error(w, "Authentication required", 401)
			return
		}
	} else {
		var ok bool
		i, ok = a.auth.Browser(r)
		if !ok {
			http.Error(w, "Authentication required", 401)
			return
		}
	}
	ctx := verified(r.Context(), i)
	who, err := a.store.LimitActor(ctx, actor(i))
	if err != nil {
		if errorCode(err) == "forbidden" {
			a.auditDenied(ctx, actor(i), "audit")
		}
		writeError(w, err)
		return
	}
	q, parseErr := url.ParseQuery(r.URL.RawQuery)
	cursor, ok := auditCursor(q, "after")
	if parseErr != nil || !ok || len(q) > 0 && (len(q) != 1 || !q.Has("after")) {
		http.Error(w, "Expected an optional nonnegative after checkpoint", 400)
		return
	}
	events, more, err := a.store.AuditPage(ctx, who, cursor, true, 200)
	if err != nil {
		if errorCode(err) == "forbidden" {
			a.auditDenied(ctx, who, "audit")
		}
		writeError(w, err)
		return
	}
	next := cursor
	if len(events) > 0 {
		next = events[len(events)-1].Sequence
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodHead {
		_ = json.NewEncoder(w).Encode(struct {
			Events []sites.AuditEvent `json:"events"`
			Next   int64              `json:"next_after"`
			More   bool               `json:"has_more"`
		}{events, next, more})
	}
}
