package sites

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type args = map[string]any

var author = Actor{"writer", "tenant-a", "publisher"}
var other = Actor{"other", "tenant-b", "admin"}
var reader = Actor{"read", "tenant-a", "reader"}

func execOp(t testing.TB, s *Store, ctx context.Context, a Actor, tool string, p args) (map[string]any, error) {
	t.Helper()
	b, e := json.Marshal(p)
	if e != nil {
		t.Fatal(e)
	}
	return s.Execute(ctx, a, tool, b)
}
func mustOp(t testing.TB, s *Store, ctx context.Context, a Actor, tool string, p args) map[string]any {
	t.Helper()
	o, e := execOp(t, s, ctx, a, tool, p)
	if e != nil {
		t.Fatalf("%s: %v", tool, e)
	}
	return o
}
func wantErr(t testing.TB, err error, code string) {
	t.Helper()
	var de *Error
	if !errors.As(err, &de) || de.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}
func testDSN(t testing.TB, driver string) string {
	t.Helper()
	if driver == "sqlite" {
		return filepath.Join(t.TempDir(), "test.sqlite")
	}
	dsn := os.Getenv("SHOOWCASE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set SHOOWCASE_TEST_POSTGRES_DSN to exercise PostgreSQL")
	}
	u, e := url.Parse(dsn)
	if e != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" {
		t.Fatal("SHOOWCASE_TEST_POSTGRES_DSN must be a postgres URL")
	}
	// Test in a unique schema, even when multiple packages run concurrently.
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := "sc_test_" + id()
	if _, e = db.Exec("CREATE SCHEMA " + schema); e != nil {
		db.Close()
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if _, e := db.Exec("DROP SCHEMA " + schema + " CASCADE"); e != nil {
			t.Errorf("dropping test schema: %v", e)
		}
		db.Close()
	})
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}
func openTest(t testing.TB, driver, dsn string) *Store {
	t.Helper()
	s, e := Open(context.Background(), driver, dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func TestPublicationLifecycle(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			ctx := context.Background()
			dsn := testDSN(t, driver)
			s := openTest(t, driver, dsn)
			s2 := openTest(t, driver, dsn)
			c := func(a Actor, tool string, p args) map[string]any { return mustOp(t, s, ctx, a, tool, p) }
			check := func(a Actor, tool string, p args, code string) {
				_, e := execOp(t, s, ctx, a, tool, p)
				wantErr(t, e, code)
			}
			create := args{"slug": "example", "name": "Example", "request_id": "create"}
			first := c(author, "create_site", create)
			if again := c(author, "create_site", create); again["revision"] != first["revision"] {
				t.Fatal("retry produced new revision")
			}
			check(author, "create_site", args{"slug": "example", "name": "Changed", "request_id": "create"}, "conflict")
			check(reader, "create_site", create, "forbidden")
			check(other, "get_site", args{"site_id": "example"}, "not_found")
			_, e := s.Fetch(ctx, "example", "", "", Actor{})
			wantErr(t, e, "not_found")
			check(author, "publish_site", args{"site_id": "example", "revision": first["revision"], "request_id": "empty"}, "invalid_argument")
			upload := func(rev any, body, req string) args {
				return args{"site_id": "example", "expected_revision": rev, "files": []args{{"path": "index.html", "text": body}, {"path": "shared.css", "text": "body{color:#182736}"}}, "request_id": req}
			}
			up := c(author, "upload_files", upload(first["revision"], "<h1>First</h1>", "u1"))
			rev1 := up["revision"].(string)
			check(author, "upload_files", upload(first["revision"], "<p>stale</p>", "stale"), "conflict")
			if _, e = s.PreviewSlug(ctx, rev1, other); e == nil {
				t.Fatal("foreign preview resolved")
			}
			if slug, e := s.PreviewSlug(ctx, rev1, reader); e != nil || slug != "example" {
				t.Fatal(slug, e)
			}
			f, e := s.Fetch(ctx, "example", rev1, "/", reader)
			if e != nil || string(f.Data) != "<h1>First</h1>" {
				t.Fatal("preview", f, e)
			}
			_, e = s.Fetch(ctx, "example", rev1, "shared.css", other)
			wantErr(t, e, "not_found")
			rel := c(author, "publish_site", args{"site_id": "example", "revision": rev1, "request_id": "p1"})
			pub := args{"site_id": "example", "revision": rev1, "visibility": "public", "request_id": "public"}
			check(author, "set_site_access", pub, "approval_required")
			if _, e := s.Fetch(ctx, "example", "", "shared.css", Actor{}); e == nil {
				t.Fatal("approval failure leaked content")
			}
			_, e = execOp(t, s, WithApproval(ctx), reader, "set_site_access", pub)
			wantErr(t, e, "forbidden")
			_, e = execOp(t, s, WithPrivateDelivery(WithApproval(ctx)), author, "set_site_access", pub)
			wantErr(t, e, "forbidden")
			mustOp(t, s, WithApproval(ctx), author, "set_site_access", pub)
			pubFile, e := s2.Fetch(ctx, "example", "", "/", Actor{})
			if e != nil || pubFile.Revision != rev1 {
				t.Fatal(pubFile, e)
			}
			cond, e := s2.FetchConditional(ctx, "example", "", "/", Actor{}, pubFile.Digest)
			if e != nil || !cond.NotModified || len(cond.Data) != 0 {
				t.Fatal("conditional public fetch", cond, e)
			}
			_, e = s.FetchConditional(WithPrivateDelivery(ctx), "example", "", "/", other, pubFile.Digest)
			wantErr(t, e, "not_found")
			f, e = s.Fetch(WithPrivateDelivery(ctx), "example", "", "/", reader)
			if e != nil || f.Visibility != "workspace" {
				t.Fatal("private instance", f, e)
			}
			up = c(author, "upload_files", upload(rev1, "<h1>Second</h1>", "u2"))
			rev2 := up["revision"].(string)
			p2 := args{"site_id": "example", "revision": rev2, "request_id": "p2"}
			check(author, "publish_site", p2, "approval_required")
			f, e = s.Fetch(ctx, "example", "", "", Actor{})
			if e != nil || f.Revision != rev1 {
				t.Fatal("failed public publish changed live", f, e)
			}
			rel2 := mustOp(t, s, WithApproval(ctx), author, "publish_site", p2)
			hist := c(reader, "get_site", args{"site_id": "example", "view": "releases", "limit": 1})
			if hist["next_cursor"] == "" {
				t.Fatal("missing history cursor")
			}
			hist2 := c(reader, "get_site", args{"site_id": "example", "view": "releases", "limit": 1, "cursor": hist["next_cursor"]})
			if hist2["next_cursor"] != "" {
				t.Fatal("bad release pagination")
			}
			if selected, e := s.ReleaseRevision(ctx, reader, "example", rel["release_id"].(string)); e != nil || selected != rev1 {
				t.Fatal(selected, e)
			}
			_, e = s.ReleaseRevision(ctx, other, "example", rel["release_id"].(string))
			wantErr(t, e, "not_found")
			rb := args{"site_id": "example", "release_id": rel["release_id"], "expected_release_id": rel2["release_id"], "request_id": "rollback"}
			check(author, "rollback_site", rb, "approval_required")
			mustOp(t, s, WithApproval(ctx), author, "rollback_site", rb)
			check(author, "set_site_access", args{"site_id": "example", "revision": rev2, "visibility": "workspace", "request_id": "stale-private"}, "conflict")
			c(author, "set_site_access", args{"site_id": "example", "revision": rev1, "visibility": "offline", "request_id": "offline"})
			_, e = s.FetchConditional(ctx, "example", "", "/", reader, pubFile.Digest)
			wantErr(t, e, "not_found")
			_, e = s.FetchConditional(ctx, "example", "", "/", Actor{}, pubFile.Digest)
			wantErr(t, e, "not_found")
			hidden := c(author, "publish_site", args{"site_id": "example", "revision": rev2, "request_id": "offline-publish"})
			if hidden["visibility"] != "offline" {
				t.Fatal(hidden)
			}
			ind := c(reader, "get_site", args{"site_id": "example", "revision": rev1, "path": "index.html"})
			if ind["file"].(map[string]any)["text"] != "<h1>First</h1>" {
				t.Fatal(ind)
			}
			// Rejected requests never create audit entries. Scoped deduplication still stores
			// the common stylesheet bytes only once even across a preview and two releases.
			var auditN, blobN int
			if e = s.db.QueryRow("SELECT count(*) FROM sc_audit WHERE request_id IN ('public','p1','p2','stale','empty')").Scan(&auditN); e != nil || auditN != 3 {
				t.Fatalf("audit=%d err=%v", auditN, e)
			}
			if e = s.db.QueryRow("SELECT count(*) FROM sc_blobs").Scan(&blobN); e != nil || blobN != 3 {
				t.Fatalf("blobs=%d err=%v", blobN, e)
			}
			// Two application instances editing the same version: exactly one succeeds.
			ch := make(chan error, 2)
			for i, store := range []*Store{s, s2} {
				go func() {
					_, e := execOp(t, store, ctx, author, "upload_files", upload(rev2, fmt.Sprintf("<p>%d</p>", i), fmt.Sprintf("race-%d", i)))
					ch <- e
				}()
			}
			var wins, loses int
			for range 2 {
				e := <-ch
				if e == nil {
					wins++
				} else {
					wantErr(t, e, "conflict")
					loses++
				}
			}
			if wins != 1 || loses != 1 {
				t.Fatal(wins, loses)
			}
			// The service reopens the database at an independent connection and still sees
			// the live immutable revision. A storage-limit failure cannot advance the draft.
			current := mustOp(t, s2, ctx, author, "get_site", args{"site_id": "example"})
			if _, e = s.db.Exec("UPDATE sc_workspace SET used_bytes=$1 WHERE id=$2", MaxWorkspaceManifestBytes, author.Workspace); e != nil {
				t.Fatal(e)
			}
			check(author, "upload_files", upload(current["revision"], "<p>over quota</p>", "quota"), "quota_exceeded")
			final := c(author, "get_site", args{"site_id": "example"})
			if final["revision"] != current["revision"] || final["live_revision"] != rev2 {
				t.Fatal(final)
			}
		})
	}
}
func TestValidation(t *testing.T) {
	invalid := []fileInput{}
	add := func(p, s string) { invalid = append(invalid, fileInput{Path: p, Text: &s}) }
	for _, p := range []string{"../escape.html", "auth/callback.html", "dir//a.html", ".secret.html", "a%2ehtml", "dir/../a.html", "test.js", "a.html/../../bad.html"} {
		add(p, "<p>safe</p>")
	}
	for _, s := range []string{"<script>alert(1)</script>", "<img src='https://outside.invalid/a.png'>", "<p onclick=x>Hello</p>", "<iframe src='/'>", "<form method='post'>", "<meta http-equiv=refresh content='0;url=/'>", "<a href='j&#97;vascript:foo'>link</a>", "<svg><title>hi</title></svg>", "<a href='//outside.invalid'>link</a>", "<img src='&#92;&#92;outside.invalid'>", "<link rel=preload href='/'>", "<style>body{background:image-set('https://outside.invalid/x.png')}</style>"} {
		add("index.html", s)
	}
	for _, s := range []string{"@import 'https://outside.invalid';", "body{background:url(https://outside.invalid)}", "x{cursor:iMaGe-SeT('/x')}", `b{background:u\72l('/xx')}`} {
		add("x.css", s)
	}
	add("x.css", strings.Repeat("a", MaxTextBytes+1))
	add("x.gif", "GIF89a")
	for _, f := range invalid {
		if _, e := prepare(f); e == nil {
			t.Errorf("accepted %s (%d bytes)", f.Path, len(*f.Text))
		}
	}
	css := "@media (min-width: 30rem) { main { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); background:linear-gradient(red, blue); } }"
	html := "<html lang=en><meta charset=utf-8><link rel=stylesheet href=/style.css><body><main><img src='images/b.png' alt='A tree'><a href='https://example.org/'>example</a><h1 class=x>OK</h1></main></body></html>"
	for _, f := range []fileInput{{Path: "style.css", Text: &css}, {Path: "index.html", Text: &html}} {
		if _, e := prepare(f); e != nil {
			t.Fatal(e)
		}
	}
}
func BenchmarkDelivery(b *testing.B) {
	for _, driver := range []string{"sqlite", "postgres"} {
		b.Run(driver, func(b *testing.B) {
			ctx := context.Background()
			s := openTest(b, driver, testDSN(b, driver))
			r := mustOp(b, s, ctx, author, "create_site", args{"slug": "bench", "name": "Bench", "request_id": "1"})
			r = mustOp(b, s, ctx, author, "upload_files", args{"site_id": "bench", "expected_revision": r["revision"], "files": []args{{"path": "index.html", "text": "<p>" + strings.Repeat("x", 32<<10) + "</p>"}}, "request_id": "2"})
			mustOp(b, s, ctx, author, "publish_site", args{"site_id": "bench", "revision": r["revision"], "request_id": "3"})
			f, e := s.Fetch(ctx, "bench", "", "", reader)
			if e != nil {
				b.Fatal(e)
			}
			for _, tc := range []struct{ name, digest string }{{"read_32KiB", ""}, {"conditional", f.Digest}} {
				b.Run(tc.name, func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						if _, e := s.FetchConditional(ctx, "bench", "", "", reader, tc.digest); e != nil {
							b.Fatal(e)
						}
					}
				})
			}
		})
	}
}

