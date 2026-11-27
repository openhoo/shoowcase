package sites

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// Even on PostgreSQL the migration lock and the DDL must fit on one connection.
func migrationDB(t *testing.T, driver, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	return db
}

func TestMigrateExistingV5AndRollback(t *testing.T) {
	for _, driver := range []string{"sqlite", "pgx"} {
		t.Run(driver, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			dsn := testDSN(t, driver)
			db := migrationDB(t, driver, dsn)
			if err := applyMigrations(ctx, db, driver, fstest.MapFS{}); err != nil {
				t.Fatal(err)
			}
			// Old releases have only the positive versions in this exact table.
			if _, err := db.ExecContext(ctx, "DELETE FROM sc_schema_migrations WHERE version=0"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, "INSERT INTO sc_workspace(id, changes) VALUES('existing', 18)"); err != nil {
				t.Fatal(err)
			}
			// Fail the version insert *after* the real migration has created its
			// index. Both the version and the index must roll back together.
			if _, err := db.ExecContext(ctx, "ALTER TABLE sc_schema_migrations ADD COLUMN fault INTEGER DEFAULT 0 CHECK (version < 6 OR fault = 1)"); err != nil {
				t.Fatal(err)
			}
			if err := migrate(ctx, db, driver); err == nil {
				t.Fatal("migration committed despite rejected history insert")
			}
			var version, n int
			if err := db.QueryRowContext(ctx, "SELECT MAX(version), COUNT(*) FROM sc_schema_migrations").Scan(&version, &n); err != nil || version != 5 || n != 5 {
				t.Fatalf("failed upgrade recorded versions %d/%d (%v)", version, n, err)
			}
			if _, err := db.ExecContext(ctx, "ALTER TABLE sc_schema_migrations DROP COLUMN fault"); err != nil {
				t.Fatal(err)
			}
			// The real CREATE INDEX has no IF NOT EXISTS: this retry would fail if
			// the version insert and its DDL had committed independently.
			for range 2 {
				if err := migrate(ctx, db, driver); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.QueryRowContext(ctx, "SELECT MAX(version), (SELECT changes FROM sc_workspace WHERE id='existing') FROM sc_schema_migrations").Scan(&version, &n); err != nil || version != 6 || n != 18 {
				t.Fatalf("upgraded version %d, saved changes %d (%v)", version, n, err)
			}
			if _, err := db.ExecContext(ctx, "INSERT INTO sc_schema_migrations VALUES(7)"); err != nil {
				t.Fatal(err)
			}
			if s, err := Open(ctx, driver, dsn); err == nil {
				s.Close()
				t.Fatal("future schema accepted")
			} else if !strings.Contains(err.Error(), "unsupported newer schema") {
				t.Fatalf("unexpected startup error: %v", err)
			}
		})
	}
}

func TestConcurrentMigrationOnFreshDatabase(t *testing.T) {
	for _, driver := range []string{"sqlite", "pgx"} {
		t.Run(driver, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			dsn := testDSN(t, driver)
			start := make(chan struct{})
			const replicas = 4
			result := make(chan error, replicas)
			for range replicas {
				go func() {
					<-start
					s, err := Open(ctx, driver, dsn)
					if err == nil {
						err = s.Close()
					}
					result <- err
				}()
			}
			close(start)
			for range replicas {
				if err := <-result; err != nil {
					t.Errorf("concurrent start: %v", err)
				}
			}
			db := migrationDB(t, driver, dsn)
			var n, latest int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*), MAX(version) FROM sc_schema_migrations WHERE version>0").Scan(&n, &latest); err != nil || n != 6 || latest != 6 {
				t.Fatalf("applied versions %d, latest %d (%v)", n, latest, err)
			}
		})
	}
}
