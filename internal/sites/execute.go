package sites

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	MaxSitesPerWorkspace      = 100
	MaxRevisionsPerSite       = 500
	MaxWorkspaceManifestBytes = 2 << 30
	MaxChangesPerWorkspace    = 50000
)

type mutation struct {
	Name            string      `json:"name"`
	Slug            string      `json:"slug"`
	Site            string      `json:"site_id"`
	Request         string      `json:"request_id"`
	Expected        string      `json:"expected_revision"`
	Revision        string      `json:"revision"`
	Files           []fileInput `json:"files"`
	Deletes         []string    `json:"delete_paths"`
	Visibility      string      `json:"visibility"`
	Release         string      `json:"release_id"`
	ExpectedRelease string      `json:"expected_release_id"`
}
type lookup struct {
	Site     string `json:"site_id"`
	Path     string `json:"path"`
	Revision string `json:"revision"`
	Cursor   string `json:"cursor"`
	Limit    *int   `json:"limit"`
	View     string `json:"view"`
}

var fields = map[string]map[string]bool{
	"create_site": wordset("name slug request_id"), "list_sites": wordset("cursor limit"), "get_site": wordset("site_id path revision cursor limit view"),
	"upload_files": wordset("site_id expected_revision files delete_paths request_id"), "publish_site": wordset("site_id revision request_id"), "set_site_access": wordset("site_id visibility revision request_id"), "rollback_site": wordset("site_id release_id expected_release_id request_id"),
}

