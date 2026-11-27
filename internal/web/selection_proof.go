package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

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
	return a.sign(ticket{Kind: "selection:" + action, Who: c.Actor, Tool: sum, Expires: time.Now().Add(10 * time.Minute).Unix()})
}

func (a *App) validSelectionProof(c consoleContext, action string, selection any, signed string) bool {
	t, ok := a.ticketValue(signed, "selection:"+action)
	return ok && t.Who == c.Actor && t.Tool != "" && t.Tool == reviewedSelectionDigest(selection)
}
