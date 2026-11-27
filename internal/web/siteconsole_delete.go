package web

import (
	"net/http"
	"strings"

	"github.com/openhoo/shoowcase/internal/sites"
)

func (a *App) deleteSiteSelection(w http.ResponseWriter, r *http.Request, d siteData) {
	if d.C.Actor.Role != "admin" {
		siteFailure(w, d.C, problem("forbidden", "Only a workspace administrator can delete a draft site."))
		return
	}
	for _, token := range r.PostForm["site"] {
		id, rev, ok := strings.Cut(token, ":")
		if !ok {
			siteFailure(w, d.C, badFile("Select draft sites from the directory."))
			return
		}
		d.Selected = append(d.Selected, sites.DraftSelection{Site: id, Revision: rev})
	}
	switch r.PostForm.Get("action") {
	case "review-delete-sites":
		labels, err := a.store.ReviewDraftDeletion(r.Context(), d.C.Actor, d.Selected)
		if err != nil {
			siteFailure(w, d.C, err)
			return
		}
		d.Labels = labels
		d.ReviewProof = a.selectionProof(d.C, "delete-sites", d.Selected)
		siteView(w, d, http.StatusOK, "Delete draft sites", "delete-sites")
	case "delete-sites":
		if r.PostForm.Get("confirm") != "delete" || !a.validSelectionProof(d.C, "delete-sites", d.Selected, r.PostForm.Get("proof")) {
			siteFailure(w, d.C, badFile("Review and confirm the draft sites to delete."))
			return
		}
		if err := a.store.DeleteDraftSites(r.Context(), d.C.Actor, d.Selected); err != nil {
			siteFailure(w, d.C, err)
			return
		}
		http.Redirect(w, r, "/sites?notice=drafts-deleted", http.StatusSeeOther)
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
