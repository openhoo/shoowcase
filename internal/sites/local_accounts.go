package sites

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// OIDC subjects must never use this namespace. Usernames identify credentials
// within one workspace; neither an email address nor a login name links accounts.
const LocalSubjectPrefix = "local:"

const localCredentialSchema = `CREATE TABLE sc_local_credentials (
  workspace TEXT NOT NULL, subject TEXT NOT NULL, username TEXT NOT NULL,
  salt BYTEA NOT NULL, digest BYTEA NOT NULL, generation BIGINT NOT NULL DEFAULT 1,
  PRIMARY KEY(workspace,subject), UNIQUE(workspace,username),
  FOREIGN KEY(workspace,subject) REFERENCES sc_members(workspace,subject))`

var localUsername = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{2,63}$`)

// Limit concurrent password derivations per replica, including unknown users.
// No hash work occurs with an open transaction or the workspace lock held.
var passwordSlots = make(chan struct{}, 4)

func derivePassword(ctx context.Context, password string, salt []byte) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case passwordSlots <- struct{}{}:
		defer func() { <-passwordSlots }()
	default:
		return nil, fail("rate_limited", "Too many sign-in attempts. Try again shortly.")
	}
	return pbkdf2.Key(sha256.New, password, salt, 600000, 32)
}

func checkNewPassword(password string) error {
	if len(password) > 1024 || !utf8.ValidString(password) || utf8.RuneCountInString(password) < 12 {
		return bad("Use a password of at least 12 characters and at most 1024 bytes.")
	}
	return nil
}

func hashNewPassword(ctx context.Context, password string) (salt, digest []byte, err error) {
	if err = checkNewPassword(password); err != nil {
		return
	}
	salt = make([]byte, 16)
	rand.Read(salt)
	digest, err = derivePassword(ctx, password, salt)
	return
}

func canonicalLogin(username string) string {
	username = strings.TrimSpace(username)
	// Check ASCII before case folding: some non-ASCII characters fold to ASCII.
	if !localUsername.MatchString(username) {
		return ""
	}
	return strings.ToLower(username)
}

func invalidPassword() error {
	return fail("unauthorized", "The username or password is incorrect.")
}

func (s *Store) CreateLocalAccount(ctx context.Context, admin Actor, username, password, role string) (Account, error) {
	return s.createLocal(ctx, admin, false, username, password, role)
}

// RegisterLocalAccount is used only by the deployment's optional public signup
// handler. Managed workspaces always require administrator provisioning.
func (s *Store) RegisterLocalAccount(ctx context.Context, workspace, username, password string) (Account, error) {
	return s.createLocal(ctx, Actor{Workspace: workspace}, true, username, password, "reader")
}

func (s *Store) createLocal(ctx context.Context, actor Actor, public bool, username, password, role string) (Account, error) {
	login := canonicalLogin(username)
	if login == "" {
		return Account{}, bad("Use 3–64 letters, numbers, periods, underscores or hyphens for the username. Start with a letter or number.")
	}
	if roleLevel(role) < 1 || roleLevel(role) > 3 {
		return Account{}, bad("Select reader, editor or publisher.")
	}
	target := Actor{Subject: LocalSubjectPrefix + id(), Workspace: actor.Workspace, Role: role}
	if err := actorErr(target, 1); err != nil {
		return Account{}, err
	}
	salt, hash, err := hashNewPassword(ctx, password)
	if err != nil {
		return Account{}, err
	}
	var tx *sql.Tx
	if public {
		actor = target
		tx, err = s.accountWrite(ctx, actor)
	} else {
		tx, err = s.adminWrite(ctx, actor)
	}
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback()
	if public {
		settings, err := readSettings(ctx, tx, actor.Workspace)
		if err != nil {
			return Account{}, err
		}
		if settings.Mode == "managed" {
			return Account{}, managedMembershipRequired()
		}
	}
	var taken bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sc_local_credentials WHERE workspace=$1 AND username=$2)", actor.Workspace, login).Scan(&taken); err != nil {
		return Account{}, err
	}
	if taken {
		return Account{}, conflict("This username is unavailable.")
	}
	if err = insertMember(ctx, tx, target, login, 0, time.Now()); err != nil {
		return Account{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO sc_local_credentials(workspace,subject,username,salt,digest) VALUES($1,$2,$3,$4,$5)", target.Workspace, target.Subject, login, salt, hash); err != nil {
		return Account{}, err
	}
	if err = recordAccountChange(ctx, tx, actor, "local_account_created", target.Subject, "", role); err != nil {
		return Account{}, err
	}
	m, _, err := member(ctx, tx, target)
	if err != nil {
		return Account{}, err
	}
	return m, tx.Commit()
}

// SetLocalRole changes the password account's sign-in grant. Group membership,
// personal limits and key scopes continue to constrain its effective access.
func (s *Store) SetLocalRole(ctx context.Context, admin Actor, subject, role, expectedRole, expectedCeiling string, expectedDisabled bool) error {
	tx, err := s.adminWrite(ctx, admin)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if subject == admin.Subject {
		return fail("forbidden", "You cannot change your own membership.")
	}
	if !strings.HasPrefix(subject, LocalSubjectPrefix) {
		return fail("forbidden", "Your identity provider manages this sign-in role.")
	}
	if roleLevel(role) == 0 {
		return bad("Choose a valid password account role.")
	}

	// Require an actual password credential in this administrator's workspace.
	var currentRole, ceiling string
	var disabled bool
	err = tx.QueryRowContext(ctx, `SELECT m.identity_role,m.ceiling,m.disabled
		FROM sc_members m JOIN sc_local_credentials c ON c.workspace=m.workspace AND c.subject=m.subject
		WHERE m.workspace=$1 AND m.subject=$2`, admin.Workspace, subject).Scan(&currentRole, &ceiling, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return fail("not_found", "Password account not found in this workspace.")
	}
	if err != nil {
		return err
	}
	if currentRole != expectedRole || ceiling != expectedCeiling || disabled != expectedDisabled {
		return conflict("This membership changed. Reload it and review the current settings.")
	}
	if currentRole == role {
		return nil
	}
	if _, err = tx.ExecContext(ctx, "UPDATE sc_members SET identity_role=$1 WHERE workspace=$2 AND subject=$3", role, admin.Workspace, subject); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE sc_local_credentials SET generation=generation+1 WHERE workspace=$1 AND subject=$2", admin.Workspace, subject); err != nil {
		return err
	}
	if err = recordAccountChange(ctx, tx, admin, "local_role_changed", subject, currentRole, role); err != nil {
		return err
	}
	return tx.Commit()
}

type localCredential struct {
	actor      Actor
	salt, hash []byte
	generation int64
}

const localCredColumns = `SELECT c.workspace,c.subject,m.identity_role,c.salt,c.digest,c.generation
	FROM sc_local_credentials c JOIN sc_members m ON m.workspace=c.workspace AND m.subject=c.subject
	WHERE NOT m.disabled AND c.workspace=$1 AND `

func (s *Store) verifyPassword(ctx context.Context, column, workspace, value, password string) (localCredential, error) {
	var c localCredential
	if len(password) > 1024 {
		return c, invalidPassword()
	}
	err := s.db.QueryRowContext(ctx, localCredColumns+column+"=$2", workspace, value).Scan(&c.actor.Workspace, &c.actor.Subject, &c.actor.Role, &c.salt, &c.hash, &c.generation)
	found := err == nil
	if !found {
		if !errors.Is(err, sql.ErrNoRows) {
			return localCredential{}, err
		}
		c.salt, c.hash = make([]byte, 16), make([]byte, 32)
	}
	derived, err := derivePassword(ctx, password, c.salt)
	if err != nil {
		return localCredential{}, err
	}
	if subtle.ConstantTimeCompare(derived, c.hash) != 1 || !found {
		return localCredential{}, invalidPassword()
	}
	return c, nil
}

func (s *Store) AuthenticateLocal(ctx context.Context, workspace, username, password string) (Actor, int64, error) {
	c, err := s.verifyPassword(ctx, "c.username", workspace, canonicalLogin(username), password)
	if err != nil {
		return Actor{}, 0, err
	}
	if err = s.ValidateLocalSession(ctx, c.actor, c.generation); err != nil {
		return Actor{}, 0, err
	}
	return c.actor, c.generation, nil
}

type localSessionKey struct{}

// WithLocalSession carries the credential generation in a verified browser
// session so the final authorization and its writes see the same snapshot.
func WithLocalSession(ctx context.Context, generation int64) context.Context {
	return context.WithValue(ctx, localSessionKey{}, generation)
}

func verifyLocalSession(ctx context.Context, q queryer, a Actor, generation int64) error {
	if generation <= 0 || !strings.HasPrefix(a.Subject, LocalSubjectPrefix) {
		return invalidPassword()
	}
	var live bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sc_local_credentials c JOIN sc_members m
	ON m.workspace=c.workspace AND m.subject=c.subject WHERE c.workspace=$1 AND c.subject=$2 AND c.generation=$3 AND NOT m.disabled)`, a.Workspace, a.Subject, generation).Scan(&live); err != nil {
		return err
	}
	if !live {
		return fail("unauthorized", "Sign in again to continue.")
	}
	return nil
}

