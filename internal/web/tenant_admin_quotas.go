package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/openhoo/shoowcase/internal/sites"
)

type quotaBudgetForm struct {
	sites.QuotaBudget
	Workspace                           bool
	CSRF, SiteLimit, StorageLimit, Unit string
	Effective                           sites.QuotaLimits
}

type quotaSiteRow struct {
	sites.QuotaSite
	GroupName string
}

type quotaPage struct {
	Workspace  quotaBudgetForm
	Groups     []quotaBudgetForm
	Unassigned sites.QuotaUsage
	Sites      []quotaSiteRow
	Selected   *quotaSiteRow
}

var quotaUnits = []struct {
	name string
	size int64
}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"B", 1}}

// The display may round a byte count, but an editor must never round-trip a
// saved limit into a different value. Pick the largest exact input unit.
func quotaStorageInput(n int64) (string, string) {
	if n < 0 {
		return "", "MiB"
	}
	if n == 0 {
		return "0", "MiB"
	}
	for _, u := range quotaUnits {
		if u.size > 1 && n%u.size == 0 {
			return strconv.FormatInt(n/u.size, 10), u.name
		}
	}
	return strconv.FormatInt(n, 10), "B"
}

func formatQuotaBytes(n int64) string {
	for _, u := range quotaUnits {
		if n >= u.size && u.size > 1 {
			if n%u.size == 0 {
				return fmt.Sprintf("%d %s", n/u.size, u.name)
			}
			return fmt.Sprintf("%.1f %s (%d B)", float64(n)/float64(u.size), u.name, n)
		}
	}
	return fmt.Sprintf("%d B", n)
}

func quotaLimitState(used, limit int64) string {
	switch {
	case used > limit:
		return "Over limit"
	case used == limit:
		return "At limit"
	default:
		return ""
	}
}

func budgetForm(b sites.QuotaBudget, ceiling sites.QuotaLimits, csrf string, workspace bool) quotaBudgetForm {
	f := quotaBudgetForm{QuotaBudget: b, CSRF: csrf, Workspace: workspace, Effective: ceiling}
	if b.Limits.Sites >= 0 {
		f.SiteLimit = strconv.FormatInt(b.Limits.Sites, 10)
		f.Effective.Sites = min(ceiling.Sites, b.Limits.Sites)
	}
	if b.Limits.Bytes >= 0 {
		f.Effective.Bytes = min(ceiling.Bytes, b.Limits.Bytes)
	}
	f.StorageLimit, f.Unit = quotaStorageInput(b.Limits.Bytes)
	return f
}

func (a *App) quotaView(w http.ResponseWriter, r *http.Request, c consoleContext, p *memberPage) bool {
	data, err := a.store.Quotas(r.Context(), actor(c.Identity))
	if err != nil {
		memberError(w, c, err)
		return false
	}
	q := &p.Quota
	q.Workspace = budgetForm(data.Workspace, data.Workspace.Limits, p.CSRF, true)
	names := map[string]string{"": "Unassigned"}
	for _, g := range data.Groups {
		names[g.ID] = g.Name
		q.Groups = append(q.Groups, budgetForm(g, data.Workspace.Limits, p.CSRF, false))
	}
	selected := r.URL.Query().Get("site")
	for _, site := range data.Sites {
		row := quotaSiteRow{site, names[site.GroupID]}
		q.Sites = append(q.Sites, row)
		if site.GroupID == "" {
			q.Unassigned.Sites++
			q.Unassigned.Bytes += site.Bytes
		}
		if site.ID == selected {
			q.Selected = &row
		}
	}
	if selected != "" && q.Selected == nil {
		consoleError(w, c, 404, "Site not found", "Choose a site in this workspace.")
		return false
	}
	return true
}

// Missing fields are invalid: only an explicitly blank group field inherits.
func quotaLimit(f url.Values, field string, max int64, allowBlank bool) (int64, bool) {
	v := f[field]
	if len(v) != 1 {
		return 0, false
	}
	if allowBlank && v[0] == "" {
		return -1, true
	}
	n, err := strconv.ParseUint(v[0], 10, 63)
	return int64(n), err == nil && int64(n) <= max
}

func quotaInputs(f url.Values, inherit bool) (sites.QuotaLimits, bool) {
	var limits sites.QuotaLimits
	if len(f["storage_unit"]) != 1 {
		return limits, false
	}
	for _, u := range quotaUnits {
		if f.Get("storage_unit") != u.name {
			continue
		}
		count, ok := quotaLimit(f, "site_limit", sites.MaxSitesPerWorkspace, inherit)
		n, valid := quotaLimit(f, "storage_limit", sites.MaxWorkspaceManifestBytes/u.size, inherit)
		if !ok || !valid {
			return limits, false
		}
		limits.Sites, limits.Bytes = count, n
		if n >= 0 {
			limits.Bytes = n * u.size
		}
		return limits, true
	}
	return limits, false
}

func quotaFormError(w http.ResponseWriter, c consoleContext, code int, title, detail string) {
	consoleView(w, c, code, title, "admin", memberPages.Lookup("quota-error"), struct{ Title, Detail string }{title, detail})
}

func (a *App) quotaMutation(w http.ResponseWriter, r *http.Request, c consoleContext, action string) bool {
	if action != "workspace-quota" && action != "group-quota" && action != "site-group" {
		return false
	}
	f := r.PostForm
	version, ok := quotaLimit(f, "version", 1<<63-1, false)
	if !ok || version < 1 {
		quotaFormError(w, c, 400, "Missing version", "Reopen Quotas before making changes.")
		return true
	}
	var err error
	group := f.Get("group")
	who, ctx := actor(c.Identity), r.Context()
	switch action {
	case "site-group":
		if len(f["group"]) != 1 || len(f["site"]) != 1 || f.Get("site") == "" {
			quotaFormError(w, c, 400, "Missing assignment", "Choose a site and a group, or select Unassigned.")
			return true
		}
		err = a.store.AssignSiteGroup(ctx, who, f.Get("site"), group, version)
	default:
		forGroup := action == "group-quota"
		if forGroup && (len(f["group"]) != 1 || group == "") || !forGroup && len(f["group"]) != 0 {
			quotaFormError(w, c, 400, "Invalid quota form", "Open the limits for the workspace or a specific group.")
			return true
		}
		limits, valid := quotaInputs(f, forGroup)
		if !valid {
			quotaFormError(w, c, 400, "Check the limits", fmt.Sprintf("Use whole numbers from zero to %d sites and up to %s of storage. Only group limits can be left blank.", sites.MaxSitesPerWorkspace, formatQuotaBytes(sites.MaxWorkspaceManifestBytes)))
			return true
		}
		err = a.store.SetQuota(ctx, who, group, limits, version)
	}
	if err != nil {
		code, title, detail := memberFailure(err)
		if e, ok := err.(*sites.Error); ok && (e.Code == "conflict" || e.Code == "quota_exceeded" || e.Code == "invalid_argument") {
			detail = e.Message
		}
		quotaFormError(w, c, code, title, detail)
		return true
	}
	location := adminLocation("quotas", "", action)
	if action == "site-group" {
		location += "#site-assignments"
	} else if action == "group-quota" {
		location += "#budget-" + url.PathEscape(group)
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
	return true
}
