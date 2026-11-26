package main

import (
	"strings"
	"testing"
)

func TestProductionPostgresRequiresVerifiedTransport(t *testing.T) {
	t.Setenv("PGSSLMODE", "prefer")
	const prefix = "postgres://example:private-fixture@db.example.test/app"
	for _, mode := range []string{"", "?sslmode=disable", "?sslmode=prefer", "?sslmode=require", "?sslmode=verify-ca", "?sslmode=invalid"} {
		err := requireDatabaseTLS(prefix + mode)
		if err == nil || strings.Contains(err.Error(), "private-fixture") {
			t.Errorf("unsafe transport or error redaction for %q", mode)
		}
	}
	if err := requireDatabaseTLS(prefix + "?sslmode=verify-full"); err != nil {
		t.Fatal(err)
	}
}
