package agentsetup

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMergeClientJSONCPreservesCommentsAndNeighbors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	// Comments, irregular whitespace, commas, and CRLF must survive byte for
	// byte, including comments attached to the next server.
	initial := "{\r\n// user setting\r\n\"inputs\": [],\r\n\"servers\" : {\r\n\t// the database\r\n\t\"db\" : { \"command\" : \"x\", /* keep */ },\r\n},\r\n}\r\n"
	if err := os.WriteFile(path, []byte(initial), 0640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"command": "my tool"}
	match := func(raw json.RawMessage) bool {
		var v map[string]string
		return json.Unmarshal(raw, &v) == nil && v["command"] == "my tool"
	}
	if err := mergeClientJSONC(path, "servers", "shoowcase", want, match); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := bytes.Replace(after, []byte("\r\n\t\"shoowcase\": {\"command\":\"my tool\"},"), nil, 1); string(got) != initial {
		t.Fatalf("changed original bytes:\n%s", after)
	}
	applied, err := os.Stat(path)
	if err != nil || applied.Mode().Perm() != before.Mode().Perm() {
		t.Fatalf("did not keep original permissions: %v", err)
	}
	if err := mergeClientJSONC(path, "servers", "shoowcase", want, match); err != nil {
		t.Fatal(err)
	}
	same, err := os.Stat(path)
	if err != nil || !os.SameFile(applied, same) {
		t.Fatalf("repeat setup rewrote file: %v", err)
	}
	if err := mergeClientJSONC(path, "servers", "shoowcase", want, func(json.RawMessage) bool { return false }); !errors.Is(err, conflict) {
		t.Fatalf("expected conflict: %v", err)
	}
	now, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(now, after) {
		t.Fatalf("conflict modified file: %v", err)
	}
}

func TestClientJSONCRefusesAmbiguousOrInvalidSettings(t *testing.T) {
	tests := []string{
		"", "null", "[]", "{} {}",
		"{\"servers\":null}", "{\"servers\":123}", "{\"servers\":{\"shoowcase\":{},\"shoowcase\":{}}}",
		`{"servers":{}, "se\u0072vers":{}}`,
		`{"servers":{"shoowcase":{"command":"a","comm\u0061nd":"b"}}}`,
		`{"servers":{"shoowcase":{"args":[{"a":0,"a":1}]}}}`,
		`{"servers":{"secret":"do-not-print-this", broken}}`,
	}
	for i, fixture := range tests {
		path := filepath.Join(t.TempDir(), "mcp.json")
		if err := os.WriteFile(path, []byte(fixture), 0600); err != nil {
			t.Fatal(err)
		}
		err := mergeClientJSONC(path, "servers", "shoowcase", true, func(json.RawMessage) bool { return true })
		if err == nil || strings.Contains(err.Error(), "do-not-print-this") {
			t.Fatalf("case %d: expected safe error", i)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != fixture {
			t.Fatalf("case %d changed existing file: %v", i, err)
		}
	}
}

func TestClientJSONCReadAndInstallRaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	raw, ok, err := readClientJSONCEntry(path, "servers", "shoowcase")
	if err != nil || ok || raw != nil {
		t.Fatalf("unexpected missing-file read: %v, %v, %s", err, ok, raw)
	}
	pending, err := readClientDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	other := []byte(`{"other":true}`)
	if err := os.WriteFile(path, other, 0600); err != nil {
		t.Fatal(err)
	}
	if err := pending.install(path, []byte("{}")); err == nil {
		t.Fatal("overwrote concurrent file creation")
	}
	baseline, err := readClientDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	other = []byte(`{"other":false}`)
	if err := os.WriteFile(path, other, 0600); err != nil {
		t.Fatal(err)
	}
	if err := baseline.install(path, []byte("{}")); err == nil {
		t.Fatal("overwrote concurrent edit")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, other) {
		t.Fatalf("unexpected concurrent file contents: %v", err)
	}
}

func TestMergeClientJSONCDoesNotFollowSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(target, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mcp.json")
	if err := os.Symlink(target, path); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("cannot create symlink: %v", err)
		}
		t.Fatal(err)
	}
	if err := mergeClientJSONC(path, "servers", "shoowcase", true, nil); err == nil {
		t.Fatal("unexpected success")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "{}" {
		t.Fatalf("modified symlink target: %v", err)
	}
}

func TestVSCodeStablePortableRegistration(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("VSCODE_APPDATA", filepath.Join(dir, "appdata"))
	t.Setenv("VSCODE_PORTABLE", filepath.Join(dir, "portable"))
	binary := filepath.Join(dir, "Shoowcase Binary")
	portable := filepath.Join(dir, "portable", "user-data", "User", "mcp.json")
	appdata := filepath.Join(dir, "appdata", "Code", "User", "mcp.json")
	for _, path := range []string{portable, appdata} {
		if path == appdata {
			if _, err := os.Stat(appdata); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("wrote inactive profile")
			}
			t.Setenv("VSCODE_PORTABLE", "")
		}
		if err := registerVSCode(t.Context(), dir, binary); err != nil {
			t.Fatal(err)
		}
		raw, ok, err := readClientJSONCEntry(path, "servers", "shoowcase")
		if err != nil || !ok || !ours(raw, binary, "vscode-local") || !json.Valid(raw) {
			t.Fatalf("bad VS Code server: %v, %v, %s", err, ok, raw)
		}
		// A matching executable with an overridden environment belongs to
		// someone else; do not redeem the code for this configuration.
		var fields map[string]any
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		fields["env"] = map[string]string{"HOME": "other-home"}
		data, _ := json.Marshal(map[string]any{"servers": map[string]any{"shoowcase": fields}})
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := registerVSCode(t.Context(), dir, binary); !errors.Is(err, conflict) {
			t.Fatalf("accepted overridden env: %v", err)
		}
	}
	t.Setenv("VSCODE_PORTABLE", "relative")
	if err := registerVSCode(t.Context(), dir, binary); err == nil {
		t.Fatal("accepted relative user directory")
	}
}
