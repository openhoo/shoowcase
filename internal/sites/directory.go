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

// Directory lists the member's own sites and those assigned to their groups.
func (s *Store) Directory(ctx context.Context, a Actor) ([]DirectorySite, error) {
	return s.directory(ctx, a, false)
}

// AdminDirectory lists all sites in the administrator's current workspace.
func (s *Store) AdminDirectory(ctx context.Context, a Actor) ([]DirectorySite, error) {
	return s.directory(ctx, a, true)
}

func (s *Store) directory(ctx context.Context, a Actor, allSites bool) ([]DirectorySite, error) {
	tx, e := s.readSnapshot(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	level := 1
	if allSites {
		level = 4
	}
	if a, e = authorizeMember(ctx, tx, a, level); e != nil {
		return nil, e
	}
	// The activity index locates both the creation event and the latest change.
	// The workspace quota bounds the result to 100 sites; no client pagination
	// or per-site database round trips are needed to compute directory totals.
	rows, e := directorydb.New(tx).Directory(ctx, directorydb.DirectoryParams{
		Workspace: a.Workspace, Subject: a.Subject, AllSites: allSites, MaxSites: MaxSitesPerWorkspace,
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

func (s *Store) listSites(ctx context.Context, a Actor, q siteListQuery) (map[string]any, error) {
	tx, a, limit, err := s.beginSiteRead(ctx, a, q.Cursor, q.Limit)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := directorydb.New(tx).Directory(ctx, directorydb.DirectoryParams{
		Workspace: a.Workspace, Subject: a.Subject, AfterID: q.Cursor, MaxSites: int32(limit + 1),
	})
	if err != nil {
		return nil, err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = rows[limit-1].ID
	}
	list := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		r := record{site: row.ID, name: row.Name, visibility: row.Visibility, draft: row.Draft, live: row.Live, release: row.ReleaseID, group: row.GroupID}
		list = append(list, effectiveAudience(ctx, r.out()))
	}
	return map[string]any{"sites": list, "next_cursor": next}, nil
}
