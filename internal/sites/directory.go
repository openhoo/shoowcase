package sites

import (
	"context"
	"time"
)

// DirectorySite is a metadata-only summary. It never loads a file body,
// release history or a row belonging to another workspace.
type DirectorySite struct {
	ID, Name, Visibility, Draft, Live string
	FileCount                         int
	Updated                           time.Time
}

func (s *Store) Directory(ctx context.Context, a Actor) ([]DirectorySite, error) {
	tx, e := s.readSnapshot(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if _, e = authorizeMember(ctx, tx, a, 1); e != nil {
		return nil, e
	}
	// The primary key on sc_files and the activity index cover both probes.
	// The workspace quota bounds the result to 100 sites; no client pagination
	// or per-site database round trips are needed to compute directory totals.
	rows, e := tx.QueryContext(ctx, `SELECT s.id,s.name,s.visibility,s.draft,s.live,
		(SELECT count(*) FROM sc_files f WHERE f.revision=s.draft),
		coalesce((SELECT max(a.created_at) FROM sc_audit a WHERE a.workspace=s.workspace AND a.site=s.id),
			r.created_at,0)
		FROM sc_sites s LEFT JOIN sc_revisions r ON r.id=s.draft AND r.site=s.id
		WHERE s.workspace=$1 ORDER BY s.id LIMIT $2`, a.Workspace, MaxSitesPerWorkspace)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []DirectorySite
	for rows.Next() {
		var x DirectorySite
		var ms int64
		if e = rows.Scan(&x.ID, &x.Name, &x.Visibility, &x.Draft, &x.Live, &x.FileCount, &ms); e != nil {
			return nil, e
		}
		if privateOnly(ctx) && x.Visibility == "public" {
			x.Visibility = "workspace"
		}
		x.Updated = time.UnixMilli(ms).UTC()
		out = append(out, x)
	}
	return out, rows.Err()
}
