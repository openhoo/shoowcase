package sites

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const MCPKeyPrefix = "sc_key_"
const maxActiveKeys = 10

type AgentKey struct {
	ID, Name, Subject, Scope string
	CreatedAt, ExpiresAt     time.Time
	Revoked                  bool
}

func keyNameIndex(name string) string {
	return norm.NFC.String(cases.Fold().String(strings.TrimSpace(name)))
}

func numberedKeyName(name string, n int) string {
	suffix := " (" + strconv.Itoa(n) + ")"
	runes := []rune(name)
	if limit := 80 - len(suffix); len(runes) > limit {
		name = strings.TrimSpace(string(runes[:limit]))
	}
	return name + suffix
}

func (s *Store) CreateKey(ctx context.Context, a Actor, name, scope string, days int) (AgentKey, string, error) {
	return s.createKey(ctx, a, name, scope, days, false)
}

// CreateKeyWithSuggestedName gives automatic Quickstart names a unique suffix
// if the suggested name is already in use. Explicit names are never changed.
func (s *Store) CreateKeyWithSuggestedName(ctx context.Context, a Actor, name, scope string, days int) (AgentKey, string, error) {
	return s.createKey(ctx, a, name, scope, days, true)
}

func (s *Store) createKey(ctx context.Context, a Actor, name, scope string, days int, suggested bool) (AgentKey, string, error) {
	var zero AgentKey
	name = strings.TrimSpace(name)
	if !labelOK(name) {
		return zero, "", bad("Enter a key name of 1–80 characters.")
	}
	if scope == "" {
		scope = "reader"
	}
	if roleLevel(scope) < 1 || roleLevel(scope) > 3 || days < 1 || days > 90 {
		return zero, "", bad("Choose a viewer, editor or publisher key and an expiry of 1–90 days.")
	}
	tx, e := s.accountWrite(ctx, a)
	if e != nil {
		return zero, "", e
	}
	defer tx.Rollback()
	allowed, e := authorizeMember(ctx, tx, a, 1)
	if e != nil {
		return zero, "", e
	}
	if roleLevel(scope) > rank(allowed) {
		return zero, "", fail("forbidden", "The key cannot have more access than your account.")
	}
	now := time.Now().UTC().Truncate(time.Second)
	var n int
	if e = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM sc_agent_keys WHERE workspace=$1 AND subject=$2 AND NOT revoked AND expires_at>$3", a.Workspace, a.Subject, now.Unix()).Scan(&n); e != nil {
		return zero, "", e
	}
	if n >= maxActiveKeys {
		return zero, "", fail("quota_exceeded", "Revoke an existing key before creating another. You can have 10 active keys.")
	}
	// Keep 25 inactive keys per account for the UI; audit has separate retention.
	// The LIMIT idiom differs between databases, so use exclusion by a bounded subquery.
	if _, e = tx.ExecContext(ctx, `DELETE FROM sc_agent_keys WHERE workspace=$1 AND subject=$2 AND (revoked OR expires_at<=$3)
	AND id NOT IN (SELECT id FROM sc_agent_keys WHERE workspace=$1 AND subject=$2 AND (revoked OR expires_at<=$3) ORDER BY created_at DESC,id DESC LIMIT 25)`, a.Workspace, a.Subject, now.Unix()); e != nil {
		return zero, "", e
	}
	key := AgentKey{ID: id(), Name: name, Subject: a.Subject, Scope: scope, CreatedAt: now, ExpiresAt: now.Add(time.Duration(days) * 24 * time.Hour)}
	var random [32]byte
	if _, e = rand.Read(random[:]); e != nil {
		return zero, "", e
	}
	secret := MCPKeyPrefix + key.ID + "." + base64.RawURLEncoding.EncodeToString(random[:])
	for n := 2; ; n++ {
		insert, e := tx.ExecContext(ctx, `INSERT INTO sc_agent_keys(id,workspace,subject,name,name_key,scope,digest,created_at,expires_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(workspace,subject,name_key) DO NOTHING`,
			key.ID, a.Workspace, a.Subject, key.Name, keyNameIndex(key.Name), scope, sum([]byte(secret)), now.Unix(), key.ExpiresAt.Unix())
		if e != nil {
			return zero, "", e
		}
		nRows, e := insert.RowsAffected()
		if e != nil {
			return zero, "", e
		}
		if nRows == 1 {
			break
		}
		if !suggested {
			return zero, "", fail("key_name_taken", "You already have a key with this name. Choose a different name.")
		}
		key.Name = numberedKeyName(name, n)
	}
	if e = recordAccountChange(ctx, tx, a, "key_created", key.ID, "", scope); e != nil {
		return zero, "", e
	}
	if e = tx.Commit(); e != nil {
		return zero, "", e
	}
	return key, secret, nil
}

