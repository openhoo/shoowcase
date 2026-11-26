package sites

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
)

type quotaFixture struct {
	t                                *testing.T
	ctx                              context.Context
	app, peer                        *Store
	admin, writer, outsider, foreign Actor
}

func newQuotaFixture(t *testing.T, driver string) *quotaFixture {
	t.Helper()
	dsn := testDSN(t, driver)
	f := &quotaFixture{t: t, ctx: WithIdentityIssuedAt(t.Context(), 100), app: openTest(t, driver, dsn), peer: openTest(t, driver, dsn),
		admin: Actor{"ops", "project", "admin"}, writer: Actor{"alice", "project", "publisher"}, outsider: Actor{"bob", "project", "publisher"}, foreign: Actor{"ops", "other-project", "admin"}}
	for _, a := range []Actor{f.admin, f.writer, f.outsider, f.foreign} {
		if _, err := f.app.EnrollAccount(f.ctx, a, a.Subject); err != nil {
			t.Fatal(err)
		}
	}
	return f
}
func (f *quotaFixture) group(name, role string, members ...Actor) Group {
	f.t.Helper()
	g, err := f.app.CreateGroup(f.ctx, f.admin, name, role)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, a := range members {
		if err = f.app.SetGroupMember(f.ctx, f.admin, g.ID, a.Subject, true, g.Version); err != nil {
			f.t.Fatal(err)
		}
		g.Version++
	}
	return g
}
func (f *quotaFixture) limits(group string, sites, bytes, version int64) {
	f.t.Helper()
	if err := f.app.SetQuota(f.ctx, f.admin, group, QuotaLimits{sites, bytes}, version); err != nil {
		f.t.Fatal(err)
	}
}
func (f *quotaFixture) view() QuotaOverview {
	f.t.Helper()
	v, err := f.peer.Quotas(f.ctx, f.admin)
	if err != nil {
		f.t.Fatal(err)
	}
	return v
}
func (f *quotaFixture) checkOp(ctx context.Context, a Actor, tool string, p args, code string) {
	f.t.Helper()
	_, err := execOp(f.t, f.peer, ctx, a, tool, p)
	wantErr(f.t, err, code)
}
func newSite(slug string) args {
	return args{"slug": slug, "name": slug, "request_id": "create-" + slug}
}
func quotaUpload(slug string, rev any, path, body, req string) args {
	return args{"site_id": slug, "expected_revision": rev, "files": []args{{"path": path, "text": body}}, "request_id": req}
}

