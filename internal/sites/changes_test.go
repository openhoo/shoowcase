package sites

import (
	"context"
	"database/sql"
	"testing"
)

func TestPublicationGenerationAndAudience(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			ctx := context.Background()
			dsn := testDSN(t, driver)
			s := openTest(t, driver, dsn)
			otherReplica := openTest(t, driver, dsn)
			site := mustOp(t, s, ctx, author, "create_site", args{"name": "Example", "slug": "example", "request_id": "initial"})
			upload := func(prev any, content, id string) map[string]any {
				return mustOp(t, s, ctx, author, "upload_files", args{"site_id": "example", "expected_revision": prev, "files": []args{{"path": "index.html", "text": content}}, "request_id": id})
			}
			first := upload(site["revision"], "<p>1</p>", "upload1")
			publish := func(rev any, id string) args { return args{"site_id": "example", "revision": rev, "request_id": id} }
			firstRelease := mustOp(t, s, ctx, author, "publish_site", publish(first["revision"], "release1"))
			access := func(rev any, vis, id string) args {
				return args{"site_id": "example", "revision": rev, "visibility": vis, "request_id": id}
			}
			public := access(first["revision"], "public", "public")
			old := approvalFor(t, s, ctx, author, "set_site_access", public)
			// A newer instruction about the audience matters even when bytes have not changed.
			mustOp(t, otherReplica, ctx, author, "set_site_access", access(first["revision"], "offline", "off"))
			mustOp(t, otherReplica, ctx, author, "set_site_access", access(first["revision"], "workspace", "internal"))
			if _, err := execOp(t, s, old, author, "set_site_access", public); err == nil {
				t.Fatal("used superseded review")
			} else {
				wantErr(t, err, "conflict")
			}
			fresh := approvalFor(t, s, ctx, author, "set_site_access", public)
			mustOp(t, otherReplica, fresh, author, "set_site_access", public)
			// Retrying an already completed request returns its historical receipt.
			mustOp(t, s, fresh, author, "set_site_access", public)
			denied := WithPrivateDelivery(ctx)
			checkAudience := func(m map[string]any) {
				t.Helper()
				if m["configured_visibility"] != "public" || m["visibility"] != "workspace" {
					t.Fatalf("wrong instance/site audience: %v", m)
				}
			}
			list := mustOp(t, s, denied, reader, "list_sites", args{})["sites"].([]map[string]any)
			if len(list) != 1 {
				t.Fatal(list)
			}
			checkAudience(list[0])
			checkAudience(mustOp(t, s, denied, reader, "get_site", args{"site_id": "example"}))
			checkAudience(mustOp(t, s, denied, author, "set_site_access", public))
			secondArgs := args{"site_id": "example", "expected_revision": first["revision"], "request_id": "upload2", "files": []args{{"path": "index.html", "text": "<p>2</p>"}}}
			second := mustOp(t, s, denied, author, "upload_files", secondArgs)
			checkAudience(second)
			if got := mustOp(t, s, ctx, author, "upload_files", secondArgs); got["visibility"] != "public" {
				t.Fatal(got)
			}
			two := publish(second["revision"], "release2")
			appr := approvalFor(t, s, ctx, author, "publish_site", two)
			// The release no longer needs an approval after this audience change; the stale
			// approval flow must nevertheless fail rather than updating the private release.
			mustOp(t, otherReplica, ctx, author, "set_site_access", access(first["revision"], "workspace", "narrow"))
			_, err := execOp(t, s, appr, author, "publish_site", two)
			wantErr(t, err, "conflict")
			current := mustOp(t, s, ctx, reader, "get_site", args{"site_id": "example"})
			if current["release_id"] != firstRelease["release_id"] {
				t.Fatalf("stale approval changed release")
			}
			// An authorized publisher can explicitly issue the operation again on current state.
			secondRelease := mustOp(t, s, ctx, author, "publish_site", two)
			pubTwo := access(second["revision"], "public", "public2")
			mustOp(t, s, approvalFor(t, s, ctx, author, "set_site_access", pubTwo), author, "set_site_access", pubTwo)
			rollback := func(target, expected any, id string) args {
				return args{"site_id": "example", "release_id": target, "expected_release_id": expected, "request_id": id}
			}
			rb := rollback(firstRelease["release_id"], secondRelease["release_id"], "rb")
			staleRollback := approvalFor(t, s, ctx, author, "rollback_site", rb)
			for _, rr := range []args{rollback(firstRelease["release_id"], secondRelease["release_id"], "other-rb"), rollback(secondRelease["release_id"], firstRelease["release_id"], "back-again")} {
				mustOp(t, otherReplica, approvalFor(t, s, ctx, author, "rollback_site", rr), author, "rollback_site", rr)
			}
			_, err = execOp(t, s, staleRollback, author, "rollback_site", rb)
			wantErr(t, err, "conflict")
			meta, err := s.FetchMetadata(ctx, "example", "", "index.html", Actor{}, "")
			if err != nil || meta.NotModified || meta.Data != nil || meta.Size != len("<p>2</p>") {
				t.Fatal(meta, err)
			}
			match, err := s.FetchMetadata(ctx, "example", "", "index.html", Actor{}, meta.Digest)
			if err != nil || !match.NotModified || match.Data != nil || match.Size != meta.Size {
				t.Fatal(match, err)
			}
			_, err = s.FetchMetadata(denied, "example", "", "index.html", Actor{}, meta.Digest)
			wantErr(t, err, "not_found")
			_, err = execOp(t, s, WithApproval(ctx, 0), author, "rollback_site", rb)
			wantErr(t, err, "conflict")
		})
	}
}

