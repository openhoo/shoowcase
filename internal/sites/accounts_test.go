package sites

import (
	"database/sql"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAccountAndAgentKeyLifecycle(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			ctx := WithIdentityIssuedAt(t.Context(), 100)
			dsn := testDSN(t, driver)
			s, peer := openTest(t, driver, dsn), openTest(t, driver, dsn)
			admin := Actor{"ops", "alpha", "admin"}
			pub := Actor{"p", "alpha", "publisher"}
			other := Actor{"ops", "beta", "admin"}
			viewer := Actor{"r", "alpha", "reader"}
			peerAdmin := Actor{"secondops", "alpha", "admin"}
			anonymous := Actor{}
			if _, _, err := s.CreateKey(ctx, pub, "one", "reader", 7); err == nil {
				t.Fatal("unregistered actor issued key")
			}
			for _, a := range []Actor{admin, pub, other, viewer, peerAdmin} {
				m, err := s.EnrollAccount(ctx, a, "Display "+a.Subject)
				if err != nil || m.Role != a.Role || m.Ceiling != "" || m.Disabled {
					t.Fatal("enrollment", a.Subject, m, err)
				}
			}
			first, err := s.EnrollAccount(ctx, pub, "different")
			if err != nil || first.DisplayName != "Display p" {
				t.Fatal("enrollment retry changed name", first, err)
			}
			if _, err = s.EnrollAccount(ctx, anonymous, "Invalid"); err == nil {
				t.Fatal("anonymous enroll")
			}
			_, _, err = s.CreateKey(ctx, viewer, "Elevated", "editor", 7)
			wantErr(t, err, "forbidden")
			for _, size := range []int{-1, 0, 91} {
				_, _, err = s.CreateKey(ctx, viewer, "Invalid", "reader", size)
				wantErr(t, err, "invalid_argument")
			}
			_, _, err = s.CreateKey(ctx, admin, "Forbidden", "admin", 1)
			wantErr(t, err, "invalid_argument")
			if _, err = s.EnrollAccount(ctx, pub, "\u202e"); err == nil {
				t.Fatal("invisible name")
			}
			key, secret, err := s.CreateKey(ctx, pub, "Team guide", "publisher", 7)
			if err != nil || key.ID != KeyID(secret) || key.Scope != "publisher" || key.ExpiresAt.Sub(key.CreatedAt) != 7*24*time.Hour {
				t.Fatal("key issuance", err)
			}
			blob, _ := json.Marshal(key)
			if strings.Contains(string(blob), secret) {
				t.Fatal("key struct exposed secret")
			}
			var digest string
			if err = s.db.QueryRow("SELECT digest FROM sc_agent_keys WHERE id=$1", key.ID).Scan(&digest); err != nil || digest == secret || digest != sum([]byte(secret)) {
				t.Fatal("key persistence")
			}
			auth, err := peer.ResolveKey(ctx, secret)
			if err != nil || auth != pub {
				t.Fatal(auth, err)
			}
			modified := []byte(secret)
			i := len(MCPKeyPrefix) + 33
			if modified[i] == 'a' {
				modified[i] = 'b'
			} else {
				modified[i] = 'a'
			}
			for _, value := range []string{key.ID, string(modified), secret + "x", "sc_key_"} {
				_, err = peer.ResolveKey(ctx, value)
				wantErr(t, err, "unauthorized")
			}
			_, err = s.ResolveKeyID(ctx, other, key.ID)
			wantErr(t, err, "unauthorized")
			if _, _, err = s.ListKeys(ctx, viewer, true, ""); err == nil {
				t.Fatal("viewer asked for workspace keys")
			}
			if _, err = s.WorkspaceAccounts(ctx, viewer); err == nil {
				t.Fatal("viewer membership list")
			}
			if pages, _, err := s.ListKeys(ctx, viewer, false, ""); err != nil || len(pages) != 0 {
				t.Fatal("viewer keys", pages, err)
			}
			for _, a := range []Actor{viewer, other} {
				err = s.RevokeKey(ctx, a, key.ID)
				wantErr(t, err, "not_found")
			}
			if _, _, err := s.CreateKey(WithKeyID(ctx, key.ID), pub, "nested", "reader", 1); err == nil {
				t.Fatal("key minted another key")
			}
			site := mustOp(t, s, WithKeyID(ctx, key.ID), pub, "create_site", args{"name": "App", "slug": "my-app", "request_id": "first"})
			err = s.SetMember(ctx, other, pub.Subject, "reader", false, "", false)
			wantErr(t, err, "not_found")
			wantErr(t, s.SetMember(ctx, peerAdmin, peerAdmin.Subject, "reader", false, "", false), "forbidden")
			err = s.SetMember(ctx, admin, admin.Subject, "", true, "", false)
			wantErr(t, err, "forbidden")
			err = s.SetMember(ctx, viewer, pub.Subject, "reader", false, "", false)
			wantErr(t, err, "forbidden")
			if err = s.SetMember(ctx, admin, pub.Subject, "reader", false, "", false); err != nil {
				t.Fatal(err)
			}
			limited, err := peer.ResolveKey(ctx, secret)
			if err != nil || limited.Role != "reader" {
				t.Fatal("key after ceiling", limited, err)
			}
			limited, err = peer.LimitActor(ctx, pub)
			if err != nil || limited.Role != "reader" {
				t.Fatal("browser after ceiling", limited, err)
			}
			_, err = execOp(t, peer, WithKeyID(ctx, key.ID), pub, "create_site", args{"name": "App", "slug": "my-app", "request_id": "first"})
			wantErr(t, err, "forbidden")
			err = s.SetMember(ctx, admin, pub.Subject, "", true, "", false)
			wantErr(t, err, "conflict")
			if err = s.SetMember(ctx, admin, pub.Subject, "reader", true, "reader", false); err != nil {
				t.Fatal(err)
			}
			_, err = peer.ResolveKey(ctx, secret)
			wantErr(t, err, "unauthorized")
			_, err = peer.LimitActor(ctx, pub)
			wantErr(t, err, "forbidden")
			_, _, err = peer.AccountFor(ctx, pub)
			wantErr(t, err, "forbidden")
			// A signed site action without a key must also recheck membership under the workspace lock.
			_, err = execOp(t, peer, ctx, pub, "upload_files", args{"site_id": "my-app", "expected_revision": site["revision"], "files": []args{{"path": "index.html", "text": "<p>no</p>"}}, "request_id": "stale"})
			wantErr(t, err, "forbidden")
			if err = s.SetMember(ctx, admin, pub.Subject, "", false, "reader", true); err != nil {
				t.Fatal(err)
			}
			_, err = peer.ResolveKey(ctx, secret)
			wantErr(t, err, "unauthorized")
			r, renewed, err := peer.CreateKey(ctx, pub, "Replacement", "publisher", 1)
			if err != nil {
				t.Fatal(err)
			}
			if _, e := s.db.Exec("UPDATE sc_agent_keys SET expires_at=$1 WHERE id=$2", time.Now().Unix(), r.ID); e != nil {
				t.Fatal(e)
			}
			_, err = s.ResolveKey(ctx, renewed)
			wantErr(t, err, "unauthorized")
			newKey, newSecret, err := s.CreateKey(ctx, pub, "Short grant", "publisher", 1)
			if err != nil {
				t.Fatal(err)
			}
			readClaim := pub
			readClaim.Role = "reader"
			_, _, err = peer.AccountFor(WithIdentityIssuedAt(ctx, 200), readClaim)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = s.AccountFor(WithIdentityIssuedAt(ctx, 150), pub)
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.ResolveKey(ctx, newSecret)
			if err != nil || got.Role != "reader" {
				t.Fatal("older grant overrode a demotion", got, err)
			}
			_, _, err = s.AccountFor(WithIdentityIssuedAt(ctx, 200), pub)
			if err != nil {
				t.Fatal(err)
			}
			got, err = s.ResolveKey(ctx, newSecret)
			if err != nil || got.Role != "reader" {
				t.Fatal("same-second grant overrode a demotion", got, err)
			}
			if err = s.RevokeKey(ctx, pub, newKey.ID); err != nil {
				t.Fatal(err)
			}
			if err = s.RevokeKey(ctx, pub, newKey.ID); err != nil {
				t.Fatal("revocation retry", err)
			}
			events, e := s.AccountActivity(ctx, admin)
			if e != nil || len(events) == 0 {
				t.Fatal(events, e)
			}
			b, _ := json.Marshal(events)
			if strings.Contains(string(b), secret) || strings.Contains(string(b), digest) || strings.Contains(string(b), "beta") {
				t.Fatal("activity scope")
			}
			stats, e := peer.WorkspaceStats(ctx, admin)
			if e != nil || stats.Members != 4 || stats.Sites != 1 || stats.ActiveKeys != 0 {
				t.Fatal(stats, e)
			}
			// This person's identity in another workspace is independent of their suspension or role.
			foreign := pub
			foreign.Workspace = "beta"
			got, err = s.LimitActor(ctx, foreign)
			if err != nil || got != foreign {
				t.Fatal(got, err)
			}
		})
	}
}

