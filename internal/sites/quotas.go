package sites

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Storage is the sum of file sizes in every retained revision, including
// unchanged files copied into a new manifest. The shared blob store is not a
// useful measure of an individual tenant's consumption.
var quotaSchema = []string{
	"ALTER TABLE sc_workspace ADD COLUMN site_limit BIGINT NOT NULL DEFAULT 100 CHECK(site_limit BETWEEN 0 AND 100)",
	"ALTER TABLE sc_workspace ADD COLUMN byte_limit BIGINT NOT NULL DEFAULT 2147483648 CHECK(byte_limit BETWEEN 0 AND 2147483648)",
	"ALTER TABLE sc_workspace ADD COLUMN quota_version BIGINT NOT NULL DEFAULT 1",
	"ALTER TABLE sc_groups ADD COLUMN site_limit BIGINT NOT NULL DEFAULT -1 CHECK(site_limit BETWEEN -1 AND 100)",
	"ALTER TABLE sc_groups ADD COLUMN byte_limit BIGINT NOT NULL DEFAULT -1 CHECK(byte_limit BETWEEN -1 AND 2147483648)",
	"ALTER TABLE sc_sites ADD COLUMN retained_bytes BIGINT NOT NULL DEFAULT 0 CHECK(retained_bytes>=0)",
	"ALTER TABLE sc_sites ADD COLUMN assignment_version BIGINT NOT NULL DEFAULT 1",
	`UPDATE sc_sites SET retained_bytes=(SELECT COALESCE(SUM(f.size),0) FROM sc_revisions r JOIN sc_files f ON f.revision=r.id WHERE r.site=sc_sites.id)`,
	"CREATE UNIQUE INDEX sc_sites_workspace_id ON sc_sites(workspace,id)",
	siteGroupsDDL,
	"CREATE INDEX sc_site_groups_group ON sc_site_groups(workspace,group_id,site)",
}

// For a group, -1 means it has no extra limit on that resource. The workspace
// ceiling applies to all sites together, including unassigned sites.
type QuotaLimits struct{ Sites, Bytes int64 }
type QuotaUsage struct{ Sites, Bytes int64 }
type QuotaBudget struct {
	ID, Name string
	Limits   QuotaLimits
	Usage    QuotaUsage
	Version  int64
}
type QuotaSite struct {
	ID, Name, GroupID string
	Bytes, Version    int64
}
type QuotaOverview struct {
	Workspace QuotaBudget
	Groups    []QuotaBudget
	Sites     []QuotaSite
}

// The grouping requirement must apply equally to new and existing unassigned
// sites as soon as the first explicit group limit is enabled.
const configuredGroupBudgetSQL = "EXISTS(SELECT 1 FROM sc_groups WHERE workspace=$1 AND (site_limit>=0 OR byte_limit>=0))"
const workspaceBudgetSQL = `SELECT w.id,w.display_name,w.site_limit,w.byte_limit,
	(SELECT COUNT(*) FROM sc_sites s WHERE s.workspace=w.id),w.used_bytes,w.quota_version
	FROM sc_workspace w WHERE w.id=$1`
const groupBudgetSQL = `SELECT g.id,g.name,g.site_limit,g.byte_limit,
	(SELECT COUNT(*) FROM sc_site_groups sg WHERE sg.workspace=g.workspace AND sg.group_id=g.id),
	(SELECT COALESCE(SUM(s.retained_bytes),0) FROM sc_site_groups sg JOIN sc_sites s ON s.workspace=sg.workspace AND s.id=sg.site
	 WHERE sg.workspace=g.workspace AND sg.group_id=g.id),g.version
	FROM sc_groups g WHERE g.workspace=$1`
const siteBudgetSQL = `SELECT s.id,s.name,COALESCE(sg.group_id,''),s.retained_bytes,s.assignment_version
	FROM sc_sites s LEFT JOIN sc_site_groups sg ON sg.workspace=s.workspace AND sg.site=s.id WHERE s.workspace=$1`

