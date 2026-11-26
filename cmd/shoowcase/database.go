package main

import (
	"crypto/tls"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// Inspect the driver's effective settings, including libpq environment and
// additional hosts. Never return a parsing error that could contain the DSN.
func requireDatabaseTLS(dsn string) error {
	c, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return errors.New("invalid production PostgreSQL configuration")
	}
	verified := func(t *tls.Config) bool {
		return t != nil && !t.InsecureSkipVerify && t.ServerName != ""
	}
	if verified(c.TLSConfig) {
		for _, f := range c.Fallbacks {
			if !verified(f.TLSConfig) {
				return errors.New("every PostgreSQL fallback must use TLS with server identity verification")
			}
		}
		return nil
	}
	return errors.New("production PostgreSQL requires TLS with server identity verification (sslmode=verify-full)")
}