func TestKeyQuotaAcrossReplicas(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			dsn := testDSN(t, driver)
			s, peer := openTest(t, driver, dsn), openTest(t, driver, dsn)
			ctx := t.Context()
			a := Actor{"m", "w", "editor"}
			if _, e := s.EnrollAccount(ctx, a, "M"); e != nil {
				t.Fatal(e)
			}
			var wg sync.WaitGroup
			results := make(chan error, 18)
			for i := 0; i < 18; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					db := s
					if i%2 == 0 {
						db = peer
					}
					_, _, e := db.CreateKey(ctx, a, numberedKeyName("Agent", i+1), "", 1)
					results <- e
				}(i)
			}
			wg.Wait()
			close(results)
			ok := 0
			for err := range results {
				if err == nil {
					ok++
					continue
				}
				wantErr(t, err, "quota_exceeded")
			}
			if ok != 10 {
				t.Fatalf("created %d active keys", ok)
			}
			keys, _, e := peer.ListKeys(ctx, a, false, "")
			if len(keys) != 10 || e != nil {
				t.Fatal(len(keys), e)
			}
			for _, k := range keys {
				if k.Scope != "reader" {
					t.Fatal("default key scope", k.Scope)
				}
			}
		})
	}
}

func TestAccountSchemaUpgradeFromV011(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			ctx := t.Context()
			dsn := testDSN(t, driver)
			name := driver
			if name == "postgres" {
				name = "pgx"
			}
			db, e := sql.Open(name, dsn)
			if e != nil {
				t.Fatal(e)
			}
			defer db.Close()
			for _, q := range schema {
				if _, e = db.Exec(q); e != nil {
					t.Fatal(e)
				}
			}
			for _, q := range []string{"CREATE TABLE sc_schema_migrations(version INTEGER PRIMARY KEY)", "INSERT INTO sc_schema_migrations VALUES(1)", "ALTER TABLE sc_sites ADD COLUMN publication_generation BIGINT NOT NULL DEFAULT 1", "INSERT INTO sc_workspace(id) VALUES('prior')", "INSERT INTO sc_sites(id,workspace,name,visibility,draft,live,release_id,publication_generation) VALUES ('first','prior','First','workspace','first-rev','','',9)"} {
				if _, e = db.Exec(q); e != nil {
					t.Fatal(e)
				}
			}
			s := openTest(t, driver, dsn)
			var gen int
			if e = s.db.QueryRowContext(ctx, "SELECT publication_generation FROM sc_sites WHERE id='first'").Scan(&gen); e != nil || gen != 9 {
				t.Fatal(gen, e)
			}
			m, ok, e := s.AccountFor(ctx, Actor{"member", "prior", "reader"})
			if e != nil || ok {
				t.Fatal(m, e)
			}
			var version int
			if e = s.db.QueryRow("SELECT MAX(version) FROM sc_schema_migrations").Scan(&version); e != nil || version != 8 {
				t.Fatal(version, e)
			}
		})
	}
}

