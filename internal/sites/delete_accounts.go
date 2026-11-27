package sites

import (
	"context"
	"database/sql"
)

// Every successful create_site mutation (including one made with a personal
// key) recorded its actor and workspace transactionally, even in old databases.
// Later edits, publications and budget-group assignment do not transfer a site.
func accountSites(ctx context.Context, tx *sql.Tx, workspace, subject string) ([]ReviewedSite, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,name,draft,live,release_id,visibility,publication_generation
		FROM sc_sites WHERE workspace=$1 AND id IN (
			SELECT site FROM sc_audit WHERE workspace=$1 AND subject=$2 AND action='create_site')
		ORDER BY id`, workspace, subject)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReviewedSite
	for rows.Next() {
		var r ReviewedSite
		if err := rows.Scan(&r.Site, &r.Name, &r.Revision, &r.Live, &r.Release, &r.Visibility, &r.Generation); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Scope generic target/value fields by event type. An OIDC subject can have
// exactly the same text as another person's site slug, role, group or key ID.
const accountActions = "('account_created','member_provisioned','local_account_created','identity_role_updated','account_suspended','account_reactivated','account_role_changed','local_password_changed')"
const siteActions = "('create_site','upload_files','publish_site','rollback_site','set_site_access','delete_site','site_group_changed')"

func (s *Store) eraseAccounts(ctx context.Context, tx *sql.Tx, admin Actor, targets []reviewedTarget) error {
	for _, ddl := range []string{
		"CREATE TEMPORARY TABLE sc_erasing_accounts(subject TEXT PRIMARY KEY)",
		"CREATE TEMPORARY TABLE sc_erasing_sites(site TEXT PRIMARY KEY)",
		"CREATE TEMPORARY TABLE sc_erasing_keys(id TEXT PRIMARY KEY)",
	} {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	var records []record
	for _, target := range targets {
		if err := rememberAccountDeletion(ctx, tx, admin.Workspace, target.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO sc_erasing_accounts(subject) VALUES($1)", target.ID); err != nil {
			return err
		}
		for _, r := range target.Sites {
			records = append(records, record{site: r.Site})
		}
		// Write first so pruning the newest events can never reuse audit sequence
		// numbers or move an existing collector's checkpoint backwards.
		if err := recordAccountEvent(ctx, tx, admin, "account_deleted", ""); err != nil {
			return err
		}
	}
	if err := s.deleteSiteContent(ctx, tx, records); err != nil {
		return err
	}
	// Other collaborators own their request IDs. Preserve duplicate protection
	// but erase the response (including the site name and all revision IDs).
	siteID := "json_extract(result,'$.site_id')"
	if s.driver == "pgx" {
		siteID = "(CAST(result AS json)->>'site_id')"
	}
	const accounts = "(SELECT subject FROM sc_erasing_accounts)"
	const sites = "(SELECT site FROM sc_erasing_sites)"
	const keys = "(SELECT id FROM sc_erasing_keys)"
	for _, query := range []string{
		// Include sites previously removed from the directory and keys already
		// pruned from the 25 most recent inactive keys per owner.
		"INSERT INTO sc_erasing_sites(site) SELECT DISTINCT site FROM sc_audit WHERE workspace=$1 AND action='create_site' AND subject IN " + accounts,
		"INSERT INTO sc_erasing_keys(id) SELECT id FROM sc_agent_keys WHERE workspace=$1 AND subject IN " + accounts +
			" UNION SELECT target FROM sc_workspace_audit WHERE workspace=$1 AND action='key_created' AND subject IN " + accounts,

		"UPDATE sc_requests SET result='{}' WHERE workspace=$1 AND " + siteID + " IN " + sites,
		"DELETE FROM sc_requests WHERE workspace=$1 AND subject IN " + accounts,
		"DELETE FROM sc_audit WHERE workspace=$1 AND (site IN " + sites + " OR subject IN " + accounts + ")",
		"DELETE FROM sc_workspace_audit WHERE workspace=$1 AND (subject IN " + accounts +
			" OR (action IN " + accountActions + " AND target IN " + accounts + ")" +
			" OR (action IN " + siteActions + " AND target IN " + sites + ")" +
			" OR (action IN ('group_member_added','group_member_removed') AND (previous_value IN " + accounts + " OR new_value IN " + accounts + "))" +
			" OR (action IN ('key_created','key_revoked') AND target IN " + keys + ") OR credential_id IN " + keys + ")",
		"DELETE FROM sc_account_events WHERE workspace=$1 AND (subject IN " + accounts +
			" OR (action IN " + accountActions + " AND target IN " + accounts + ")" +
			" OR (action='site_group_changed' AND target IN " + sites + ")" +
			" OR (action IN ('key_created','key_revoked') AND target IN " + keys + "))",
		"UPDATE sc_groups SET version=version+1 WHERE workspace=$1 AND id IN (SELECT group_id FROM sc_group_members WHERE workspace=$1 AND subject IN " + accounts + ")",
		"DELETE FROM sc_group_members WHERE workspace=$1 AND subject IN " + accounts,
		"DELETE FROM sc_local_credentials WHERE workspace=$1 AND subject IN " + accounts,
		"DELETE FROM sc_agent_keys WHERE workspace=$1 AND subject IN " + accounts,
		"DELETE FROM sc_members WHERE workspace=$1 AND subject IN " + accounts,
	} {
		if _, err := tx.ExecContext(ctx, query, admin.Workspace); err != nil {
			return err
		}
	}
	// Quickstart receipts are global, but key IDs are globally unique. Other
	// replay markers are random, expiring nonces with no account binding.
	if _, err := tx.ExecContext(ctx, "DELETE FROM sc_replay WHERE key IN (SELECT 'quickstart:' || id FROM sc_erasing_keys)"); err != nil {
		return err
	}
	for _, ddl := range []string{"DROP TABLE sc_erasing_keys", "DROP TABLE sc_erasing_sites", "DROP TABLE sc_erasing_accounts"} {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	return nil
}
