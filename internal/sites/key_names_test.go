package sites

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestKeyNameUniquenessAcrossReplicas(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			ctx := t.Context()
			dsn := testDSN(t, driver)
			replicas := []*Store{openTest(t, driver, dsn), openTest(t, driver, dsn)}
			a := Actor{"owner", "alpha", "reader"}
			b := Actor{"other", "alpha", "reader"}
			c := Actor{"owner", "beta", "reader"}
			for _, user := range []Actor{a, b, c} {
				if _, err := replicas[0].EnrollAccount(ctx, user, user.Subject); err != nil {
					t.Fatal(err)
				}
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			for i, name := range []string{" Résumé ", "RE\u0301SUME\u0301"} {
				go func() {
					<-start
					_, _, err := replicas[i].CreateKey(ctx, a, name, "reader", 7)
					results <- err
				}()
			}
			close(start)
			first, second := <-results, <-results
			if first != nil {
				first, second = second, first
			}
			if first != nil {
				t.Fatal("neither concurrent request created a key")
			}
			wantErr(t, second, "key_name_taken")
			keys, _, err := replicas[0].ListKeys(ctx, a, false, "")
			if err != nil || len(keys) != 1 {
				t.Fatal("expected exactly one key", err)
			}
			if err = replicas[1].RevokeKey(ctx, a, keys[0].ID); err != nil {
				t.Fatal(err)
			}
			_, _, err = replicas[0].CreateKey(ctx, a, "Résumé", "reader", 7)
			wantErr(t, err, "key_name_taken")
			suggested, _, err := replicas[0].CreateKeyWithSuggestedName(ctx, a, "Résumé", "reader", 7)
			if err != nil || suggested.Name != "Résumé (2)" {
				t.Fatal("did not find a free automatic name", err)
			}
			for _, user := range []Actor{b, c} {
				if _, _, err = replicas[0].CreateKey(ctx, user, "Résumé", "reader", 7); err != nil {
					t.Fatal("another workspace or account reserved this name", err)
				}
			}
		})
	}
}

func TestUpgradeV7PreservesKeysAndDeduplicatesNames(t *testing.T) {
	for _, driver := range []string{"sqlite", "pgx"} {
		t.Run(driver, func(t *testing.T) {
			ctx := t.Context()
			dsn := testDSN(t, driver)
			db := migrationDB(t, driver, dsn)
			old := fstest.MapFS{}
			for _, name := range []string{"00006_release_history.sql", "00007_draft_deletion.sql"} {
				data, err := fs.ReadFile(migrationFiles, "migrations/"+name)
				if err != nil {
					t.Fatal(err)
				}
				old[name] = &fstest.MapFile{Data: data}
			}
			if err := applyMigrations(ctx, db, driver, old); err != nil {
				t.Fatal(err)
			}
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(ctx, query, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec("INSERT INTO sc_workspace(id) VALUES('team')")
			exec("INSERT INTO sc_members(workspace,subject,display_name,identity_role,identity_at,created_at) VALUES('team','owner','Owner','reader',1,1),('team','other','Other','reader',1,1)")
			names := []struct{ subject, before, after string }{
				{"owner", "Desk", "Desk"},
				{"owner", "desk", "desk (3)"},
				{"owner", "Desk (2)", "Desk (2)"},
				{"other", "Desk", "Desk"},
				{"owner", strings.Repeat("ö", 80), strings.Repeat("ö", 80)},
				{"owner", strings.Repeat("Ö", 80), strings.Repeat("Ö", 76) + " (2)"},
			}
			ids := make([]string, len(names))
			expiry := time.Now().Add(time.Hour).Unix()
			for i, row := range names {
				ids[i] = id()
				exec(`INSERT INTO sc_agent_keys(id,workspace,subject,name,scope,digest,created_at,expires_at,revoked)
					VALUES($1,'team',$2,$3,'reader','fixture-digest',$4,$5,$6)`, ids[i], row.subject, row.before, i+1, expiry, i == 1)
			}
			if err := migrate(ctx, db, driver); err != nil {
				t.Fatal(err)
			}
			for i, row := range names {
				var name, digest string
				var exp int64
				var revoked bool
				err := db.QueryRowContext(ctx, "SELECT name,digest,expires_at,revoked FROM sc_agent_keys WHERE id=$1", ids[i]).Scan(&name, &digest, &exp, &revoked)
				if err != nil || name != row.after || digest != "fixture-digest" || exp != expiry || revoked != (i == 1) {
					t.Fatalf("key %d changed unexpectedly: %q (%v)", i, name, err)
				}
			}
		})
	}
}
