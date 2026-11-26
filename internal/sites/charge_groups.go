package sites

import (
	"context"
	"database/sql"
)

type GroupOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type CreationGroupOptions struct {
	Groups          []GroupOption
	AllowUnassigned bool
}

// CreationGroups discloses only the groups the current credential may charge.
// The mutation rechecks the same policy under the workspace write lock.
func (s *Store) CreationGroups(ctx context.Context, a Actor) (CreationGroupOptions, error) {
	tx, err := s.readSnapshot(ctx)
	if err != nil {
		return CreationGroupOptions{}, err
	}
	defer tx.Rollback()
	a, err = accountPolicy(ctx, tx, a, false)
	if err != nil {
		return CreationGroupOptions{}, err
	}
	if err = actorErr(a, 2); err != nil {
		return CreationGroupOptions{}, err
	}
	return creationGroups(ctx, tx, a)
}
func creationGroups(ctx context.Context, tx *sql.Tx, a Actor) (CreationGroupOptions, error) {
	options := CreationGroupOptions{Groups: []GroupOption{}, AllowUnassigned: a.Role == "admin"}
	query := "SELECT g.id,g.name FROM sc_groups g WHERE g.workspace=$1"
	args := []any{a.Workspace}
	if a.Role != "admin" {
		query += ` AND g.role IN ('editor','publisher') AND EXISTS (SELECT 1 FROM sc_group_members gm
			WHERE gm.workspace=g.workspace AND gm.group_id=g.id AND gm.subject=$2)`
		args = append(args, a.Subject)
	}
	rows, err := tx.QueryContext(ctx, query+" ORDER BY g.name_key,g.id", args...)
	if err != nil {
		return options, err
	}
	defer rows.Close()
	for rows.Next() {
		var g GroupOption
		if err = rows.Scan(&g.ID, &g.Name); err != nil {
			return options, err
		}
		options.Groups = append(options.Groups, g)
	}
	if err = rows.Err(); err != nil {
		return options, err
	}
	if a.Role != "admin" {
		// Existing memberships alone do not change the pre-quota creation
		// contract. Enable mandatory assignment with the first group limit.
		err = tx.QueryRowContext(ctx, "SELECT NOT "+configuredGroupBudgetSQL, a.Workspace).Scan(&options.AllowUnassigned)
	}
	return options, err
}

func creationGroup(ctx context.Context, tx *sql.Tx, a Actor, requested string) (string, error) {
	options, err := creationGroups(ctx, tx, a)
	if err != nil {
		return "", err
	}
	if requested != "" {
		for _, g := range options.Groups {
			if g.ID == requested {
				return requested, nil
			}
		}
		return "", fail("forbidden", "This credential cannot charge that group. Use list_site_groups to see your available groups.")
	}
	if options.AllowUnassigned {
		return "", nil
	}
	switch len(options.Groups) {
	case 0:
		return "", fail("forbidden", "Ask a workspace administrator to add you to an editor or publisher group before creating a site.")
	case 1:
		return options.Groups[0].ID, nil
	default:
		return "", bad("Choose a group_id from list_site_groups before creating the site.")
	}
}

// An upload charges the site's assigned group, regardless of the uploader.
// A writer cannot spend another group's budget by editing one of its sites.
func canChargeSiteGroup(ctx context.Context, tx *sql.Tx, a Actor, group string) error {
	if a.Role == "admin" {
		return nil
	}
	if group == "" {
		var limited bool
		err := tx.QueryRowContext(ctx, "SELECT "+configuredGroupBudgetSQL, a.Workspace).Scan(&limited)
		if err != nil {
			return err
		}
		if limited {
			return fail("forbidden", "Ask a workspace administrator to assign this site to a group before adding to its retained storage.")
		}
		return nil
	}
	var allowed bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sc_group_members gm JOIN sc_groups g ON g.workspace=gm.workspace AND g.id=gm.group_id
		WHERE gm.workspace=$1 AND gm.subject=$2 AND gm.group_id=$3 AND g.role IN ('editor','publisher'))`, a.Workspace, a.Subject, group).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return fail("forbidden", "You need an editor or publisher membership in this site’s group to add a revision.")
	}
	return nil
}