func TestQuotaAttributionAndLimits(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			f := newQuotaFixture(t, driver)
			// Sites retained before a budget is enabled remain accessible.
			legacy := mustOp(t, f.app, f.ctx, f.writer, "create_site", newSite("legacy"))
			g := f.group("Editors", "editor", f.writer)
			second := f.group("Publishers", "publisher")
			_ = f.group("Viewers", "reader", f.writer)
			// With inherited-only group ceilings, legacy edits can still use the workspace.
			legacyUpload := quotaUpload("legacy", legacy["revision"], "index.html", "abc", "legacy-edit")
			legacy = mustOp(t, f.app, f.ctx, f.writer, "upload_files", legacyUpload)
			f.limits(g.ID, 1, 10, g.Version)
			key, _, err := f.app.CreateKey(f.ctx, f.writer, "Agent", "editor", 1)
			if err != nil {
				t.Fatal(err)
			}
			keyctx := WithKeyID(f.ctx, key.ID)
			opts, err := f.peer.CreationGroups(keyctx, f.writer)
			if err != nil || opts.AllowUnassigned || len(opts.Groups) != 1 || opts.Groups[0].ID != g.ID {
				t.Fatal(opts, err)
			}
			list := mustOp(t, f.peer, keyctx, f.writer, "list_site_groups", args{})
			if list["allow_unassigned"] != false || len(list["groups"].([]GroupOption)) != 1 {
				t.Fatal(list)
			}
			// Explicit foreign/unrelated group and omission with no eligible group both fail.
			wrong := newSite("wrong")
			wrong["group_id"] = second.ID
			f.checkOp(keyctx, f.writer, "create_site", wrong, "forbidden")
			f.checkOp(f.ctx, f.outsider, "create_site", newSite("bypass"), "forbidden")
			f.checkOp(f.ctx, f.writer, "upload_files", quotaUpload("legacy", legacy["revision"], "index.html", "def", "unassigned"), "forbidden")
			f.checkOp(f.ctx, f.outsider, "upload_files", quotaUpload("legacy", legacy["revision"], "index.html", "def", "unassigned"), "forbidden")
			if r := mustOp(t, f.peer, f.ctx, f.writer, "upload_files", legacyUpload); r["revision"] != legacy["revision"] {
				t.Fatal("retry before budget changed history")
			}
			mustOp(t, f.peer, f.ctx, f.outsider, "upload_files", args{"site_id": "legacy", "expected_revision": legacy["revision"], "request_id": "empty-legacy", "delete_paths": []string{"index.html"}})
			if err = f.app.SetGroupMember(f.ctx, f.admin, second.ID, f.writer.Subject, true, second.Version); err != nil {
				t.Fatal(err)
			}
			f.checkOp(keyctx, f.writer, "create_site", newSite("ambiguous"), "invalid_argument")
			if err = f.app.SetGroupMember(f.ctx, f.admin, second.ID, f.writer.Subject, false, second.Version+1); err != nil {
				t.Fatal(err)
			}
			p := newSite("guide")
			site := mustOp(t, f.app, keyctx, f.writer, "create_site", p)
			if site["group_id"] != g.ID {
				t.Fatal(site)
			}
			f.checkOp(keyctx, f.writer, "create_site", newSite("too-many"), "quota_exceeded")
			if got := mustOp(t, f.peer, keyctx, f.writer, "create_site", p); got["revision"] != site["revision"] {
				t.Fatal(got)
			}
			changedRequest := newSite("guide")
			changedRequest["group_id"] = g.ID
			f.checkOp(keyctx, f.writer, "create_site", changedRequest, "conflict")
			f.checkOp(f.ctx, f.outsider, "upload_files", quotaUpload("guide", site["revision"], "index.html", "abc", "unauthorized"), "forbidden")
			site = mustOp(t, f.app, keyctx, f.writer, "upload_files", quotaUpload("guide", site["revision"], "index.html", "abc", "v1"))
			// A second revision retaining the old 3 bytes and adding 4 bytes fills the 10-byte limit.
			p = quotaUpload("guide", site["revision"], "style.css", "1234", "v2")
			site = mustOp(t, f.app, keyctx, f.writer, "upload_files", p)
			f.checkOp(keyctx, f.writer, "upload_files", quotaUpload("guide", site["revision"], "index.html", "1", "overflow"), "quota_exceeded")
			if again := mustOp(t, f.peer, keyctx, f.writer, "upload_files", p); again["revision"] != site["revision"] {
				t.Fatal(again)
			}
			v := f.view()
			if v.Workspace.Usage != (QuotaUsage{2, 13}) || v.Groups[0].Usage != (QuotaUsage{1, 10}) || v.Sites[0].GroupID != g.ID || v.Sites[0].Bytes != 10 {
				t.Fatalf("%+v", v)
			}
			// Shrink both limits below today's usage. Publication and a genuine 0-byte revision still work.
			f.limits(g.ID, 0, 0, g.Version+1)
			f.limits("", 0, 0, 1)
			mustOp(t, f.peer, f.ctx, f.writer, "publish_site", args{"site_id": "guide", "revision": site["revision"], "request_id": "publish"})
			clearDraft := args{"site_id": "guide", "expected_revision": site["revision"], "request_id": "empty", "delete_paths": []string{"index.html", "style.css"}}
			f.checkOp(f.ctx, f.outsider, "upload_files", clearDraft, "forbidden")
			deleted := mustOp(t, f.peer, keyctx, f.writer, "upload_files", clearDraft)
			f.checkOp(keyctx, f.writer, "upload_files", quotaUpload("guide", deleted["revision"], "index.html", "x", "cannot-grow"), "quota_exceeded")
			wantErr(t, f.app.DeleteGroup(f.ctx, f.admin, g.ID, g.Version+2), "conflict")
			// A transfer checks actual retained bytes, even when the current revision is empty.
			f.limits(second.ID, -1, 9, second.Version+2)
			wantErr(t, f.app.AssignSiteGroup(f.ctx, f.admin, "guide", second.ID, 1), "quota_exceeded")
			if after := f.view(); after.Sites[0] != v.Sites[0] || after.Workspace.Usage != v.Workspace.Usage {
				t.Fatal(after)
			}
			f.limits(second.ID, -1, 10, second.Version+3)
			if err = f.app.AssignSiteGroup(f.ctx, f.admin, "guide", second.ID, 1); err != nil {
				t.Fatal(err)
			}
			wantErr(t, f.app.AssignSiteGroup(f.ctx, f.admin, "guide", "", 1), "conflict")
			if after := f.view(); after.Sites[0].GroupID != second.ID || after.Groups[0].Usage != (QuotaUsage{}) || after.Groups[1].Usage != (QuotaUsage{1, 10}) || after.Workspace.Usage != v.Workspace.Usage {
				t.Fatal(after)
			}
			if err = f.app.DeleteGroup(f.ctx, f.admin, g.ID, g.Version+2); err != nil {
				t.Fatal(err)
			}
			if err = f.app.AssignSiteGroup(f.ctx, f.admin, "guide", "", 2); err != nil {
				t.Fatal(err)
			}
			if after := f.view(); after.Sites[0].GroupID != "" || after.Sites[0].Version != 3 || after.Sites[0].Bytes != 10 {
				t.Fatal(after)
			}
		})
	}
}