func scanBudget(row scanner) (QuotaBudget, error) {
	var b QuotaBudget
	err := row.Scan(&b.ID, &b.Name, &b.Limits.Sites, &b.Limits.Bytes, &b.Usage.Sites, &b.Usage.Bytes, &b.Version)
	return b, err
}
func scanQuotaSite(row scanner) (QuotaSite, error) {
	var s QuotaSite
	err := row.Scan(&s.ID, &s.Name, &s.GroupID, &s.Bytes, &s.Version)
	return s, err
}

func budget(ctx context.Context, tx *sql.Tx, workspace, group string) (QuotaBudget, error) {
	if group == "" {
		return scanBudget(tx.QueryRowContext(ctx, workspaceBudgetSQL, workspace))
	}
	if !idRE.MatchString(group) {
		return QuotaBudget{}, bad("Invalid group ID.")
	}
	b, err := scanBudget(tx.QueryRowContext(ctx, groupBudgetSQL+" AND g.id=$2", workspace, group))
	if errors.Is(err, sql.ErrNoRows) {
		return QuotaBudget{}, fail("not_found", "Group not found in this workspace.")
	}
	return b, err
}

func (s *Store) Quotas(ctx context.Context, a Actor) (QuotaOverview, error) {
	var v QuotaOverview
	tx, err := s.adminRead(ctx, a)
	if err != nil {
		return v, err
	}
	defer tx.Rollback()
	v.Workspace, err = budget(ctx, tx, a.Workspace, "")
	if err != nil {
		return v, err
	}
	rows, err := tx.QueryContext(ctx, groupBudgetSQL+" ORDER BY g.name_key,g.id", a.Workspace)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		b, err := scanBudget(rows)
		if err != nil {
			return v, err
		}
		v.Groups = append(v.Groups, b)
	}
	if err = rows.Err(); err != nil {
		return v, err
	}
	rows, err = tx.QueryContext(ctx, siteBudgetSQL+" ORDER BY s.id", a.Workspace)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		site, err := scanQuotaSite(rows)
		if err != nil {
			return v, err
		}
		v.Sites = append(v.Sites, site)
	}
	return v, rows.Err()
}

func (l QuotaLimits) valid(group bool) bool {
	var min int64
	if group {
		min = -1
	}
	return l.Sites >= min && l.Sites <= MaxSitesPerWorkspace && l.Bytes >= min && l.Bytes <= MaxWorkspaceManifestBytes
}
func (l QuotaLimits) summary() string {
	label := func(n int64) string {
		if n < 0 {
			return "inherit"
		}
		return fmt.Sprint(n)
	}
	return "sites=" + label(l.Sites) + ", bytes=" + label(l.Bytes)
}

// Lowering a ceiling is non-destructive. Existing sites and revisions remain
// readable; writes that consume more of the overdrawn resource are refused.
func (s *Store) SetQuota(ctx context.Context, a Actor, group string, limits QuotaLimits, version int64) error {
	tx, err := s.adminWrite(ctx, a)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setQuotaTx(ctx, tx, a, group, limits, version); err != nil {
		return err
	}
	return tx.Commit()
}

func setQuotaTx(ctx context.Context, tx *sql.Tx, a Actor, group string, limits QuotaLimits, version int64) error {
	if !limits.valid(group != "") {
		return bad("Choose valid limits of at most 100 sites and 2 GiB of retained storage.")
	}
	prev, err := budget(ctx, tx, a.Workspace, group)
	if err != nil {
		return err
	}
	if prev.Version != version {
		return conflict("The quota settings changed. Reload and review the current limits.")
	}
	if prev.Limits == limits {
		return nil
	}
	target, action := a.Workspace, "workspace_quota_changed"
	if group == "" {
		_, err = tx.ExecContext(ctx, `UPDATE sc_workspace SET site_limit=$1,byte_limit=$2,quota_version=quota_version+1 WHERE id=$3`, limits.Sites, limits.Bytes, a.Workspace)
	} else {
		target, action = group, "group_quota_changed"
		_, err = tx.ExecContext(ctx, `UPDATE sc_groups SET site_limit=$1,byte_limit=$2,version=version+1 WHERE workspace=$3 AND id=$4`, limits.Sites, limits.Bytes, a.Workspace, group)
	}
	if err != nil {
		return err
	}
	if err = recordAccountChange(ctx, tx, a, action, target, prev.Limits.summary(), limits.summary()); err != nil {
		return err
	}
	return nil
}

