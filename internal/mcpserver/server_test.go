package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type identityKey struct{}

func TestSDKClientsNegotiateAndCall(t *testing.T) {
	// The current spec uses server/discover; older clients send initialize and
	// notifications/initialized. The same stateless endpoint must accept both.
	h := New(func(ctx context.Context, name string, args json.RawMessage) (map[string]any, error) {
		if name != "list_sites" || ctx.Value(identityKey{}) != "pilot" {
			t.Errorf("lost authenticated context: tool=%s, identity=%v", name, ctx.Value(identityKey{}))
		}
		return map[string]any{"sites": []any{}, "next_cursor": "second-page"}, nil
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, "pilot")))
	}))
	defer server.Close()
	for _, v := range []string{"2025-03-26", "2025-06-18", "2025-11-25", "2026-07-28"} {
		t.Run(v, func(t *testing.T) {
			c := connect(t, server.URL, v)
			if got := c.InitializeResult().ProtocolVersion; got != v || c.ID() != "" {
				t.Fatalf("negotiated version %q, session ID %q", got, c.ID())
			}
			res, err := c.ListTools(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, tool := range res.Tools {
				names = append(names, tool.Name)
			}
			slices.Sort(names)
			if want := []string{"create_site", "get_site", "list_site_groups", "list_sites", "prepare_upload", "publish_site", "rollback_site", "set_site_access", "upload_files"}; !slices.Equal(names, want) {
				t.Fatalf("catalog: got %v", names)
			}
			out, err := c.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_sites", Arguments: map[string]any{"limit": 1}})
			if err != nil || out.IsError {
				t.Fatalf("call: %v, %v", out, err)
			}
			// March 2025 predates structured output; JSON text must be independently sufficient.
			plain := out.Content[0].(*mcp.TextContent).Text
			if !strings.Contains(plain, "second-page") || (v >= "2025-06-18" && out.StructuredContent.(map[string]any)["next_cursor"] != "second-page") {
				t.Fatalf("incomplete result %v", out)
			}
		})
	}
}

func connect(t *testing.T, url, version string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "independent-client", Version: "test"}, nil)
	s, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: url}, &mcp.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestInputBoundariesAndActionableErrors(t *testing.T) {
	var calls atomic.Int32
	h := New(func(ctx context.Context, name string, args json.RawMessage) (map[string]any, error) {
		calls.Add(1)
		if name == "publish_site" {
			return map[string]any{"approval_url": "https://control.example/approve"}, &Error{Code: "approval_required", Message: "This revision needs review."}
		}
		return nil, errors.New("private database connection string")
	})
	server := httptest.NewServer(h)
	defer server.Close()
	c := connect(t, server.URL, "2026-07-28")
	for _, tc := range []struct{ name, args string }{
		{"list_sites", `{"workspace":"different-tenant"}`},
		{"list_site_groups", `{"workspace":"different-tenant"}`},
		{"create_site", `{"name":"Guide","request_id":"123","group_id":"a name instead of an ID"}`},
		{"list_sites", `{"limit":101}`},
		{"upload_files", `{"site_id":"a","expected_revision":"r","request_id":"123","files":[{"path":"index.html","text":"","base64":""}]}`},
		{"upload_files", `{"site_id":"a","expected_revision":"r","request_id":"123","files":[{"path":"../secret","text":"x"}]}`},
		{"upload_files", `{"site_id":"a","expected_revision":"r","request_id":"123","files":[{"path":"/etc/passwd","text":"x"}]}`},
		{"prepare_upload", `{"site_id":"a","expected_revision":"r","path":"img.png","size":1,"request_id":"123","sha256":"wrong"}`},
		{"publish_site", `{"site_id":"a","request_id":"123"}`},
	} {
		r, err := c.CallTool(t.Context(), &mcp.CallToolParams{Name: tc.name, Arguments: json.RawMessage(tc.args)})
		if err != nil || !r.IsError || !strings.Contains(r.Content[0].(*mcp.TextContent).Text, "invalid_argument") {
			t.Fatalf("%s: %v %v", tc.name, r, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("schema violations reached domain callback")
	}
	r, err := c.CallTool(t.Context(), &mcp.CallToolParams{Name: "publish_site", Arguments: map[string]any{"request_id": "123", "site_id": "a", "revision": "r"}})
	if err != nil || !r.IsError {
		t.Fatalf("unexpected result %v, %v", r, err)
	}
	out := r.StructuredContent.(map[string]any)
	perr := out["error"].(map[string]any)
	if perr["code"] != "approval_required" || perr["action"] == "" || out["approval_url"] != "https://control.example/approve" {
		t.Fatalf("lost approval details: %v", out)
	}
	r, err = c.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_sites"})
	if err != nil || !r.IsError || strings.Contains(r.Content[0].(*mcp.TextContent).Text, "private database") {
		t.Fatalf("internal error was not redacted")
	}
}

type proxyHost string

func (host proxyHost) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.Host = string(host)
	return http.DefaultTransport.RoundTrip(copy)
}

func TestLoopbackIngressPreservesOnlyConfiguredControlHost(t *testing.T) {
	const expected = "studio.example.test:8443"
	handler := NewForHost(func(context.Context, string, json.RawMessage) (map[string]any, error) {
		return map[string]any{"sites": []any{}}, nil
	}, expected, "test")
	s := httptest.NewServer(handler)
	defer s.Close()
	ctx := t.Context()
	realClient := mcp.NewClient(&mcp.Implementation{Name: "proxy-test", Version: "1"}, nil)
	session, err := realClient.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: s.URL, HTTPClient: &http.Client{Transport: proxyHost(expected)},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.ListTools(ctx, nil); err != nil {
		t.Fatal(err)
	}

	for _, wrong := range []string{"studio.example.test", "other.example.test:8443", "localhost"} {
		session, err := realClient.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint: s.URL, HTTPClient: &http.Client{Transport: proxyHost(wrong)},
		}, nil)
		if err == nil {
			session.Close()
			t.Errorf("accepted unexpected control authority %q", wrong)
		}
	}
}