// KeyID returns the nonsecret reference embedded in a well-formed credential.
// It does not authenticate a request.
func KeyID(secret string) string {
	if len(secret) != len(MCPKeyPrefix)+32+1+43 || !strings.HasPrefix(secret, MCPKeyPrefix) {
		return ""
	}
	id, token, ok := strings.Cut(secret[len(MCPKeyPrefix):], ".")
	if !ok || !idRE.MatchString(id) {
		return ""
	}
	_, e := base64.RawURLEncoding.Strict().DecodeString(token)
	if e != nil {
		return ""
	}
	return id
}
func invalidKey() error { return fail("unauthorized", "This agent key is not valid.") }

const keyPrincipal = `SELECT k.workspace,k.subject,k.scope,k.digest,m.identity_role,m.ceiling,(w.access_mode='managed'),` + groupLevelSQL + `
	FROM sc_agent_keys k JOIN sc_members m ON m.workspace=k.workspace AND m.subject=k.subject
	JOIN sc_workspace w ON w.id=k.workspace
	WHERE k.id=$1 AND NOT k.revoked AND NOT m.disabled AND k.expires_at>$2`

func readKey(ctx context.Context, q queryer, id string) (a Actor, digest string, err error) {
	var scope string
	var m Account
	err = q.QueryRowContext(ctx, keyPrincipal, id, time.Now().Unix()).Scan(&a.Workspace, &a.Subject, &scope, &digest, &m.IdentityRole, &m.Ceiling, &m.managed, &m.groupLevel)
	if errors.Is(err, sql.ErrNoRows) {
		return Actor{}, "", invalidKey()
	}
	if err != nil {
		return Actor{}, "", err
	}
	if roleLevel(scope) < 1 || roleLevel(scope) > 3 || roleLevel(m.IdentityRole) < 1 {
		return Actor{}, "", invalidKey()
	}
	a.Role = m.effective(scope)
	if rank(a) < 1 {
		return Actor{}, "", invalidKey()
	}
	return a, digest, nil
}
func (s *Store) ResolveKey(ctx context.Context, secret string) (Actor, error) {
	id := KeyID(secret)
	if id == "" {
		return Actor{}, invalidKey()
	}
	a, digest, e := readKey(ctx, s.db, id)
	if e != nil {
		return Actor{}, e
	}
	if subtle.ConstantTimeCompare([]byte(digest), []byte(sum([]byte(secret)))) != 1 {
		return Actor{}, invalidKey()
	}
	return a, nil
}
func resolveKeyID(ctx context.Context, q queryer, owner Actor, id string) (Actor, error) {
	if !idRE.MatchString(id) {
		return Actor{}, invalidKey()
	}
	a, _, e := readKey(ctx, q, id)
	if e != nil {
		return Actor{}, e
	}
	if owner.Workspace != a.Workspace || owner.Subject != a.Subject || rank(owner) < 1 {
		return Actor{}, invalidKey()
	}
	a.Role = minGrant(a.Role, owner.Role)
	return a, nil
}

// ResolveKeyID is only for trusted signed tickets and already-authenticated
// request contexts. A public key ID is never a bearer credential.
func (s *Store) ResolveKeyID(ctx context.Context, owner Actor, id string) (Actor, error) {
	return resolveKeyID(ctx, s.db, owner, id)
}