func (b QuotaBudget) check(scope string, add QuotaUsage) error {
	if add.Sites > 0 && b.Limits.Sites >= 0 && b.Usage.Sites+add.Sites > b.Limits.Sites {
		return fail("quota_exceeded", scope+" site limit reached. Ask a workspace administrator to review the site quota.")
	}
	if add.Bytes > 0 && b.Limits.Bytes >= 0 && b.Usage.Bytes+add.Bytes > b.Limits.Bytes {
		return fail("quota_exceeded", scope+" retained storage limit reached. Ask a workspace administrator to review the storage quota.")
	}
	return nil
}
func checkCharge(ctx context.Context, tx *sql.Tx, workspace, group string, add QuotaUsage) error {
	b, err := budget(ctx, tx, workspace, "")
	if err != nil {
		return err
	}
	if err = b.check("Workspace", add); err != nil || group == "" {
		return err
	}
	b, err = budget(ctx, tx, workspace, group)
	if err != nil {
		return err
	}
	return b.check("Group", add)
}

// AssignSiteGroup moves the site's full history under the workspace write lock.
// It does not change workspace usage, content, or publication state.
func (s *Store) AssignSiteGroup(ctx context.Context, a Actor, siteID, group string, version int64) error {
	tx, err := s.adminWrite(ctx, a)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := assignSiteGroupTx(ctx, tx, a, siteID, group, version); err != nil {
		return err
	}
	return tx.Commit()
}

func assignSiteGroupTx(ctx context.Context, tx *sql.Tx, a Actor, siteID, group string, version int64) error {
	site, err := scanQuotaSite(tx.QueryRowContext(ctx, siteBudgetSQL+" AND s.id=$2", a.Workspace, siteID))
	if errors.Is(err, sql.ErrNoRows) {
		return absent()
	}
	if err != nil {
		return err
	}
	if site.Version != version {
		return conflict("The site’s assignment changed. Reload and review its current group.")
	}
	if site.GroupID == group {
		return nil
	}
	if group == "" {
		_, err = tx.ExecContext(ctx, "DELETE FROM sc_site_groups WHERE workspace=$1 AND site=$2", a.Workspace, siteID)
	} else {
		var target QuotaBudget
		target, err = budget(ctx, tx, a.Workspace, group)
		if err != nil {
			return err
		}
		if err = target.check("Target group", QuotaUsage{Sites: 1, Bytes: site.Bytes}); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO sc_site_groups(workspace,site,group_id) VALUES($1,$2,$3)
			ON CONFLICT(workspace,site) DO UPDATE SET group_id=excluded.group_id`, a.Workspace, siteID, group)
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE sc_sites SET assignment_version=assignment_version+1 WHERE workspace=$1 AND id=$2", a.Workspace, siteID); err != nil {
		return err
	}
	if err = recordAccountChange(ctx, tx, a, "site_group_changed", siteID, site.GroupID, group); err != nil {
		return err
	}
	return nil
}

// QuotaSelection identifies the exact version an administrator reviewed.
type QuotaSelection struct {
	ID      string
	Version int64
}

func (s *Store) ClearQuotaSelection(ctx context.Context, a Actor, assignments bool, rows []QuotaSelection) error {
	if len(rows) == 0 || len(rows) > MaxSelection {
		return bad("Select between 1 and 128 rows at a time.")
	}
	tx, err := s.adminWrite(ctx, a)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		// Never allow an empty group ID to target the workspace ceiling.
		if row.ID == "" || seen[row.ID] || row.Version < 1 {
			return bad("Invalid or repeated selection.")
		}
		seen[row.ID] = true
		if assignments {
			err = assignSiteGroupTx(ctx, tx, a, row.ID, "", row.Version)
		} else {
			err = setQuotaTx(ctx, tx, a, row.ID, QuotaLimits{Sites: -1, Bytes: -1}, row.Version)
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
