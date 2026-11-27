package web

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openhoo/shoowcase/internal/sites"
)

func TestReviewAndConfirmKeySelection(t *testing.T) {
	app, store := handlerFixture(t, filepath.Join(t.TempDir(), "selection.sqlite"), "team-a", false)
	cookie, csrf := joinMemberConsole(t, app)
	owner := sites.Actor{Subject: "tester", Workspace: "team-a", Role: "admin"}
	var ids, secrets []string
	for _, name := range []string{"Research notebook", "Test builder", "Leave active"} {
		k, sec, err := store.CreateKey(t.Context(), owner, name, "reader", 1)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, k.ID)
		secrets = append(secrets, sec)
	}
	data := url.Values{"action": {"review-key-revocation"}, "csrf": {csrf}, "key_id": ids[:2]}
	out := memberPost(t, app, cookie, "/keys", data)
	requireMemberStatus(t, out, 200)
	body := out.Body.String()
	if !strings.Contains(body, "Research notebook") || !strings.Contains(body, "Test builder") || strings.Contains(body, "Leave active") {
		t.Fatal("confirmation does not name the precise selection")
	}
	for _, sec := range secrets {
		if _, err := store.ResolveKey(t.Context(), sec); err != nil {
			t.Fatal("review prematurely revoked a key", err)
		}
	}
	review := memberControl(t, body, "name", "review")
	if review == "" {
		t.Fatal("missing confirmation")
	}
	data = url.Values{"action": {"revoke-selected"}, "review": {review}, "key_id": {ids[2]}}
	requireMemberStatus(t, memberPost(t, app, cookie, "/keys", data), 403)
	data.Set("csrf", csrf)
	requireMemberStatus(t, memberPost(t, app, cookie, "/keys", data), 409)
	data.Set("confirm", "yes")
	requireMemberStatus(t, memberPost(t, app, cookie, "/admin", data), 409)
	// A changed input added to the confirmation cannot extend the reviewed set.
	requireMemberStatus(t, memberPost(t, app, cookie, "/keys", data), 303)
	for _, sec := range secrets[:2] {
		if _, err := store.ResolveKey(t.Context(), sec); err == nil {
			t.Fatal("selected key remains active")
		}
	}
	if _, err := store.ResolveKey(t.Context(), secrets[2]); err != nil {
		t.Fatal("unreviewed key was revoked", err)
	}
}