func (s *Store) ListKeys(ctx context.Context, a Actor, workspaceWide bool, after string) ([]AgentKey, string, error) {
	if after != "" && !idRE.MatchString(after) {
		return nil, "", bad("Invalid key cursor.")
	}
	level := 1
	if workspaceWide {
		level = 4
	}
	tx, e := s.readSnapshot(ctx)
	if e != nil {
		return nil, "", e
	}
	defer tx.Rollback()
	if _, e = authorizeMember(ctx, tx, a, level); e != nil {
		return nil, "", e
	}
	query := "SELECT id,name,subject,scope,created_at,expires_at,revoked FROM sc_agent_keys WHERE workspace=$1 AND id>$2"
	args := []any{a.Workspace, after}
	if !workspaceWide {
		query += " AND subject=$3"
		args = append(args, a.Subject)
	}
	rows, e := tx.QueryContext(ctx, query+" ORDER BY id LIMIT 26", args...)
	if e != nil {
		return nil, "", e
	}
	defer rows.Close()
	out := []AgentKey{}
	for rows.Next() {
		if len(out) == accountPage {
			return out, out[accountPage-1].ID, nil
		}
		var k AgentKey
		var c, x int64
		if e = rows.Scan(&k.ID, &k.Name, &k.Subject, &k.Scope, &c, &x, &k.Revoked); e != nil {
			return nil, "", e
		}
		k.CreatedAt = time.Unix(c, 0).UTC()
		k.ExpiresAt = time.Unix(x, 0).UTC()
		out = append(out, k)
	}
	return out, "", rows.Err()
}

// KeysForRevocation resolves the names from the database for the confirmation
// page. A personal list never includes another member’s key, even for an admin.
func (s *Store) KeysForRevocation(ctx context.Context, a Actor, workspaceWide bool, ids []string) ([]AgentKey, error) {
	tx, err := s.readSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	return selectedKeys(ctx, tx, a, workspaceWide, ids)
}

func selectedKeys(ctx context.Context, tx *sql.Tx, a Actor, workspaceWide bool, ids []string) ([]AgentKey, error) {
	if len(ids) == 0 || len(ids) > accountPage {
		return nil, bad("Select between 1 and 25 keys.")
	}
	level := 1
	query := "SELECT id,name,subject,revoked FROM sc_agent_keys WHERE workspace=$1 AND id=$2"
	if workspaceWide {
		level = 4
	} else {
		query += " AND subject=$3"
	}
	if _, err := authorizeMember(ctx, tx, a, level); err != nil {
		return nil, err
	}
	keys := make([]AgentKey, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !idRE.MatchString(id) || seen[id] {
			return nil, bad("Select each key once.")
		}
		seen[id] = true
		args := []any{a.Workspace, id}
		if !workspaceWide {
			args = append(args, a.Subject)
		}
		var k AgentKey
		err := tx.QueryRowContext(ctx, query, args...).Scan(&k.ID, &k.Name, &k.Subject, &k.Revoked)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fail("not_found", "Agent key not found.")
		}
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, nil
}

// RevokeKeys rechecks the entire selection under the workspace write lock.
// A rejected selection revokes nothing. A retried revocation adds no event.
func (s *Store) RevokeKeys(ctx context.Context, a Actor, workspaceWide bool, ids []string) error {
	tx, err := s.accountWrite(ctx, a)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	keys, err := selectedKeys(ctx, tx, a, workspaceWide, ids)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if k.Revoked {
			continue
		}
		if _, err = tx.ExecContext(ctx, "UPDATE sc_agent_keys SET revoked=TRUE WHERE workspace=$1 AND id=$2", a.Workspace, k.ID); err != nil {
			return err
		}
		if err = recordAccountEvent(ctx, tx, a, "key_revoked", k.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) RevokeKey(ctx context.Context, a Actor, id string) error {
	return s.RevokeKeys(ctx, a, false, []string{id})
}
