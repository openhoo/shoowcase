// Package sites owns tenant isolation, immutable site revisions, releases and delivery.
package sites

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type Actor struct{ Subject, Workspace, Role string }
type File struct {
	Data                                      []byte
	ContentType, Digest, Revision, Visibility string
	Size                                      int
	NotModified                               bool
}
type Error struct {
	Code, Message      string
	approvalGeneration int64
}

func (e *Error) Error() string     { return e.Message }
func (e *Error) ErrorCode() string { return e.Code }
func fail(c, m string) error       { return &Error{Code: c, Message: m} }
func bad(m string) error           { return fail("invalid_argument", m) }
func absent() error                { return fail("not_found", "Site, revision, release, or file not found.") }
func conflict(m string) error      { return fail("conflict", m) }

type privateDeliveryKey struct{}

func WithPrivateDelivery(ctx context.Context) context.Context {
	return context.WithValue(ctx, privateDeliveryKey{}, true)
}
func privateOnly(ctx context.Context) bool { b, _ := ctx.Value(privateDeliveryKey{}).(bool); return b }

type approvalKey struct{}

// ApprovalGeneration returns the publication generation observed under the
// workspace lock. A web approval must sign this value with the operation.
func ApprovalGeneration(err error) (int64, bool) {
	var e *Error
	if !errors.As(err, &e) || e.Code != "approval_required" || e.approvalGeneration < 1 {
		return 0, false
	}
	return e.approvalGeneration, true
}

func WithApproval(ctx context.Context, generation int64) context.Context {
	return context.WithValue(ctx, approvalKey{}, generation)
}

type Store struct {
	db     *sql.DB
	driver string
}

func (s *Store) PoolStats() sql.DBStats         { return s.db.Stats() }
func (s *Store) Close() error                   { return s.db.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Use one snapshot for authorization and subsequent result queries: a reader
// whose access changes during a lookup cannot observe content created after
// that change. SQLite already pins the snapshot on its first read.
func (s *Store) readSnapshot(ctx context.Context) (*sql.Tx, error) {
	opts := &sql.TxOptions{ReadOnly: true}
	if s.driver == "pgx" {
		opts.Isolation = sql.LevelRepeatableRead
	}
	return s.db.BeginTx(ctx, opts)
}

func Open(ctx context.Context, driver, dsn string) (*Store, error) {
	switch driver {
	case "postgres", "postgresql", "pgx":
		driver = "pgx"
	case "sqlite", "sqlite3":
		driver = "sqlite"
	default:
		return nil, fmt.Errorf("unsupported store driver %q", driver)
	}
	if dsn == "" {
		return nil, errors.New("empty store DSN")
	}
	db, e := sql.Open(driver, dsn)
	if e != nil {
		return nil, e
	}
	if driver == "sqlite" {
		db.SetMaxOpenConns(1)
	} else {
		db.SetMaxOpenConns(16)
		db.SetMaxIdleConns(4)
		db.SetConnMaxLifetime(30 * time.Minute)
	}
	if driver == "sqlite" {
		for _, q := range []string{"PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000"} {
			if _, e = db.ExecContext(ctx, q); e != nil {
				db.Close()
				return nil, e
			}
		}
	}
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		db.Close()
		return nil, e
	}
	defer tx.Rollback()
	if driver == "pgx" {
		_, e = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(4637380929301)")
		if e != nil {
			db.Close()
			return nil, e
		}
	}
	for _, q := range schema {
		if _, e = tx.ExecContext(ctx, q); e != nil {
			tx.Rollback()
			db.Close()
			return nil, fmt.Errorf("initialize store: %w", e)
		}
	}
	if e = migrate(ctx, tx); e != nil {
		tx.Rollback()
		db.Close()
		return nil, fmt.Errorf("migrate store: %w", e)
	}
	if e = tx.Commit(); e != nil {
		db.Close()
		return nil, e
	}
	return &Store{db: db, driver: driver}, nil
}

