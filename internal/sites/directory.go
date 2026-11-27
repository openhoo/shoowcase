package sites

import (
	"context"
	"time"

	"github.com/openhoo/shoowcase/internal/sites/directorydb"
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
	rows, e := directorydb.New(tx).Directory(ctx, directorydb.DirectoryParams{
		Workspace: a.Workspace, MaxSites: MaxSitesPerWorkspace,
	})
	if e != nil {
		return nil, e
	}
	var out []DirectorySite
	for _, row := range rows {
		out = append(out, DirectorySite{
			ID: row.ID, Name: row.Name, Draft: row.Draft, Live: row.Live,
			Visibility: visibleAudience(ctx, row.Visibility), FileCount: int(row.FileCount), Updated: time.UnixMilli(row.UpdatedMs).UTC(),
		})
	}
	return out, nil
}
