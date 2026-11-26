package sites

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	accountPage      = 25
	maxMembers       = 2000
	maxAccountEvents = 2000
)

var accountSchema = []string{
	`CREATE TABLE sc_members (
	  workspace TEXT NOT NULL REFERENCES sc_workspace(id), subject TEXT NOT NULL,
	  display_name TEXT NOT NULL, identity_role TEXT NOT NULL, identity_at BIGINT NOT NULL,
	  ceiling TEXT NOT NULL DEFAULT '', disabled BOOLEAN NOT NULL DEFAULT FALSE,
	  created_at BIGINT NOT NULL, PRIMARY KEY(workspace,subject))`,
	`CREATE TABLE sc_agent_keys (
	  id TEXT PRIMARY KEY, workspace TEXT NOT NULL, subject TEXT NOT NULL, name TEXT NOT NULL,
	  scope TEXT NOT NULL, digest TEXT NOT NULL, created_at BIGINT NOT NULL, expires_at BIGINT NOT NULL,
	  revoked BOOLEAN NOT NULL DEFAULT FALSE,
	  FOREIGN KEY(workspace,subject) REFERENCES sc_members(workspace,subject))`,
	"CREATE INDEX sc_agent_keys_owner ON sc_agent_keys(workspace,subject,id)",
	"CREATE INDEX sc_agent_keys_workspace ON sc_agent_keys(workspace,id)",
	`CREATE TABLE sc_account_events (
	  id TEXT PRIMARY KEY, workspace TEXT NOT NULL REFERENCES sc_workspace(id),
	  subject TEXT NOT NULL, action TEXT NOT NULL, target TEXT NOT NULL, at BIGINT NOT NULL)`,
	"CREATE INDEX sc_account_events_timeline ON sc_account_events(workspace,at,id)",
}

type Account struct {
	Subject, Workspace, DisplayName, IdentityRole, Ceiling, Role string
	Disabled                                                     bool
	CreatedAt                                                    time.Time
	identityAt                                                   int64
}

type WorkspaceSummary struct{ Members, Sites, ActiveKeys int }
type AccountEvent struct {
	Action, Subject, Target string
	At                      time.Time
}

type verifiedIssuedAt struct{}
type keyCredentialID struct{}

// WithIdentityIssuedAt carries the issue time of a verified work identity.
// Do not use this for personal keys or roles taken from a signed action ticket.
func WithIdentityIssuedAt(ctx context.Context, unixSeconds int64) context.Context {
	return context.WithValue(ctx, verifiedIssuedAt{}, unixSeconds)
}