var schema = []string{
	"CREATE TABLE IF NOT EXISTS sc_workspace (id TEXT PRIMARY KEY, used_bytes BIGINT NOT NULL DEFAULT 0, changes BIGINT NOT NULL DEFAULT 0)",
	"CREATE TABLE IF NOT EXISTS sc_sites (id TEXT PRIMARY KEY, workspace TEXT NOT NULL REFERENCES sc_workspace(id), name TEXT NOT NULL, visibility TEXT NOT NULL, draft TEXT NOT NULL, live TEXT NOT NULL, release_id TEXT NOT NULL)",
	"CREATE INDEX IF NOT EXISTS sc_sites_workspace ON sc_sites(workspace,id)",
	"CREATE TABLE IF NOT EXISTS sc_revisions (id TEXT PRIMARY KEY, site TEXT NOT NULL REFERENCES sc_sites(id), created_at BIGINT NOT NULL)",
	"CREATE INDEX IF NOT EXISTS sc_revisions_site ON sc_revisions(site,id)",
	"CREATE TABLE IF NOT EXISTS sc_blobs (digest TEXT PRIMARY KEY, body BYTEA NOT NULL)",
	"CREATE TABLE IF NOT EXISTS sc_files (revision TEXT NOT NULL REFERENCES sc_revisions(id), path TEXT NOT NULL, digest TEXT NOT NULL REFERENCES sc_blobs(digest), size BIGINT NOT NULL, content_type TEXT NOT NULL, PRIMARY KEY(revision,path))",
	"CREATE TABLE IF NOT EXISTS sc_releases (id TEXT PRIMARY KEY, site TEXT NOT NULL REFERENCES sc_sites(id), revision TEXT NOT NULL REFERENCES sc_revisions(id), created_at BIGINT NOT NULL)",
	"CREATE INDEX IF NOT EXISTS sc_releases_site ON sc_releases(site,id)",
	"CREATE TABLE IF NOT EXISTS sc_requests (workspace TEXT NOT NULL, subject TEXT NOT NULL, request_id TEXT NOT NULL, hash TEXT NOT NULL, result TEXT NOT NULL, PRIMARY KEY(workspace,subject,request_id))",
	"CREATE TABLE IF NOT EXISTS sc_audit (id TEXT PRIMARY KEY, workspace TEXT NOT NULL, site TEXT NOT NULL, subject TEXT NOT NULL, action TEXT NOT NULL, revision TEXT NOT NULL, release_id TEXT NOT NULL, live_revision TEXT NOT NULL, visibility TEXT NOT NULL, request_id TEXT NOT NULL, created_at BIGINT NOT NULL)",
	"CREATE INDEX IF NOT EXISTS sc_audit_workspace ON sc_audit(workspace,created_at)",
	"CREATE INDEX IF NOT EXISTS sc_audit_site_activity ON sc_audit(workspace,site,created_at)",
	"CREATE TABLE IF NOT EXISTS sc_replay (key TEXT PRIMARY KEY, expires_at BIGINT NOT NULL)",
	"CREATE INDEX IF NOT EXISTS sc_replay_expiry ON sc_replay(expires_at)",
}

// Every change here runs in the same transaction as schema initialization.
// Never alter an existing migration: old installations and fresh databases
// pass through the same numbered steps.
func migrate(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS sc_schema_migrations (version INTEGER PRIMARY KEY)"); err != nil {
		return err
	}
	var version int
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(version),0) FROM sc_schema_migrations").Scan(&version); err != nil {
		return err
	}
	if version > 5 {
		return errors.New("unsupported newer schema version")
	}
	if version < 1 {
		if _, err := tx.ExecContext(ctx, "ALTER TABLE sc_sites ADD COLUMN publication_generation BIGINT NOT NULL DEFAULT 1"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO sc_schema_migrations(version) VALUES(1)"); err != nil {
			return err
		}
	}
	if version < 2 {
		for _, stmt := range accountSchema {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO sc_schema_migrations(version) VALUES(2)"); err != nil {
			return err
		}
	}
	if version < 3 {
		for _, statements := range [][]string{tenantSchema, {issuerBindingSchema}, auditSchema} {
			for _, stmt := range statements {
				if _, err := tx.ExecContext(ctx, stmt); err != nil {
					return err
				}
			}
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO sc_schema_migrations(version) VALUES(3)"); err != nil {
			return err
		}
	}
	if version < 4 {
		for _, stmt := range quotaSchema {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO sc_schema_migrations(version) VALUES(4)"); err != nil {
			return err
		}
	}
	if version < 5 {
		if _, err := tx.ExecContext(ctx, localCredentialSchema); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO sc_schema_migrations(version) VALUES(5)"); err != nil {
			return err
		}
	}
	return nil
}

func id() string          { var buf [16]byte; _, _ = rand.Read(buf[:]); return hex.EncodeToString(buf[:]) }
func sum(b []byte) string { x := sha256.Sum256(b); return hex.EncodeToString(x[:]) }
func rank(a Actor) int {
	if strings.TrimSpace(a.Subject) == "" || strings.TrimSpace(a.Workspace) == "" {
		return 0
	}
	switch a.Role {
	case "reader":
		return 1
	case "editor":
		return 2
	case "publisher":
		return 3
	case "admin":
		return 4
	}
	return 0
}
func actorErr(a Actor, level int) error {
	if a.Subject == "" || a.Workspace == "" {
		return fail("unauthorized", "Sign in to a workspace.")
	}
	if rank(a) < level {
		return fail("forbidden", "Your workspace role does not permit this action.")
	}
	return nil
}

