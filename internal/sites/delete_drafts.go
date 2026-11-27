package sites

import (
	"context"
	"database/sql"
)

type DraftSelection struct {
	Site, Revision string
}

// A release protects the site's entire history, including when delivery is offline.
func selectedDrafts(ctx context.Context, tx *sql.Tx, a Actor, selected []DraftSelection) ([]record, error) {
	if len(selected) == 0 || len(selected) > MaxSitesPerWorkspace {
		return nil, bad("Select between 1 and 100 draft sites.")
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
		if r.draft != x.Revision || r.live != "" || r.release != "" {
			return nil, conflict("A selected site changed or has a release. Reload the directory and review the selection.")
		}
		records = append(records, r)
	}
	return records, nil
}

func (s *Store) ReviewDraftDeletion(ctx context.Context, a Actor, selected []DraftSelection) ([]string, error) {
	tx, err := s.adminRead(ctx, a)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	records, err := selectedDrafts(ctx, tx, a, selected)
	if err != nil {
		return nil, err
	}
	labels := make([]string, 0, len(records))
	for _, r := range records {
		labels = append(labels, r.name+" ("+r.site+")")
	}
	return labels, nil
}

// DeleteDraftSites frees the historical manifests and their quota charge in one
// transaction. Address reservations, audit events and replay receipts survive.
func (s *Store) DeleteDraftSites(ctx context.Context, a Actor, selected []DraftSelection) error {
	tx, err := s.adminWrite(ctx, a)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	records, err := selectedDrafts(ctx, tx, a, selected)
	if err != nil {
		return err
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
