package sites

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestBoundIssuerPersistsAcrossReplicas(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			ctx := context.Background()
			dsn := testDSN(t, driver)
			first := openTest(t, driver, dsn)
			second := openTest(t, driver, dsn)
			if err := first.RequireIssuer(ctx, "https://issuer.example/tenant-a"); err != nil {
				t.Fatal(err)
			}
			if err := second.RequireIssuer(ctx, "https://issuer.example/tenant-b"); err == nil {
				t.Fatal("another issuer could reuse existing identities")
			}
			if err := second.RequireIssuer(ctx, "urn:shoowcase:development"); err == nil {
				t.Fatal("development could reuse production identities")
			}
			if err := second.RequireIssuer(ctx, "https://issuer.example/tenant-a"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAtomicSessionLogout(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			ctx := context.Background()
			s := openTest(t, driver, testDSN(t, driver))
			key := "logout:" + strings.Repeat("a", 43)
			if _, err := s.db.ExecContext(ctx, "ALTER TABLE sc_workspace_audit ADD COLUMN lock_logout INTEGER NOT NULL DEFAULT 0 CHECK (lock_logout<>0 OR action<>'auth.logout')"); err != nil {
				t.Fatal(err)
			}
			until := time.Now().Add(time.Minute)
			if err := s.RevokeSessionAndAudit(ctx, key, until, author); err == nil {
				t.Fatal("logout succeeded with broken audit sink")
			}
			if revoked, err := s.IsConsumed(ctx, key); err != nil || revoked {
				t.Fatal("failed audit committed a partial revocation")
			}
			if _, err := s.db.ExecContext(ctx, "ALTER TABLE sc_workspace_audit DROP COLUMN lock_logout"); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := s.RevokeSessionAndAudit(ctx, key, until, author); err != nil {
					t.Fatal(err)
				}
			}
			if revoked, err := s.IsConsumed(ctx, key); err != nil || !revoked {
				t.Fatal("logout did not revoke browser family")
			}
			var events int
			if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sc_workspace_audit WHERE workspace=$1 AND subject=$2 AND action='auth.logout'", author.Workspace, author.Subject).Scan(&events); err != nil || events != 1 {
				t.Fatalf("logout count=%d, err=%v", events, err)
			}
		})
	}
}
