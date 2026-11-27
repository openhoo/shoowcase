package web

import (
	"cmp"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/openhoo/shoowcase/internal/sites"
)

type adminDirectory struct {
	Query, Role, State, Sort string
	Filtered                 bool
	Total, Matches           int
	Prev, Next               string
}

func parseAdminDirectory(w http.ResponseWriter, r *http.Request, c consoleContext, members bool) (adminDirectory, bool) {
	q := r.URL.Query()
	d := adminDirectory{Query: strings.TrimSpace(q.Get("q")), Role: q.Get("role"), State: q.Get("state"), Sort: q.Get("sort")}
	if d.Sort == "" {
		d.Sort = "name"
	}
	validRole := slices.Contains([]string{"", "reader", "editor", "publisher"}, d.Role) || members && d.Role == "admin"
	validState := d.State == "" || members && (d.State == "active" || d.State == "suspended")
	validSort := d.Sort == "name" || members && d.Sort == "subject" || !members && d.Sort == "members"
	if !utf8.ValidString(d.Query) || utf8.RuneCountInString(d.Query) > 100 || strings.ContainsFunc(d.Query, unicode.IsControl) ||
		!validRole || !validState || !validSort || len(q.Get("after")) > 255 {
		consoleError(w, c, 400, "Invalid directory filter", "Search for up to 100 characters and choose one of the listed options.")
		return d, false
	}
	d.Filtered = d.Query != "" || d.Role != "" || d.State != ""
	return d, true
}

func (a *App) membersView(w http.ResponseWriter, r *http.Request, c consoleContext, p *memberPage) bool {
	who := actor(c.Identity)
	if subject := r.URL.Query().Get("member"); subject != "" {
		m, err := a.store.WorkspaceAccount(r.Context(), who, subject)
		if err != nil {
			memberError(w, c, err)
			return false
		}
		p.Member = &memberRow{Account: m, Manage: m.Subject != who.Subject, Ceilings: memberCeilings(m)}
		return true
	}
	d, ok := parseAdminDirectory(w, r, c, true)
	if !ok {
		return false
	}
	all, err := a.store.WorkspaceAccounts(r.Context(), who)
	if err != nil {
		memberError(w, c, err)
		return false
	}
	p.Directory = d
	p.Members = p.Directory.members(all, r.URL.Query().Get("after"))
	return true
}

func (d *adminDirectory) members(all []sites.Account, after string) []sites.Account {
	d.Total = len(all)
	needle := strings.ToLower(d.Query)
	matched := slices.DeleteFunc(all, func(m sites.Account) bool {
		return d.Role != "" && m.Role != d.Role ||
			d.State != "" && m.Disabled != (d.State == "suspended") ||
			!strings.Contains(strings.ToLower(m.DisplayName), needle) && !strings.Contains(strings.ToLower(m.Subject), needle)
	})
	slices.SortFunc(matched, func(a, b sites.Account) int {
		if d.Sort == "name" {
			if n := cmp.Compare(strings.ToLower(a.DisplayName), strings.ToLower(b.DisplayName)); n != 0 {
				return n
			}
		}
		return cmp.Compare(a.Subject, b.Subject)
	})
	d.Matches = len(matched)
	start := 0
	if after != "" {
		if i := slices.IndexFunc(matched, func(m sites.Account) bool { return m.Subject == after }); i >= 0 {
			start = i + 1
		}
	}
	const pageSize = 25
	end := min(start+pageSize, d.Matches)
	pageLink := func(cursor string) string {
		q := url.Values{"view": {"members"}, "sort": {d.Sort}}
		for key, value := range map[string]string{"q": d.Query, "role": d.Role, "state": d.State, "after": cursor} {
			if value != "" {
				q.Set(key, value)
			}
		}
		return "/admin?" + q.Encode()
	}
	if end < d.Matches {
		d.Next = pageLink(matched[end-1].Subject)
	}
	if start > 0 {
		prev := ""
		if start > pageSize {
			prev = matched[start-pageSize-1].Subject
		}
		d.Prev = pageLink(prev)
	}
	return matched[start:end]
}

func (d *adminDirectory) groups(all []sites.Group) []sites.Group {
	d.Total = len(all)
	needle := strings.ToLower(d.Query)
	matched := slices.DeleteFunc(all, func(g sites.Group) bool {
		return d.Role != "" && g.Role != d.Role || !strings.Contains(strings.ToLower(g.Name), needle)
	})
	// ListGroups returns groups ordered by name and ID. Keep that order for ties.
	if d.Sort == "members" {
		slices.SortStableFunc(matched, func(a, b sites.Group) int { return cmp.Compare(b.Members, a.Members) })
	}
	d.Matches = len(matched)
	return matched
}
