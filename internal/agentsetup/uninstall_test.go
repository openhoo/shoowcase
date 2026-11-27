package agentsetup

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUninstallRemovesQuickstartOnly(t *testing.T) {
	home := tempUser(t)
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "localappdata"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("GEMINI_CLI_HOME", filepath.Join(home, "gemini profile"))
	t.Setenv("VSCODE_PORTABLE", filepath.Join(home, "portable"))
	binary := filepath.Join(home, ".local", "share", "shoowcase", "bin", "shoowcase")
	if runtime.GOOS == "windows" {
		binary = filepath.Join(os.Getenv("LOCALAPPDATA"), "Shoowcase", "bin", "shoowcase.exe")
	}
	dir, err := credentialDir()
	if err != nil {
		t.Fatal(err)
	}
	write := func(p, data string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(binary, "old installed connector")
	keepFile := filepath.Join(filepath.Dir(binary), "notes.txt")
	write(keepFile, "keep")
	write(filepath.Join(dir, "other-data.json"), "keep")
	var credentials []string
	for _, slot := range append([]string{"stdio-" + strings.Repeat("a", 32)}, automaticClients...) {
		p := filepath.Join(dir, slot+".json")
		write(p, testToken)
		credentials = append(credentials, p)
	}
	vs, err := vsCodePath(home)
	if err != nil {
		t.Fatal(err)
	}
	gemini := geminiPath(home)
	openCode := filepath.Join(openCodeDir(home), "opencode.jsonc")
	cursor := standardClientPath(home, "cursor")
	claude := standardClientPath(home, "claude")
	copilot := standardClientPath(home, "copilot")
	remote := `{"type":"http","url":"https://another.example/mcp"}`
	encode := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	for _, x := range []struct {
		id, path, collection string
		comment              bool
	}{
		{"claude", claude, "mcpServers", false}, {"copilot", copilot, "mcpServers", false},
		{"gemini", gemini, "mcpServers", true}, {"vscode-local", vs, "servers", true},
		{"opencode", openCode, "mcp", true},
	} {
		var server any = entry{Command: binary, Args: []string{"mcp-stdio", x.id}}
		if x.id == "copilot" {
			server = entry{Type: "local", Command: binary, Args: []string{"mcp-stdio", x.id}, Tools: []string{"*"}}
		}
		if x.id == "opencode" {
			server = openCodeServer{Type: "local", Command: []string{binary, "mcp-stdio", x.id}}
		}
		comment := ""
		if x.comment {
			comment = "// another server\n"
		}
		write(x.path, "{\"theme\":\"dark\", \""+x.collection+"\":{"+comment+"\"other\":"+remote+"  , \"shoowcase\":"+encode(server)+"}}")
	}
	custom := "{\"mcpServers\":{\"shoowcase\":" + remote + "}}"
	write(cursor, custom)
	for i := 0; i < 2; i++ {
		var output bytes.Buffer
		if err := Uninstall(t.Context(), nil, &output); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), "Agent keys") || strings.Contains(output.String(), testToken) {
			t.Fatalf("unexpected uninstall instructions: %s", output.String())
		}
	}
	for _, path := range append(credentials, binary) {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("uninstall left %s: %v", path, err)
		}
	}
	for _, p := range []string{keepFile, filepath.Join(dir, "other-data.json"), cursor} {
		want := "keep"
		if p == cursor {
			want = custom
		}
		if b, err := os.ReadFile(p); err != nil || string(b) != want {
			t.Fatalf("changed unrelated file %s: %v", p, err)
		}
	}
	for _, p := range []string{claude, copilot, gemini, vs, openCode} {
		b, err := os.ReadFile(p)
		if err != nil || strings.Contains(string(b), `"shoowcase"`) ||
			!strings.Contains(string(b), remote) || !strings.Contains(string(b), `"theme":"dark"`) {
			t.Fatalf("incorrect client cleanup in %s: %v", p, err)
		}
		if p == claude || p == copilot {
			if !json.Valid(b) {
				t.Fatalf("cleanup broke strict JSON in %s", p)
			}
		} else if !strings.Contains(string(b), "// another server") {
			t.Fatalf("cleanup lost comment in %s", p)
		}
	}
}
