package sites

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// MaxAssertionClockSkew is the IdP clock tolerance at token validation. The
// same interval must be included when invalidating previously usable tokens.
const MaxAssertionClockSkew = time.Minute

// Password accounts receive a random subject on each creation and do not need
// a marker. OIDC subjects are external, stable, and can hold unexpired tokens.
func identityDigest(workspace, subject string) string {
	return sum([]byte("shoowcase/deleted-identity/v1\x00" + workspace + "\x00" + subject))
}

func rememberAccountDeletion(ctx context.Context, tx *sql.Tx, workspace, subject string) error {
	if strings.HasPrefix(subject, LocalSubjectPrefix) {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO sc_deleted_identities(workspace,identity_digest,blocked,cutoff) VALUES($1,$2,TRUE,$3)
		ON CONFLICT(workspace,identity_digest) DO UPDATE SET blocked=TRUE,cutoff=excluded.cutoff`,
		workspace, identityDigest(workspace, subject), time.Now().Add(MaxAssertionClockSkew).Unix())
	return err
}

// Keep a delegated ticket's authority separate from the verified IdP role.
// A ticket cannot update someone's role even when they have been re-enrolled.
type actionAssertionTime struct{}

func WithActionAssertion(ctx context.Context, assertionTime int64) context.Context {
	return context.WithValue(ctx, actionAssertionTime{}, assertionTime)
}

// AssertionTime returns the time of the verified OIDC assertion for a
// delegated operation. It is not a new identity observation.
func AssertionTime(ctx context.Context) int64 { return issued(ctx) }

func actionTime(ctx context.Context) int64 {
	if at, ok := ctx.Value(actionAssertionTime{}).(int64); ok {
		if session, ok := ctx.Value(verifiedIssuedAt{}).(int64); ok {
			return min(at, session)
		}
		return at
	}
	return issued(ctx)
}

// Keys are removed at deletion; callers authenticated by key independently
// recheck its ID and owner in the same transaction. A fresh IdP assertion is
// required after an administrator explicitly provisions the subject again.
func identityBlocked(ctx context.Context, q queryer, a Actor) (bool, error) {
	var denied bool
	if strings.HasPrefix(a.Subject, LocalSubjectPrefix) {
		err := q.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM sc_members WHERE workspace=$1 AND subject=$2)`, a.Workspace, a.Subject).Scan(&denied)
		return denied, err
	}
	key, _ := ctx.Value(keyCredentialID{}).(string)
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sc_deleted_identities WHERE workspace=$1 AND identity_digest=$2 AND (blocked OR ($3='' AND cutoff >= $4)))`,
		a.Workspace, identityDigest(a.Workspace, a.Subject), key, actionTime(ctx)).Scan(&denied)
	return denied, err
}

func checkIdentity(ctx context.Context, q queryer, a Actor) error {
	denied, err := identityBlocked(ctx, q, a)
	if err != nil {
		return err
	}
	if denied {
		return fail("forbidden", "This sign-in no longer has access. Contact a workspace administrator.")
	}
	return nil
}
