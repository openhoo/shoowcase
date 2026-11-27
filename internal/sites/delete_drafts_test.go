package sites

import (
	"context"
	"testing"
	"time"
)

func TestDeleteDraftSites(t *testing.T) {
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
			create := func(a Actor, slug, groupID string) DraftSelection {
				p := args{"name": slug, "slug": slug, "request_id": "create-" + slug}
				if groupID != "" {
					p["group_id"] = groupID
				}
				out := mustOp(t, s, ctx, a, "create_site", p)
				return DraftSelection{slug, out["revision"].(string)}
			}
			uploadFile := func(a Actor, ref DraftSelection, path, text string) DraftSelection {
				p := args{"site_id": ref.Site, "expected_revision": ref.Revision, "request_id": id(), "files": []args{{"path": path, "text": text}}}
				out := mustOp(t, s, ctx, a, "upload_files", p)
				return DraftSelection{ref.Site, out["revision"].(string)}
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
			if _, err = s.ReviewDraftDeletion(ctx, admin, []DraftSelection{a, b}); err != nil {
				t.Fatal(err)
			}
			wantErr(t, s.DeleteDraftSites(ctx, admin, []DraftSelection{a, released}), "conflict")
			wantErr(t, s.DeleteDraftSites(ctx, admin, []DraftSelection{a, f}), "not_found")
			wantErr(t, s.DeleteDraftSites(ctx, admin, []DraftSelection{a, a}), "invalid_argument")
			wantErr(t, s.DeleteDraftSites(ctx, Actor{admin.Subject, admin.Workspace, "publisher"}, []DraftSelection{a}), "forbidden")
			oldB := b
			b = uploadFile(admin, b, "b.css", "b{}")
			wantErr(t, s.DeleteDraftSites(ctx, admin, []DraftSelection{a, oldB}), "conflict")
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
			if err = s.DeleteDraftSites(deleteCtx, admin, []DraftSelection{a, b}); err != nil {
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
			for _, slug := range []string{a.Site, b.Site} {
				_, err = execOp(t, s, ctx, foreign, "create_site", args{"slug": slug, "name": "New owner", "request_id": id()})
				wantErr(t, err, "conflict")
			}
		})
	}
}