type record struct {
	site, name, visibility, draft, live, release, group string
	generation                                          int64
}

func (r record) out() map[string]any {
	return map[string]any{"site_id": r.site, "slug": r.site, "group_id": r.group, "name": r.name, "visibility": r.visibility, "revision": r.draft, "live_revision": r.live, "release_id": r.release}
}

// effectiveAudience also supports receipts persisted before configured_visibility
// existed. Receipts retain the historical site policy but use current instance policy.
func effectiveAudience(ctx context.Context, out map[string]any) map[string]any {
	configured, ok := out["configured_visibility"].(string)
	if !ok {
		configured, _ = out["visibility"].(string)
	}
	out["configured_visibility"] = configured
	if configured == "public" && privateOnly(ctx) {
		out["visibility"] = "workspace"
	} else {
		out["visibility"] = configured
	}
	return out
}
func readSite(ctx context.Context, tx *sql.Tx, a Actor, site string) (record, error) {
	var r record
	e := tx.QueryRowContext(ctx, `SELECT s.id,s.name,s.visibility,s.draft,s.live,s.release_id,s.publication_generation,COALESCE(sg.group_id,'')
		FROM sc_sites s LEFT JOIN sc_site_groups sg ON sg.workspace=s.workspace AND sg.site=s.id WHERE s.id=$1 AND s.workspace=$2`, site, a.Workspace).Scan(&r.site, &r.name, &r.visibility, &r.draft, &r.live, &r.release, &r.generation, &r.group)
	if errors.Is(e, sql.ErrNoRows) {
		return r, absent()
	}
	return r, e
}

// Embed this predicate in the selection of the resource itself. A cached
// browser identity cannot outlive a member suspension, even on HEAD or 304.
// Only identity-mode tenants permit IdP readers without local membership.
const memberAudience = `s.workspace=$1 AND EXISTS (
    SELECT 1 FROM sc_workspace w LEFT JOIN sc_members m ON m.workspace=w.id AND m.subject=$2
    WHERE w.id=s.workspace AND (m.disabled=FALSE OR (m.subject IS NULL AND w.access_mode='identity')))
    AND ($3='' OR EXISTS (SELECT 1 FROM sc_agent_keys k WHERE k.workspace=s.workspace
      AND k.subject=$2 AND k.id=$3 AND NOT k.revoked AND k.expires_at>$4))`

func deliveryIdentity(ctx context.Context, a Actor) []any {
	if rank(a) == 0 {
		a = Actor{}
	}
	keyID, _ := ctx.Value(keyCredentialID{}).(string)
	return []any{a.Workspace, a.Subject, keyID, time.Now().Unix()}
}

func (s *Store) PreviewSlug(ctx context.Context, revision string, a Actor) (string, error) {
	if rank(a) == 0 {
		return "", absent()
	}
	var site string
	e := s.db.QueryRowContext(ctx, "SELECT s.id FROM sc_sites s JOIN sc_revisions r ON r.site=s.id WHERE r.id=$5 AND "+memberAudience, append(deliveryIdentity(ctx, a), revision)...).Scan(&site)
	if errors.Is(e, sql.ErrNoRows) {
		return "", absent()
	}
	return site, e
}
func (s *Store) Fetch(ctx context.Context, slug, revision, path string, a Actor) (File, error) {
	return s.FetchConditional(ctx, slug, revision, path, a, "")
}
func (s *Store) FetchConditional(ctx context.Context, slug, revision, path string, a Actor, ifDigest string) (File, error) {
	return s.fetch(ctx, slug, revision, path, a, ifDigest, false, "")
}

// FetchMetadata checks the same authorization as GET but does not read blob
// bytes. Size is the length of the selected file, including on conditional hits.
func (s *Store) FetchMetadata(ctx context.Context, slug, revision, path string, a Actor, ifDigest string) (File, error) {
	return s.fetch(ctx, slug, revision, path, a, ifDigest, true, "")
}

