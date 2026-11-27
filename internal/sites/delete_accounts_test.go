package sites

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestEraseAccounts(t *testing.T) {
	for _, driver := range []string{"sqlite", "pgx"} {
		t.Run(driver, func(t *testing.T) {
			ctx := t.Context()
			dsn := testDSN(t, driver)
			s, peer := openTest(t, driver, dsn), openTest(t, driver, dsn)
			admin := Actor{"admin", "team", "admin"}
			// This subject is also another person's site slug. It must never make
			// the matching site or its creation audit part of the deletion.
			oidc := Actor{"handbook", "team", "publisher"}
			foreign := Actor{oidc.Subject, "neighbor", "publisher"}
			for _, actor := range []Actor{admin, oidc, foreign} {
				if _, err := s.EnrollAccount(ctx, actor, actor.Subject); err != nil {
					t.Fatal(err)
				}
			}
			const password = "disposable fixture password"
			m, err := s.CreateLocalAccount(ctx, admin, "test-user", password, "publisher")
			if err != nil {
				t.Fatal(err)
			}
			local, generation, err := s.AuthenticateLocal(ctx, admin.Workspace, m.DisplayName, password)
			if err != nil {
				t.Fatal(err)
			}
			group, err := s.CreateGroup(ctx, admin, "Other editors", "publisher")
			if err != nil {
				t.Fatal(err)
			}
			if err = s.SetGroupMember(ctx, admin, group.ID, local.Subject, true, group.Version); err != nil {
				t.Fatal(err)
			}
			key, token, err := s.CreateKey(ctx, local, "personal", "publisher", 1)
			if err != nil {
				t.Fatal(err)
			}

			expires := time.Now().Add(time.Minute)
			if ok, err := s.RedeemSetupKey(ctx, token, expires); err != nil || !ok {
				t.Fatalf("initial Quickstart redemption: %v, %v", ok, err)
			}
			if ok, err := peer.RedeemSetupKey(ctx, token, expires); err != nil || ok {
				t.Fatalf("repeated Quickstart redemption: %v, %v", ok, err)
			}

			create := func(a Actor, slug string) string {
				t.Helper()
				return mustOp(t, s, ctx, a, "create_site", args{"slug": slug, "name": slug, "request_id": id()})["revision"].(string)
			}
			upload := func(a Actor, slug, rev, body, req string) string {
				t.Helper()
				return mustOp(t, s, ctx, a, "upload_files", args{"site_id": slug, "request_id": req, "expected_revision": rev, "files": []args{{"path": "index.html", "text": body}}})["revision"].(string)
			}
			own := mustOp(t, s, WithKeyID(ctx, key.ID), local, "create_site", args{"slug": "own", "name": "A personal site", "request_id": "my-create"})["revision"].(string)
			collabReq := "collaborator-upload"
			own = upload(admin, "own", own, "collaborator's content", collabReq)
			pub := args{"site_id": "own", "revision": own, "request_id": "pub"}
			mustOp(t, s, ctx, local, "publish_site", pub)
			public := args{"site_id": "own", "visibility": "public", "revision": own, "request_id": "pub-access"}
			mustOp(t, s, approvalFor(t, s, ctx, local, "set_site_access", public), local, "set_site_access", public)
			keep := upload(oidc, "handbook", create(admin, "handbook"), "keep shared bytes", "edit-other")
			create(oidc, "owned-by-oidc")
			neighbor := upload(foreign, "neighboring", create(foreign, "neighboring"), "keep shared bytes", "neighbor-upload")
			old := create(local, "previously-removed")
			if err := s.DeleteSites(ctx, admin, []SiteDeletionState{{SiteSelection: SiteSelection{"previously-removed", old}, Visibility: "workspace", Generation: 1}}); err != nil {
				t.Fatal(err)
			}

			review := func(ids ...string) (AdminSelection, AdminSelectionReview) {
				t.Helper()
				sel := AdminSelection{Action: "delete-members", IDs: ids}
				r, err := peer.ReviewAdminSelection(ctx, admin, sel)
				if err != nil {
					t.Fatal(err)
				}
				return sel, r
			}
			sel, r := review(local.Subject, oidc.Subject)
			for _, phrase := range []string{"test-user (" + local.Subject + ")", "Site: A personal site (own)", "Site: owned-by-oidc (owned-by-oidc)"} {
				if !slices.Contains(r.Names, phrase) {
					t.Fatalf("missing %q in %v", phrase, r.Names)
				}
			}
			if len(r.Names) != 4 {
				t.Fatalf("unexpected cascade: %v", r.Names)
			}
			_, err = s.ReviewAdminSelection(ctx, oidc, sel)
			wantErr(t, err, "forbidden")
			_, err = s.ReviewAdminSelection(ctx, admin, AdminSelection{Action: "delete-members", IDs: []string{local.Subject, admin.Subject}})
			wantErr(t, err, "forbidden")
			_, err = s.ReviewAdminSelection(ctx, admin, AdminSelection{Action: "delete-members", IDs: []string{local.Subject, "unknown"}})
			wantErr(t, err, "not_found")
			wantErr(t, s.ApplyAdminSelection(WithKeyID(ctx, key.ID), admin, sel, r.Fingerprint), "forbidden")

			newRev := upload(local, "own", own, "keep shared bytes", "edit-own")
			if err := peer.ApplyAdminSelection(ctx, admin, sel, r.Fingerprint); err == nil {
				t.Fatal("old review erased an unreviewed edit")
			}
			sel, r = review(local.Subject, oidc.Subject)
			if err := s.SetMember(ctx, admin, oidc.Subject, "", true, "", false); err != nil {
				t.Fatal(err)
			}
			wantErr(t, peer.ApplyAdminSelection(ctx, admin, sel, r.Fingerprint), "conflict")
			sel, r = review(local.Subject, oidc.Subject)

			// A storage failure on the second account must restore the first
			// account, credentials and the public site, even across replicas.
			if _, err := s.db.ExecContext(ctx, "CREATE TABLE sc_test_restrict(workspace TEXT, subject TEXT, FOREIGN KEY(workspace,subject) REFERENCES sc_members(workspace,subject))"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.ExecContext(ctx, "INSERT INTO sc_test_restrict VALUES ($1,$2)", oidc.Workspace, oidc.Subject); err != nil {
				t.Fatal(err)
			}
			if err := s.ApplyAdminSelection(ctx, admin, sel, r.Fingerprint); err == nil {
				t.Fatal("induced failure did not roll back")
			}
			if _, err := peer.ResolveKey(ctx, token); err != nil {
				t.Fatal("key lost after rollback", err)
			}
			publicFile, err := peer.Fetch(ctx, "own", "", "index.html", Actor{})
			if err != nil || publicFile.Digest == "" {
				t.Fatal("public content lost after rollback", err)
			}
			if _, err := s.db.ExecContext(ctx, "DROP TABLE sc_test_restrict"); err != nil {
				t.Fatal(err)
			}

			if err := s.ApplyAdminSelection(ctx, admin, sel, r.Fingerprint); err != nil {
				t.Fatal(err)
			}
			_, err = peer.FetchConditional(ctx, "own", "", "index.html", Actor{}, publicFile.Digest)
			wantErr(t, err, "not_found")
			_, err = peer.FetchMetadata(ctx, "own", "", "index.html", Actor{}, publicFile.Digest)
			wantErr(t, err, "not_found")
			_, err = peer.ResolveKey(ctx, token)
			wantErr(t, err, "unauthorized")
			_, _, err = peer.AuthenticateLocal(ctx, local.Workspace, m.DisplayName, password)
			wantErr(t, err, "unauthorized")
			wantErr(t, peer.ValidateLocalSession(ctx, local, generation), "unauthorized")
			for _, rev := range []string{"", own, newRev} {
				if _, err := peer.Fetch(ctx, "own", rev, "index.html", admin); err == nil {
					t.Fatal("deleted site still available")
				}
			}
			for _, x := range []struct {
				site, rev string
				viewer    Actor
			}{{"handbook", keep, admin}, {"neighboring", neighbor, foreign}} {
				f, err := peer.Fetch(ctx, x.site, x.rev, "index.html", x.viewer)
				if err != nil || string(f.Data) != "keep shared bytes" {
					t.Fatalf("damaged unrelated site %s: %v", x.site, err)
				}
			}
			count := func(want int, query string, values ...any) {
				t.Helper()
				var n int
				if err := s.db.QueryRowContext(ctx, query, values...).Scan(&n); err != nil || n != want {
					t.Fatalf("%s: got %d, want %d (%v)", query, n, want, err)
				}
			}
			for _, table := range []string{"sc_members", "sc_local_credentials", "sc_group_members", "sc_agent_keys", "sc_requests", "sc_audit", "sc_workspace_audit", "sc_account_events"} {
				count(0, "SELECT COUNT(*) FROM "+table+" WHERE workspace=$1 AND subject IN ($2,$3)", admin.Workspace, local.Subject, oidc.Subject)
			}
			_, err = peer.RedeemSetupKey(ctx, token, expires)
			wantErr(t, err, "unauthorized")
			count(0, "SELECT COUNT(*) FROM sc_replay WHERE key=$1", "quickstart:"+key.ID)
			count(0, "SELECT COUNT(*) FROM sc_workspace_audit WHERE workspace='team' AND (target=$1 OR previous_value=$1 OR new_value=$1 OR target=$2 OR target='previously-removed')", local.Subject, key.ID)
			count(0, "SELECT COUNT(*) FROM sc_audit WHERE site IN ('own','owned-by-oidc','previously-removed')")
			count(1, "SELECT COUNT(*) FROM sc_workspace_audit WHERE workspace='team' AND action='create_site' AND target='handbook'")
			count(1, "SELECT COUNT(*) FROM sc_requests WHERE workspace='team' AND subject=$1 AND request_id=$2 AND result='{}'", admin.Subject, collabReq)
			count(0, "SELECT COUNT(*) FROM sc_blobs WHERE digest=$1", sum([]byte("collaborator's content")))
			count(1, "SELECT COUNT(*) FROM sc_blobs WHERE digest=$1", sum([]byte("keep shared bytes")))
			count(2, "SELECT COUNT(*) FROM sc_workspace_audit WHERE workspace='team' AND action='account_deleted' AND subject=$1 AND target=''", admin.Subject)
			count(1, "SELECT COUNT(*) FROM sc_members WHERE workspace=$1 AND subject=$2", foreign.Workspace, foreign.Subject)
			count(3, "SELECT COUNT(*) FROM sc_reserved_sites WHERE id IN ('own','owned-by-oidc','previously-removed')")
			q, err := s.Quotas(ctx, admin)
			if err != nil || q.Workspace.Usage.Sites != 1 || q.Workspace.Usage.Bytes != int64(len("keep shared bytes")) {
				t.Fatalf("quota after purge: %+v (%v)", q, err)
			}
			fresh, err := s.CreateLocalAccount(ctx, admin, m.DisplayName, password, "reader")
			if err != nil || fresh.Subject == local.Subject || !strings.HasPrefix(fresh.Subject, LocalSubjectPrefix) {
				t.Fatal("username not freed for an independent account", err)
			}
		})
	}
}