// WithKeyID marks requests already authenticated as this key by ResolveKey;
// store mutations recheck it while holding the workspace lock.
func WithKeyID(ctx context.Context, keyID string) context.Context {
	return context.WithValue(ctx, keyCredentialID{}, keyID)
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
type scanner interface{ Scan(...any) error }

const memberColumns = "workspace,subject,display_name,identity_role,identity_at,ceiling,disabled,created_at"

func roleLevel(role string) int {
	switch role {
	case "reader":
		return 1
	case "editor":
		return 2
	case "publisher":
		return 3
	case "admin":
		return 4
	}
	return 0
}
func minGrant(a, b string) string {
	if roleLevel(a) <= roleLevel(b) {
		return a
	}
	return b
}
func (m Account) effective(claim string) string {
	r := minGrant(m.IdentityRole, claim)
	if m.Ceiling != "" {
		r = minGrant(r, m.Ceiling)
	}
	return r
}
func scanMember(row scanner) (Account, error) {
	var m Account
	var t int64
	e := row.Scan(&m.Workspace, &m.Subject, &m.DisplayName, &m.IdentityRole, &m.identityAt, &m.Ceiling, &m.Disabled, &t)
	m.CreatedAt = time.UnixMilli(t).UTC()
	m.Role = m.effective(m.IdentityRole)
	return m, e
}
func member(ctx context.Context, q queryer, a Actor) (Account, bool, error) {
	m, e := scanMember(q.QueryRowContext(ctx, "SELECT "+memberColumns+" FROM sc_members WHERE workspace=$1 AND subject=$2", a.Workspace, a.Subject))
	if errors.Is(e, sql.ErrNoRows) {
		return Account{}, false, nil
	}
	return m, e == nil, e
}
func labelOK(n string) bool {
	return n != "" && utf8.ValidString(n) && utf8.RuneCountInString(n) <= 80 && !strings.ContainsFunc(n, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) })
}
func suspended() error {
	return fail("forbidden", "This account is suspended. Contact a workspace administrator.")
}
func membershipRequired() error        { return fail("forbidden", "Create your workspace account first.") }
func issued(ctx context.Context) int64 { i, _ := ctx.Value(verifiedIssuedAt{}).(int64); return i }
func lockWorkspace(ctx context.Context, tx *sql.Tx, driver, workspace string) error {
	if _, err := tx.ExecContext(ctx, "INSERT INTO sc_workspace(id) VALUES($1) ON CONFLICT(id) DO NOTHING", workspace); err != nil {
		return err
	}
	if driver == "pgx" {
		var id string
		return tx.QueryRowContext(ctx, "SELECT id FROM sc_workspace WHERE id=$1 FOR UPDATE", workspace).Scan(&id)
	}
	return nil
}
func (s *Store) accountWrite(ctx context.Context, a Actor) (*sql.Tx, error) {
	if err := actorErr(a, 1); err != nil {
		return nil, err
	}
	if _, isKey := ctx.Value(keyCredentialID{}).(string); isKey {
		return nil, fail("forbidden", "Sign in to manage an account.")
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	if e = lockWorkspace(ctx, tx, s.driver, a.Workspace); e != nil {
		tx.Rollback()
		return nil, e
	}
	return tx, nil
}

func recordAccountEvent(ctx context.Context, tx *sql.Tx, a Actor, action, target string) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO sc_account_events(id,workspace,subject,action,target,at) VALUES($1,$2,$3,$4,$5,$6)", id(), a.Workspace, a.Subject, action, target, time.Now().UnixMicro())
	if err != nil {
		return err
	}
	// The account console retains a bounded history. Its operational feed never
	// includes key values, digests, URLs, file content or IdP tokens.
	_, err = tx.ExecContext(ctx, "DELETE FROM sc_account_events WHERE id IN (SELECT id FROM sc_account_events WHERE workspace=$1 ORDER BY at DESC,id DESC LIMIT 1 OFFSET $2)", a.Workspace, maxAccountEvents)
	return err
}

func (s *Store) EnrollAccount(ctx context.Context, a Actor, displayName string) (Account, error) {
	displayName = strings.TrimSpace(displayName)
	if !labelOK(displayName) {
		return Account{}, bad("Enter a display name of 1–80 characters.")
	}
	tx, e := s.accountWrite(ctx, a)
	if e != nil {
		return Account{}, e
	}
	defer tx.Rollback()
	m, ok, e := member(ctx, tx, a)
	if e != nil {
		return Account{}, e
	}
	if ok {
		if m.Disabled {
			return Account{}, suspended()
		}
		m.Role = m.effective(a.Role)
		return m, nil
	}
	var n int
	if e = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM sc_members WHERE workspace=$1", a.Workspace).Scan(&n); e != nil {
		return Account{}, e
	}
	if n >= maxMembers {
		return Account{}, fail("quota_exceeded", "Workspace account limit reached. Contact your administrator.")
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	_, e = tx.ExecContext(ctx, "INSERT INTO sc_members(workspace,subject,display_name,identity_role,identity_at,created_at) VALUES($1,$2,$3,$4,$5,$6)", a.Workspace, a.Subject, displayName, a.Role, issued(ctx), now.UnixMilli())
	if e != nil {
		return Account{}, e
	}
	if e = recordAccountEvent(ctx, tx, a, "account_created", a.Subject); e != nil {
		return Account{}, e
	}
	m = Account{Workspace: a.Workspace, Subject: a.Subject, DisplayName: displayName, IdentityRole: a.Role, Role: a.Role, CreatedAt: now}
	return m, tx.Commit()
}

