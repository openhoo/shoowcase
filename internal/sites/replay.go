package sites

import (
	"context"
	"crypto/rand"
	"database/sql"
	"time"
)

// Consume spends an expiring authentication nonce once, across all replicas.
// The nonce is random and expires before a key can legitimately be reused.
func (s *Store) Consume(ctx context.Context, key string, expiry time.Time) (bool, error) {
	return consumeOnce(ctx, s.db, key, expiry)
}

type replayExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func consumeOnce(ctx context.Context, q replayExecer, key string, expiry time.Time) (bool, error) {
	if key == "" || len(key) > 512 || !expiry.After(time.Now()) {
		return false, nil
	}
	result, err := q.ExecContext(ctx, "INSERT INTO sc_replay(key,expires_at) VALUES($1,$2) ON CONFLICT(key) DO NOTHING", key, expiry.Unix())
	if err != nil {
		return false, err
	}
	var sample [1]byte
	if _, err := rand.Read(sample[:]); err == nil && sample[0] == 0 {
		_, _ = q.ExecContext(ctx, "DELETE FROM sc_replay WHERE expires_at < $1", time.Now().Unix())
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// RedeemSetupKey consumes a Quickstart code only while its personal key is
// still valid. Serialize with deletion so a stale code cannot recreate a
// key-specific replay record after an administrator erased that account.
func (s *Store) RedeemSetupKey(ctx context.Context, secret string, expiry time.Time) (bool, error) {
	owner, err := s.ResolveKey(ctx, secret)
	if err != nil {
		return false, err
	}
	tx, err := s.accountWrite(ctx, owner)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	key := KeyID(secret)
	if _, err := resolveKeyID(ctx, tx, owner, key); err != nil {
		return false, err
	}
	used, err := consumeOnce(ctx, tx, "quickstart:"+key, expiry)
	if err != nil || !used {
		return false, err
	}
	return true, tx.Commit()
}
