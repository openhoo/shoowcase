package sites

import (
	"context"
	"errors"
	"strings"
	"time"
)

// A Shoowcase database is scoped to one OIDC issuer. Subjects and workspace
// claims are identifiers assigned by that provider, not globally unique names.
const issuerBindingSchema = "CREATE TABLE sc_identity_provider (singleton INTEGER PRIMARY KEY CHECK (singleton=1), issuer TEXT NOT NULL)"

func (s *Store) RequireIssuer(ctx context.Context, issuer string) error {
	if issuer == "" {
		return errors.New("missing identity issuer")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT INTO sc_identity_provider(singleton,issuer) VALUES(1,$1) ON CONFLICT(singleton) DO NOTHING", issuer); err != nil {
		return err
	}
	var bound string
	if err = tx.QueryRowContext(ctx, "SELECT issuer FROM sc_identity_provider WHERE singleton=1").Scan(&bound); err != nil {
		return err
	}
	if bound != issuer {
		return errors.New("configured identity issuer differs from the issuer bound to this database")
	}
	return tx.Commit()
}

// IsConsumed checks a session revocation until the associated session expires.
func (s *Store) IsConsumed(ctx context.Context, key string) (bool, error) {
	var spent bool
	err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sc_replay WHERE key=$1 AND expires_at>$2)", key, time.Now().Unix()).Scan(&spent)
	return spent, err
}

// RevokeSessionAndAudit commits the revocation and its tenant audit event
// together. Retrying a logout with the same valid cookie is idempotent.
func (s *Store) RevokeSessionAndAudit(ctx context.Context, key string, expires time.Time, a Actor) error {
	if len(key) != len("logout:")+43 || !strings.HasPrefix(key, "logout:") {
		return bad("Invalid revocation marker.")
	}
	tx, err := s.accountWrite(ctx, a)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, "INSERT INTO sc_replay(key,expires_at) VALUES($1,$2) ON CONFLICT(key) DO NOTHING", key, expires.Unix())
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	if err := appendAudit(ctx, tx, a, AuditEvent{Action: "auth.logout"}); err != nil {
		return err
	}
	return tx.Commit()
}
