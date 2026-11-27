package sites

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"time"
)

// GetSiteQuery selects a manifest page, a named file, or a release-history page.
// A nil Limit uses the default; an explicitly supplied zero is invalid.
type GetSiteQuery struct {
	Site     string `json:"site_id"`
	Path     string `json:"path,omitempty"`
	Revision string `json:"revision,omitempty"`
	Cursor   string `json:"cursor,omitempty"`
	Limit    *int   `json:"limit,omitempty"`
	View     string `json:"view,omitempty"`
}

// The raw tool adapter rejects nulls and noncanonical names. encoding/json's
// struct decoder alone would accept both. Derive names from the actual input.
var getSiteFields = func() map[string]bool {
	out := make(map[string]bool)
	for _, f := range reflect.VisibleFields(reflect.TypeFor[GetSiteQuery]()) {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		out[name] = true
	}
	return out
}()

type FileMetadata struct {
	Path        string `json:"path"`
	Digest      string `json:"digest"`
	Size        int    `json:"size"`
	ContentType string `json:"content_type"`
}
type SiteFile struct {
	FileMetadata
	Text, Base64 *string
}
type SiteRelease struct {
	ID        string `json:"release_id"`
	Revision  string `json:"revision"`
	CreatedAt string `json:"created_at"`
}

// SiteDetails contains exactly one selected view: Files, File, or Releases.
// An empty paged view is a non-nil slice.
type SiteDetails struct {
	SiteID, Name, GroupID, Visibility, ConfiguredVisibility string
	Revision, LiveRevision, ReleaseID, SelectedRevision     string
	StorageRestriction, NextCursor                          string
	Files                                                   []FileMetadata
	File                                                    *SiteFile
	Releases                                                []SiteRelease
}

func (s *SiteDetails) HasFile(path string) bool {
	for _, f := range s.Files {
		if f.Path == path {
			return true
		}
	}
	return false
}

// Only the raw tool adapter needs the MCP wire shape. Page keys remain present
// for empty pages, while a single file omits both the page and its cursor.
func (s *SiteDetails) toolResult() map[string]any {
	r := map[string]any{
		"site_id": s.SiteID, "slug": s.SiteID, "group_id": s.GroupID, "name": s.Name,
		"visibility": s.Visibility, "configured_visibility": s.ConfiguredVisibility,
		"revision": s.Revision, "live_revision": s.LiveRevision, "release_id": s.ReleaseID,
	}
	if s.SelectedRevision != "" {
		r["selected_revision"] = s.SelectedRevision
	}
	if s.StorageRestriction != "" {
		r["storage_restriction"] = s.StorageRestriction
	}
	switch {
	case s.File != nil:
		f := s.File
		value := map[string]any{"path": f.Path, "digest": f.Digest, "size": f.Size, "content_type": f.ContentType}
		if f.Text != nil {
			value["text"] = *f.Text
		} else {
			value["base64"] = *f.Base64
		}
		r["file"] = value
	case s.Releases != nil:
		r["releases"], r["next_cursor"] = s.Releases, s.NextCursor
	default:
		r["files"], r["next_cursor"] = s.Files, s.NextCursor
	}
	return r
}

func (s *Store) beginSiteRead(ctx context.Context, a Actor, cursor string, pageSize *int) (*sql.Tx, Actor, int, error) {
	limit := 25
	if pageSize != nil {
		limit = *pageSize
	}
	if limit < 1 || limit > 100 || len(cursor) > 256 {
		return nil, a, 0, bad("limit must be 1–100 and cursor cannot exceed 256 characters.")
	}
	tx, err := s.readSnapshot(ctx)
	if err != nil {
		return nil, a, 0, err
	}
	a, err = accountPolicy(ctx, tx, a, false)
	if err == nil {
		err = actorErr(a, 1)
	}
	if err != nil {
		tx.Rollback()
		return nil, a, 0, err
	}
	return tx, a, limit, nil
}

func (s *Store) GetSite(ctx context.Context, a Actor, q GetSiteQuery) (*SiteDetails, error) {
	if err := actorErr(a, 1); err != nil {
		return nil, err
	}
	tx, a, limit, e := s.beginSiteRead(ctx, a, q.Cursor, q.Limit)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if !validSlug(q.Site) {
		return nil, bad("Invalid site_id.")
	}
	r, e := readSite(ctx, tx, a, q.Site)
	if e != nil {
		return nil, e
	}
	out := &SiteDetails{
		SiteID: r.site, Name: r.name, GroupID: r.group,
		Visibility: visibleAudience(ctx, r.visibility), ConfiguredVisibility: r.visibility,
		Revision: r.draft, LiveRevision: r.live, ReleaseID: r.release,
	}
	// Expose the same group policy before an editor opens the form or an
	// agent prepares an upload. The actual write always checks it again.
	if rank(a) >= 2 && q.View != "releases" {
		if err := canChargeSiteGroup(ctx, tx, a, r.group); err != nil {
			var denial *Error
			if !errors.As(err, &denial) || denial.Code != "forbidden" {
				return nil, err
			}
			out.StorageRestriction = denial.Message
		}
	}
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
		list := []SiteRelease{}
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
			list = append(list, SiteRelease{ID: release, Revision: rev, CreatedAt: time.UnixMicro(at).UTC().Format(time.RFC3339Nano)})
			last = release
		}
		out.Releases = list
		out.NextCursor = next
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
	out.SelectedRevision = v
	if q.Path != "" {
		if !validPath(q.Path) {
			return nil, bad("Invalid file path.")
		}
		var item FileMetadata
		var b []byte
		e = tx.QueryRowContext(ctx, "SELECT f.path,f.digest,f.size,f.content_type,b.body FROM sc_files f JOIN sc_blobs b ON b.digest=f.digest WHERE f.revision=$1 AND f.path=$2", v, q.Path).Scan(&item.Path, &item.Digest, &item.Size, &item.ContentType, &b)
		if errors.Is(e, sql.ErrNoRows) {
			return nil, absent()
		}
		if e != nil {
			return nil, e
		}
		file := &SiteFile{FileMetadata: item}
		if strings.HasPrefix(item.ContentType, "text/") {
			file.Text = new(string(b))
		} else {
			file.Base64 = new(base64.StdEncoding.EncodeToString(b))
		}
		out.File = file
		return out, nil
	}
	rows, e := tx.QueryContext(ctx, "SELECT path,digest,size,content_type FROM sc_files WHERE revision=$1 AND path>$2 ORDER BY path LIMIT $3", v, q.Cursor, limit+1)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	list := []FileMetadata{}
	last := ""
	next := ""
	for rows.Next() {
		var item FileMetadata
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
	out.Files = list
	out.NextCursor = next
	return out, rows.Err()
}
