package sites

import (
	"context"
	"database/sql"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxGroups = 128

var tenantSchema = []string{
	"ALTER TABLE sc_workspace ADD COLUMN display_name TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE sc_workspace ADD COLUMN access_mode TEXT NOT NULL DEFAULT 'identity' CHECK(access_mode IN ('identity','managed'))",
	"ALTER TABLE sc_workspace ADD COLUMN settings_version BIGINT NOT NULL DEFAULT 1",
	`CREATE TABLE sc_groups (
	    workspace TEXT NOT NULL REFERENCES sc_workspace(id), id TEXT NOT NULL,
	    name TEXT NOT NULL, name_key TEXT NOT NULL, role TEXT NOT NULL CHECK(role IN ('reader','editor','publisher')),
	    version BIGINT NOT NULL, PRIMARY KEY(workspace,id), UNIQUE(workspace,name_key))`,
	groupMembersDDL,
	"CREATE INDEX sc_group_members_subject ON sc_group_members(workspace,subject,group_id)",
}

type WorkspaceSettings struct {
	ID, DisplayName, Mode string
	Version               int64
}

func readSettings(ctx context.Context, q queryer, workspace string) (WorkspaceSettings, error) {
	s := WorkspaceSettings{ID: workspace}
	err := q.QueryRowContext(ctx, "SELECT display_name,access_mode,settings_version FROM sc_workspace WHERE id=$1", workspace).Scan(&s.DisplayName, &s.Mode, &s.Version)
	return s, err
}

func managedMembershipRequired() error {
	return fail("forbidden", "A workspace administrator must provision this account before it can access the workspace.")
}

func (s *Store) adminRead(ctx context.Context, a Actor) (*sql.Tx, error) {
	tx, err := s.readSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = authorizeMember(ctx, tx, a, 4); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func (s *Store) adminWrite(ctx context.Context, a Actor) (*sql.Tx, error) {
	tx, err := s.accountWrite(ctx, a)
	if err != nil {
		return nil, err
	}
	if _, err = authorizeMember(ctx, tx, a, 4); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func (s *Store) WorkspaceSettings(ctx context.Context, a Actor) (WorkspaceSettings, error) {
	tx, err := s.adminRead(ctx, a)
	if err != nil {
		return WorkspaceSettings{}, err
	}
	defer tx.Rollback()
	return readSettings(ctx, tx, a.Workspace)
}

// Switching to managed mode preserves existing members. Non-admin members
// get reader access plus their highest group role, still capped by the IdP
// and their individual role limit. Users without accounts lose private access.
func (s *Store) SetWorkspaceSettings(ctx context.Context, a Actor, name, mode string, version int64) error {
	name = strings.TrimSpace(name)
	if name != "" && !labelOK(name) || mode != "identity" && mode != "managed" {
		return bad("Choose a valid workspace name and access mode.")
	}
	tx, err := s.adminWrite(ctx, a)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	prev, err := readSettings(ctx, tx, a.Workspace)
	if err != nil {
		return err
	}
	if prev.Version != version {
		return conflict("Workspace settings changed. Reload and review the current settings.")
	}
	if prev.DisplayName == name && prev.Mode == mode {
		return nil
	}
	if _, err = tx.ExecContext(ctx, "UPDATE sc_workspace SET display_name=$1, access_mode=$2, settings_version=settings_version+1 WHERE id=$3", name, mode, a.Workspace); err != nil {
		return err
	}
	if prev.Mode != mode {
		if err = recordAccountChange(ctx, tx, a, "workspace_access_changed", a.Workspace, prev.Mode, mode); err != nil {
			return err
		}
	}
	if prev.DisplayName != name {
		if err = recordAccountEvent(ctx, tx, a, "workspace_renamed", a.Workspace); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func insertMember(ctx context.Context, tx *sql.Tx, target Actor, name string, assertionTime int64, now time.Time) error {
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM sc_members WHERE workspace=$1", target.Workspace).Scan(&n); err != nil {
		return err
	}
	if n >= maxMembers {
		return fail("quota_exceeded", "Workspace account limit reached.")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO sc_members(workspace,subject,display_name,identity_role,identity_at,created_at)
		VALUES($1,$2,$3,$4,$5,$6)`, target.Workspace, target.Subject, name, target.Role, assertionTime, now.UnixMilli())
	return err
}

// The target is an exact subject from the configured issuer. Provisioning
// cannot create credentials or attest an IdP role. First verified sign-in
// supplies that role through AccountFor; until then we use a reader ceiling.
func (s *Store) ProvisionMember(ctx context.Context, a Actor, subject, name string) error {
	name = strings.TrimSpace(name)
	if !labelOK(name) || strings.HasPrefix(subject, LocalSubjectPrefix) || strings.TrimSpace(subject) == "" || len(subject) > 255 || !utf8.ValidString(subject) || strings.ContainsFunc(subject, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
		return bad("Enter an exact sign-in subject and a display name of 1–80 characters.")
	}
	tx, err := s.adminWrite(ctx, a)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	target := Actor{Workspace: a.Workspace, Subject: subject, Role: "reader"}
	if _, ok, err := member(ctx, tx, target); err != nil {
		return err
	} else if ok {
		return conflict("This account already exists in the workspace.")
	}
	if err = insertMember(ctx, tx, target, name, 0, time.Now()); err != nil {
		return err
	}
	// Explicit provisioning creates an empty account. Credentials issued before
	// (or in the same second as) this decision cannot act as its new owner.
	if _, err = tx.ExecContext(ctx, `UPDATE sc_deleted_identities SET blocked=FALSE,cutoff=CASE WHEN cutoff>$3 THEN cutoff ELSE $3 END WHERE workspace=$1 AND identity_digest=$2`, a.Workspace, identityDigest(a.Workspace, subject), time.Now().Unix()); err != nil {
		return err
	}
	if err = recordAccountEvent(ctx, tx, a, "member_provisioned", subject); err != nil {
		return err
	}
	return tx.Commit()
}