func TestMaterializeLocalLinks(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			ctx := context.Background()
			s := openTest(t, driver, testDSN(t, driver))
			r := mustOp(t, s, ctx, author, "create_site", args{"slug": "links", "name": "Links", "request_id": "1"})
			files := []args{{"path": "index.html", "text": "<a href='docs/?lang=en#chapter'>Docs</a><a href='https://example.org/external'>External</a>"}, {"path": "docs/index.html", "text": "<a href='../?home'>Home</a><a href='#chapter' id='chapter'>Jump</a><link rel=stylesheet href='/site.css?version=1'>"}, {"path": "site.css", "text": "p{color:blue}"}}
			r = mustOp(t, s, ctx, author, "upload_files", args{"site_id": "links", "expected_revision": r["revision"], "files": files, "request_id": "2"})
			live := mustOp(t, s, ctx, author, "publish_site", args{"site_id": "links", "revision": r["revision"], "request_id": "3"})
			for i, broken := range []string{"<a href='missing/'>404</a>", "<img src='/site.css' alt='wrong type'>", "<link rel=stylesheet href='/missing.css'>"} {
				r = mustOp(t, s, ctx, author, "upload_files", args{"site_id": "links", "expected_revision": r["revision"], "files": []args{{"path": "index.html", "text": broken}}, "request_id": fmt.Sprintf("u%d", i)})
				_, e := execOp(t, s, ctx, author, "publish_site", args{"site_id": "links", "revision": r["revision"], "request_id": fmt.Sprintf("p%d", i)})
				wantErr(t, e, "invalid_argument")
				got, e := s.Fetch(ctx, "links", "", "/docs/", reader)
				if e != nil || got.Revision != live["live_revision"] {
					t.Fatal(got, e)
				}
			}
		})
	}
}
