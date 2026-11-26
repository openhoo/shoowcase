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
	NotModified                               bool
}
type Error struct{ Code, Message string }

func (e *Error) Error() string     { return e.Message }
func (e *Error) ErrorCode() string { return e.Code }
func fail(c, m string) error       { return &Error{c, m} }
func bad(m string) error           { return fail("invalid_argument", m) }
func absent() error                { return fail("not_found", "Site, revision, release, or file not found.") }
func conflict(m string) error      { return fail("conflict", m) }

type privateDeliveryKey struct{}

func WithPrivateDelivery(ctx context.Context) context.Context {
	return context.WithValue(ctx, privateDeliveryKey{}, true)
}
func privateOnly(ctx context.Context) bool { b, _ := ctx.Value(privateDeliveryKey{}).(bool); return b }

type approvalKey struct{}

func WithApproval(ctx context.Context) context.Context {
	return context.WithValue(ctx, approvalKey{}, true)
}
func approved(ctx context.Context) bool { v, _ := ctx.Value(approvalKey{}).(bool); return v }

type Store struct {
	db     *sql.DB
	driver string
}

func (s *Store) Close() error                   { return s.db.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
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
	"CREATE TABLE IF NOT EXISTS sc_replay (key TEXT PRIMARY KEY, expires_at BIGINT NOT NULL)",
	"CREATE INDEX IF NOT EXISTS sc_replay_expiry ON sc_replay(expires_at)",
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

type record struct{ site, name, visibility, draft, live, release string }

func (r record) out() map[string]any {
	return map[string]any{"site_id": r.site, "slug": r.site, "name": r.name, "visibility": r.visibility, "revision": r.draft, "live_revision": r.live, "release_id": r.release}
}
func readSite(ctx context.Context, tx *sql.Tx, a Actor, site string) (record, error) {
	var r record
	e := tx.QueryRowContext(ctx, "SELECT id,name,visibility,draft,live,release_id FROM sc_sites WHERE id=$1 AND workspace=$2", site, a.Workspace).Scan(&r.site, &r.name, &r.visibility, &r.draft, &r.live, &r.release)
	if errors.Is(e, sql.ErrNoRows) {
		return r, absent()
	}
	return r, e
}
func (s *Store) PreviewSlug(ctx context.Context, revision string, a Actor) (string, error) {
	if rank(a) == 0 {
		return "", absent()
	}
	var site string
	e := s.db.QueryRowContext(ctx, "SELECT s.id FROM sc_sites s JOIN sc_revisions r ON r.site=s.id WHERE r.id=$1 AND s.workspace=$2", revision, a.Workspace).Scan(&site)
	if errors.Is(e, sql.ErrNoRows) {
		return "", absent()
	}
	return site, e
}
func (s *Store) Fetch(ctx context.Context, slug, revision, path string, a Actor) (File, error) {
	return s.FetchConditional(ctx, slug, revision, path, a, "")
}
func (s *Store) FetchConditional(ctx context.Context, slug, revision, path string, a Actor, ifDigest string) (File, error) {
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
	ws := ""
	if rank(a) > 0 {
		ws = a.Workspace
	}
	const query = "SELECT CASE WHEN f.digest=$1 THEN NULL ELSE b.body END,f.content_type,f.digest,r.id,s.visibility FROM sc_sites s JOIN sc_revisions r ON r.site=s.id JOIN sc_files f ON f.revision=r.id JOIN sc_blobs b ON b.digest=f.digest WHERE s.id=$2 AND f.path=$3 AND (($4='' AND r.id=s.live AND ((s.visibility='public' AND $6) OR (s.visibility IN ('public','workspace') AND s.workspace=$5))) OR ($4<>'' AND r.id=$4 AND s.workspace=$5))"
	e := s.db.QueryRowContext(ctx, query, ifDigest, slug, path, revision, ws, !privateOnly(ctx)).Scan(&f.Data, &f.ContentType, &f.Digest, &f.Revision, &f.Visibility)
	if errors.Is(e, sql.ErrNoRows) {
		return f, absent()
	}
	if e == nil {
		f.NotModified = f.Digest == ifDigest
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
	e := s.db.QueryRowContext(ctx, "SELECT r.revision FROM sc_releases r JOIN sc_sites s ON s.id=r.site WHERE r.id=$1 AND s.id=$2 AND s.workspace=$3", release, site, a.Workspace).Scan(&rev)
	if errors.Is(e, sql.ErrNoRows) {
		return "", absent()
	}
	return rev, e
}