// Only newer assertions can update stored IdP privileges. A stricter role
// wins ties when an IdP issues two assertions within the same second.
func needsObservation(m Account, a Actor, at int64) bool {
	return at > 0 && (at > m.identityAt || at == m.identityAt && roleLevel(a.Role) < roleLevel(m.IdentityRole))
}
func (s *Store) AccountFor(ctx context.Context, a Actor) (Account, bool, error) {
	if e := actorErr(a, 1); e != nil {
		return Account{}, false, e
	}
	m, ok, e := member(ctx, s.db, a)
	if !ok || e != nil {
		return m, ok, e
	}
	if i := issued(ctx); needsObservation(m, a, i) {
		tx, e := s.accountWrite(ctx, a)
		if e != nil {
			return Account{}, false, e
		}
		defer tx.Rollback()
		m, ok, e = member(ctx, tx, a)
		if !ok || e != nil {
			return m, ok, e
		}
		if needsObservation(m, a, i) {
			if _, e = tx.ExecContext(ctx, "UPDATE sc_members SET identity_role=$1,identity_at=$2 WHERE workspace=$3 AND subject=$4", a.Role, i, a.Workspace, a.Subject); e != nil {
				return Account{}, false, e
			}
			if m.IdentityRole != a.Role {
				if e = recordAccountEvent(ctx, tx, a, "identity_role_updated", a.Subject); e != nil {
					return Account{}, false, e
				}
			}
			m.IdentityRole, m.identityAt = a.Role, i
		}
		if e = tx.Commit(); e != nil {
			return Account{}, false, e
		}
	}
	m.Role = m.effective(a.Role)
	if m.Disabled {
		return m, true, suspended()
	}
	return m, true, nil
}

// LimitActor keeps existing IdP-only clients working before enrollment, and
// enforces the workspace's local restrictions for every enrolled identity.
func (s *Store) LimitActor(ctx context.Context, a Actor) (Actor, error) {
	m, ok, e := s.AccountFor(ctx, a)
	if e != nil {
		return Actor{}, e
	}
	if ok {
		a.Role = m.Role
	}
	return a, nil
}

// Reevaluate membership and, for delegated requests, key validity within
// the same transaction as a site mutation. A key's role never updates IdP claims.
func accountPolicy(ctx context.Context, q queryer, a Actor, require bool) (Actor, error) {
	if key, ok := ctx.Value(keyCredentialID{}).(string); ok {
		if require {
			return Actor{}, fail("forbidden", "Sign in to manage an account.")
		}
		return resolveKeyID(ctx, q, a, key)
	}
	m, ok, e := member(ctx, q, a)
	if e != nil {
		return Actor{}, e
	}
	if !ok && require {
		return Actor{}, membershipRequired()
	}
	if ok {
		if m.Disabled {
			return Actor{}, suspended()
		}
		a.Role = m.effective(a.Role)
	}
	return a, nil
}
func authorizeMember(ctx context.Context, q queryer, a Actor, level int) (Actor, error) {
	if e := actorErr(a, 1); e != nil {
		return Actor{}, e
	}
	a, e := accountPolicy(ctx, q, a, true)
	if e != nil {
		return Actor{}, e
	}
	return a, actorErr(a, level)
}

