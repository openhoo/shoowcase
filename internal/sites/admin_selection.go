package sites

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"unicode/utf8"
)

type AdminSelection struct {
	Action, Group string
	IDs           []string
}

// A review captures membership state (including per-group revisions), not just
// labels. The same read is repeated under the workspace write lock on confirm.
type AdminSelectionReview struct {
	Names       []string
	Fingerprint string
}

type reviewedTarget struct {
	ID, Name, Role, SignInRole, Ceiling string
	Disabled                            bool
	Version                             int64
}

const MaxSelection = 128

func reviewAdminSelection(ctx context.Context, tx *sql.Tx, a Actor, sel AdminSelection) (AdminSelectionReview, []reviewedTarget, error) {
	var review AdminSelectionReview
	var rows []reviewedTarget
	if len(sel.IDs) == 0 || len(sel.IDs) > MaxSelection || (sel.Action == "remove-members") != (sel.Group != "") {
		return review, nil, bad("Select between 1 and 128 entries from this list.")
	}
	var group Group
	var err error
	if sel.Group != "" {
		group, err = loadGroup(ctx, tx, a, sel.Group)
		if err != nil {
			return review, nil, err
		}
	}
	seen := make(map[string]bool, len(sel.IDs))
	for _, id := range sel.IDs {
		if seen[id] || id == "" || len(id) > 255 || !utf8.ValidString(id) {
			return review, nil, bad("Choose distinct entries from this list.")
		}
		seen[id] = true
		row := reviewedTarget{ID: id}
		label := ""
		switch sel.Action {
		case "suspend-members", "restore-members", "remove-members":
			if sel.Action != "remove-members" && id == a.Subject {
				return review, nil, fail("forbidden", "You cannot change your own membership.")
			}
			m, ok, err := member(ctx, tx, Actor{Workspace: a.Workspace, Subject: id})
			if err != nil {
				return review, nil, err
			}
			if !ok {
				return review, nil, fail("not_found", "Member not found in this workspace.")
			}
			row.Name, row.Role, row.SignInRole, row.Ceiling, row.Disabled = m.DisplayName, m.Role, m.IdentityRole, m.Ceiling, m.Disabled
			label = m.DisplayName + " (" + m.Subject + ")"
			if sel.Action == "remove-members" {
				var found bool
				if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sc_group_members WHERE workspace=$1 AND group_id=$2 AND subject=$3)", a.Workspace, sel.Group, id).Scan(&found); err != nil {
					return review, nil, err
				}
				if !found {
					return review, nil, conflict("A selected member is not in this group. Reload the group and review the selection.")
				}
				row.Version = group.Version
				label += " · " + group.Name
			} else if m.Disabled != (sel.Action == "restore-members") {
				return review, nil, conflict("Choose only active members to suspend or suspended members to restore.")
			}
		case "delete-groups":
			g, err := loadGroup(ctx, tx, a, id)
			if err != nil {
				return review, nil, err
			}
			row.Name, row.Role, row.Version = g.Name, g.Role, g.Version
			label = g.Name + " (" + strconv.Itoa(g.Members) + " members)"
		default:
			return review, nil, bad("Choose a listed action.")
		}
		review.Names = append(review.Names, label)
		rows = append(rows, row)
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return review, nil, err
	}
	review.Fingerprint = sum(b)
	return review, rows, nil
}

func (s *Store) ReviewAdminSelection(ctx context.Context, a Actor, sel AdminSelection) (AdminSelectionReview, error) {
	tx, err := s.adminRead(ctx, a)
	if err != nil {
		return AdminSelectionReview{}, err
	}
	defer tx.Rollback()
	review, _, err := reviewAdminSelection(ctx, tx, a, sel)
	return review, err
}

func (s *Store) ApplyAdminSelection(ctx context.Context, a Actor, sel AdminSelection, fingerprint string) error {
	tx, err := s.adminWrite(ctx, a)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	review, rows, err := reviewAdminSelection(ctx, tx, a, sel)
	if err != nil {
		return err
	}
	if fingerprint != review.Fingerprint {
		return conflict("The selected entries changed. Review them again before continuing.")
	}
	for _, r := range rows {
		switch sel.Action {
		case "suspend-members", "restore-members":
			err = changeMember(ctx, tx, a, r.ID, r.Ceiling, sel.Action == "suspend-members", r.Ceiling, r.Disabled)
		case "delete-groups":
			err = deleteGroup(ctx, tx, a, Group{ID: r.ID, Role: r.Role})
		case "remove-members":
			_, err = tx.ExecContext(ctx, "DELETE FROM sc_group_members WHERE workspace=$1 AND group_id=$2 AND subject=$3", a.Workspace, sel.Group, r.ID)
			if err == nil {
				err = recordAccountChange(ctx, tx, a, "group_member_removed", sel.Group, r.ID, "")
			}
		}
		if err != nil {
			return err
		}
	}
	if sel.Action == "remove-members" {
		if _, err := tx.ExecContext(ctx, "UPDATE sc_groups SET version=version+1 WHERE workspace=$1 AND id=$2", a.Workspace, sel.Group); err != nil {
			return err
		}
	}
	return tx.Commit()
}