func decodeStrict(raw json.RawMessage, dst any) error {
	if len(raw) == 0 || len(raw) > MaxRequestBytes || !utf8.Valid(raw) {
		return bad("Invalid UTF-8 JSON or request exceeds 12 MiB.")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(dst); e != nil {
		return bad("Invalid JSON or argument types.")
	}
	var extra any
	if e := d.Decode(&extra); e != io.EOF {
		return bad("Expected one JSON object.")
	}
	return nil
}
func checkedFields(raw json.RawMessage, tool string) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if e := decodeStrict(raw, &m); e != nil {
		return nil, e
	}
	if m == nil {
		return nil, bad("Tool arguments must be an object.")
	}
	for k, v := range m {
		if !fields[tool][k] || string(v) == "null" {
			return nil, bad("Unknown or null argument: " + k)
		}
	}
	return m, nil
}
func (s *Store) Execute(ctx context.Context, a Actor, tool string, raw json.RawMessage) (out map[string]any, err error) {
	if _, ok := fields[tool]; !ok {
		return nil, bad("Unknown site tool.")
	}
	level := 3
	switch tool {
	case "list_sites", "get_site":
		level = 1
	case "create_site", "upload_files":
		level = 2
	}
	if e := actorErr(a, level); e != nil {
		return nil, e
	}
	m, e := checkedFields(raw, tool)
	if e != nil {
		return nil, e
	}
	if level == 1 {
		var q lookup
		if e = strict(raw, &q); e != nil {
			return nil, e
		}
		return s.lookup(ctx, a, tool, q)
	}
	var p mutation
	if e = strict(raw, &p); e != nil {
		return nil, e
	}
	if len(p.Request) < 1 || len(p.Request) > 128 || strings.ContainsFunc(p.Request, func(r rune) bool { return r < 33 || r > 126 }) {
		return nil, bad("request_id must contain 1–128 printable ASCII characters without spaces.")
	}
	if tool != "create_site" && !validSlug(p.Site) {
		return nil, bad("Invalid site_id.")
	}
	switch tool {
	case "create_site":
		p.Name = strings.TrimSpace(p.Name)
		if p.Name == "" || utf8.RuneCountInString(p.Name) > 100 || strings.ContainsFunc(p.Name, unicode.IsControl) {
			return nil, bad("Site name must contain 1–100 characters without control characters.")
		}
		if _, has := m["slug"]; has && !validSlug(p.Slug) {
			return nil, bad("Invalid or reserved slug.")
		}
	case "upload_files":
		if !idRE.MatchString(p.Expected) || len(p.Files)+len(p.Deletes) == 0 || len(p.Files) > MaxFiles || len(p.Deletes) > MaxFiles {
			return nil, bad("Provide expected_revision and 1–100 files or paths to delete.")
		}
	case "publish_site", "set_site_access":
		if !idRE.MatchString(p.Revision) {
			return nil, bad("Provide a valid revision.")
		}
		if tool == "set_site_access" && p.Visibility != "workspace" && p.Visibility != "public" && p.Visibility != "offline" {
			return nil, bad("Unknown visibility.")
		}
	case "rollback_site":
		if !idRE.MatchString(p.Release) || !idRE.MatchString(p.ExpectedRelease) {
			return nil, bad("Provide valid release_id and expected_release_id.")
		}
	}
	var additions []asset
	if tool == "upload_files" {
		seen := map[string]bool{}
		total := 0
		for _, f := range p.Files {
			if seen[f.Path] {
				return nil, bad("Duplicate file operation.")
			}
			seen[f.Path] = true
			asset, e := prepare(f)
			if e != nil {
				return nil, e
			}
			total += asset.Size
			if total > MaxRevisionBytes {
				return nil, fail("quota_exceeded", "Upload exceeds 8 MiB.")
			}
			additions = append(additions, asset)
		}
		for _, d := range p.Deletes {
			if !validPath(d) || seen[d] {
				return nil, bad("Invalid or duplicate deletion path.")
			}
			seen[d] = true
		}
	}
	// Canonicalize the typed data, including nested files, for idempotency across JSON layouts.
	canonical, _ := json.Marshal(p)
	hash := sum(append([]byte(tool+"\x00"), canonical...))
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	// One lock per workspace serializes slug changes, quotas, optimistic checks and
	// approvals across PostgreSQL instances. No global lock over unrelated tenants.
	if _, e = tx.ExecContext(ctx, "INSERT INTO sc_workspace(id) VALUES($1) ON CONFLICT(id) DO NOTHING", a.Workspace); e != nil {
		return nil, e
	}
	if s.driver == "pgx" {
		var lock string
		if e = tx.QueryRowContext(ctx, "SELECT id FROM sc_workspace WHERE id=$1 FOR UPDATE", a.Workspace).Scan(&lock); e != nil {
			return nil, e
		}
	}
	var h, saved string
	e = tx.QueryRowContext(ctx, "SELECT hash,result FROM sc_requests WHERE workspace=$1 AND subject=$2 AND request_id=$3", a.Workspace, a.Subject, p.Request).Scan(&h, &saved)
	if e == nil {
		if h != hash {
			return nil, conflict("request_id was already used with different arguments.")
		}
		var res map[string]any
		if e = json.Unmarshal([]byte(saved), &res); e != nil {
			return nil, e
		}
		return res, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	}
	var n int
	if e = tx.QueryRowContext(ctx, "SELECT changes FROM sc_workspace WHERE id=$1", a.Workspace).Scan(&n); e != nil {
		return nil, e
	}
	if n >= MaxChangesPerWorkspace && (tool != "set_site_access" || p.Visibility == "public") {
		return nil, fail("quota_exceeded", "Workspace change history is full. Contact an administrator.")
	}
	var r record
	if tool == "create_site" {
		r, e = create(ctx, tx, a, p)
	} else {
		r, e = readSite(ctx, tx, a, p.Site)
		if e != nil {
			return nil, e
		}
		if n >= MaxChangesPerWorkspace && !(tool == "set_site_access" && r.visibility == "public" && p.Visibility != "public" || tool == "set_site_access" && r.visibility == "workspace" && p.Visibility == "offline") {
			return nil, fail("quota_exceeded", "Only restricting live access is permitted after the change history fills.")
		}
		switch tool {
		case "upload_files":
			e = upload(ctx, tx, a, &r, p, additions)
		case "publish_site", "rollback_site", "set_site_access":
			e = releaseChange(ctx, tx, &r, p, tool)
		}
	}
	if e != nil {
		return nil, e
	}
	if _, e = tx.ExecContext(ctx, "UPDATE sc_workspace SET changes=changes+1 WHERE id=$1", a.Workspace); e != nil {
		return nil, e
	}
	if e = audit(ctx, tx, a, r, tool, p.Request); e != nil {
		return nil, e
	}
	res := r.out()
	j, _ := json.Marshal(res)
	_, e = tx.ExecContext(ctx, "INSERT INTO sc_requests(workspace,subject,request_id,hash,result) VALUES($1,$2,$3,$4,$5)", a.Workspace, a.Subject, p.Request, hash, string(j))
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return res, nil
}
func create(ctx context.Context, tx *sql.Tx, a Actor, p mutation) (record, error) {
	var n int
	if e := tx.QueryRowContext(ctx, "SELECT count(*) FROM sc_sites WHERE workspace=$1", a.Workspace).Scan(&n); e != nil {
		return record{}, e
	}
	if n >= MaxSitesPerWorkspace {
		return record{}, fail("quota_exceeded", "Workspace site limit reached.")
	}
	slug := p.Slug
	if slug == "" {
		slug = defaultSlug(p.Name)
	}
	r := record{site: slug, name: p.Name, visibility: "workspace", draft: id()}
	res, e := tx.ExecContext(ctx, "INSERT INTO sc_sites(id,workspace,name,visibility,draft,live,release_id) VALUES($1,$2,$3,'workspace',$4,'','') ON CONFLICT(id) DO NOTHING", slug, a.Workspace, p.Name, r.draft)
	if e != nil {
		return r, e
	}
	num, e := res.RowsAffected()
	if e != nil {
		return r, e
	}
	if num != 1 {
		return r, conflict("Site slug is unavailable.")
	}
	_, e = tx.ExecContext(ctx, "INSERT INTO sc_revisions(id,site,created_at) VALUES($1,$2,$3)", r.draft, r.site, time.Now().UnixMilli())
	return r, e
}
func upload(ctx context.Context, tx *sql.Tx, a Actor, r *record, p mutation, adds []asset) error {
	if r.draft != p.Expected {
		return conflict("Draft changed; read the current revision and retry.")
	}
	var count int
	if e := tx.QueryRowContext(ctx, "SELECT count(*) FROM sc_revisions WHERE site=$1", r.site).Scan(&count); e != nil {
		return e
	}
	if count >= MaxRevisionsPerSite {
		return fail("quota_exceeded", "Site revision limit reached.")
	}
	rows, e := tx.QueryContext(ctx, "SELECT path,digest,size,content_type FROM sc_files WHERE revision=$1", r.draft)
	if e != nil {
		return e
	}
	manifest := map[string]asset{}
	for rows.Next() {
		var v asset
		if e = rows.Scan(&v.Path, &v.Digest, &v.Size, &v.ContentType); e != nil {
			rows.Close()
			return e
		}
		manifest[v.Path] = v
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, p := range p.Deletes {
		if _, ok := manifest[p]; !ok {
			return bad("Deletion path is absent: " + p)
		}
		delete(manifest, p)
	}
	for _, v := range adds {
		manifest[v.Path] = v
	}
	size := 0
	for _, v := range manifest {
		size += v.Size
	}
	if len(manifest) > MaxFiles || size > MaxRevisionBytes {
		return fail("quota_exceeded", "Revision exceeds 100 files or 8 MiB.")
	}
	var storage int64
	if e = tx.QueryRowContext(ctx, "SELECT used_bytes FROM sc_workspace WHERE id=$1", a.Workspace).Scan(&storage); e != nil {
		return e
	}
	if storage+int64(size) > MaxWorkspaceManifestBytes {
		return fail("quota_exceeded", "Workspace revision history exceeds 2 GiB.")
	}
	if _, e = tx.ExecContext(ctx, "UPDATE sc_workspace SET used_bytes=used_bytes+$1 WHERE id=$2", size, a.Workspace); e != nil {
		return e
	}
	next := id()
	if _, e = tx.ExecContext(ctx, "INSERT INTO sc_revisions(id,site,created_at) VALUES($1,$2,$3)", next, r.site, time.Now().UnixMilli()); e != nil {
		return e
	}
	// Insert shared digests in the same order across tenants to avoid lock inversion.
	sort.Slice(adds, func(i, j int) bool { return adds[i].Digest < adds[j].Digest })
	for _, v := range adds {
		if _, e = tx.ExecContext(ctx, "INSERT INTO sc_blobs(digest,body) VALUES($1,$2) ON CONFLICT(digest) DO NOTHING", v.Digest, v.data); e != nil {
			return e
		}
	}
	for _, v := range manifest {
		if _, e = tx.ExecContext(ctx, "INSERT INTO sc_files(revision,path,digest,size,content_type) VALUES($1,$2,$3,$4,$5)", next, v.Path, v.Digest, v.Size, v.ContentType); e != nil {
			return e
		}
	}
	if _, e = tx.ExecContext(ctx, "UPDATE sc_sites SET draft=$1 WHERE id=$2", next, r.site); e == nil {
		r.draft = next
	}
	return e
}
func releaseChange(ctx context.Context, tx *sql.Tx, r *record, p mutation, tool string) error {
	requireApproval := func() error {
		if privateOnly(ctx) {
			return fail("forbidden", "Public publishing is disabled on this instance.")
		}
		if !approved(ctx) {
			return fail("approval_required", "Confirm the exact public change in your browser.")
		}
		return nil
	}
	switch tool {
	case "publish_site":
		if p.Revision != r.draft {
			return conflict("Only the current draft can be published.")
		}
		var ok int
		e := tx.QueryRowContext(ctx, "SELECT 1 FROM sc_files WHERE revision=$1 AND path='index.html'", r.draft).Scan(&ok)
		if errors.Is(e, sql.ErrNoRows) {
			return bad("Add index.html before publishing.")
		}
		if e != nil {
			return e
		}
		if r.live == p.Revision {
			return nil
		}
		if r.visibility == "public" {
			if e = requireApproval(); e != nil {
				return e
			}
		}
		if e = checkReleaseLinks(ctx, tx, p.Revision); e != nil {
			return e
		}
		r.release = id()
		r.live = p.Revision
		if _, e = tx.ExecContext(ctx, "INSERT INTO sc_releases(id,site,revision,created_at) VALUES($1,$2,$3,$4)", r.release, r.site, r.live, time.Now().UnixMicro()); e != nil {
			return e
		}
	case "set_site_access":
		current := r.live
		if current == "" {
			current = r.draft
		}
		if current != p.Revision {
			return conflict("Access change must match the current live revision (or draft if unpublished).")
		}
		if p.Visibility == "public" {
			if r.live == "" {
				return bad("Publish a workspace release before enabling public access.")
			}
			if r.visibility != "public" {
				if e := requireApproval(); e != nil {
					return e
				}
			}
		}
		r.visibility = p.Visibility
	case "rollback_site":
		if r.release != p.ExpectedRelease {
			return conflict("Live release changed; read it again.")
		}
		var target string
		e := tx.QueryRowContext(ctx, "SELECT revision FROM sc_releases WHERE id=$1 AND site=$2", p.Release, r.site).Scan(&target)
		if errors.Is(e, sql.ErrNoRows) {
			return absent()
		}
		if e != nil {
			return e
		}
		if r.visibility == "public" && p.Release != r.release {
			if e = requireApproval(); e != nil {
				return e
			}
		}
		r.release = p.Release
		r.live = target
	}
	_, e := tx.ExecContext(ctx, "UPDATE sc_sites SET live=$1,release_id=$2,visibility=$3 WHERE id=$4", r.live, r.release, r.visibility, r.site)
	return e
}
func (s *Store) lookup(ctx context.Context, a Actor, tool string, q lookup) (map[string]any, error) {
	limit := 25
	if q.Limit != nil {
		limit = *q.Limit
	}
	if limit < 1 || limit > 100 || len(q.Cursor) > 256 {
		return nil, bad("limit must be 1–100 and cursor cannot exceed 256 characters.")
	}
	tx, e := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if tool == "list_sites" {
		rows, e := tx.QueryContext(ctx, "SELECT id,name,visibility,draft,live,release_id FROM sc_sites WHERE workspace=$1 AND id>$2 ORDER BY id LIMIT $3", a.Workspace, q.Cursor, limit+1)
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		list := []map[string]any{}
		last := ""
		next := ""
		for rows.Next() {
			var r record
			if e = rows.Scan(&r.site, &r.name, &r.visibility, &r.draft, &r.live, &r.release); e != nil {
				return nil, e
			}
			if len(list) == limit {
				next = last
				break
			}
			list = append(list, r.out())
			last = r.site
		}
		return map[string]any{"sites": list, "next_cursor": next}, rows.Err()
	}
	if !validSlug(q.Site) {
		return nil, bad("Invalid site_id.")
	}
	r, e := readSite(ctx, tx, a, q.Site)
	if e != nil {
		return nil, e
	}
	out := r.out()
	if q.View != "" && q.View != "releases" && q.View != "files" {
		return nil, bad("view must be files or releases.")
	}
	if q.View == "releases" {
		if q.Path != "" || q.Revision != "" {
			return nil, bad("Release history does not take a path or revision.")
		}
		// Keyset pagination is ordered by timestamp then id; the cursor is the last release ID.
		var stamp int64 = 1 << 62
		cut := ""
		if q.Cursor != "" {
			cut = q.Cursor
			e = tx.QueryRowContext(ctx, "SELECT created_at FROM sc_releases WHERE site=$1 AND id=$2", r.site, cut).Scan(&stamp)
			if errors.Is(e, sql.ErrNoRows) {
				return nil, bad("Invalid release cursor.")
			}
			if e != nil {
				return nil, e
			}
		}
		rows, e := tx.QueryContext(ctx, "SELECT id,revision,created_at FROM sc_releases WHERE site=$1 AND (created_at<$2 OR (created_at=$2 AND id<$3)) ORDER BY created_at DESC,id DESC LIMIT $4", r.site, stamp, cut, limit+1)
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		list := []map[string]any{}
		last := ""
		next := ""
		for rows.Next() {
			var release, rev string
			var at int64
			if e = rows.Scan(&release, &rev, &at); e != nil {
				return nil, e
			}
			if len(list) == limit {
				next = last
				break
			}
			list = append(list, map[string]any{"release_id": release, "revision": rev, "created_at": time.UnixMicro(at).UTC().Format(time.RFC3339Nano)})
			last = release
		}
		out["releases"] = list
		out["next_cursor"] = next
		return out, rows.Err()
	}
	v := q.Revision
	if v == "" {
		v = r.draft
	} else {
		var ok int
		if e = tx.QueryRowContext(ctx, "SELECT 1 FROM sc_revisions WHERE id=$1 AND site=$2", v, r.site).Scan(&ok); errors.Is(e, sql.ErrNoRows) {
			return nil, absent()
		}
		if e != nil {
			return nil, e
		}
	}
	out["selected_revision"] = v
	if q.Path != "" {
		if !validPath(q.Path) {
			return nil, bad("Invalid file path.")
		}
		var item asset
		var b []byte
		e = tx.QueryRowContext(ctx, "SELECT f.path,f.digest,f.size,f.content_type,b.body FROM sc_files f JOIN sc_blobs b ON b.digest=f.digest WHERE f.revision=$1 AND f.path=$2", v, q.Path).Scan(&item.Path, &item.Digest, &item.Size, &item.ContentType, &b)
		if errors.Is(e, sql.ErrNoRows) {
			return nil, absent()
		}
		if e != nil {
			return nil, e
		}
		file := map[string]any{"path": item.Path, "digest": item.Digest, "size": item.Size, "content_type": item.ContentType}
		if strings.HasPrefix(item.ContentType, "text/") {
			file["text"] = string(b)
		} else {
			file["base64"] = base64.StdEncoding.EncodeToString(b)
		}
		out["file"] = file
		return out, nil
	}
	rows, e := tx.QueryContext(ctx, "SELECT path,digest,size,content_type FROM sc_files WHERE revision=$1 AND path>$2 ORDER BY path LIMIT $3", v, q.Cursor, limit+1)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	list := []asset{}
	last := ""
	next := ""
	for rows.Next() {
		var item asset
		if e = rows.Scan(&item.Path, &item.Digest, &item.Size, &item.ContentType); e != nil {
			return nil, e
		}
		if len(list) == limit {
			next = last
			break
		}
		list = append(list, item)
		last = item.Path
	}
	out["files"] = list
	out["next_cursor"] = next
	return out, rows.Err()
}
