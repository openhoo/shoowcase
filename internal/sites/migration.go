package sites

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"slices"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

const migrationLockID int64 = 4637380929301

func migrate(ctx context.Context, db *sql.DB, driver string) error {
	files, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return err
	}
	return applyMigrations(ctx, db, driver, files, goose.NewGoMigration(8, &goose.GoFunc{RunTx: migrateAgentKeyNames}, nil))
}

func applyMigrations(ctx context.Context, db *sql.DB, driver string, files fs.FS, extra ...*goose.Migration) error {
	versions := &schemaVersions{postgres: driver == "pgx"}
	opts := []goose.ProviderOption{goose.WithStore(versions), goose.WithGoMigrations(append(legacyMigrations(), extra...)...), goose.WithDisableGlobalRegistry(true)}
	if versions.postgres {
		// This is the same key used by pre-Goose binaries. The provider holds
		// its session lock on the same connection that executes the migrations.
		locker, err := lock.NewPostgresSessionLocker(lock.WithLockID(migrationLockID), lock.WithLockTimeout(1, 300))
		if err != nil {
			return err
		}
		opts = append(opts, goose.WithSessionLocker(locker))
	}
	provider, err := goose.NewProvider(goose.DialectCustom, db, files, opts...)
	if err != nil {
		return err
	}
	sources := provider.ListSources()
	versions.latest = sources[len(sources)-1].Version
	for {
		_, err = provider.Up(ctx)
		var failed *goose.PartialError
		if versions.postgres || !errors.As(err, &failed) {
			return err
		}
		// SQLite serializes writers, but another process may commit after
		// Goose plans the pending steps and before our transaction starts.
		// A duplicate version cannot commit. Replan only if the other writer
		// actually committed this step; never mask a failed migration.
		_, check := versions.GetMigration(ctx, db, failed.Failed.Source.Version)
		if check != nil {
			return err
		}
	}
}

// Keep the shipped SQL and historic version numbers unchanged. Prefer transactional SQL
// files in migrations/; schema 8 uses the same Go normalization as key issuance. An unversioned v0 database has
// the base tables already; their CREATE IF NOT EXISTS statements are idempotent.
func legacyMigrations() []*goose.Migration {
	steps := [][]string{
		append(slices.Clone(schema), "ALTER TABLE sc_sites ADD COLUMN publication_generation BIGINT NOT NULL DEFAULT 1"),
		accountSchema,
		slices.Concat(tenantSchema, []string{issuerBindingSchema}, auditSchema),
		quotaSchema,
		{localCredentialSchema},
	}
	var out []*goose.Migration
	for i, statements := range steps {
		out = append(out, goose.NewGoMigration(int64(i+1), &goose.GoFunc{RunTx: func(ctx context.Context, tx *sql.Tx) error {
			for _, q := range statements {
				if _, err := tx.ExecContext(ctx, q); err != nil {
					return err
				}
			}
			return nil
		}}, nil))
	}
	return out
}

// schemaVersions lets Goose write the original version table in the same
// transaction as each migration. Previous binaries still see and refuse
// higher versions. No second history or reconciliation is needed.
type schemaVersions struct {
	postgres bool
	latest   int64
}

func (*schemaVersions) Tablename() string { return "sc_schema_migrations" }
func (s *schemaVersions) CreateVersionTable(ctx context.Context, q database.DBTxConn) error {
	// Goose may bootstrap this table before taking its session lock. Match
	// the legacy transaction lock also when two binaries start on an empty DB.
	if s.postgres {
		if _, err := q.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLockID); err != nil {
			return err
		}
	}
	_, err := q.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS sc_schema_migrations (version INTEGER PRIMARY KEY)")
	return err
}
func (s *schemaVersions) TableExists(ctx context.Context, q database.DBTxConn) (bool, error) {
	query := "SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name='sc_schema_migrations')"
	if s.postgres {
		query = "SELECT to_regclass('sc_schema_migrations') IS NOT NULL"
	}
	var exists bool
	err := q.QueryRowContext(ctx, query).Scan(&exists)
	return exists, err
}
func (*schemaVersions) Insert(ctx context.Context, q database.DBTxConn, req database.InsertRequest) error {
	query := "INSERT INTO sc_schema_migrations(version) VALUES($1)"
	if req.Version == 0 {
		// Fresh processes may both have observed an absent ledger before Goose
		// bootstraps it. Its empty baseline is the only idempotent insert.
		query += " ON CONFLICT(version) DO NOTHING"
	}
	_, err := q.ExecContext(ctx, query, req.Version)
	return err
}
func (*schemaVersions) Delete(context.Context, database.DBTxConn, int64) error {
	return errors.New("restore a pre-upgrade backup to downgrade")
}
func (*schemaVersions) GetMigration(ctx context.Context, q database.DBTxConn, version int64) (*database.GetMigrationResult, error) {
	var v int64
	err := q.QueryRowContext(ctx, "SELECT version FROM sc_schema_migrations WHERE version=$1", version).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, database.ErrVersionNotFound
	}
	if err != nil {
		return nil, err
	}
	return &database.GetMigrationResult{IsApplied: true}, nil
}
func (*schemaVersions) GetLatestVersion(ctx context.Context, q database.DBTxConn) (int64, error) {
	var v sql.NullInt64
	err := q.QueryRowContext(ctx, "SELECT MAX(version) FROM sc_schema_migrations").Scan(&v)
	if err == nil && !v.Valid {
		return 0, database.ErrVersionNotFound
	}
	return v.Int64, err
}
func (s *schemaVersions) ListMigrations(ctx context.Context, q database.DBTxConn) ([]*database.ListMigrationsResult, error) {
	rows, err := q.QueryContext(ctx, "SELECT version FROM sc_schema_migrations ORDER BY version DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var versions []*database.ListMigrationsResult
	for rows.Next() {
		v := &database.ListMigrationsResult{IsApplied: true}
		if err := rows.Scan(&v.Version); err != nil {
			return nil, err
		}
		if v.Version > s.latest {
			return nil, fmt.Errorf("unsupported newer schema version: %d", v.Version)
		}
		versions = append(versions, v)
	}
	return versions, rows.Err()
}
