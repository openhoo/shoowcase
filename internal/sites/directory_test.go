package sites

import (
	"context"
	"slices"
	"testing"
)

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

func TestPersonalAndAdminSiteLists(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			f := newQuotaFixture(t, driver)
			key, _, err := f.app.CreateKey(f.ctx, f.writer, "Writer's agent", "editor", 1)
			if err != nil {
				t.Fatal(err)
			}
			keyCtx := WithKeyID(f.ctx, key.ID)
			// The member may list sites assigned to a reader group even though they cannot charge it.
			g := f.group("Reviewers", "reader", f.writer)
			hidden := mustOp(t, f.app, f.ctx, f.outsider, "create_site", newSite("a-hidden"))
			mustOp(t, f.app, f.ctx, f.writer, "upload_files", quotaUpload("a-hidden", hidden["revision"], "index.html", "edited", "edit-only"))
			mustOp(t, f.app, keyCtx, f.writer, "create_site", newSite("b-owned"))
			mustOp(t, f.app, f.ctx, f.outsider, "create_site", newSite("c-private"))
			mustOp(t, f.app, f.ctx, f.admin, "create_site", newSite("d-group"))
			mustOp(t, f.app, f.ctx, f.writer, "create_site", newSite("e-both"))
			mustOp(t, f.app, f.ctx, f.foreign, "create_site", newSite("f-foreign"))
			for _, site := range []string{"d-group", "e-both"} {
				if err := f.app.AssignSiteGroup(f.ctx, f.admin, site, g.ID, 1); err != nil {
					t.Fatal(err)
				}
			}
			checkRows := func(rows []DirectorySite, err error, want []string) {
				t.Helper()
				var ids []string
				for _, row := range rows {
					ids = append(ids, row.ID)
				}
				if err != nil || !slices.Equal(ids, want) {
					t.Fatalf("directory: got %v, want %v; err %v", ids, want, err)
				}
			}
			checkMCP := func(ctx context.Context, actor Actor, want []string) {
				t.Helper()
				// A page contains one visible site; hidden rows before or between visible sites
				// must not consume the limit or be disclosed in a continuation cursor.
				cursor := ""
				for i, id := range want {
					page := mustOp(t, f.peer, ctx, actor, "list_sites", args{"cursor": cursor, "limit": 1})
					rows := page["sites"].([]map[string]any)
					if len(rows) != 1 || rows[0]["site_id"] != id {
						t.Fatalf("MCP page after %q: got %v, want %q", cursor, rows, id)
					}
					cursor = page["next_cursor"].(string)
					if i == len(want)-1 && cursor != "" || i < len(want)-1 && cursor != id {
						t.Fatalf("unexpected cursor %q after %q", cursor, id)
					}
				}
			}
			checkPersonal := func(actor Actor, want ...string) {
				t.Helper()
				rows, err := f.peer.Directory(f.ctx, actor)
				checkRows(rows, err, want)
				checkMCP(f.ctx, actor, want)
			}
			checkPersonal(f.writer, "b-owned", "d-group", "e-both")
			checkMCP(keyCtx, f.writer, []string{"b-owned", "d-group", "e-both"})
			checkPersonal(f.admin, "d-group")
			all, err := f.peer.AdminDirectory(f.ctx, f.admin)
			checkRows(all, err, []string{"a-hidden", "b-owned", "c-private", "d-group", "e-both"})
			// The stored grant caps a presented admin claim. Personal keys cannot manage the directory either.
			claimedAdmin := f.writer
			claimedAdmin.Role = "admin"
			_, err = f.peer.AdminDirectory(f.ctx, claimedAdmin)
			wantErr(t, err, "forbidden")
			_, err = f.peer.AdminDirectory(keyCtx, f.admin)
			wantErr(t, err, "forbidden")

			if err := f.app.SetGroupMember(f.ctx, f.admin, g.ID, f.writer.Subject, false, g.Version); err != nil {
				t.Fatal(err)
			}
			checkPersonal(f.writer, "b-owned", "e-both")
			checkMCP(keyCtx, f.writer, []string{"b-owned", "e-both"})
			if err := f.app.SetGroupMember(f.ctx, f.admin, g.ID, f.writer.Subject, true, g.Version+1); err != nil {
				t.Fatal(err)
			}
			if err := f.app.AssignSiteGroup(f.ctx, f.admin, "d-group", "", 2); err != nil {
				t.Fatal(err)
			}
			checkPersonal(f.writer, "b-owned", "e-both")
		})
	}
}