// FetchCompanion loads a script using the same audience as its page. The
// live pointer can change between requests; never follow it to a mismatched
// revision or allow the supplied ID to turn a live read into a preview.
func (s *Store) FetchCompanion(ctx context.Context, slug, previewRevision, pageRevision, path string, a Actor) (File, error) {
	if !idRE.MatchString(pageRevision) || !validPath(path) || !strings.HasSuffix(strings.ToLower(path), ".js") {
		return File{}, absent()
	}
	return s.fetch(ctx, slug, previewRevision, path, a, "", false, pageRevision)
}

func (s *Store) fetch(ctx context.Context, slug, revision, path string, a Actor, ifDigest string, metadataOnly bool, exactRevision string) (File, error) {
	var f File
	if strings.HasPrefix(path, "/") {
		path = path[1:]
	}
	if path == "" || strings.HasSuffix(path, "/") {
		path += "index.html"
	}
	if !validPath(path) {
		return f, absent()
	}
	// HEAD never references the blob table. Conditional GET loads a body only
	// on a miss, after authorizing the chosen revision in the same snapshot.
	const selectFile = `,f.content_type,f.digest,f.size,r.id,s.visibility,(f.digest=$5)
      FROM sc_sites s JOIN sc_revisions r ON r.site=s.id AND r.id=CASE WHEN $8='' THEN s.live ELSE $8 END
      JOIN sc_files f ON f.revision=r.id
      WHERE s.id=$6 AND f.path=$7 AND ($10='' OR r.id=$10) AND (
        ($8='' AND s.visibility='public' AND $9) OR
        (($8<>'' OR s.visibility IN ('public','workspace')) AND ` + memberAudience + `))`
	query := "SELECT CASE WHEN f.digest=$5 THEN NULL ELSE (SELECT body FROM sc_blobs WHERE digest=f.digest) END" + selectFile
	if metadataOnly {
		query = "SELECT NULL" + selectFile
	}
	params := append(deliveryIdentity(ctx, a), ifDigest, slug, path, revision, !privateOnly(ctx), exactRevision)
	e := s.db.QueryRowContext(ctx, query, params...).Scan(&f.Data, &f.ContentType, &f.Digest, &f.Size, &f.Revision, &f.Visibility, &f.NotModified)
	if errors.Is(e, sql.ErrNoRows) {
		return f, absent()
	}
	if e == nil {
		if revision != "" || privateOnly(ctx) {
			f.Visibility = "workspace"
		}
	}
	return f, e
}

var slugRE = regexp.MustCompile("^[a-z0-9]([a-z0-9-]{0,46}[a-z0-9])?$")
var idRE = regexp.MustCompile("^[a-f0-9]{32}$")

func validSlug(s string) bool {
	if !slugRE.MatchString(s) || strings.HasPrefix(s, "pv-") {
		return false
	}
	switch s {
	case "www", "admin", "api", "auth", "app", "sites", "preview", "mail", "assets", "cdn", "localhost", "mcp", "studio":
		return false
	}
	return true
}
func defaultSlug(name string) string {
	base := regexp.MustCompile("[^a-z0-9]+").ReplaceAllString(strings.ToLower(name), "-")
	base = strings.Trim(base, "-")
	if len(base) > 35 {
		base = strings.TrimRight(base[:35], "-")
	}
	if base == "" || strings.HasPrefix(base, "pv-") {
		base = "site"
	}
	return base + "-" + id()[:10]
}
func audit(ctx context.Context, tx *sql.Tx, a Actor, r record, tool, req string) error {
	if e := appendAudit(ctx, tx, a, AuditEvent{Action: tool, Target: r.site, Value: r.visibility, DraftRevision: r.draft, LiveRevision: r.live, ReleaseID: r.release}); e != nil {
		return e
	}
	_, e := tx.ExecContext(ctx, "INSERT INTO sc_audit(id,workspace,site,subject,action,revision,release_id,live_revision,visibility,request_id,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)", id(), a.Workspace, r.site, a.Subject, tool, r.draft, r.release, r.live, r.visibility, req, time.Now().UnixMilli())
	return e
}
func strict(raw json.RawMessage, dst any) error { return decodeStrict(raw, dst) }

// ReleaseRevision resolves an immutable release within the authenticated workspace.
func (s *Store) ReleaseRevision(ctx context.Context, a Actor, site, release string) (string, error) {
	if rank(a) == 0 {
		return "", absent()
	}
	var rev string
	e := s.db.QueryRowContext(ctx, "SELECT r.revision FROM sc_releases r JOIN sc_sites s ON s.id=r.site WHERE r.id=$5 AND s.id=$6 AND "+memberAudience, append(deliveryIdentity(ctx, a), release, site)...).Scan(&rev)
	if errors.Is(e, sql.ErrNoRows) {
		return "", absent()
	}
	return rev, e
}
