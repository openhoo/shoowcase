package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// A browser review (or key-creation token) belongs to one sign-in, including
// when a deleted OIDC subject is later provisioned as a fresh account.
func (a *App) signConsole(c consoleContext, t ticket) string {
	t.Who = c.Actor
	t.Session = reviewedSelectionDigest(c.Identity.CSRF)
	return a.sign(t)
}

func (c consoleContext) ownsTicket(t ticket) bool {
	return t.Who == c.Actor && t.Session == reviewedSelectionDigest(c.Identity.CSRF)
}

// Keep the token small even when a whole page of long file names is selected.
func reviewedSelectionDigest(selection any) string {
	b, err := json.Marshal(selection)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (a *App) selectionProof(c consoleContext, action string, selection any) string {
	sum := reviewedSelectionDigest(selection)
	if sum == "" {
		return ""
	}
	return a.signConsole(c, ticket{Kind: "selection:" + action, Tool: sum, Expires: time.Now().Add(10 * time.Minute).Unix()})
}

func (a *App) validSelectionProof(c consoleContext, action string, selection any, signed string) bool {
	t, ok := a.ticketValue(signed, "selection:"+action)
	return ok && c.ownsTicket(t) && t.Tool != "" && t.Tool == reviewedSelectionDigest(selection)
}