func TestQuotaAdminBoundaries(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			f := newQuotaFixture(t, driver)
			g := f.group("Readers", "reader", f.writer)
			v := f.view()
			if v.Workspace.Limits != (QuotaLimits{MaxSitesPerWorkspace, MaxWorkspaceManifestBytes}) || v.Groups[0].Limits != (QuotaLimits{-1, -1}) {
				t.Fatal(v)
			}
			opts, err := f.peer.CreationGroups(f.ctx, f.writer)
			if err != nil || !opts.AllowUnassigned || len(opts.Groups) != 0 {
				t.Fatal(opts, err)
			}
			key, _, err := f.app.CreateKey(f.ctx, f.admin, "Agent", "reader", 1)
			if err != nil {
				t.Fatal(err)
			}
			keyctx := WithKeyID(f.ctx, key.ID)
			_, err = f.peer.CreationGroups(keyctx, f.admin)
			wantErr(t, err, "forbidden")
			_, err = f.peer.Quotas(keyctx, f.admin)
			wantErr(t, err, "forbidden")
			_, err = f.peer.Quotas(f.ctx, f.writer)
			wantErr(t, err, "forbidden")
			wantErr(t, f.peer.SetQuota(keyctx, f.admin, g.ID, QuotaLimits{0, 0}, 1), "forbidden")
			wantErr(t, f.peer.SetQuota(f.ctx, f.writer, "", QuotaLimits{0, 0}, 1), "forbidden")
			wantErr(t, f.peer.SetQuota(f.ctx, f.foreign, g.ID, QuotaLimits{0, 0}, 1), "not_found")
			for _, l := range []QuotaLimits{{-1, -1}, {101, 0}, {1, MaxWorkspaceManifestBytes + 1}} {
				wantErr(t, f.peer.SetQuota(f.ctx, f.admin, "", l, 1), "invalid_argument")
			}
			wantErr(t, f.peer.SetQuota(f.ctx, f.admin, g.ID, QuotaLimits{-2, -1}, 1), "invalid_argument")
			f.limits("", 2, 6, 1)
			f.limits(g.ID, 2, -1, g.Version)
			wantErr(t, f.peer.SetQuota(f.ctx, f.admin, "", QuotaLimits{1, 2}, 1), "conflict")
			wantErr(t, f.peer.SetQuota(f.ctx, f.admin, g.ID, QuotaLimits{1, 2}, 1), "conflict")
			p := newSite("assigned")
			p["group_id"] = g.ID
			site := mustOp(t, f.app, f.ctx, f.admin, "create_site", p)
			mustOp(t, f.app, f.ctx, f.admin, "upload_files", quotaUpload("assigned", site["revision"], "index.html", "12345", "u"))
			free := mustOp(t, f.app, f.ctx, f.admin, "create_site", newSite("unassigned"))
			mustOp(t, f.app, f.ctx, f.admin, "upload_files", quotaUpload("unassigned", free["revision"], "index.html", "1", "u-free"))
			f.checkOp(f.ctx, f.admin, "upload_files", quotaUpload("assigned", mustOp(t, f.app, f.ctx, f.admin, "get_site", args{"site_id": "assigned"})["revision"], "index.html", "1", "exhausted"), "quota_exceeded")
			f.checkOp(f.ctx, f.admin, "create_site", newSite("over-limit"), "quota_exceeded")
			wantErr(t, f.peer.AssignSiteGroup(f.ctx, f.foreign, "assigned", "", 1), "not_found")
			wantErr(t, f.peer.AssignSiteGroup(keyctx, f.admin, "assigned", "", 1), "forbidden")
			wantErr(t, f.peer.AssignSiteGroup(f.ctx, f.writer, "assigned", "", 1), "forbidden")
			foreignGroup, err := f.app.CreateGroup(f.ctx, f.foreign, "Other", "editor")
			if err != nil {
				t.Fatal(err)
			}
			wantErr(t, f.peer.AssignSiteGroup(f.ctx, f.admin, "assigned", foreignGroup.ID, 1), "not_found")
			foreignView, err := f.peer.Quotas(f.ctx, f.foreign)
			if err != nil || foreignView.Workspace.Usage != (QuotaUsage{}) || len(foreignView.Sites) != 0 || len(foreignView.Groups) != 1 {
				t.Fatal(foreignView, err)
			}
			if err = f.app.AssignSiteGroup(f.ctx, f.admin, "unassigned", g.ID, 1); err != nil {
				t.Fatal(err)
			}
			ev, _, err := f.peer.AuditPage(f.ctx, f.admin, 0, false, 64)
			if err != nil {
				t.Fatal(err)
			}
			changes := map[string]int{}
			for _, e := range ev {
				changes[e.Action]++
				if e.Action == "site_group_changed" && (e.Target != "unassigned" || e.Previous != "" || e.Value != g.ID) {
					t.Fatal(e)
				}
			}
			for _, name := range []string{"workspace_quota_changed", "group_quota_changed", "site_group_changed"} {
				if changes[name] != 1 {
					t.Fatal(changes)
				}
			}
		})
	}
}

