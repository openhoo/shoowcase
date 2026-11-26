package sites

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

// Reconstruct a v2 database with two clocks of different precision, then
// continue exporting as users, credentials and local policies change.
func TestAuditHistoryAcrossUpgradeAndPaging(t *testing.T) {
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
			run := func(q string) {
				t.Helper()
				if _, err := db.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			for _, q := range schema {
				run(q)
			}
			run("CREATE TABLE sc_schema_migrations(version INTEGER PRIMARY KEY)")
			run("INSERT INTO sc_schema_migrations VALUES(1),(2)")
			run("ALTER TABLE sc_sites ADD COLUMN publication_generation BIGINT NOT NULL DEFAULT 1")
			for _, q := range accountSchema {
				run(q)
			}
			run("INSERT INTO sc_workspace(id) VALUES('audit'),('other')")
			run("INSERT INTO sc_members(workspace,subject,display_name,identity_role,identity_at,created_at) VALUES('audit','ops','Operator','admin',1,1)")
			run("INSERT INTO sc_account_events VALUES('a','audit','ops','key_revoked','public-key-id',1001),('b','other','other-secret-subject','account_created','other',1)")
			run("INSERT INTO sc_audit VALUES('s','audit','demo','publisher','rollback_site','new-draft','old-release','restored-revision','workspace','sensitive-request-id',1)")
			store := openTest(t, driver, dsn)
			admin := Actor{"ops", "audit", "admin"}
			page, more, err := store.AuditPage(ctx, admin, 0, true, 1)
			if err != nil || !more || len(page) != 1 || page[0].Sequence != 1 || page[0].At.UnixMicro() != 1000 || page[0].DraftRevision != "new-draft" || page[0].LiveRevision != "restored-revision" {
				t.Fatal(page, more, err)
			}
			if err := store.RecordSession(ctx, admin, "auth.login"); err != nil {
				t.Fatal(err)
			}
			key, secret, err := store.CreateKey(ctx, admin, "private-key-name", "editor", 2)
			if err != nil {
				t.Fatal(err)
			}
			mustOp(t, store, WithKeyID(ctx, key.ID), admin, "create_site", args{"name": "private-site-name", "slug": "example", "request_id": "secret-retry-id"})
			mustOp(t, store, WithKeyID(ctx, key.ID), admin, "create_site", args{"name": "private-site-name", "slug": "example", "request_id": "secret-retry-id"})
			items, more, err := store.AuditPage(ctx, admin, 1, true, 200)
			if err != nil || more || len(items) != 4 || items[0].At.UnixMicro() != 1001 || items[3].Sequence != 5 || items[3].CredentialID != key.ID {
				t.Fatal(items, more, err)
			}
			encoded, _ := json.Marshal(items)
			for _, v := range []string{"other-secret-subject", "private-site-name", "private-key-name", "secret-retry-id", "sensitive-request-id", secret, sum([]byte(secret))} {
				if strings.Contains(string(encoded), v) {
					t.Fatal("private data in export")
				}
			}
			desc, more, err := store.AuditPage(ctx, admin, 4, false, 2)
			if err != nil || !more || len(desc) != 2 || desc[0].Sequence != 3 || desc[1].Sequence != 2 {
				t.Fatal(desc, more, err)
			}
			admin.Role = "reader"
			_, _, err = store.AuditPage(ctx, admin, 0, true, 10)
			wantErr(t, err, "forbidden")
			admin.Role = "admin"
			_, _, err = store.AuditPage(WithKeyID(ctx, key.ID), admin, 0, true, 10)
			wantErr(t, err, "forbidden")
			// Simulate a journal outage. The protected domain change must roll back.
			if _, err = store.db.Exec("DROP TABLE sc_workspace_audit"); err != nil {
				t.Fatal(err)
			}
			if _, err = store.CreateGroup(ctx, admin, "Uncommitted", "editor"); err == nil {
				t.Fatal("unaudited change succeeded")
			}
			groups, err := store.ListGroups(ctx, admin)
			if err != nil || len(groups) != 0 {
				t.Fatal(groups, err)
			}
		})
	}
}