// Both authorized operations begin on different replicas. Exactly one can
// win: the second administrator must lose their authority before their own
// queued action is considered. Restoring a member never revives old keys.
func TestPeerAdminSuspensionAcrossReplicas(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			ctx := t.Context()
			dsn := testDSN(t, driver)
			replicas := []*Store{openTest(t, driver, dsn), openTest(t, driver, dsn)}
			admins := []Actor{{"first", "team", "admin"}, {"second", "team", "admin"}}
			secrets := make([]string, 2)
			for i, a := range admins {
				if _, e := replicas[i].EnrollAccount(ctx, a, a.Subject); e != nil {
					t.Fatal(e)
				}
				_, key, e := replicas[i].CreateKey(ctx, a, "Studio", "publisher", 2)
				if e != nil {
					t.Fatal(e)
				}
				secrets[i] = key
			}
			start := make(chan struct{})
			var failures [2]error
			var wg sync.WaitGroup
			for i, a := range admins {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					failures[i] = replicas[i].SetMember(ctx, a, admins[1-i].Subject, "", true, "", false)
				}()
			}
			close(start)
			wg.Wait()
			winner := 0
			if failures[0] != nil {
				winner = 1
			}
			if failures[winner] != nil {
				t.Fatal("neither admin could act", failures)
			}
			loser := 1 - winner
			wantErr(t, failures[loser], "forbidden")
			db := replicas[loser]
			_, e := db.ResolveKey(ctx, secrets[loser])
			wantErr(t, e, "unauthorized")
			if _, e = db.ResolveKey(ctx, secrets[winner]); e != nil {
				t.Fatal(e)
			}
			e = db.SetMember(ctx, admins[loser], admins[loser].Subject, "", false, "", true)
			wantErr(t, e, "forbidden")
			if e = db.SetMember(ctx, admins[winner], admins[loser].Subject, "", false, "", true); e != nil {
				t.Fatal(e)
			}
			if got, e := db.LimitActor(ctx, admins[loser]); e != nil || got.Role != "admin" {
				t.Fatal(got, e)
			}
			_, e = db.ResolveKey(ctx, secrets[loser])
			wantErr(t, e, "unauthorized")
			events, e := db.AccountActivity(ctx, admins[winner])
			if e != nil {
				t.Fatal(e)
			}
			seen := map[string]int{}
			for _, ev := range events {
				if ev.Target == admins[loser].Subject && ev.Subject == admins[winner].Subject {
					seen[ev.Action]++
				}
			}
			if seen["account_suspended"] != 1 || seen["account_reactivated"] != 1 {
				t.Fatal("missing exact peer-admin audit", seen)
			}
		})
	}
}

