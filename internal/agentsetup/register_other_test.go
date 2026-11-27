package agentsetup

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOtherClientsPreserveSettingsAndRejectConflicts(t *testing.T) {
	for _, name := range []string{"gemini", "opencode"} {
		t.Run(name, func(t *testing.T) {
			home := tempUser(t)
			for _, key := range []string{"OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR", "OPENCODE_CONFIG_CONTENT"} {
				t.Setenv(key, "")
			}
			t.Setenv("GEMINI_CLI_HOME", filepath.Join(home, "gemini-home"))
			binary := filepath.Join(home, "Space in path", "shoowcase")
			var path, collection string
			if name == "gemini" {
				path = filepath.Join(home, "gemini-home", ".gemini", "settings.json")
				collection = "mcpServers"
			} else {
				path = filepath.Join(home, ".config", "opencode", "opencode.jsonc")
				collection = "mcp"
			}
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			// Preserve the existing entry verbatim, including its explanatory
			// comment and unusual spacing. Gemini allows comments but no trailing comma.
			prior := `"custom": { "url": "https://other.example.test/mcp" }`
			value := "{\n // keep my profile\n \"" + collection + "\": {" + prior + "}\n}\n"
			if name == "opencode" {
				value = strings.Replace(value, prior+"}", prior+",}", 1)
			}
			if err := os.WriteFile(path, []byte(value), 0600); err != nil {
				t.Fatal(err)
			}
			if err := registerOther(t.Context(), name, binary); err != nil {
				t.Fatal(err)
			}
			out, err := os.ReadFile(path)
			if err != nil || !bytes.Contains(out, []byte(prior)) || !bytes.Contains(out, []byte("// keep my profile")) {
				t.Fatal("registration changed unrelated settings or comments")
			}
			raw, ok, err := readClientJSONCEntry(path, collection, "shoowcase")
			if !ok || err != nil {
				t.Fatalf("missing registration: %v", err)
			}
			if name == "gemini" {
				if !ours(raw, binary, name) || !json.Valid(bytes.Replace(out, []byte("// keep my profile"), nil, 1)) {
					t.Fatal("entry is not accepted by Gemini's JSON-with-comments parser")
				}
			} else {
				var got openCodeServer
				if json.Unmarshal(raw, &got) != nil || got.Type != "local" || len(got.Command) != 3 || got.Command[0] != binary || got.Command[1] != "mcp-stdio" || got.Command[2] != name {
					t.Fatal("invalid OpenCode command array")
				}
			}
			if err = registerOther(t.Context(), name, binary); err != nil {
				t.Fatal(err)
			}
			again, _ := os.ReadFile(path)
			if !bytes.Equal(out, again) {
				t.Fatal("renewal unnecessarily rewrote user settings")
			}
			if err = registerOther(t.Context(), name, filepath.Join(home, "different", "binary")); !errors.Is(err, conflict) {
				t.Fatalf("must refuse collision: %v", err)
			}
			again, _ = os.ReadFile(path)
			if !bytes.Equal(out, again) {
				t.Fatal("conflict changed user settings")
			}
		})
	}
}

func TestOpenCodeDoesNotHideLowerPrecedenceOrWriteOverride(t *testing.T) {
	home := tempUser(t)
	for _, key := range []string{"OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR", "OPENCODE_CONFIG_CONTENT"} {
		t.Setenv(key, "")
	}
	dir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"mcp":{"shoowcase":{"type":"remote","url":"https://elsewhere.invalid/mcp"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := registerOther(t.Context(), "opencode", filepath.Join(home, "bin", "shoowcase")); !errors.Is(err, conflict) {
		t.Fatalf("shadowed lower precedence server: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "opencode.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created a new config despite collision")
	}
	t.Setenv("OPENCODE_CONFIG", filepath.Join(home, "project", "opencode.json"))
	if err := registerOther(t.Context(), "opencode", "unused"); err == nil {
		t.Fatal("accepted a project config override")
	}
	if _, err := os.Stat(os.Getenv("OPENCODE_CONFIG")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created a project config")
	}
}

func TestGeminiInvalidOrModifiedEntryUntouched(t *testing.T) {
	home := tempUser(t)
	t.Setenv("GEMINI_CLI_HOME", home)
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.json")
	for _, body := range []string{`{"unrelated":{"flags":[true,]}}`, `{"mcpServers":{"shoowcase":{"command":"binary","args":["mcp-stdio","gemini"],"env":{"HOME":"different"}}}}`} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := registerOther(t.Context(), "gemini", "binary"); err == nil {
			t.Fatal("unsafe Gemini settings accepted")
		}
		got, _ := os.ReadFile(path)
		if string(got) != body {
			t.Fatal("rejected Gemini settings were modified")
		}
	}
}
