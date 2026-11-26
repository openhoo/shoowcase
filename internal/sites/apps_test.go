package sites

import (
	"strings"
	"testing"
)

func TestGuestMarkup(t *testing.T) {
	for _, tt := range []struct{ page, src, want string }{
		{"index.html", "./app.js", "app.js"}, {"guide/index.html", "../ui/app.js", "ui/app.js"}, {"guide/index.html", "/js/APP.JS", "js/APP.JS"},
	} {
		full := "<main><label for=n>Quantity</label><input id=n type=number min=0 value=1><button type=button disabled>Calculate</button><output for=n>1</output></main><SCRIPT SRC='" + tt.src + "'></SCRIPT>"
		if _, err := prepare(fileInput{Path: tt.page, Text: &full}); err != nil {
			t.Fatal(err)
		}
		if got := GuestScript(tt.page, []byte(full)); got != tt.want {
			t.Fatalf("script %s: got %q, want %q", tt.src, got, tt.want)
		}
	}
	for _, page := range []string{
		"<script src='https://example.invalid/x.js'></script>", "<script src='//example.invalid/x.js'></script>",
		"<script src=app.js>inline()</script>", "<script type=module src=app.js></script>",
		"<script id=unused src=app.js></script>", "<script src=app.js></script><script src=app.js></script>",
		"<script src='app.js?x'></script>", "<script src='app.js#x'></script>", "<script src='%2fapp.js'></script>",
		"<script src='/auth/x.js'></script>", "<script src='../outside.js'></script>",
		"<input type=file>", "<input type=password>", "<input type=image src=x.png>", "<button formaction='https://outside.invalid'>Go</button>",
		"<form><input></form>", "<button onclick='doThings()'>Go</button>",
	} {
		if _, e := prepare(fileInput{Path: "index.html", Text: &page}); e == nil {
			t.Errorf("accepted %q", page)
		}
	}
	for _, page := range []string{"<p>No script</p>", "<!-- <script src=app.js></script> -->", "<title>&lt;script src=app.js&gt;</title>", "<textarea><script src=unused.js></script></textarea>", "<script src=../../no.js></script>"} {
		if got := GuestScript("index.html", []byte(page)); got != "" {
			t.Errorf("false script %q from %q", got, page)
		}
	}
	for _, src := range []string{string([]byte{0xff}), "var x=1;\x00", strings.Repeat("x", MaxTextBytes+1)} {
		if _, e := prepare(fileInput{Path: "app.js", Text: &src}); e == nil {
			t.Fatal("accepted invalid JS text")
		}
	}
}

