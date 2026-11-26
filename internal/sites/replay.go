package sites

import (
	"context"
	"crypto/rand"
	"time"
)

// Consume spends an expiring authentication nonce once, across all replicas.
// The nonce is random and expires before a key can legitimately be reused.
func (s *Store) Consume(ctx context.Context, key string, expiry time.Time) (bool, error) {
	if key == "" || len(key) > 512 || !expiry.After(time.Now()) {
		return false, nil
	}
	result, err := s.db.ExecContext(ctx, "INSERT INTO sc_replay(key,expires_at) VALUES($1,$2) ON CONFLICT(key) DO NOTHING", key, expiry.Unix())
	if err != nil {
		return false, err
	}
	var sample [1]byte
	if _, err := rand.Read(sample[:]); err == nil && sample[0] == 0 {
		_, _ = s.db.ExecContext(ctx, "DELETE FROM sc_replay WHERE expires_at < $1", time.Now().Unix())
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
