package web

import (
	"crypto/rand"

	"encoding/json"
	"net/http"
	"time"

	"github.com/openhoo/shoowcase/internal/sites"
)

type adminActionText struct{ Title, Button, Effect, Done, View string }

var adminActionTexts = map[string]adminActionText{
	"suspend-members": {"Suspend members", "Suspend members", "These members lose workspace access and their personal agent keys are revoked. Their accounts and audit history are kept.", "Selected members suspended. Their keys have been revoked.", "members"},
	"delete-members":  {"Review account deletion", "Permanently delete accounts", "This permanently deletes the selected accounts from this workspace, along with their Shoowcase credentials, group memberships and every site they created. This includes drafts, published releases and edits that others made to those sites. External sign-in accounts remain with their identity provider. This cannot be undone.", "Selected accounts and their sites were permanently deleted.", "members"},
	"restore-members": {"Restore members", "Restore access", "These members can access the workspace again. Previously revoked keys stay inactive.", "Selected members restored. Previously revoked keys remain inactive.", "members"},
	"delete-groups":   {"Delete groups", "Delete groups", "These groups and their grants will be removed. Member accounts stay in the workspace. Move any sites assigned to these groups before deleting them.", "Selected groups deleted.", "groups"},
	"remove-members":  {"Remove group members", "Remove from group", "These people will lose this group’s role and access to its project budget. They will keep their workspace accounts and other group memberships.", "Selected members removed from the group.", "groups"},
}

func (a *App) adminSelection(w http.ResponseWriter, r *http.Request, c consoleContext, stage string) bool {
	if stage != "review-selection" && stage != "apply-selection" {
		return false
	}
	f := r.PostForm
	sel := sites.AdminSelection{Action: f.Get("operation"), IDs: f["selected"], Group: f.Get("group")}
	text, ok := adminActionTexts[sel.Action]
	if !ok || len(f["operation"]) != 1 || len(f["group"]) > 1 {
		consoleError(w, c, 400, "Unknown selection", "Choose an action from the directory.")
		return true
	}
	ctx, who := r.Context(), actor(c.Identity)
	back := adminLocation(text.View, sel.Group, "")
	if stage == "apply-selection" {
		t, valid := a.ticketValue(f.Get("review"), "admin-selection")
		if !valid || !c.ownsTicket(t) || t.Tool != reviewedSelectionDigest(sel) || len(t.Args) == 0 || f.Get("confirm") != "yes" {
			consoleError(w, c, 409, "Review the selection again", "The confirmation is missing, expired, or belongs to a different selection.")
			return true
		}
		var fingerprint string
		if json.Unmarshal(t.Args, &fingerprint) != nil {
			consoleError(w, c, 409, "Invalid review", "Review the selection again.")
			return true
		}
		used, err := a.store.Consume(ctx, "admin-selection:"+t.Nonce, time.Unix(t.Expires, 0))
		if err != nil {
			memberError(w, c, err)
			return true
		}
		if !used {
			consoleError(w, c, 409, "Review already submitted", "Return to the directory and review the current entries.")
			return true
		}
		if err := a.store.ApplyAdminSelection(ctx, who, sel, fingerprint); err != nil {
			adminSelectionError(w, c, err, back)
			return true
		}
		http.Redirect(w, r, adminLocation(text.View, sel.Group, sel.Action), http.StatusSeeOther)
		return true
	}
	review, err := a.store.ReviewAdminSelection(ctx, who, sel)
	if err != nil {
		adminSelectionError(w, c, err, back)
		return true
	}
	hashJSON, _ := json.Marshal(review.Fingerprint)
	token := a.signConsole(c, ticket{Kind: "admin-selection", Tool: reviewedSelectionDigest(sel), Args: hashJSON, Expires: time.Now().Add(10 * time.Minute).Unix(), Nonce: rand.Text()})
	page := struct {
		adminActionText
		Selection         sites.AdminSelection
		Names             []string
		CSRF, Token, Back string
	}{text, sel, review.Names, c.Identity.CSRF, token, back}
	consoleView(w, c, 200, text.Title, "admin", memberPages.Lookup("admin-selection-review"), page)
	return true
}

func adminSelectionError(w http.ResponseWriter, c consoleContext, err error, back string) {
	code, title, detail := memberFailure(err)
	if e, ok := err.(*sites.Error); ok && (e.Code == "conflict" || e.Code == "invalid_argument" || e.Code == "not_found") {
		detail = e.Message
	}
	consoleView(w, c, code, title, "admin", memberPages.Lookup("admin-selection-error"), struct{ Title, Detail, Back string }{title, detail, back})
}