func TestCompanionAndMemberAudience(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			ctx := t.Context()
			dsn := testDSN(t, driver)
			s, peer := openTest(t, driver, dsn), openTest(t, driver, dsn)
			admin := Actor{"ops", reader.Workspace, "admin"}
			for _, a := range []Actor{reader, admin} {
				if _, e := s.EnrollAccount(ctx, a, a.Subject); e != nil {
					t.Fatal(e)
				}
			}
			site := "team-tools"
			q := func(tool string, p args) map[string]any { return mustOp(t, s, ctx, author, tool, p) }
			draft := q("create_site", args{"name": "Team tools", "slug": site, "request_id": "start"})["revision"].(string)
			html := "<script src='js/calc.js'></script>"
			draft = q("upload_files", args{"site_id": site, "expected_revision": draft, "files": []args{{"path": "index.html", "text": html}}, "request_id": "upload-page"})["revision"].(string)
			_, e := execOp(t, s, ctx, author, "publish_site", args{"site_id": site, "revision": draft, "request_id": "incomplete"})
			wantErr(t, e, "invalid_argument")
			src := "document.title = 'Budget';"
			draft = q("upload_files", args{"site_id": site, "expected_revision": draft, "files": []args{{"path": "js/calc.js", "text": src}}, "request_id": "upload-script"})["revision"].(string)
			rel := q("publish_site", args{"site_id": site, "revision": draft, "request_id": "release"})["release_id"].(string)
			fp, e := s.FetchCompanion(ctx, site, "", draft, "js/calc.js", reader)
			if e != nil || string(fp.Data) != src || fp.ContentType != "text/javascript; charset=utf-8" {
				t.Fatal(fp, e)
			}
			for _, a := range []Actor{{}, other} {
				_, e = peer.FetchCompanion(ctx, site, "", draft, "js/calc.js", a)
				wantErr(t, e, "not_found")
				_, e = peer.FetchCompanion(ctx, site, draft, draft, "js/calc.js", a)
				wantErr(t, e, "not_found")
			}
			before, e := peer.Fetch(ctx, site, "", "/", reader)
			if e != nil {
				t.Fatal(e)
			}
			key, _, e := peer.CreateKey(ctx, reader, "Read", "reader", 1)
			if e != nil {
				t.Fatal(e)
			}
			keyCtx := WithKeyID(ctx, key.ID)
			if _, e = peer.Fetch(keyCtx, site, "", "/", reader); e != nil {
				t.Fatal(e)
			}
			if e = peer.RevokeKey(ctx, reader, key.ID); e != nil {
				t.Fatal(e)
			}
			_, e = s.Fetch(keyCtx, site, "", "/", reader)
			wantErr(t, e, "not_found")
			if e = s.SetMember(ctx, admin, reader.Subject, "", true, "", false); e != nil {
				t.Fatal(e)
			}
			_, e = peer.FetchCompanion(ctx, site, "", draft, "js/calc.js", reader)
			wantErr(t, e, "not_found")
			_, e = peer.FetchConditional(ctx, site, "", "/", reader, before.Digest)
			wantErr(t, e, "not_found")
			_, e = peer.FetchMetadata(ctx, site, "", "/", reader, before.Digest)
			wantErr(t, e, "not_found")
			_, e = peer.PreviewSlug(ctx, draft, reader)
			wantErr(t, e, "not_found")
			_, e = peer.ReleaseRevision(ctx, reader, site, rel)
			wantErr(t, e, "not_found")
			pub := args{"site_id": site, "revision": draft, "visibility": "public", "request_id": "public"}
			mustOp(t, s, approvalFor(t, s, ctx, author, "set_site_access", pub), author, "set_site_access", pub)
			if _, e = s.FetchCompanion(ctx, site, "", draft, "js/calc.js", Actor{}); e != nil {
				t.Fatal(e)
			}
			_, e = peer.FetchCompanion(WithPrivateDelivery(ctx), site, "", draft, "js/calc.js", reader)
			wantErr(t, e, "not_found")
			next := q("upload_files", args{"site_id": site, "expected_revision": draft, "files": []args{{"path": "js/calc.js", "text": "document.title = 'Updated';"}}, "request_id": "update-script"})["revision"].(string)
			_, e = peer.FetchCompanion(ctx, site, "", next, "js/calc.js", Actor{})
			wantErr(t, e, "not_found")
			p := args{"site_id": site, "revision": next, "request_id": "release-update"}
			mustOp(t, s, approvalFor(t, s, ctx, author, "publish_site", p), author, "publish_site", p)
			_, e = peer.FetchCompanion(ctx, site, "", draft, "js/calc.js", Actor{})
			wantErr(t, e, "not_found")
			_, e = peer.FetchCompanion(ctx, site, next, draft, "js/calc.js", author)
			wantErr(t, e, "not_found")
			_, e = peer.FetchCompanion(ctx, site, draft, draft, "js/calc.js", Actor{})
			wantErr(t, e, "not_found")
			if f, e := peer.FetchCompanion(ctx, site, draft, draft, "js/calc.js", author); e != nil || string(f.Data) != src {
				t.Fatal(f, e)
			}
		})
	}
}