func TestSSOAdminRoleLimit(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			ctx := WithIdentityIssuedAt(t.Context(), 100)
			dsn := testDSN(t, backend)
			s, peer := openTest(t, backend, dsn), openTest(t, backend, dsn)
			manager := Actor{"ops", "design", "admin"}
			target := Actor{"staff", "design", "admin"}
			for _, a := range []Actor{manager, target} {
				if _, err := s.EnrollAccount(ctx, a, a.Subject); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.SetMember(ctx, manager, target.Subject, "reader", false, "", false); err != nil {
				t.Fatal(err)
			}
			current, ok, err := peer.AccountFor(WithIdentityIssuedAt(ctx, 200), target)
			if err != nil || !ok || current.IdentityRole != "admin" || current.Role != "reader" {
				t.Fatalf("IdP grant after fresh sign-in: %+v (%v)", current, err)
			}
			_, err = peer.WorkspaceSettings(ctx, target)
			wantErr(t, err, "forbidden")
			wantErr(t, peer.SetMember(ctx, target, target.Subject, "", false, "reader", false), "forbidden")
			if err := s.SetMember(ctx, manager, target.Subject, "", false, "reader", false); err != nil {
				t.Fatal(err)
			}
			if _, err := peer.WorkspaceSettings(ctx, target); err != nil {
				t.Fatal(err)
			}
			// Even a previously issued admin assertion cannot exceed a newer IdP demotion.
			if err := s.SetMember(ctx, manager, target.Subject, "reader", false, "", false); err != nil {
				t.Fatal(err)
			}
			demoted := target
			demoted.Role = "editor"
			if _, _, err := peer.AccountFor(WithIdentityIssuedAt(ctx, 300), demoted); err != nil {
				t.Fatal(err)
			}
			wantErr(t, s.SetMember(ctx, manager, target.Subject, "publisher", false, "reader", false), "invalid_argument")
			if err := s.SetMember(ctx, manager, target.Subject, "", false, "reader", false); err != nil {
				t.Fatal(err)
			}
			got, err := peer.LimitActor(WithIdentityIssuedAt(ctx, 200), target)
			if err != nil || got.Role != "editor" {
				t.Fatalf("effective access after clearing limit: %+v (%v)", got, err)
			}
			_, err = peer.WorkspaceSettings(ctx, target)
			wantErr(t, err, "forbidden")
		})
	}
}