func (s *Store) SetMember(ctx context.Context, a Actor, subject, ceiling string, disabled bool, expectedCeiling string, expectedDisabled bool) error {
	tx, e := s.accountWrite(ctx, a)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = authorizeMember(ctx, tx, a, 4); e != nil {
		return e
	}
	if subject == a.Subject {
		return fail("forbidden", "You cannot change your own membership.")
	}
	target := Actor{Subject: subject, Workspace: a.Workspace}
	m, ok, e := member(ctx, tx, target)
	if e != nil {
		return e
	}
	if !ok {
		return fail("not_found", "Account not found in this workspace.")
	}
	if m.IdentityRole == "admin" && ceiling != m.Ceiling {
		return fail("forbidden", "An administrator’s role is managed by the identity provider.")
	}
	if m.Ceiling != expectedCeiling || m.Disabled != expectedDisabled {
		return conflict("This membership changed. Reload it and review the current settings.")
	}
	if ceiling != m.Ceiling && ceiling != "" && (roleLevel(ceiling) < 1 || roleLevel(ceiling) > 3 || roleLevel(ceiling) > roleLevel(m.IdentityRole)) {
		return bad("Choose a role within this member’s identity-provider permissions.")
	}
	if m.Ceiling == ceiling && m.Disabled == disabled {
		return nil
	}
	if _, e = tx.ExecContext(ctx, "UPDATE sc_members SET ceiling=$1,disabled=$2 WHERE workspace=$3 AND subject=$4", ceiling, disabled, a.Workspace, subject); e != nil {
		return e
	}
	if disabled && !m.Disabled {
		if _, e = tx.ExecContext(ctx, "UPDATE sc_agent_keys SET revoked=TRUE WHERE workspace=$1 AND subject=$2 AND revoked=FALSE", a.Workspace, subject); e != nil {
			return e
		}
	}
	if disabled != m.Disabled {
		action := "account_reactivated"
		if disabled {
			action = "account_suspended"
		}
		if e = recordAccountEvent(ctx, tx, a, action, subject); e != nil {
			return e
		}
	}
	if m.Ceiling != ceiling {
		if e = recordAccountEvent(ctx, tx, a, "account_role_changed", subject); e != nil {
			return e
		}
	}
	return tx.Commit()
}

func (s *Store) ListAccounts(ctx context.Context, a Actor, after string) ([]Account, string, error) {
	if len(after) > 255 {
		return nil, "", bad("Invalid account cursor.")
	}
	tx, e := s.readSnapshot(ctx)
	if e != nil {
		return nil, "", e
	}
	defer tx.Rollback()
	if _, e = authorizeMember(ctx, tx, a, 4); e != nil {
		return nil, "", e
	}
	rows, e := tx.QueryContext(ctx, "SELECT "+memberColumns+" FROM sc_members WHERE workspace=$1 AND subject>$2 ORDER BY subject LIMIT $3", a.Workspace, after, accountPage+1)
	if e != nil {
		return nil, "", e
	}
	defer rows.Close()
	out := []Account{}
	for rows.Next() {
		if len(out) == accountPage {
			return out, out[accountPage-1].Subject, nil
		}
		m, e := scanMember(rows)
		if e != nil {
			return nil, "", e
		}
		out = append(out, m)
	}
	return out, "", rows.Err()
}

func (s *Store) WorkspaceStats(ctx context.Context, a Actor) (WorkspaceSummary, error) {
	var d WorkspaceSummary
	tx, e := s.readSnapshot(ctx)
	if e != nil {
		return d, e
	}
	defer tx.Rollback()
	if _, e = authorizeMember(ctx, tx, a, 4); e != nil {
		return d, e
	}
	e = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM sc_members WHERE workspace=$1),
	(SELECT COUNT(*) FROM sc_sites WHERE workspace=$1),
	(SELECT COUNT(*) FROM sc_agent_keys WHERE workspace=$1 AND NOT revoked AND expires_at>$2)`, a.Workspace, time.Now().Unix()).Scan(&d.Members, &d.Sites, &d.ActiveKeys)
	return d, e
}
func (s *Store) AccountActivity(ctx context.Context, a Actor) ([]AccountEvent, error) {
	tx, e := s.readSnapshot(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if _, e = authorizeMember(ctx, tx, a, 4); e != nil {
		return nil, e
	}
	rows, e := tx.QueryContext(ctx, "SELECT action,subject,target,at FROM sc_account_events WHERE workspace=$1 ORDER BY at DESC,id DESC LIMIT 25", a.Workspace)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []AccountEvent
	for rows.Next() {
		var v AccountEvent
		var n int64
		if e = rows.Scan(&v.Action, &v.Subject, &v.Target, &n); e != nil {
			return nil, e
		}
		v.At = time.UnixMicro(n).UTC()
		out = append(out, v)
	}
	return out, rows.Err()
}
