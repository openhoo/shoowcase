package sites

import (
	"bytes"
	"context"
	"database/sql"
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
	GroupID         string      `json:"group_id,omitempty"` // Omit to preserve canonical mutation hashes from older releases.
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
type siteListQuery struct {
	Cursor string `json:"cursor"`
	Limit  *int   `json:"limit"`
}

var fields = map[string]map[string]bool{
	"get_site": getSiteFields, "list_site_groups": {}, "create_site": wordset("name slug request_id group_id"), "list_sites": wordset("cursor limit"),
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
func checkedFields(raw json.RawMessage, allowed map[string]bool) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if e := decodeStrict(raw, &m); e != nil {
		return nil, e
	}
	if m == nil {
		return nil, bad("Tool arguments must be an object.")
	}
	for k, v := range m {
		if !allowed[k] || string(v) == "null" {
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
	case "get_site", "list_sites":
		level = 1
	case "create_site", "upload_files", "list_site_groups":
		level = 2
	}
	if e := actorErr(a, level); e != nil {
		return nil, e
	}
	m, e := checkedFields(raw, fields[tool])
	if e != nil {
		return nil, e
	}
	if tool == "list_site_groups" {
		opts, err := s.CreationGroups(ctx, a)
		if err != nil {
			return nil, err
		}
		return map[string]any{"groups": opts.Groups, "allow_unassigned": opts.AllowUnassigned}, nil
	}
	if tool == "get_site" {
		var q GetSiteQuery
		if e = decodeStrict(raw, &q); e != nil {
			return nil, e
		}
		detail, e := s.GetSite(ctx, a, q)
		if e != nil {
			return nil, e
		}
		return detail.toolResult(), nil
	}
	if tool == "list_sites" {
		var q siteListQuery
		if e = strict(raw, &q); e != nil {
			return nil, e
		}
		return s.listSites(ctx, a, q)
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
		if _, has := m["group_id"]; has && !idRE.MatchString(p.GroupID) {
			return nil, bad("Provide a valid group_id or omit it.")
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
				// Only echo paths that satisfy the bounded, printable site path rules.
				var field *Error
				if validPath(f.Path) && errors.As(e, &field) && field.Code == "invalid_argument" {
					return nil, bad(f.Path + ": " + field.Message)
				}
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
	if e = lockWorkspace(ctx, tx, s.driver, a.Workspace); e != nil {
		return nil, e
	}
	// Serialize key revocation and account suspension with writes, including
	// idempotent retries and previously signed upload/approval tickets.
	if a, e = accountPolicy(ctx, tx, a, false); e != nil {
		return nil, e
	}
	if e = actorErr(a, level); e != nil {
		return nil, e
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
		// Receipts survive deletion to reserve the request ID. Do not return
		// a successful upload, approval or creation for a removed site.
		siteID, _ := res["site_id"].(string)
		if _, e = readSite(ctx, tx, a, siteID); e != nil {
			return nil, e
		}
		return effectiveAudience(ctx, res), nil
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
	return effectiveAudience(ctx, res), nil
}
func create(ctx context.Context, tx *sql.Tx, a Actor, p mutation) (record, error) {
	group, err := creationGroup(ctx, tx, a, p.GroupID)
	if err != nil {
		return record{}, err
	}
	if err = checkCharge(ctx, tx, a.Workspace, group, QuotaUsage{Sites: 1}); err != nil {
		return record{}, err
	}
	slug := p.Slug
	if slug == "" {
		slug = defaultSlug(p.Name)
	}
	r := record{group: group, site: slug, name: p.Name, visibility: "workspace", draft: id()}
	res, e := tx.ExecContext(ctx, "INSERT INTO sc_reserved_sites(id) VALUES($1) ON CONFLICT(id) DO NOTHING", slug)
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
	if _, e = tx.ExecContext(ctx, "INSERT INTO sc_sites(id,workspace,name,visibility,draft,live,release_id) VALUES($1,$2,$3,'workspace',$4,'','')", slug, a.Workspace, p.Name, r.draft); e != nil {
		return r, e
	}
	if group != "" {
		if _, e = tx.ExecContext(ctx, "INSERT INTO sc_site_groups(workspace,site,group_id) VALUES($1,$2,$3)", a.Workspace, r.site, group); e != nil {
			return r, e
		}
	}
	_, e = tx.ExecContext(ctx, "INSERT INTO sc_revisions(id,site,created_at) VALUES($1,$2,$3)", r.draft, r.site, time.Now().UnixMilli())
	return r, e
}

// ErrDraftChanged means a mutation targeted a revision that is no longer current.
var ErrDraftChanged = conflict("Draft changed; read the current revision and retry.")

func upload(ctx context.Context, tx *sql.Tx, a Actor, r *record, p mutation, adds []asset) error {
	if r.draft != p.Expected {
		return ErrDraftChanged
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
	// Membership protects the assigned draft even if the proposed manifest
	// is empty. Unassigned legacy sites require assignment only for growth.
	if r.group != "" || size > 0 {
		if e = canChargeSiteGroup(ctx, tx, a, r.group); e != nil {
			return e
		}
	}
	if size > 0 {
		if e = checkCharge(ctx, tx, a.Workspace, r.group, QuotaUsage{Bytes: int64(size)}); e != nil {
			return e
		}
	}
	if _, e = tx.ExecContext(ctx, "UPDATE sc_workspace SET used_bytes=used_bytes+$1 WHERE id=$2", size, a.Workspace); e != nil {
		return e
	}
	next := id()
	if _, e = tx.ExecContext(ctx, "INSERT INTO sc_revisions(id,site,created_at) VALUES($1,$2,$3)", next, r.site, time.Now().UnixMilli()); e != nil {
		return e
	}
	// Take locks on all submitted digests in digest order, including those
	// that already exist. A conditional upsert that targets the primary key
	// locks the conflicting PostgreSQL row without rewriting the blob body.
	// Deletion uses NO KEY UPDATE in the same order, compatible with the
	// manifest's later foreign-key checks on unchanged digests.
	sort.Slice(adds, func(i, j int) bool { return adds[i].Digest < adds[j].Digest })
	for _, v := range adds {
		if _, e = tx.ExecContext(ctx, "INSERT INTO sc_blobs(digest,body) VALUES($1,$2) ON CONFLICT(digest) DO UPDATE SET digest=excluded.digest WHERE FALSE", v.Digest, v.data); e != nil {
			return e
		}
	}
	for _, v := range manifest {
		if _, e = tx.ExecContext(ctx, "INSERT INTO sc_files(revision,path,digest,size,content_type) VALUES($1,$2,$3,$4,$5)", next, v.Path, v.Digest, v.Size, v.ContentType); e != nil {
			return e
		}
	}
	if _, e = tx.ExecContext(ctx, "UPDATE sc_sites SET draft=$1,retained_bytes=retained_bytes+$2 WHERE workspace=$3 AND id=$4", next, size, a.Workspace, r.site); e == nil {
		r.draft = next
	}
	return e
}
func releaseChange(ctx context.Context, tx *sql.Tx, r *record, p mutation, tool string) error {
	generation, hasApproval := ctx.Value(approvalKey{}).(int64)
	// Check before branching on current visibility: an action which originally
	// needed approval might no longer do so after another publisher changes it.
	if hasApproval && (generation < 1 || generation != r.generation) {
		return conflict("Publication or access changed since review. Read the current site and request a new approval.")
	}
	requireApproval := func() error {
		if privateOnly(ctx) {
			return fail("forbidden", "Public publishing is disabled on this instance.")
		}
		if !hasApproval {
			return &Error{Code: "approval_required", Message: "Confirm the exact change to the public site.", approvalGeneration: r.generation}
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
		// Even reasserting workspace/offline access is an explicit decision.
		// Increment the generation below so pending public approvals expire.
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
	_, e := tx.ExecContext(ctx, "UPDATE sc_sites SET live=$1,release_id=$2,visibility=$3,publication_generation=publication_generation+1 WHERE id=$4", r.live, r.release, r.visibility, r.site)
	return e
}
func (s *Store) listSites(ctx context.Context, a Actor, q siteListQuery) (map[string]any, error) {
	tx, a, limit, e := s.beginSiteRead(ctx, a, q.Cursor, q.Limit)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT s.id,s.name,s.visibility,s.draft,s.live,s.release_id,COALESCE(sg.group_id,'') FROM sc_sites s
			LEFT JOIN sc_site_groups sg ON sg.workspace=s.workspace AND sg.site=s.id WHERE s.workspace=$1 AND s.id>$2 ORDER BY s.id LIMIT $3`, a.Workspace, q.Cursor, limit+1)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	list := []map[string]any{}
	last := ""
	next := ""
	for rows.Next() {
		var r record
		if e = rows.Scan(&r.site, &r.name, &r.visibility, &r.draft, &r.live, &r.release, &r.group); e != nil {
			return nil, e
		}
		if len(list) == limit {
			next = last
			break
		}
		list = append(list, effectiveAudience(ctx, r.out()))
		last = r.site
	}
	return map[string]any{"sites": list, "next_cursor": next}, rows.Err()
}