func (s *Store) ValidateLocalSession(ctx context.Context, a Actor, generation int64) error {
	return verifyLocalSession(ctx, s.db, a, generation)
}

func checkSessionContext(ctx context.Context, q queryer, a Actor) error {
	if generation, ok := ctx.Value(localSessionKey{}).(int64); ok {
		return verifyLocalSession(ctx, q, a, generation)
	}
	return checkIdentity(ctx, q, a)
}

func (s *Store) ChangeLocalPassword(ctx context.Context, a Actor, oldPassword, newPassword string) error {
	if _, isKey := ctx.Value(keyCredentialID{}).(string); isKey || !strings.HasPrefix(a.Subject, LocalSubjectPrefix) {
		return fail("forbidden", "Sign in with your local account to change its password.")
	}
	if err := checkNewPassword(newPassword); err != nil {
		return err
	}
	if oldPassword == newPassword {
		return bad("Choose a different password.")
	}
	c, err := s.verifyPassword(ctx, "c.subject", a.Workspace, a.Subject, oldPassword)
	if err != nil {
		return err
	}
	salt, digest, err := hashNewPassword(ctx, newPassword)
	if err != nil {
		return err
	}
	tx, err := s.accountWrite(ctx, a)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = authorizeMember(ctx, tx, a, 1); err != nil {
		return err
	}
	// Another password change or suspension while we derived the new hash
	// invalidates the previous password proof.
	r, err := tx.ExecContext(ctx, "UPDATE sc_local_credentials SET salt=$1,digest=$2,generation=generation+1 WHERE workspace=$3 AND subject=$4 AND generation=$5", salt, digest, a.Workspace, a.Subject, c.generation)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fail("unauthorized", "Sign in again to continue.")
	}
	if err = recordAccountEvent(ctx, tx, a, "local_password_changed", a.Subject); err != nil {
		return err
	}
	return tx.Commit()
}