func TestQuotaAcrossReplicas(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			f := newQuotaFixture(t, driver)
			g := f.group("Team", "editor", f.writer)
			f.limits(g.ID, 1, 4, g.Version)
			results := make(chan error, 2)
			contest := func() {
				t.Helper()
				wins := 0
				for range 2 {
					e := <-results
					if e == nil {
						wins++
					} else {
						wantErr(t, e, "quota_exceeded")
					}
				}
				if wins != 1 {
					t.Fatalf("%d concurrent writes succeeded", wins)
				}
			}
			for i, s := range []*Store{f.app, f.peer} {
				go func() {
					_, e := execOp(t, s, f.ctx, f.writer, "create_site", newSite(fmt.Sprintf("race-%d", i)))
					results <- e
				}()
			}
			contest()
			f.limits(g.ID, 2, 4, g.Version+1)
			mustOp(t, f.app, f.ctx, f.writer, "create_site", newSite("companion"))
			for i, site := range f.view().Sites {
				s := []*Store{f.app, f.peer}[i]
				current := mustOp(t, s, f.ctx, f.writer, "get_site", args{"site_id": site.ID})
				go func() {
					_, e := execOp(t, s, f.ctx, f.writer, "upload_files", quotaUpload(site.ID, current["revision"], "index.html", "123", "upload-"+site.ID))
					results <- e
				}()
			}
			contest()
			v := f.view()
			if v.Groups[0].Usage != (QuotaUsage{2, 3}) || v.Workspace.Usage != v.Groups[0].Usage {
				t.Fatal(v)
			}
		})
	}
}

