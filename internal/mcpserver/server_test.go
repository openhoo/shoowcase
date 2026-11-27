package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openhoo/shoowcase/internal/sites"
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
			if want := []string{"approve_public_change", "create_site", "get_site", "list_site_groups", "list_sites", "prepare_upload", "publish_site", "rollback_site", "set_site_access", "upload_files"}; !slices.Equal(names, want) {
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

// The tool catalog adds path, paging, and view constraints beyond Go's type
// system. Every property and its optionality must still match the typed query.
func TestGetSiteCatalogMatchesDomainQuery(t *testing.T) {
	typed, err := jsonschema.For[sites.GetSiteQuery](nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range definitions() {
		if tool.name != "get_site" {
			continue
		}
		got, want := slices.Sorted(maps.Keys(tool.input.Properties)), slices.Sorted(maps.Keys(typed.Properties))
		if !slices.Equal(got, want) {
			t.Fatalf("get_site catalog properties %v, typed query %v", got, want)
		}
		if !slices.Equal(slices.Sorted(slices.Values(tool.input.Required)), slices.Sorted(slices.Values(typed.Required))) {
			t.Fatalf("get_site required properties: catalog %v, typed query %v", tool.input.Required, typed.Required)
		}
		return
	}
	t.Fatal("get_site missing from catalog")
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
			return map[string]any{"approval_url": "https://control.example/approve", "approval_token": "example-token"}, &Error{Code: "approval_required", Message: "This revision needs review."}
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
		{"approve_public_change", `{}`},
		{"approve_public_change", `{"approval_token":"example-token","site_id":"other"}`},
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
	action, _ := perr["action"].(string)
	if perr["code"] != "approval_required" || !strings.Contains(action, "approve_public_change") ||
		out["approval_token"] != "example-token" || out["approval_url"] != "https://control.example/approve" {
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

func TestSDKInputErrorsIdentifyCorrectionWithoutEchoingValues(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(New(func(context.Context, string, json.RawMessage) (map[string]any, error) {
		calls.Add(1)
		return map[string]any{"sites": []any{}}, nil
	}))
	defer server.Close()
	client := connect(t, server.URL, "2025-11-25")

	for _, tc := range []struct {
		tool, args, field, guidance string
	}{
		{"list_sites", `{"limit":101}`, "limit", "1 to 100"},
		{"list_sites", `{"limit":"private-input"}`, "limit", "integer"},
		{"publish_site", `{"site_id":"private-input","request_id":"123"}`, "revision", "required"},
		{"set_site_access", `{"site_id":"s","revision":"r","request_id":"123","visibility":"private-input"}`, "visibility", "workspace, public, offline"},
		{"upload_files", `{"site_id":"s","expected_revision":"r","request_id":"123","files":[{"path":"index.html","text":"private-input"},{"path":"app.js"}]}`, "files[1]", "exactly one"},
		{"upload_files", `{"site_id":"s","expected_revision":"r","request_id":"123","files":[{"path":"index.html","text":"private-input","base64":"private-input"}]}`, "files[0]", "exactly one"},
		{"upload_files", `{"site_id":"s","expected_revision":"r","request_id":"123","files":[{"path":"folder with spaces/private-input","text":"private-input"}]}`, "files[0].path", "Relative site asset"},
		{"upload_files", `{"site_id":"s","expected_revision":"r","request_id":"123","files":"private-input"}`, "files", "array"},
		{"list_sites", `{"private-input":true}`, "", "cursor, limit"},
		{"list_site_groups", `{"private-input":true}`, "", "no arguments"},
		{"approve_public_change", `{"approval_token":{"token":"private-input"}}`, "approval_token", "string"},
	} {
		t.Run(tc.tool+"/"+tc.field+"/"+tc.guidance, func(t *testing.T) {
			r, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: tc.tool, Arguments: json.RawMessage(tc.args)})
			if err != nil || !r.IsError {
				t.Fatalf("expected tool error, got %v, %v", r, err)
			}
			text := r.Content[0].(*mcp.TextContent).Text
			var out struct {
				Error struct{ Code, Message, Field string }
			}
			if err := json.Unmarshal([]byte(text), &out); err != nil {
				t.Fatal(err)
			}
			if out.Error.Code != "invalid_argument" || out.Error.Field != tc.field || !strings.Contains(out.Error.Message, tc.guidance) || strings.Contains(text, "private-input") {
				t.Errorf("unexpected diagnostic: %s", text)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input reached the callback")
	}
	r, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_sites", Arguments: map[string]any{"limit": 100}})
	if err != nil || r.IsError || calls.Load() != 1 {
		t.Fatalf("corrected retry: %v, %v", r, err)
	}
}
