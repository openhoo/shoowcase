package sites

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestDeletedIdentityNeedsProvisioningAndFreshAuthority(t *testing.T) {
	for _, driver := range []string{"sqlite", "pgx"} {
		t.Run(driver, func(t *testing.T) {
			ctx := t.Context()
			s := openTest(t, driver, testDSN(t, driver))
			admin := Actor{"owner", "workspace", "admin"}
			who := Actor{"peer", "workspace", "admin"}
			old := WithIdentityIssuedAt(ctx, time.Now().Add(-time.Hour).Unix())
			for _, a := range []Actor{admin, who} {
				if _, err := s.EnrollAccount(old, a, a.Subject); err != nil {
					t.Fatal(err)
				}
			}
			site := mustOp(t, s, ctx, admin, "create_site", args{"name": "Private", "slug": "kept", "request_id": "create"})
			draft := mustOp(t, s, ctx, admin, "upload_files", args{"site_id": "kept", "expected_revision": site["revision"], "files": []args{{"path": "index.html", "text": "private"}}, "request_id": "write"})
			live := mustOp(t, s, ctx, admin, "publish_site", args{"site_id": "kept", "revision": draft["revision"], "request_id": "publish"})
			file, err := s.Fetch(old, "kept", "", "/", who)
			if err != nil {
				t.Fatal(err)
			}
			oldKey, _, err := s.CreateKey(old, who, "Retired key", "reader", 1)
			if err != nil {
				t.Fatal(err)
			}
			sel := AdminSelection{Action: "delete-members", IDs: []string{who.Subject}}
			r, err := s.ReviewAdminSelection(ctx, admin, sel)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.ApplyAdminSelection(ctx, admin, sel, r.Fingerprint); err != nil {
				t.Fatal(err)
			}
			fresh := WithIdentityIssuedAt(ctx, time.Now().Add(MaxAssertionClockSkew+time.Second).Unix())
			for _, c := range []context.Context{old, fresh} {
				_, err = s.LimitActor(c, who)
				wantErr(t, err, "forbidden")
				_, err = s.EnrollAccount(c, who, "Again")
				wantErr(t, err, "forbidden")
			}
			// A late denied operation, login callback or logout cannot re-log the erased identity.
			if err = s.RecordDenied(old, who, "admin"); err != nil {
				t.Fatal(err)
			}
			wantErr(t, s.RecordSession(old, who, "auth.login"), "forbidden")
			if err = s.RevokeSessionAndAudit(old, "logout:"+strings.Repeat("A", 43), time.Now().Add(time.Hour), who); err != nil {
				t.Fatal(err)
			}
			var count int
			if err = s.db.QueryRow("SELECT count(*) FROM sc_workspace_audit WHERE workspace=$1 AND subject=$2", who.Workspace, who.Subject).Scan(&count); err != nil || count != 0 {
				t.Fatalf("late audit inserted identity: %d, %v", count, err)
			}
			if err = s.ProvisionMember(ctx, admin, who.Subject, "New membership"); err != nil {
				t.Fatal(err)
			}
			if err := s.RecordDenied(WithKeyID(ctx, oldKey.ID), who, "admin"); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow("SELECT count(*) FROM sc_workspace_audit WHERE workspace=$1 AND credential_id=$2", who.Workspace, oldKey.ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("late denial restored erased key: %d, %v", count, err)
			}
			for _, c := range []context.Context{old, WithIdentityIssuedAt(ctx, time.Now().Add(40*time.Second).Unix()), WithActionAssertion(fresh, issued(old)), WithActionAssertion(ctx, 0)} {
				_, err = s.LimitActor(c, who)
				wantErr(t, err, "forbidden")
				_, err = s.PreviewSlug(c, draft["revision"].(string), who)
				wantErr(t, err, "not_found")
				_, err = s.ReleaseRevision(c, who, "kept", live["release_id"].(string))
				wantErr(t, err, "not_found")
				_, err = s.FetchMetadata(c, "kept", "", "/", who, file.Digest)
				wantErr(t, err, "not_found")
				_, err = execOp(t, s, c, who, "create_site", args{"name": "Late", "slug": "late", "request_id": "old"})
				wantErr(t, err, "forbidden")
			}
			if _, err = s.LimitActor(fresh, who); err != nil {
				t.Fatalf("new sign-in denied: %v", err)
			}
			if _, err = s.FetchMetadata(fresh, "kept", "", "/", who, file.Digest); err != nil {
				t.Fatal(err)
			}
			key, _, err := s.CreateKey(fresh, who, "New credential", "publisher", 1)
			if err != nil {
				t.Fatal(err)
			}
			mustOp(t, s, WithKeyID(ctx, key.ID), who, "create_site", args{"name": "Fresh", "slug": "fresh", "request_id": "new"})
		})
	}
}
