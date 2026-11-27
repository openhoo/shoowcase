package agentsetup

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openhoo/shoowcase/internal/mcpserver"
)

var testToken = "sc_key_" + strings.Repeat("1", 32) + "." + strings.Repeat("A", 43)

const testCode = "synthetic-short-lived-example"

func tempUser(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	for k, v := range map[string]string{"HOME": home, "USERPROFILE": home, "APPDATA": filepath.Join(home, "roaming"), "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "COPILOT_HOME": filepath.Join(home, ".copilot"), "CLAUDE_CONFIG_DIR": home} {
		t.Setenv(k, v)
	}
	return home
}

func TestConnectRenewalAndConflict(t *testing.T) {
	for _, name := range []string{"cursor", "copilot"} {
		t.Run(name, func(t *testing.T) {
			home := tempUser(t)
			var calls atomic.Int32
			var failure atomic.Bool
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var req map[string]string
				if r.Method != "POST" || r.URL.RequestURI() != "/quickstart/redeem" || r.Header.Get("Content-Type") != "application/json" || json.NewDecoder(r.Body).Decode(&req) != nil || req["code"] != testCode || req["client"] != name {
					t.Error("incorrect credential exchange")
				}
				if failure.Load() {
					http.Error(w, testToken+" "+testCode, http.StatusForbidden)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"token": testToken})
			}))
			defer s.Close()
			config := filepath.Join(home, ".cursor", "mcp.json")
			if name == "copilot" {
				config = filepath.Join(home, ".copilot", "mcp-config.json")
			}
			if err := os.MkdirAll(filepath.Dir(config), 0700); err != nil {
				t.Fatal(err)
			}
			initial := `{"unrelated":{"config":123},"mcpServers":{"keep":{"x":true},"shoowcase":{"command":"keep-existing"}}}`
			if err := os.WriteFile(config, []byte(initial), 0600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			connect := func() error {
				return Connect(t.Context(), []string{name, "--url", s.URL + "/mcp", "--setup-code", testCode}, &out)
			}
			if err := connect(); err != conflict || calls.Load() != 0 {
				t.Fatalf("conflict: %v, redeem count %d", err, calls.Load())
			}
			b, _ := os.ReadFile(config)
			if string(b) != initial {
				t.Fatal("existing config was changed")
			}
			if err := os.WriteFile(config, []byte(`{"unrelated":{"config":123},"mcpServers":{"keep":{"x":true}}}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := connect(); err != nil {
				t.Fatal(err)
			}
			b, _ = os.ReadFile(config)
			root, servers, _, err := loadClientFile(config)
			binary, _ := os.Executable()
			if err != nil || len(root["unrelated"]) == 0 || !bytes.Contains(servers["keep"], []byte("true")) || !ours(servers["shoowcase"], binary, name) {
				t.Fatal("wrong config content")
			}
			for _, secret := range []string{testToken, testCode} {
				if bytes.Contains(b, []byte(secret)) || strings.Contains(out.String(), secret) {
					t.Fatal("sensitive data leaked")
				}
			}
			dir, _ := credentialDir()
			path := filepath.Join(dir, name+".json")
			data, err := os.ReadFile(path)
			if err != nil || !privatePath(path, false) {
				t.Fatal("missing private credential")
			}
			if runtime.GOOS == "windows" && bytes.Contains(data, []byte(testToken)) {
				t.Fatal("stored Windows credential contains the token in clear text")
			}
			clear, err := unseal(data)
			if err != nil || !bytes.Contains(clear, []byte(testToken)) {
				t.Fatal("private store did not retain the credential")
			}
			failure.Store(true)
			err = connect()
			if err == nil || strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), testCode) {
				t.Fatal("failed redemption not handled safely")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(data, after) {
				t.Fatal("failed renewal destroyed previous credential")
			}
			failure.Store(false)
			if err := connect(); err != nil || calls.Load() != 3 {
				t.Fatalf("renewal: %v requests %d", err, calls.Load())
			}
			if runtime.GOOS != "windows" {
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
				if err := connect(); err == nil || calls.Load() != 3 {
					t.Fatal("accepted an insecure credential file")
				}
				if err := Stdio(t.Context(), []string{name}, strings.NewReader(""), io.Discard); err == nil {
					t.Fatal("stdio read an insecure credential file")
				}
			}
		})
	}
}

func TestEndpointAndRedirects(t *testing.T) {
	for _, bad := range []string{"http://public.example/mcp", "http://localhost.example/mcp", "https://user:pass@host/mcp", "https://host:443/%6dcp", "https://host/mcp?", "https://host/mcp#fragment", "https://host/mcp/other", "https://host/other"} {
		if _, err := endpoint(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	for _, good := range []string{"https://example.test/mcp", "http://127.0.0.1:1234/mcp", "http://[::1]:1234/mcp", "http://localhost:1234/mcp"} {
		if _, err := endpoint(good); err != nil {
			t.Error(err)
		}
	}
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	ep, _ := endpoint(source.URL + "/mcp")
	if _, err := redeem(t.Context(), httpClient(), *ep, "codex", testCode); err == nil {
		t.Fatal("accepted redirect")
	}
	in := strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\n")
	var out bytes.Buffer
	if err := bridge(t.Context(), httpClient(), credential{URL: ep.String(), Token: testToken}, in, &out); err != nil || !strings.Contains(out.String(), "\"error\"") || strings.Contains(out.String(), testToken) {
		t.Fatal("stdio did not report safe failure")
	}
	if hits.Load() != 0 {
		t.Fatal("followed a credential-bearing redirect")
	}
}

func TestBridgeWithRealSDK(t *testing.T) {
	h := mcpserver.New(func(ctx context.Context, name string, args json.RawMessage) (map[string]any, error) {
		if name != "list_sites" {
			t.Errorf("unexpected tool %q", name)
		}
		return map[string]any{"sites": []string{"example"}}, nil
	})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken || r.URL.Path != "/mcp" {
			t.Error("missing MCP credential or wrong path")
		}
		h.ServeHTTP(w, r)
	}))
	defer s.Close()
	for _, version := range []string{"2025-03-26", "2025-06-18", "2025-11-25", "2026-07-28"} {
		t.Run(version, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			in, writer := io.Pipe()
			reader, out := io.Pipe()
			done := make(chan error, 1)
			go func() {
				defer out.Close()
				done <- bridge(ctx, httpClient(), credential{URL: s.URL + "/mcp", Token: testToken}, in, out)
			}()
			defer in.Close()
			defer reader.Close()
			client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, nil)
			session, err := client.Connect(ctx, &mcp.IOTransport{Reader: reader, Writer: writer}, &mcp.ClientSessionOptions{ProtocolVersion: version})
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			if got := session.InitializeResult().ProtocolVersion; got != version {
				t.Fatalf("negotiated %q", got)
			}
			tools, err := session.ListTools(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			names := []string{}
			for _, tool := range tools.Tools {
				names = append(names, tool.Name)
			}
			if !slices.Contains(names, "list_sites") || len(names) != 9 {
				t.Fatal(names)
			}
			call, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_sites"})
			if err != nil || call.IsError || !strings.Contains(call.Content[0].(*mcp.TextContent).Text, "example") {
				t.Fatalf("lost tool result: %v %v", call, err)
			}
			writer.Close()
			select {
			case e := <-done:
				if e != nil {
					t.Error(e)
				}
			case <-ctx.Done():
				t.Fatal("bridge did not exit on EOF")
			}
		})
	}
}