func TestUpgradeV3QuotaHistoryAndReplay(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			ctx := t.Context()
			dsn := testDSN(t, driver)
			name := driver
			if name == "postgres" {
				name = "pgx"
			}
			db, err := sql.Open(name, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			exec := func(q string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(ctx, q, args...); err != nil {
					t.Fatal(err)
				}
			}
			for _, q := range schema {
				exec(q)
			}
			exec("CREATE TABLE sc_schema_migrations(version INTEGER PRIMARY KEY)")
			exec("ALTER TABLE sc_sites ADD COLUMN publication_generation BIGINT NOT NULL DEFAULT 1")
			for _, stmts := range [][]string{accountSchema, tenantSchema, {issuerBindingSchema}, auditSchema} {
				for _, q := range stmts {
					exec(q)
				}
			}
			exec("INSERT INTO sc_schema_migrations VALUES(1),(2),(3)")
			exec("INSERT INTO sc_workspace(id,used_bytes,changes) VALUES('project',6,3)")
			exec("INSERT INTO sc_members(workspace,subject,display_name,identity_role,identity_at,created_at) VALUES('project','ops','Ops','admin',1,1)")
			writer := Actor{"writer", "project", "editor"}
			exec("INSERT INTO sc_members(workspace,subject,display_name,identity_role,identity_at,created_at) VALUES('project','writer','Writer','editor',1,1)")
			g1, g2 := id(), id()
			exec("INSERT INTO sc_groups(workspace,id,name,name_key,role,version) VALUES('project',$1,'Docs','docs','editor',1),('project',$2,'Reports','reports','publisher',1)", g1, g2)
			exec("INSERT INTO sc_group_members(workspace,group_id,subject) VALUES('project',$1,'writer'),('project',$2,'writer')", g1, g2)
			r1, r2 := id(), id()
			exec("INSERT INTO sc_sites(id,workspace,name,visibility,draft,live,release_id) VALUES('old','project','old','workspace',$1,'','')", r2)
			exec("INSERT INTO sc_revisions VALUES($1,'old',1),($2,'old',2)", r1, r2)
			exec("INSERT INTO sc_blobs(digest,body) VALUES('blob',$1)", []byte("abc"))
			exec("INSERT INTO sc_files VALUES($1,'index.html','blob',3,'text/html'),($2,'index.html','blob',3,'text/html')", r1, r2)
			// Literal v3 canonical mutation. A new optional argument must not change its hash.
			oldHash := sum([]byte("create_site\x00" + `{"name":"old","slug":"old","site_id":"","request_id":"create-old","expected_revision":"","revision":"","files":null,"delete_paths":null,"visibility":"","release_id":"","expected_release_id":""}`))
			exec("INSERT INTO sc_requests VALUES('project','ops','create-old',$1,$2)", oldHash, `{"site_id":"old","revision":"original-receipt","visibility":"workspace"}`)
			s := openTest(t, driver, dsn)
			admin := Actor{"ops", "project", "admin"}
			v, err := s.Quotas(ctx, admin)
			if err != nil || len(v.Sites) != 1 || v.Sites[0].Bytes != 6 || v.Sites[0].GroupID != "" || v.Workspace.Usage != (QuotaUsage{1, 6}) {
				t.Fatal(v, err)
			}
			if r := mustOp(t, s, ctx, admin, "create_site", newSite("old")); r["revision"] != "original-receipt" {
				t.Fatal(r)
			}
			g, err := s.CreateGroup(ctx, admin, "Old project", "editor")
			if err != nil {
				t.Fatal(err)
			}
			if err = s.AssignSiteGroup(ctx, admin, "old", g.ID, 1); err != nil {
				t.Fatal(err)
			}
			second := openTest(t, driver, dsn)
			v, err = second.Quotas(ctx, admin)
			if err != nil || v.Groups[1].ID != g.ID || v.Groups[1].Usage != (QuotaUsage{1, 6}) || v.Sites[0].GroupID != g.ID || v.Workspace.Usage != (QuotaUsage{1, 6}) {
				t.Fatal(v, err)
			}
			// Pre-v4 automation may create without naming one of the writer's
			// existing groups. Opt-in charging also works before a limit exists.
			if out := mustOp(t, second, ctx, writer, "create_site", newSite("legacy-automation")); out["group_id"] != "" {
				t.Fatal(out)
			}
			chosen := newSite("explicit-owner")
			chosen["group_id"] = g1
			if out := mustOp(t, second, ctx, writer, "create_site", chosen); out["group_id"] != g1 {
				t.Fatal(out)
			}
			if err := s.SetGroupMember(ctx, admin, g2, writer.Subject, false, 1); err != nil {
				t.Fatal(err)
			}
			if out := mustOp(t, second, ctx, writer, "create_site", newSite("single-membership")); out["group_id"] != "" {
				t.Fatal(out)
			}
		})
	}
}
