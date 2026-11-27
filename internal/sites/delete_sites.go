package sites

import (
	"context"
	"database/sql"
)

type SiteSelection struct {
	Site, Revision string
}

// The generation detects even a change of access or rollback that later
// restores exactly the same live release.
type SiteDeletionState struct {
	SiteSelection
	Live, Release, Visibility string
	Generation                int64
}

type ReviewedSite struct {
	SiteDeletionState
	Name string
}

func deletionState(r record) SiteDeletionState {
	return SiteDeletionState{SiteSelection{r.site, r.draft}, r.live, r.release, r.visibility, r.generation}
}

func selectedSites(ctx context.Context, tx *sql.Tx, a Actor, selected []SiteSelection) ([]record, error) {
	if len(selected) == 0 || len(selected) > MaxSitesPerWorkspace {
		return nil, bad("Select between 1 and 100 sites.")
	}
	records := make([]record, 0, len(selected))
	seen := make(map[string]bool, len(selected))
	for _, x := range selected {
		if !validSlug(x.Site) || !idRE.MatchString(x.Revision) || seen[x.Site] {
			return nil, bad("Select distinct sites from the directory.")
		}
		seen[x.Site] = true
		r, err := readSite(ctx, tx, a, x.Site)
		if err != nil {
			return nil, err
		}
		if r.draft != x.Revision {
			return nil, conflict("A selected draft changed. Reload the directory and review the selection.")
		}
		records = append(records, r)
	}
	return records, nil
}

func (s *Store) ReviewSiteDeletion(ctx context.Context, a Actor, selected []SiteSelection) ([]ReviewedSite, error) {
	tx, err := s.adminRead(ctx, a)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	records, err := selectedSites(ctx, tx, a, selected)
	if err != nil {
		return nil, err
	}
	review := make([]ReviewedSite, 0, len(records))
	for _, r := range records {
		review = append(review, ReviewedSite{deletionState(r), r.name})
	}
	return review, nil
}

// DeleteSites removes releases, historical manifests and their quota charge in
// one transaction. Address reservations, audit events and replay receipts survive.
func (s *Store) DeleteSites(ctx context.Context, a Actor, reviewed []SiteDeletionState) error {
	tx, err := s.adminWrite(ctx, a)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	selected := make([]SiteSelection, len(reviewed))
	for i, x := range reviewed {
		selected[i] = x.SiteSelection
	}
	records, err := selectedSites(ctx, tx, a, selected)
	if err != nil {
		return err
	}
	for i, r := range records {
		if deletionState(r) != reviewed[i] {
			return conflict("Publication or access changed. Reload the directory and review the selection.")
		}
	}
	if _, err = tx.ExecContext(ctx, "CREATE TEMPORARY TABLE sc_deleting_digests(digest TEXT PRIMARY KEY)"); err != nil {
		return err
	}
	for _, r := range records {
		if _, err = tx.ExecContext(ctx, `INSERT INTO sc_deleting_digests(digest)
			SELECT DISTINCT f.digest FROM sc_files f JOIN sc_revisions v ON v.id=f.revision
			WHERE v.site=$1 ON CONFLICT(digest) DO NOTHING`, r.site); err != nil {
			return err
		}
	}
	// Other tenants may upload the same bytes while this site is deleted.
	// Lock affected blobs in the same order as upload, then recheck references
	// in a fresh statement after acquiring the locks. NO KEY UPDATE serializes
	// competing uploads/deletions but allows foreign-key checks for unchanged
	// files. Any such file already has a reference in the uploader's previous
	// revision, so our final DELETE cannot reclaim it. SQLite serializes writers.
	if s.driver == "pgx" {
		var n int64
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (
			SELECT b.digest FROM sc_blobs b JOIN sc_deleting_digests d ON d.digest=b.digest
			ORDER BY b.digest COLLATE "C" FOR NO KEY UPDATE OF b) locked`).Scan(&n); err != nil {
			return err
		}
	}
	request := id()
	for _, r := range records {
		// Also cover any site created by an older process after the backfill.
		if _, err = tx.ExecContext(ctx, "INSERT INTO sc_reserved_sites(id) VALUES($1) ON CONFLICT(id) DO NOTHING", r.site); err != nil {
			return err
		}
		if err = audit(ctx, tx, a, r, "delete_site", request); err != nil {
			return err
		}
		for _, statement := range []string{
			"UPDATE sc_workspace SET used_bytes=used_bytes-(SELECT retained_bytes FROM sc_sites WHERE id=$1) WHERE id=(SELECT workspace FROM sc_sites WHERE id=$1)",
			"DELETE FROM sc_files WHERE revision IN (SELECT id FROM sc_revisions WHERE site=$1)",
			"DELETE FROM sc_releases WHERE site=$1",
			"DELETE FROM sc_revisions WHERE site=$1",
			"DELETE FROM sc_site_groups WHERE site=$1",
			"DELETE FROM sc_sites WHERE id=$1",
		} {
			if _, err = tx.ExecContext(ctx, statement, r.site); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM sc_blobs WHERE digest IN (SELECT digest FROM sc_deleting_digests)
		AND NOT EXISTS(SELECT 1 FROM sc_files f WHERE f.digest=sc_blobs.digest)`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DROP TABLE sc_deleting_digests"); err != nil {
		return err
	}
	return tx.Commit()
}
