package web

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/openhoo/shoowcase/internal/sites"
)

func keyRevocationNotice(done string) string {
	switch done {
	case "revoked":
		return "Key revoked."
	case "keys-revoked":
		return "Selected keys revoked."
	}
	return ""
}

func (a *App) keyRevocation(w http.ResponseWriter, r *http.Request, c consoleContext, workspaceWide bool) {
	who := actor(c.Identity)
	path, back, active := "/keys", "/keys", "keys"
	if workspaceWide {
		path, back, active = "/admin", "/admin?view=keys", "admin"
	}
	ids := r.PostForm["key_id"]
	switch r.PostForm.Get("action") {
	case "review-key-revocation":
		if len(ids) == 0 {
			consoleError(w, c, 400, "No keys selected", "Select at least one active key to revoke.")
			return
		}
		selected, err := a.store.KeysForRevocation(r.Context(), who, workspaceWide, ids)
		if err != nil {
			memberError(w, c, err)
			return
		}
		args, _ := json.Marshal(ids)
		review := a.sign(ticket{Kind: "revoke-keys", Who: c.Actor, Tool: path, Args: args, Expires: time.Now().Add(15 * time.Minute).Unix()})
		page := struct {
			CSRF, Path, Back, Review string
			Admin                    bool
			Keys                     []sites.AgentKey
		}{c.Identity.CSRF, path, back, review, workspaceWide, selected}
		consoleView(w, c, 200, "Review key revocation", active, memberPages.Lookup("key-revocation-review"), page)
		return
	case "revoke-selected":
		review, ok := a.ticketValue(r.PostForm.Get("review"), "revoke-keys")
		if r.PostForm.Get("confirm") != "yes" || !ok || review.Who != c.Actor || review.Tool != path || json.Unmarshal(review.Args, &ids) != nil {
			consoleError(w, c, 409, "Selection expired", "Select the keys and review them again.")
			return
		}
	case "revoke":
		if len(ids) != 1 || r.PostForm.Get("confirm") != "yes" {
			consoleError(w, c, 400, "Confirm revocation", "Select the confirmation checkbox to revoke this key.")
			return
		}
	default:
		consoleError(w, c, 400, "Unknown action", "Open the key directory and try again.")
		return
	}
	if err := a.store.RevokeKeys(r.Context(), who, workspaceWide, ids); err != nil {
		memberError(w, c, err)
		return
	}
	done := "revoked"
	if len(ids) > 1 {
		done = "keys-revoked"
	}
	if workspaceWide {
		http.Redirect(w, r, back+"&done="+done, http.StatusSeeOther)
	} else {
		http.Redirect(w, r, back+"?done="+done, http.StatusSeeOther)
	}
}
