package web

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openhoo/shoowcase/internal/auth"
	"github.com/openhoo/shoowcase/internal/sites"
)

// Keep an independently owned site unchanged across erasure, so a stale ticket
// cannot be refused merely because its site, revision or publication changed.
func TestReprovisionedPublisherNeedsFreshUploadAndApproval(t *testing.T) {
	a, s := handlerFixture(t, filepath.Join(t.TempDir(), "ticket-epoch.db"), "team-a", true)
	ctx := t.Context()
	admin := sites.Actor{Subject: "admin", Workspace: "team-a", Role: "admin"}
	old := auth.Identity{Subject: "publisher", Workspace: "team-a", Role: auth.Publisher, IssuedAt: time.Now().Add(-time.Hour).Unix()}
	who := actor(old)
	for _, member := range []sites.Actor{admin, who} {
		if _, err := s.EnrollAccount(verified(ctx, old), member, member.Subject); err != nil {
			t.Fatal(err)
		}
	}
	rev := seedSiteFor(t, s, admin)
	marshal := func(value any) json.RawMessage {
		t.Helper()
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	access := map[string]string{"site_id": "field", "revision": rev, "visibility": "workspace", "request_id": "private"}
	if _, err := s.Execute(ctx, admin, "set_site_access", marshal(access)); err != nil {
		t.Fatal(err)
	}
	access["visibility"], access["request_id"] = "public", "public-again"
	const body = "body { color: teal }"
	issue := func(c context.Context) (string, ticket, json.RawMessage) {
		t.Helper()
		upload, err := a.executeAs(c, who, "prepare_upload", marshal(uploadSpec{Site: "field", Revision: rev, Path: "colors.css",
			SHA: fmt.Sprintf("%x", sha256.Sum256([]byte(body))), Size: int64(len(body)), Request: "upload-colors"}))
		if err != nil {
			t.Fatal(err)
		}
		review, err := a.executeAs(c, who, "set_site_access", marshal(access))
		if _, pending := sites.ApprovalGeneration(err); !pending {
			t.Fatalf("expected public change to require approval: %v", err)
		}
		tk, ok := a.ticket(httptest.NewRequest("GET", review["approval_url"].(string), nil), "approval")
		if !ok {
			t.Fatal("browser approval did not produce a valid signed ticket")
		}
		return upload["upload_url"].(string), tk, marshal(map[string]any{"approval_token": review["approval_token"]})
	}
	put := func(link string, want int) {
		t.Helper()
		req := httptest.NewRequest("PUT", link, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:45550"
		resp := httptest.NewRecorder()
		a.ServeHTTP(resp, req)
		requireMemberStatus(t, resp, want)
	}
	before, oldBrowserReview, oldAgentReview := issue(verified(ctx, old))
	sel := sites.AdminSelection{Action: "delete-members", IDs: []string{who.Subject}}
	r, err := s.ReviewAdminSelection(ctx, admin, sel)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ApplyAdminSelection(ctx, admin, sel, r.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if err = s.ProvisionMember(ctx, admin, who.Subject, "New publisher"); err != nil {
		t.Fatal(err)
	}
	// Model a verified OIDC sign-in beyond the accepted IdP clock skew.
	fresh := old
	fresh.IssuedAt = time.Now().Add(sites.MaxAssertionClockSkew + time.Minute).Unix()
	freshCtx := verified(ctx, fresh)
	if _, err = s.LimitActor(freshCtx, who); err != nil {
		t.Fatal(err)
	}
	put(before, 403)
	if _, _, err = a.ticketActor(ctx, oldBrowserReview, &fresh); errorCode(err) != "forbidden" {
		t.Fatalf("old browser review with fresh sign-in: %v", err)
	}
	if _, err = a.executeAs(freshCtx, who, "approve_public_change", oldAgentReview); errorCode(err) != "forbidden" {
		t.Fatalf("old MCP approval with fresh assertion: %v", err)
	}

	after, newBrowserReview, newAgentReview := issue(freshCtx)
	if _, _, err = a.ticketActor(ctx, newBrowserReview, &old); errorCode(err) != "forbidden" {
		t.Fatalf("new review with old browser session: %v", err)
	}
	if _, principal, err := a.ticketActor(ctx, newBrowserReview, &fresh); err != nil || principal != who {
		t.Fatalf("new review with fresh browser session: %v, %v", principal, err)
	}
	if _, err = a.executeAs(freshCtx, who, "approve_public_change", newAgentReview); err != nil {
		t.Fatalf("fresh MCP approval: %v", err)
	}
	put(after, 200)
}

func TestConsoleReviewsAndKeyCreationBelongToOneLogin(t *testing.T) {
	a, s := handlerFixture(t, filepath.Join(t.TempDir(), "signins.db"), "team-a", false)
	first, csrf1 := joinMemberConsole(t, a)
	second, _ := browserSessions(t, a, testSite)
	keys := hitRoute(t, a, "GET", testControl+"/keys", first, nil)
	token := memberControl(t, keys.Body.String(), "name", "creation_token")
	keys2 := hitRoute(t, a, "GET", testControl+"/keys", second, nil)
	csrf2 := memberControl(t, keys2.Body.String(), "name", "csrf")
	if token == "" || csrf1 == csrf2 || csrf2 == "" {
		t.Fatal("expected two separate sign-ins of the same account")
	}
	create := url.Values{"csrf": {csrf2}, "action": {"create"}, "name": {"session key"}, "days": {"5"}, "scope": {"reader"}, "creation_token": {token}}
	requireMemberStatus(t, memberPost(t, a, second, "/keys", create), 409)
	create.Set("csrf", csrf1)
	requireMemberStatus(t, memberPost(t, a, first, "/keys", create), 200)

	admin := sites.Actor{Subject: "tester", Workspace: "team-a", Role: "admin"}
	if err := s.ProvisionMember(t.Context(), admin, "target", "Delete fixture"); err != nil {
		t.Fatal(err)
	}
	sel := url.Values{"csrf": {csrf1}, "action": {"review-selection"}, "operation": {"delete-members"}, "selected": {"target"}}
	review := memberPost(t, a, first, "/admin", sel)
	requireMemberStatus(t, review, 200)
	sel.Set("action", "apply-selection")
	sel.Set("confirm", "yes")
	sel.Set("review", memberControl(t, review.Body.String(), "name", "review"))
	sel.Set("csrf", csrf2)
	requireMemberStatus(t, memberPost(t, a, second, "/admin", sel), 409)
	sel.Set("csrf", csrf1)
	requireMemberStatus(t, memberPost(t, a, first, "/admin", sel), 303)
}
