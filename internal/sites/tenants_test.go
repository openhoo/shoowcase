package sites

import (
	"database/sql"
	"encoding/base64"
	"testing"
	"time"
)

// Exercise the same authorization boundary from an independent replica, with
// credentials, signed actions and a conditional response captured before the
// tenant changed its policy.
func TestManagedTenantAccess(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			ctx := WithIdentityIssuedAt(t.Context(), 1200)
			dsn := testDSN(t, driver)
			s, peer := openTest(t, driver, dsn), openTest(t, driver, dsn)
			admin := Actor{"ops", "alpha", "admin"}
			foreign := Actor{"ops", "beta", "admin"}
			bob := Actor{"bob", "alpha", "publisher"}
			implicit := Actor{"legacy", "alpha", "publisher"}
			for _, a := range []Actor{admin, foreign, bob} {
				if _, err := s.EnrollAccount(ctx, a, a.Subject); err != nil {
					t.Fatal(err)
				}
			}
			key, secret, err := s.CreateKey(ctx, bob, "Publisher", "publisher", 10)
			if err != nil {
				t.Fatal(err)
			}
			staleKey := WithKeyID(ctx, key.ID)
			site := mustOp(t, s, staleKey, bob, "create_site", args{"slug": "guide", "name": "Guide", "request_id": "c"})
			site = mustOp(t, s, staleKey, bob, "upload_files", args{"site_id": "guide", "expected_revision": site["revision"], "request_id": "u", "files": []args{{"path": "index.html", "text": "<h1>Guide</h1>"}}})
			rev := site["revision"].(string)
			site = mustOp(t, s, staleKey, bob, "publish_site", args{"site_id": "guide", "revision": rev, "request_id": "p"})
			release := site["release_id"].(string)
			body, err := peer.Fetch(ctx, "guide", "", "", implicit)
			if err != nil {
				t.Fatal(err)
			}
			share := args{"site_id": "guide", "visibility": "public", "revision": rev, "request_id": "share"}
			staleApproval := approvalFor(t, peer, staleKey, bob, "set_site_access", share)
			cfg, err := s.WorkspaceSettings(ctx, admin)
			if err != nil || cfg.Mode != "identity" || cfg.Version != 1 {
				t.Fatal(cfg, err)
			}
			err = s.SetWorkspaceSettings(ctx, foreign, "Elsewhere", "managed", 1)
			if err != nil {
				t.Fatal(err)
			}
			err = s.SetWorkspaceSettings(ctx, admin, "Team Alpha", "managed", 1)
			if err != nil {
				t.Fatal(err)
			}
			wantErr(t, s.SetWorkspaceSettings(ctx, admin, "Wrong", "identity", 1), "conflict")
			if cfg, err := s.WorkspaceSettings(ctx, admin); err != nil || cfg.DisplayName != "Team Alpha" || cfg.Mode != "managed" || cfg.Version != 2 {
				t.Fatal(cfg, err)
			}
			_, err = peer.LimitActor(ctx, implicit)
			wantErr(t, err, "forbidden")
			_, err = peer.EnrollAccount(ctx, implicit, "Legacy")
			wantErr(t, err, "forbidden")
			_, err = execOp(t, peer, ctx, implicit, "list_sites", args{})
			wantErr(t, err, "forbidden")
			_, err = execOp(t, peer, ctx, implicit, "create_site", args{"name": "No", "request_id": "denied"})
			wantErr(t, err, "forbidden")
			_, err = peer.FetchConditional(ctx, "guide", "", "", implicit, body.Digest)
			wantErr(t, err, "not_found")
			_, err = peer.FetchMetadata(ctx, "guide", "", "", implicit, body.Digest)
			wantErr(t, err, "not_found")
			_, err = peer.PreviewSlug(ctx, rev, implicit)
			wantErr(t, err, "not_found")
			_, err = peer.ReleaseRevision(ctx, implicit, "guide", release)
			wantErr(t, err, "not_found")
			if m, e := peer.LimitActor(ctx, bob); e != nil || m.Role != "reader" {
				t.Fatal(m, e)
			}
			if a, e := peer.ResolveKey(ctx, secret); e != nil || a.Role != "reader" {
				t.Fatal(a, e)
			}
			_, err = execOp(t, peer, staleApproval, bob, "set_site_access", share)
			wantErr(t, err, "forbidden")
			if _, err = s.CreateGroup(ctx, admin, "Owner", "admin"); err == nil {
				t.Fatal("group granted admin")
			}
			g, err := s.CreateGroup(ctx, admin, "Authors", "editor")
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.CreateGroup(ctx, admin, "authors", "publisher")
			wantErr(t, err, "conflict")
			_, err = s.CreateGroup(WithKeyID(ctx, key.ID), admin, "No", "editor")
			wantErr(t, err, "forbidden")
			_, err = s.CreateGroup(ctx, bob, "No", "editor")
			wantErr(t, err, "forbidden")
			_, err = s.GroupMembers(ctx, foreign, g.ID)
			wantErr(t, err, "not_found")
			wantErr(t, s.SetGroupMember(ctx, foreign, g.ID, foreign.Subject, true, 1), "not_found")
			wantErr(t, s.SetGroupMember(ctx, admin, g.ID, "not-local", true, 1), "not_found")
			if err = s.SetGroupMember(ctx, admin, g.ID, bob.Subject, true, 1); err != nil {
				t.Fatal(err)
			}
			wantErr(t, s.DeleteGroup(ctx, admin, g.ID, 1), "conflict")
			wantErr(t, s.UpdateGroup(ctx, admin, g.ID, "New", "publisher", 1), "conflict")
			if groups, e := s.ListGroups(ctx, foreign); e != nil || len(groups) != 0 {
				t.Fatal(groups, e)
			}
			if groups, e := s.ListGroups(ctx, admin); e != nil || len(groups) != 1 || groups[0].Version != 2 || groups[0].Members != 1 {
				t.Fatal(groups, e)
			}
			if members, e := s.GroupMembers(ctx, admin, g.ID); e != nil || len(members) != 1 || members[0] != bob.Subject {
				t.Fatal(members, e)
			}
			if a, e := peer.ResolveKey(ctx, secret); e != nil || a.Role != "editor" {
				t.Fatal(a, e)
			}
			if m, e := peer.LimitActor(ctx, bob); e != nil || m.Role != "editor" {
				t.Fatal(m, e)
			}
			_, err = execOp(t, peer, staleApproval, bob, "set_site_access", share)
			wantErr(t, err, "forbidden")
			high, err := s.CreateGroup(ctx, admin, "Publishers", "publisher")
			if err != nil {
				t.Fatal(err)
			}
			if err = s.SetGroupMember(ctx, admin, high.ID, bob.Subject, true, high.Version); err != nil {
				t.Fatal(err)
			}
			if a, e := peer.ResolveKey(ctx, secret); e != nil || a.Role != "publisher" {
				t.Fatal(a, e)
			}
			readClaim := bob
			readClaim.Role = "reader"
			if m, e := peer.LimitActor(WithIdentityIssuedAt(ctx, 1300), readClaim); e != nil || m.Role != "reader" {
				t.Fatal(m, e)
			}
			if a, e := peer.ResolveKey(ctx, secret); e != nil || a.Role != "reader" {
				t.Fatal(a, e)
			}
			if m, e := peer.LimitActor(WithIdentityIssuedAt(ctx, 1400), bob); e != nil || m.Role != "publisher" {
				t.Fatal(m, e)
			}
			if err = s.SetMember(ctx, admin, bob.Subject, "reader", false, "", false); err != nil {
				t.Fatal(err)
			}
			if a, e := peer.ResolveKey(ctx, secret); e != nil || a.Role != "reader" {
				t.Fatal(a, e)
			}
			if err = s.SetMember(ctx, admin, bob.Subject, "", false, "reader", false); err != nil {
				t.Fatal(err)
			}
			if err = s.DeleteGroup(ctx, admin, high.ID, 2); err != nil {
				t.Fatal(err)
			}
			if a, e := peer.ResolveKey(ctx, secret); e != nil || a.Role != "editor" {
				t.Fatal(a, e)
			}
			if err = s.UpdateGroup(ctx, admin, g.ID, "Readers", "reader", 2); err != nil {
				t.Fatal(err)
			}
			if a, e := peer.ResolveKey(ctx, secret); e != nil || a.Role != "reader" {
				t.Fatal(a, e)
			}
			if err = s.SetGroupMember(ctx, admin, g.ID, bob.Subject, false, 3); err != nil {
				t.Fatal(err)
			}
			if users, e := s.GroupMembers(ctx, admin, g.ID); e != nil || len(users) != 0 {
				t.Fatal(users, e)
			}
			if err = s.ProvisionMember(ctx, admin, implicit.Subject, "Legacy Member"); err != nil {
				t.Fatal(err)
			}
			wantErr(t, s.ProvisionMember(ctx, admin, implicit.Subject, "Overwrite"), "conflict")
			if m, e := peer.LimitActor(ctx, implicit); e != nil || m.Role != "reader" {
				t.Fatal(m, e)
			}
			if _, e := peer.FetchMetadata(ctx, "guide", "", "", implicit, body.Digest); e != nil {
				t.Fatal(e)
			}
			newAdmin := Actor{"replacement", "alpha", "admin"}
			if _, e := peer.LimitActor(ctx, newAdmin); e == nil {
				t.Fatal("implicit admin bypassed managed membership")
			}
			if _, e := peer.EnrollAccount(t.Context(), newAdmin, "No verified identity"); e == nil {
				t.Fatal("managed enrollment accepted an unverified administrator")
			}
			if _, e := peer.EnrollAccount(ctx, newAdmin, "Other admin"); e != nil {
				t.Fatal(e)
			}
			if _, e := peer.WorkspaceSettings(ctx, newAdmin); e != nil {
				t.Fatal(e)
			}
			// Reverting to the IdP grants the member's observed role, not a
			// subject-matched identity from the other tenant.
			if e := s.SetWorkspaceSettings(ctx, admin, "", "identity", 2); e != nil {
				t.Fatal(e)
			}
			if m, e := peer.LimitActor(ctx, implicit); e != nil || m.Role != "publisher" {
				t.Fatal(m, e)
			}
			otherBob := bob
			otherBob.Workspace = foreign.Workspace
			_, err = peer.LimitActor(ctx, otherBob)
			wantErr(t, err, "forbidden")
		})
	}
}

