package sites

import (
	"context"
	"testing"
	"time"
)

func TestPermanentSiteDeletion(t *testing.T) {
	for _, driver := range []string{"sqlite", "pgx"} {
		t.Run(driver, func(t *testing.T) {
			ctx := t.Context()
			s := openTest(t, driver, testDSN(t, driver))
			admin := Actor{"owner", "one", "admin"}
			foreign := Actor{"owner", "two", "admin"}
			for _, a := range []Actor{admin, foreign} {
				if _, err := s.EnrollAccount(ctx, a, "Owner"); err != nil {
					t.Fatal(err)
				}
			}
			group, err := s.CreateGroup(ctx, admin, "Field", "editor")
			if err != nil {
				t.Fatal(err)
			}
			create := func(a Actor, slug, groupID string) SiteSelection {
				p := args{"name": slug, "slug": slug, "request_id": "create-" + slug}
				if groupID != "" {
					p["group_id"] = groupID
				}
				out := mustOp(t, s, ctx, a, "create_site", p)
				return SiteSelection{slug, out["revision"].(string)}
			}
			uploadFile := func(a Actor, ref SiteSelection, path, text string) SiteSelection {
				p := args{"site_id": ref.Site, "expected_revision": ref.Revision, "request_id": id(), "files": []args{{"path": path, "text": text}}}
				out := mustOp(t, s, ctx, a, "upload_files", p)
				return SiteSelection{ref.Site, out["revision"].(string)}
			}
			a := uploadFile(admin, create(admin, "a", group.ID), "notes.css", "a{}")
			b := create(admin, "b", group.ID)
			f := uploadFile(foreign, create(foreign, "separate", ""), "shared.css", "a{}")
			released := uploadFile(admin, create(admin, "released", ""), "index.html", "<p>kept</p>")
			mustOp(t, s, ctx, admin, "publish_site", args{"site_id": released.Site, "request_id": "publish", "revision": released.Revision})
			mustOp(t, s, ctx, admin, "set_site_access", args{"site_id": released.Site, "request_id": "offline", "revision": released.Revision, "visibility": "offline"})

			before, err := s.Quotas(ctx, admin)
			if err != nil {
				t.Fatal(err)
			}
			review := func(actor Actor, refs ...SiteSelection) []SiteDeletionState {
				t.Helper()
				rows, err := s.ReviewSiteDeletion(ctx, actor, refs)
				if err != nil {
					t.Fatal(err)
				}
				var states []SiteDeletionState
				for _, row := range rows {
					states = append(states, row.SiteDeletionState)
				}
				return states
			}
			oldSelection := review(admin, a, b)
			foreignSelection := review(foreign, f)
			wantErr(t, s.DeleteSites(ctx, admin, []SiteDeletionState{oldSelection[0], foreignSelection[0]}), "not_found")
			wantErr(t, s.DeleteSites(ctx, admin, []SiteDeletionState{oldSelection[0], oldSelection[0]}), "invalid_argument")
			wantErr(t, s.DeleteSites(ctx, Actor{admin.Subject, admin.Workspace, "publisher"}, oldSelection), "forbidden")
			peer := Actor{"other-admin", admin.Workspace, "admin"}
			if _, err = s.EnrollAccount(ctx, peer, "Other administrator"); err != nil {
				t.Fatal(err)
			}
			beforeSuspension := review(peer, a)
			if err := s.SetMember(ctx, admin, peer.Subject, "", true, "", false); err != nil {
				t.Fatal(err)
			}
			wantErr(t, s.DeleteSites(ctx, peer, beforeSuspension), "forbidden")
			b = uploadFile(admin, b, "b.css", "b{}")
			wantErr(t, s.DeleteSites(ctx, admin, oldSelection), "conflict")
			// Access changes can restore the exact prior state; generation still invalidates review.
			staleAccess := review(admin, a, released)
			for _, v := range []string{"workspace", "offline"} {
				mustOp(t, s, ctx, admin, "set_site_access", args{"site_id": released.Site, "request_id": id(), "revision": released.Revision, "visibility": v})
			}
			wantErr(t, s.DeleteSites(ctx, admin, staleAccess), "conflict")
			if _, err = s.Fetch(ctx, a.Site, a.Revision, "notes.css", admin); err != nil {
				t.Fatal("partially deleted selection", err)
			}
			// Simulate an old process that created this site after the address backfill.
			if _, err = s.db.ExecContext(ctx, "DELETE FROM sc_reserved_sites WHERE id=$1", a.Site); err != nil {
				t.Fatal(err)
			}

			// An unchanged cross-tenant manifest acquires KEY SHARE for its
			// existing digest. Cleanup may run (and collect b.css) while that FK
			// lock is still held; waiting for it can deadlock with a concurrent upsert.
			deleteCtx := ctx
			if driver == "pgx" {
				tx, err := s.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				var locked string
				if err = tx.QueryRowContext(ctx, "SELECT digest FROM sc_blobs WHERE digest=$1 FOR KEY SHARE", sum([]byte("a{}"))).Scan(&locked); err != nil {
					t.Fatal(err)
				}
				short, cancel := context.WithTimeout(ctx, 3*time.Second)
				defer cancel()
				deleteCtx = short
			}
			if err = s.DeleteSites(deleteCtx, admin, review(admin, a, b)); err != nil {
				t.Fatal(err)
			}
			after, err := s.Quotas(ctx, admin)
			if err != nil {
				t.Fatal(err)
			}
			if after.Workspace.Usage.Sites != 1 || after.Workspace.Usage.Bytes != int64(len("<p>kept</p>")) || after.Groups[0].Usage != (QuotaUsage{}) || before.Groups[0].Usage.Sites != 2 {
				t.Fatal("quota mismatch following deletion", after)
			}
			if _, err = s.Fetch(ctx, a.Site, a.Revision, "notes.css", admin); err == nil {
				t.Fatal("deleted preview remains accessible")
			}
			if v, err := s.Fetch(ctx, f.Site, f.Revision, "shared.css", foreign); err != nil || string(v.Data) != "a{}" {
				t.Fatalf("shared content lost: %v", err)
			}
			if v, err := s.Fetch(ctx, released.Site, released.Revision, "index.html", admin); err != nil || string(v.Data) != "<p>kept</p>" {
				t.Fatal("offline release damaged", err)
			}
			var n int
			if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sc_blobs WHERE digest=$1", sum([]byte("b{}"))).Scan(&n); err != nil || n != 0 {
				t.Fatalf("unreferenced bytes retained: %d %v", n, err)
			}
			if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sc_workspace_audit WHERE workspace=$1 AND action='delete_site'", admin.Workspace).Scan(&n); err != nil || n != 2 {
				t.Fatalf("missing audit: %d %v", n, err)
			}
			// Permanently remove both a released offline site and a live site with
			// multiple historical releases, including a rollback since the initial review.
			live := uploadFile(admin, create(admin, "live", ""), "index.html", "release one")
			first := mustOp(t, s, ctx, admin, "publish_site", args{"site_id": live.Site, "revision": live.Revision, "request_id": id()})
			live = uploadFile(admin, live, "index.html", "release two")
			stalePublish := review(admin, released, live)
			pub := args{"site_id": live.Site, "revision": live.Revision, "request_id": "publish-second"}
			second := mustOp(t, s, ctx, admin, "publish_site", pub)
			wantErr(t, s.DeleteSites(ctx, admin, stalePublish), "conflict")
			staleRollback := review(admin, released, live)
			mustOp(t, s, ctx, admin, "rollback_site", args{"site_id": live.Site, "release_id": first["release_id"], "expected_release_id": second["release_id"], "request_id": id()})
			wantErr(t, s.DeleteSites(ctx, admin, staleRollback), "conflict")
			final := review(admin, released, live)
			if err := s.DeleteSites(ctx, admin, final); err != nil {
				t.Fatal(err)
			}
			wantErr(t, s.DeleteSites(ctx, admin, final), "not_found")
			for _, x := range final {
				for _, table := range []string{"sc_releases", "sc_revisions", "sc_site_groups"} {
					if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE site=$1", x.Site).Scan(&n); err != nil || n != 0 {
						t.Fatalf("%s keeps %d references to %s: %v", table, n, x.Site, err)
					}
				}
				if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sc_workspace_audit WHERE workspace=$1 AND action='delete_site' AND target=$2 AND live_revision=$3 AND release_id=$4", admin.Workspace, x.Site, x.Live, x.Release).Scan(&n); err != nil || n != 1 {
					t.Fatalf("lost retained release audit for %s: %d %v", x.Site, n, err)
				}
			}
			quotas, err := s.Quotas(ctx, admin)
			if err != nil || quotas.Workspace.Usage != (QuotaUsage{}) {
				t.Fatal("retained workspace quota", quotas.Workspace.Usage, err)
			}
			_, err = execOp(t, s, ctx, admin, "publish_site", pub)
			wantErr(t, err, "not_found")
			_, err = execOp(t, s, ctx, admin, "create_site", args{"slug": live.Site, "name": live.Site, "request_id": "create-" + live.Site})
			wantErr(t, err, "not_found")
			for _, slug := range []string{a.Site, b.Site, released.Site, live.Site} {
				_, err = execOp(t, s, ctx, foreign, "create_site", args{"slug": slug, "name": "New owner", "request_id": id()})
				wantErr(t, err, "conflict")
			}
		})
	}
}
