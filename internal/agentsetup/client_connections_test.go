package agentsetup

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopilotSurfacesReuseRegistrationButRedeemTheirOwnCodes(t *testing.T) {
	home := tempUser(t)
	var got []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		got = append(got, req["client"])
		_ = json.NewEncoder(w).Encode(map[string]string{"token": testToken})
	}))
	defer server.Close()
	var original []byte
	for _, name := range []string{"copilot", "copilot-app", "vscode"} {
		var out bytes.Buffer
		if err := Connect(t.Context(), []string{name, "--url", server.URL + "/mcp", "--setup-code", testCode}, &out); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(home, ".copilot", "mcp-config.json"))
		if err != nil {
			t.Fatal(err)
		}
		if name == "copilot" {
			original = b
		} else if !bytes.Equal(b, original) {
			t.Fatal("shared registration was rewritten")
		}
		dir, _ := credentialDir()
		creds, _ := filepath.Glob(filepath.Join(dir, "*.json"))
		if len(creds) != 1 || filepath.Base(creds[0]) != "copilot.json" {
			t.Fatal("expected the existing CLI credential slot")
		}
	}
	if strings.Join(got, ",") != "copilot,copilot-app,vscode" {
		t.Fatal("wrong client binding")
	}
}

func TestIndependentCustomStdioCredentials(t *testing.T) {
	tempUser(t)
	tokens := []string{testToken, strings.Replace(testToken, "1111", "2222", 1)}
	i := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"token": tokens[i]})
		i++
	}))
	defer s.Close()
	var slots []string
	for _, token := range tokens {
		var out bytes.Buffer
		if err := Connect(t.Context(), []string{"stdio", "--url", s.URL + "/mcp", "--setup-code", testCode}, &out); err != nil {
			t.Fatal(err)
		}
		_, config, found := strings.Cut(out.String(), "\n")
		var e entry
		if !found || json.Unmarshal([]byte(config), &e) != nil || len(e.Args) != 3 || e.Args[0] != "mcp-stdio" || strings.Contains(out.String(), token) || strings.Contains(out.String(), testCode) {
			t.Fatal("invalid or secret-bearing manual configuration")
		}
		path, err := os.Executable()
		if err != nil || e.Command != path {
			t.Fatal("incorrect connector command")
		}
		slot, ok := stdioSlot(e.Args[1:])
		if !ok {
			t.Fatal("unusable connection ID")
		}
		slots = append(slots, slot)
	}
	if slots[0] == slots[1] {
		t.Fatal("second setup replaced the first connection")
	}
	dir, _ := credentialDir()
	for i, slot := range slots {
		raw, err := os.ReadFile(filepath.Join(dir, slot+".json"))
		if err != nil {
			t.Fatal(err)
		}
		b, err := unseal(raw)
		if err != nil || !bytes.Contains(b, []byte(tokens[i])) {
			t.Fatal("wrong credential for custom connection")
		}
	}
	if _, ok := stdioSlot([]string{"stdio", "../../other"}); ok {
		t.Fatal("accepted a path as a connection ID")
	}
}
