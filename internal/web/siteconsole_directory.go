package web

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/openhoo/shoowcase/internal/sites"
)

type directoryData struct {
	Sites                                         []directoryEntry
	Total, Changes, Published, Drafts, MatchCount int
	Filtered                                      bool
	Query, State, Audience, Sort                  string
}
type directoryEntry struct {
	ID, Name, Address, Href, State, StateLabel, Audience, AudienceLabel string
	DeletionValue                                                       string
	FileCount                                                           int
	Updated                                                             time.Time
	UpdatedLabel                                                        string
	IsPublished                                                         bool
}

func directoryHref(id string) string {
	if id == "new" {
		return "/sites/~new"
	} // Existing site slugs remain reachable alongside /sites/new.
	return "/sites/" + url.PathEscape(id)
}

func (a *App) directory(ctx context.Context, who sites.Actor, q url.Values) (d directoryData, err error) {
	d.Query, d.State, d.Audience, d.Sort = strings.TrimSpace(q.Get("q")), q.Get("state"), q.Get("audience"), q.Get("sort")
	if !utf8.ValidString(d.Query) || utf8.RuneCountInString(d.Query) > 100 || strings.ContainsFunc(d.Query, unicode.IsControl) {
		return d, badFile("Search for up to 100 characters without line breaks.")
	}
	if !slices.Contains([]string{"", "draft", "published", "changes", "offline"}, d.State) || !slices.Contains([]string{"", "workspace", "public"}, d.Audience) {
		return d, badFile("Choose a listed status or audience.")
	}
	if d.Sort == "" {
		d.Sort = "updated"
	}
	if !slices.Contains([]string{"updated", "name", "oldest"}, d.Sort) {
		return d, badFile("Choose a listed sort order.")
	}
	d.Filtered = d.Query != "" || d.State != "" || d.Audience != ""
	if !a.config.Public {
		ctx = sites.WithPrivateDelivery(ctx)
	}
	all, err := a.store.Directory(ctx, who)
	if err != nil {
		return d, err
	}
	needle := strings.ToLower(d.Query)
	for _, x := range all {
		e := directoryEntry{ID: x.ID, Name: x.Name, Address: x.ID, Href: directoryHref(x.ID), Audience: x.Visibility, FileCount: x.FileCount, Updated: x.Updated, UpdatedLabel: x.Updated.Format("2 Jan 2006, 15:04 UTC")}
		d.Total++
		e.IsPublished = x.Live != "" && x.Visibility != "offline"
		if e.IsPublished {
			d.Published++
		}
		if who.Role == "admin" {
			e.DeletionValue = siteSelectionValue(x.ID, x.Draft)
		}
		if x.Live == "" {
			d.Drafts++
		}
		switch {
		case x.Visibility == "offline":
			e.State, e.StateLabel = "offline", "Offline"
		case x.Live == "":
			e.State, e.StateLabel = "draft", "Draft"
		case x.Live != x.Draft:
			e.State, e.StateLabel = "changes", "Unpublished changes"
			d.Changes++
		default:
			e.State, e.StateLabel = "published", "Published"
		}
		switch e.Audience {
		case "public":
			e.AudienceLabel = "Public"
		case "offline":
			e.AudienceLabel = "Offline"
		default:
			e.AudienceLabel = "Workspace"
		}
		if d.State != "" && d.State != e.State || d.Audience != "" && d.Audience != e.Audience {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(e.Name), needle) && !strings.Contains(e.ID, needle) {
			continue
		}
		d.Sites = append(d.Sites, e)
	}
	slices.SortFunc(d.Sites, func(a, b directoryEntry) int {
		if d.Sort == "name" {
			if cmp := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); cmp != 0 {
				return cmp
			}
		} else if cmp := a.Updated.Compare(b.Updated); cmp != 0 {
			if d.Sort == "oldest" {
				return cmp
			}
			return -cmp
		}
		return strings.Compare(a.ID, b.ID)
	})
	d.MatchCount = len(d.Sites)
	return d, nil
}
