package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/openhoo/shoowcase/internal/sites"
)

const deleteSitesProofKind = "permanent-delete-sites-v1"

type selectedSiteRow struct {
	DeletionValue string
}

func siteSelectionValue(id, revision string) string { return id + ":" + revision }

func reviewedSiteValue(s sites.SiteDeletionState) string {
	return strings.Join([]string{s.Site, s.Revision, s.Live, s.Release, s.Visibility, strconv.FormatInt(s.Generation, 10)}, ":")
}

func parseReviewedSite(token string) (sites.SiteDeletionState, bool) {
	parts := strings.Split(token, ":")
	if len(parts) != 6 {
		return sites.SiteDeletionState{}, false
	}
	generation, err := strconv.ParseInt(parts[5], 10, 64)
	state := sites.SiteDeletionState{SiteSelection: sites.SiteSelection{Site: parts[0], Revision: parts[1]}, Live: parts[2], Release: parts[3], Visibility: parts[4], Generation: generation}
	return state, err == nil && generation > 0 && reviewedSiteValue(state) == token
}

func deletionLabel(x sites.ReviewedSite, publicDelivery bool) string {
	state := "Draft only"
	switch {
	case x.Visibility == "offline":
		state = "Offline"
	case x.Live != "":
		state = "Live (Workspace)"
		if x.Visibility == "public" && publicDelivery {
			state = "Live (Public)"
		}
	}
	return x.Name + " (" + x.Site + ") · " + state
}

func (a *App) deleteSiteSelection(w http.ResponseWriter, r *http.Request, d siteData) {
	if d.C.Actor.Role != "admin" {
		siteFailure(w, d.C, problem("forbidden", "Only a workspace administrator can delete sites."))
		return
	}
	tokens := r.PostForm["site"]
	if len(r.PostForm["action"]) != 1 || len(tokens) == 0 || len(tokens) > sites.MaxSitesPerWorkspace {
		siteFailure(w, d.C, badFile("Select between 1 and 100 sites from the directory."))
		return
	}
	switch r.PostForm.Get("action") {
	case "review-delete-sites":
		var selected []sites.SiteSelection
		for _, token := range tokens {
			id, rev, ok := strings.Cut(token, ":")
			if !ok {
				siteFailure(w, d.C, badFile("Select sites from the directory."))
				return
			}
			selected = append(selected, sites.SiteSelection{Site: id, Revision: rev})
		}
		review, err := a.store.ReviewSiteDeletion(r.Context(), d.C.Actor, selected)
		if err != nil {
			siteFailure(w, d.C, err)
			return
		}
		snapshots := make([]string, 0, len(review))
		for _, x := range review {
			value := reviewedSiteValue(x.SiteDeletionState)
			snapshots = append(snapshots, value)
			d.Selected = append(d.Selected, selectedSiteRow{DeletionValue: value})
			d.Labels = append(d.Labels, deletionLabel(x, a.config.Public))
		}
		d.ReviewProof = a.selectionProof(d.C, deleteSitesProofKind, snapshots)
		title := "Delete sites"
		if len(review) == 1 {
			title = "Delete site"
		}
		siteView(w, d, http.StatusOK, title, "delete-sites")
	case "delete-sites":
		if len(r.PostForm["confirm"]) != 1 || len(r.PostForm["proof"]) != 1 || r.PostForm.Get("confirm") != "delete" || !a.validSelectionProof(d.C, deleteSitesProofKind, tokens, r.PostForm.Get("proof")) {
			siteFailure(w, d.C, badFile("Review and confirm the sites to delete."))
			return
		}
		var reviewed []sites.SiteDeletionState
		for _, token := range tokens {
			state, ok := parseReviewedSite(token)
			if !ok {
				siteFailure(w, d.C, badFile("Review and confirm the sites to delete."))
				return
			}
			reviewed = append(reviewed, state)
		}
		if err := a.store.DeleteSites(r.Context(), d.C.Actor, reviewed); err != nil {
			siteFailure(w, d.C, err)
			return
		}
		http.Redirect(w, r, "/sites?notice=sites-deleted", http.StatusSeeOther)
	default:
		siteFailure(w, d.C, badFile("Choose a listed site action."))
	}
}

type fileDeletionReview struct {
	Site, Revision string
	Paths          []string
}

func (a *App) reviewFileDeletion(w http.ResponseWriter, r *http.Request, d siteData, id string) {
	if len(d.Paths) == 0 || len(d.Paths) > sites.MaxFiles {
		siteFailure(w, d.C, badFile("Select between 1 and 100 files to remove."))
		return
	}
	detail, err := a.getSite(r.Context(), sites.GetSiteQuery{Site: id, Limit: new(sites.MaxFiles)})
	if err != nil {
		siteFailure(w, d.C, err)
		return
	}
	if detail.StorageRestriction != "" && detail.GroupID != "" {
		siteFailure(w, d.C, problem("forbidden", detail.StorageRestriction))
		return
	}
	if d.Rev != detail.Revision {
		siteFailure(w, d.C, problem("conflict", "The draft changed. Reload the file list and review the selection."))
		return
	}
	seen := make(map[string]bool, len(d.Paths))
	for _, p := range d.Paths {
		if seen[p] || !detail.HasFile(p) {
			siteFailure(w, d.C, badFile("Select distinct files from the current draft."))
			return
		}
		seen[p] = true
	}
	d.S = detail
	d.ReviewProof = a.selectionProof(d.C, "remove-files", fileDeletionReview{id, d.Rev, d.Paths})
	siteView(w, d, http.StatusOK, "Review file removal", "delete")
}
