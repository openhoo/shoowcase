//go:build windows

package agentsetup

import (
	"bytes"
	"testing"
)

func TestDPAPIUserCredential(t *testing.T) {
	plain := []byte(`{"url":"https://control.example.test/mcp","token":"` + testToken + `"}`)
	blob, err := seal(plain)
	if err != nil {
		t.Fatalf("protect credential: %v", err)
	}
	if bytes.Contains(blob, []byte(testToken)) || bytes.Contains(blob, []byte("control.example.test")) {
		t.Fatal("DPAPI blob contains the key or its destination in clear text")
	}
	restored, err := unseal(blob)
	if err != nil || !bytes.Equal(restored, plain) {
		t.Fatal("current Windows user could not recover the exact credential")
	}
	blob[len(blob)-1] ^= 1
	if _, err := unseal(blob); err == nil {
		t.Fatal("tampered DPAPI blob was accepted")
	}
}
