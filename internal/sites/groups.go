package sites

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

type Group struct {
	ID, Name, Role string
	Version        int64
	Members        int
}

func validGroup(name, role string) bool {
	return labelOK(name) && roleLevel(role) >= 1 && roleLevel(role) <= 3
}

const groupFields = `g.id,g.name,g.role,g.version,(SELECT COUNT(*) FROM sc_group_members gm
    WHERE gm.workspace=g.workspace AND gm.group_id=g.id)`

func scanGroup(row scanner) (Group, error) {
	var g Group
	err := row.Scan(&g.ID, &g.Name, &g.Role, &g.Version, &g.Members)
	return g, err
}

func loadGroup(ctx context.Context, q queryer, a Actor, groupID string) (Group, error) {
	if !idRE.MatchString(groupID) {
		return Group{}, bad("Invalid group ID.")
	}
	g, err := scanGroup(q.QueryRowContext(ctx, "SELECT "+groupFields+" FROM sc_groups g WHERE g.workspace=$1 AND g.id=$2", a.Workspace, groupID))
	if errors.Is(err, sql.ErrNoRows) {
		return Group{}, fail("not_found", "Group not found in this workspace.")
	}
	return g, err
}

func (s *Store) ListGroups(ctx context.Context, a Actor) ([]Group, error) {
	tx, err := s.adminRead(ctx, a)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT "+groupFields+" FROM sc_groups g WHERE g.workspace=$1 ORDER BY g.name_key,g.id", a.Workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups []Group
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

func (s *Store) GroupMembers(ctx context.Context, a Actor, id string) ([]string, error) {
	tx, err := s.adminRead(ctx, a)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = loadGroup(ctx, tx, a, id); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT subject FROM sc_group_members WHERE workspace=$1 AND group_id=$2 ORDER BY subject", a.Workspace, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []string
	for rows.Next() {
		var subject string
		if err := rows.Scan(&subject); err != nil {
			return nil, err
		}
		members = append(members, subject)
	}
	return members, rows.Err()
}

func groupNameAvailable(ctx context.Context, q queryer, workspace, name, except string) error {
	var taken bool
	err := q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sc_groups WHERE workspace=$1 AND name_key=$2 AND id<>$3)", workspace, strings.ToLower(name), except).Scan(&taken)
	if err != nil {
		return err
	}
	if taken {
		return conflict("A group with this name already exists in the workspace.")
	}
	return nil
}

func (s *Store) CreateGroup(ctx context.Context, a Actor, name, role string) (Group, error) {
	name = strings.TrimSpace(name)
	if !validGroup(name, role) {
		return Group{}, bad("Enter a group name and choose reader, editor or publisher.")
	}
	tx, err := s.adminWrite(ctx, a)
	if err != nil {
		return Group{}, err
	}
	defer tx.Rollback()
	var n int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM sc_groups WHERE workspace=$1", a.Workspace).Scan(&n); err != nil {
		return Group{}, err
	}
	if n >= maxGroups {
		return Group{}, fail("quota_exceeded", "Workspace group limit reached.")
	}
	if err = groupNameAvailable(ctx, tx, a.Workspace, name, ""); err != nil {
		return Group{}, err
	}
	g := Group{ID: id(), Name: name, Role: role, Version: 1}
	if _, err = tx.ExecContext(ctx, "INSERT INTO sc_groups(workspace,id,name,name_key,role,version) VALUES($1,$2,$3,$4,$5,1)", a.Workspace, g.ID, name, strings.ToLower(name), role); err != nil {
		return Group{}, err
	}
	if err = recordAccountChange(ctx, tx, a, "group_created", g.ID, "", role); err != nil {
		return Group{}, err
	}
	return g, tx.Commit()
}

func (s *Store) groupWrite(ctx context.Context, a Actor, id string, version int64) (*sql.Tx, Group, error) {
	tx, err := s.adminWrite(ctx, a)
	if err != nil {
		return nil, Group{}, err
	}
	g, err := loadGroup(ctx, tx, a, id)
	if err == nil && g.Version != version {
		err = conflict("The group changed. Reload and review the current settings.")
	}
	if err != nil {
		tx.Rollback()
		return nil, Group{}, err
	}
	return tx, g, nil
}

func (s *Store) UpdateGroup(ctx context.Context, a Actor, id, name, role string, version int64) error {
	name = strings.TrimSpace(name)
	if !validGroup(name, role) {
		return bad("Enter a group name and choose reader, editor or publisher.")
	}
	tx, prev, err := s.groupWrite(ctx, a, id, version)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if prev.Name == name && prev.Role == role {
		return nil
	}
	if err = groupNameAvailable(ctx, tx, a.Workspace, name, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE sc_groups SET name=$1,name_key=$2,role=$3,version=version+1 WHERE workspace=$4 AND id=$5", name, strings.ToLower(name), role, a.Workspace, id); err != nil {
		return err
	}
	if err = recordAccountChange(ctx, tx, a, "group_updated", id, prev.Role, role); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteGroup(ctx context.Context, a Actor, id string, version int64) error {
	tx, g, err := s.groupWrite(ctx, a, id, version)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := deleteGroup(ctx, tx, a, g); err != nil {
		return err
	}
	return tx.Commit()
}

func deleteGroup(ctx context.Context, tx *sql.Tx, a Actor, g Group) error {
	id := g.ID
	var assigned bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sc_site_groups WHERE workspace=$1 AND group_id=$2)", a.Workspace, id).Scan(&assigned); err != nil {
		return err
	}
	if assigned {
		return conflict("Reassign this group’s sites before deleting the group.")
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM sc_groups WHERE workspace=$1 AND id=$2", a.Workspace, id); err != nil {
		return err
	}
	return recordAccountChange(ctx, tx, a, "group_deleted", id, g.Role, "")
}

func (s *Store) SetGroupMember(ctx context.Context, a Actor, id, subject string, present bool, version int64) error {
	tx, _, err := s.groupWrite(ctx, a, id, version)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	target := Actor{Workspace: a.Workspace, Subject: subject}
	if _, ok, err := member(ctx, tx, target); err != nil {
		return err
	} else if !ok {
		return fail("not_found", "Account not found in this workspace.")
	}
	var changed sql.Result
	action, before, after := "group_member_removed", subject, ""
	if present {
		action, before, after = "group_member_added", "", subject
		changed, err = tx.ExecContext(ctx, "INSERT INTO sc_group_members(workspace,group_id,subject) VALUES($1,$2,$3) ON CONFLICT(workspace,group_id,subject) DO NOTHING", a.Workspace, id, subject)
	} else {
		changed, err = tx.ExecContext(ctx, "DELETE FROM sc_group_members WHERE workspace=$1 AND group_id=$2 AND subject=$3", a.Workspace, id, subject)
	}
	if err != nil {
		return err
	}
	n, err := changed.RowsAffected()
	if err != nil || n == 0 {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE sc_groups SET version=version+1 WHERE workspace=$1 AND id=$2", a.Workspace, id); err != nil {
		return err
	}
	if err = recordAccountChange(ctx, tx, a, action, id, before, after); err != nil {
		return err
	}
	return tx.Commit()
}
