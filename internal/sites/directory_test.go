package sites

import "testing"

func TestWorkspaceDirectoryMetadata(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			ctx := t.Context()
			s, err := Open(ctx, driver, testDSN(t, driver))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			author := Actor{Workspace: "team", Subject: "member", Role: "publisher"}
			other := Actor{Workspace: "foreign", Subject: "member", Role: "publisher"}
			admin := Actor{Workspace: "team", Subject: "admin", Role: "admin"}
			for _, who := range []Actor{admin, author, other} {
				if _, e := s.EnrollAccount(ctx, who, who.Subject); e != nil {
					t.Fatal(e)
				}
			}
			mustOp(t, s, ctx, other, "create_site", args{"name": "Elsewhere", "slug": "other", "request_id": "create"})
			first := mustOp(t, s, ctx, author, "create_site", args{"name": "Field notes", "slug": "notes", "request_id": "create"})
			p := args{"site_id": "notes", "expected_revision": first["revision"], "request_id": "upload", "files": []args{{"path": "index.html", "text": "<p>Notes</p>"}, {"path": "style.css", "text": "p{}"}}}
			next := mustOp(t, s, ctx, author, "upload_files", p)
			release := mustOp(t, s, ctx, author, "publish_site", args{"site_id": "notes", "revision": next["revision"], "request_id": "publish"})
			share := args{"site_id": "notes", "visibility": "public", "revision": release["revision"], "request_id": "share"}
			mustOp(t, s, approvalFor(t, s, ctx, author, "set_site_access", share), author, "set_site_access", share)
			all, err := s.Directory(ctx, author)
			if err != nil || len(all) != 1 {
				t.Fatal(all, err)
			}
			x := all[0]
			if x.ID != "notes" || x.Name != "Field notes" || x.Visibility != "public" || x.FileCount != 2 || x.Draft != next["revision"] || x.Live != next["revision"] {
				t.Fatalf("unexpected entry %+v", x)
			}
			var stamp int64
			if err := s.db.QueryRowContext(ctx, "SELECT created_at FROM sc_audit WHERE workspace=$1 AND request_id='share'", author.Workspace).Scan(&stamp); err != nil || x.Updated.UnixMilli() != stamp {
				t.Fatal("directory omitted the audience change time", err)
			}
			hidden, err := s.Directory(WithPrivateDelivery(ctx), author)
			if err != nil || len(hidden) != 1 || hidden[0].Visibility != "workspace" {
				t.Fatal("global override ignored", hidden, err)
			}
			if err := s.SetMember(ctx, admin, author.Subject, "", true, "", false); err != nil {
				t.Fatal(err)
			}
			if rows, err := s.Directory(ctx, author); err == nil || len(rows) > 0 {
				t.Fatal("suspended account retained directory access")
			}
		})
	}
}
