package agentsetup

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRelayReportsRecoveryAndStaysConnected(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"revoked", 401, testToken, "reconnect"},
		{"forbidden", 403, testCode, "access"},
		{"wrong endpoint", 404, testCode, "/mcp"},
		{"proxy", 407, testCode, "proxy"},
		{"busy", 429, testCode, "wait"},
		{"maintenance", 503, testCode, "temporarily unavailable"},
		{"invalid reply", 200, "<html>" + testToken, "invalid response"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls < 3 {
					w.WriteHeader(tt.status)
					_, _ = w.Write([]byte(tt.body))
					return
				}
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":9,"result":{}}`))
			}))
			defer s.Close()
			in := strings.NewReader("{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n" +
				"{\"jsonrpc\":\"2.0\",\"id\":\"a\",\"method\":\"ping\"}\n" +
				"{\"jsonrpc\":\"2.0\",\"id\":9,\"method\":\"ping\"}\n")
			var out bytes.Buffer
			if err := bridge(t.Context(), httpClient(), credential{URL: s.URL + "/mcp", Token: testToken}, in, &out); err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			var first struct {
				ID    string
				Error struct {
					Code    int
					Message string
				}
			}
			if len(lines) != 2 || json.Unmarshal([]byte(lines[0]), &first) != nil || first.ID != "a" || first.Error.Code != -32000 ||
				!strings.Contains(strings.ToLower(first.Error.Message), tt.want) || lines[1] != `{"jsonrpc":"2.0","id":9,"result":{}}` {
				t.Fatalf("expected an actionable request error and a successful next reply: %s", out.String())
			}
			if strings.Contains(out.String(), testToken) || strings.Contains(out.String(), testCode) {
				t.Fatal("relayed private response")
			}
		})
	}
}

func TestSetupDistinguishesExpiredCodeFromTemporaryFailure(t *testing.T) {
	for _, tt := range []struct {
		status int
		want   string
	}{
		{401, "new command"}, {403, "access"}, {404, "control URL"}, {407, "proxy"}, {429, "wait"}, {503, "temporarily unavailable"},
	} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, testCode, tt.status) }))
		ep, _ := endpoint(s.URL + "/mcp")
		_, err := redeem(t.Context(), httpClient(), *ep, "stdio", testCode)
		s.Close()
		if err == nil || !strings.Contains(err.Error(), tt.want) || strings.Contains(err.Error(), testCode) {
			t.Errorf("status %d: expected %q, got %v", tt.status, tt.want, err)
		}
	}
}