// Install the v0.1 layout by hand rather than constructing the fixture through
// Open: this catches migrations that happen to work only with an empty database.
func TestUpgradeFromInitialRelease(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			ctx := context.Background()
			dsn := testDSN(t, driver)
			driverName := driver
			if driver == "postgres" {
				driverName = "pgx"
			}
			db, err := sql.Open(driverName, dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			exec := func(q string, args ...any) {
				t.Helper()
				if _, err := db.Exec(q, args...); err != nil {
					t.Fatal(err)
				}
			}
			// All remaining v0.1 tables are created by the baseline schema below; the
			// critical pre-upgrade sites table lacks the publication_generation column.
			exec("CREATE TABLE sc_workspace (id TEXT PRIMARY KEY, used_bytes BIGINT NOT NULL DEFAULT 0, changes BIGINT NOT NULL DEFAULT 0)")
			exec("CREATE TABLE sc_sites (id TEXT PRIMARY KEY, workspace TEXT NOT NULL REFERENCES sc_workspace(id), name TEXT NOT NULL, visibility TEXT NOT NULL, draft TEXT NOT NULL, live TEXT NOT NULL, release_id TEXT NOT NULL)")
			for _, q := range schema {
				exec(q)
			}
			rev, release := id(), id()
			body := []byte("<h1>From v0.1.0</h1>")
			digest := sum(body)
			exec("INSERT INTO sc_workspace(id,used_bytes,changes) VALUES($1,$2,3)", author.Workspace, len(body))
			exec("INSERT INTO sc_sites(id,workspace,name,visibility,draft,live,release_id) VALUES('old',$1,'Old site','workspace',$2,$2,$3)", author.Workspace, rev, release)
			exec("INSERT INTO sc_revisions VALUES($1,'old',12345)", rev)
			exec("INSERT INTO sc_releases VALUES($1,'old',$2,12345)", release, rev)
			exec("INSERT INTO sc_blobs VALUES($1,$2)", digest, body)
			exec("INSERT INTO sc_files VALUES($1,'index.html',$2,$3,'text/html; charset=utf-8')", rev, digest, len(body))
			s := openTest(t, driver, dsn)
			saved := mustOp(t, s, ctx, reader, "get_site", args{"site_id": "old", "path": "index.html"})
			if saved["release_id"] != release || saved["revision"] != rev || saved["file"].(map[string]any)["text"] != string(body) {
				t.Fatal(saved)
			}
			var version, gen int64
			if e := db.QueryRow("SELECT (SELECT MAX(version) FROM sc_schema_migrations),publication_generation FROM sc_sites WHERE id='old'").Scan(&version, &gen); e != nil || gen != 1 || version != 8 {
				t.Fatal(version, gen, e)
			}
			public := args{"site_id": "old", "revision": rev, "visibility": "public", "request_id": "share"}
			req := approvalFor(t, s, ctx, author, "set_site_access", public)
			// A second instance picks up the same generation after restart and a new
			// approval can proceed; opening it does not re-run the migration.
			second := openTest(t, driver, dsn)
			mustOp(t, second, req, author, "set_site_access", public)
			if err := db.QueryRow("SELECT publication_generation FROM sc_sites WHERE id='old'").Scan(&gen); err != nil || gen != 2 {
				t.Fatalf("generation after upgrade=%d (%v)", gen, err)
			}
			var used, changes int64
			if err := db.QueryRow("SELECT used_bytes,changes FROM sc_workspace").Scan(&used, &changes); err != nil || used != int64(len(body)) || changes != 4 {
				t.Fatalf("usage=%d changes=%d (%v)", used, changes, err)
			}
		})
	}
}