// The upgrade must not drop an individual role limit or revive a suspended
// account, even when a legacy key record itself has not been revoked.
func TestUpgradeExistingTenantMembership(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			ctx := t.Context()
			dsn := testDSN(t, driver)
			name := driver
			if driver == "postgres" {
				name = "pgx"
			}
			db, err := sql.Open(name, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			exec := func(stmt string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(ctx, stmt, args...); err != nil {
					t.Fatal(err)
				}
			}
			for _, q := range schema {
				exec(q)
			}
			exec("CREATE TABLE sc_schema_migrations (version INTEGER PRIMARY KEY)")
			exec("ALTER TABLE sc_sites ADD COLUMN publication_generation BIGINT NOT NULL DEFAULT 1")
			for _, q := range accountSchema {
				exec(q)
			}
			exec("INSERT INTO sc_schema_migrations VALUES(1),(2)")
			exec("INSERT INTO sc_workspace(id) VALUES ('prior')")
			exec(`INSERT INTO sc_members(workspace,subject,display_name,identity_role,identity_at,ceiling,disabled,created_at)
				VALUES ('prior','ops','Operator','admin',100,'',FALSE,10),
				('prior','writer','Author','publisher',100,'editor',FALSE,11),
				('prior','left','Former member','publisher',101,'',TRUE,12)`)
			key := func(subject string, revoked bool) string {
				keyID := id()
				secret := MCPKeyPrefix + keyID + "." + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
				exec(`INSERT INTO sc_agent_keys(id,workspace,subject,name,scope,digest,created_at,expires_at,revoked)
					VALUES ($1,'prior',$2,'Key','publisher',$3,11,$4,$5)`, keyID, subject, sum([]byte(secret)), time.Now().Add(time.Hour).Unix(), revoked)
				return secret
			}
			active, suspended, revoked := key("writer", false), key("left", false), key("writer", true)
			s := openTest(t, driver, dsn)
			admin := Actor{"ops", "prior", "admin"}
			settings, err := s.WorkspaceSettings(ctx, admin)
			if err != nil || settings.Mode != "identity" {
				t.Fatal(settings, err)
			}
			if a, e := s.ResolveKey(ctx, active); e != nil || a.Role != "editor" || a.Subject != "writer" || a.Workspace != "prior" {
				t.Fatal(a, e)
			}
			for _, invalid := range []string{suspended, revoked} {
				_, err = s.ResolveKey(ctx, invalid)
				wantErr(t, err, "unauthorized")
			}
			implicit := Actor{"implicit", "prior", "publisher"}
			if a, e := s.LimitActor(ctx, implicit); e != nil || a != implicit {
				t.Fatal(a, e)
			}
			if g, e := s.ListGroups(ctx, admin); e != nil || len(g) != 0 {
				t.Fatal(g, e)
			}
		})
	}
}
