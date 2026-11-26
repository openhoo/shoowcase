package telemetry_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openhoo/shoowcase/internal/mcpserver"
	"github.com/openhoo/shoowcase/internal/telemetry"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/trace"
	clog "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	cmetric "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	ctrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	logpb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricpb "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

const marker = "private-tenant-canary"
const parentID = "0123456789abcdef0123456789abcdef"
const traceparent = "00-" + parentID + "-1122334455667788-01"

func cleanEnv(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		k, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(k, "OTEL_") || k == "SHOOWCASE_OTEL_TRUST_TRACEPARENT" {
			t.Setenv(k, "")
		}
	}
}

func attr(attrs []*common.KeyValue, key string) *common.AnyValue {
	for _, v := range attrs {
		if v.Key == key {
			return v.Value
		}
	}
	return nil
}

func TestOTLPCollectorObservesMCPFailuresWithoutTenantData(t *testing.T) {
	cleanEnv(t)
	var mu sync.Mutex
	var spans []*tracepb.Span
	var logs []*logpb.LogRecord
	var metrics []*metricpb.Metric
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if bytes.Contains(data, []byte(marker)) {
			t.Error("tenant canary reached " + r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/x-protobuf" {
			t.Error("missing OTLP protobuf content type")
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/v1/traces":
			var req ctrace.ExportTraceServiceRequest
			if err := proto.Unmarshal(data, &req); err != nil {
				t.Error(err)
				return
			}
			for _, rs := range req.ResourceSpans {
				if attr(rs.GetResource().GetAttributes(), "service.name").GetStringValue() != "shoowcase" || attr(rs.GetResource().GetAttributes(), "service.version").GetStringValue() != "test" || attr(rs.GetResource().GetAttributes(), "service.instance.id").GetStringValue() == "" {
					t.Error("missing service identity")
				}
				for _, ss := range rs.ScopeSpans {
					spans = append(spans, ss.Spans...)
				}
			}
		case "/v1/metrics":
			var req cmetric.ExportMetricsServiceRequest
			if err := proto.Unmarshal(data, &req); err != nil {
				t.Error(err)
				return
			}
			for _, rm := range req.ResourceMetrics {
				for _, sm := range rm.ScopeMetrics {
					metrics = append(metrics, sm.Metrics...)
				}
			}
		case "/v1/logs":
			var req clog.ExportLogsServiceRequest
			if err := proto.Unmarshal(data, &req); err != nil {
				t.Error(err)
				return
			}
			for _, rl := range req.ResourceLogs {
				for _, sl := range rl.ScopeLogs {
					logs = append(logs, sl.LogRecords...)
				}
			}
		default:
			t.Error("unexpected telemetry endpoint")
		}
	}))
	defer collector.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL)
	app := httptest.NewUnstartedServer(nil)
	defer app.Close()
	control := "http://" + app.Listener.Addr().String()
	rt, err := telemetry.New(t.Context(), "test", control, func() sql.DBStats {
		return sql.DBStats{Idle: 2, InUse: 3, MaxOpenConnections: 16, WaitCount: 5, WaitDuration: 750 * time.Millisecond}
	})
	if err != nil {
		t.Fatal(err)
	}
	m := mcpserver.NewForHost(func(_ context.Context, tool string, _ json.RawMessage) (map[string]any, error) {
		if tool == "publish_site" {
			return nil, &mcpserver.Error{Code: "revision_conflict", Message: "Read the current revision."}
		}
		return nil, errors.New(marker)
	}, app.Listener.Addr().String(), "test")
	app.Config.Handler = rt.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp" {
			m.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/deny" {
			status := http.StatusUnauthorized
			if r.Method == "POST" {
				status = http.StatusForbidden
			}
			http.Error(w, marker, status)
			return
		}
		http.Error(w, marker, http.StatusServiceUnavailable)
	}))
	app.Start()
	client := mcp.NewClient(&mcp.Implementation{Name: "synthetic"}, nil)
	s, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: app.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_site", Arguments: map[string]any{"site_id": marker}})
	if err != nil || !out.IsError {
		t.Fatalf("expected tool error: %v", err)
	}
	out, err = s.CallTool(t.Context(), &mcp.CallToolParams{Name: "publish_site", Arguments: map[string]any{"site_id": "site", "revision": "revision", "request_id": "request"}})
	if err != nil || !out.IsError || out.StructuredContent.(map[string]any)["error"].(map[string]any)["code"] != "conflict" {
		t.Fatalf("expected actionable revision conflict, got %v, %v", out, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	r, _ := http.NewRequest("GET", app.URL+"/sites/"+marker+"/edit?t="+marker, nil)
	r.Header.Set("Authorization", "Bearer "+marker)
	r.Header.Set("Baggage", "tenant="+marker)
	r.Header.Set("Traceparent", traceparent)
	r.Header.Set("Tracestate", "tenant="+marker)
	resp, err := app.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	r, _ = http.NewRequest("GET", app.URL+"/"+marker, nil)
	r.Host = marker + ".example.test"
	resp, err = app.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	for _, host := range []string{app.Listener.Addr().String(), marker + ".example.test"} {
		for _, method := range []string{"GET", "POST"} {
			for range 3 {
				req, _ := http.NewRequest(method, app.URL+"/deny", nil)
				req.Host = host
				res, err := app.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				res.Body.Close()
			}
		}
	}
	d, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := rt.Shutdown(d); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	var tool, conflict, edit, delivery *tracepb.Span
	for _, s := range spans {
		switch s.Name {
		case "tools/call get_site":
			tool = s
		case "tools/call publish_site":
			conflict = s
		case "GET /sites/{site}/edit":
			edit = s
		case "GET /{path...}":
			delivery = s
		}
		if s.TraceState != "" || hex.EncodeToString(s.TraceId) == parentID {
			t.Error("untrusted trace headers accepted")
		}
	}
	if tool == nil || edit == nil || delivery == nil || tool.GetStatus().GetCode() != tracepb.Status_STATUS_CODE_ERROR {
		t.Fatal("missing tool or HTTP error span")
	}
	if attr(conflict.GetAttributes(), "shoowcase.outcome").GetStringValue() != "conflict" {
		t.Fatal("revision conflict was classified as a server error")
	}
	var parent *tracepb.Span
	for _, s := range spans {
		if bytes.Equal(tool.ParentSpanId, s.SpanId) && bytes.Equal(s.TraceId, tool.TraceId) {
			parent = s
		}
	}
	if parent.GetName() != "POST /mcp" || attr(parent.GetAttributes(), "http.response.status_code").GetIntValue() != 200 {
		t.Fatal("failed tool is missing parent HTTP 200 span")
	}
	correlated := 0
	for _, l := range logs {
		if bytes.Equal(l.TraceId, conflict.TraceId) && bytes.Equal(l.SpanId, conflict.SpanId) {
			t.Fatal("revision conflict incorrectly emitted an operational error log")
		}
		if bytes.Equal(l.TraceId, tool.TraceId) && bytes.Equal(l.SpanId, tool.SpanId) {
			correlated++
		}
		if bytes.Equal(l.TraceId, edit.TraceId) && bytes.Equal(l.SpanId, edit.SpanId) {
			correlated++
		}
	}
	var denialLogs int
	for _, l := range logs {
		if l.EventName == "shoowcase.http.access_denied" {
			denialLogs++
			status := attr(l.Attributes, "http.response.status_code").GetIntValue()
			if l.SeverityNumber != logpb.SeverityNumber_SEVERITY_NUMBER_WARN || (status != 401 && status != 403) {
				t.Error("denial log missing severity or status")
			}
		}
	}
	if denialLogs != 4 {
		t.Fatalf("expected four throttled denial logs for 12 requests, got %d", denialLogs)
	}
	if correlated != 2 {
		t.Fatalf("expected error logs correlated to both failures, got %d", correlated)
	}
	var deniedRequests uint64
	var toolFailure, toolConflict, httpFailure bool
	pool := map[string]float64{}
	for _, m := range metrics {
		for _, p := range m.GetSum().GetDataPoints() {
			if attr(p.Attributes, "db.client.connection.pool.name").GetStringValue() == "store" {
				if m.Name == "shoowcase.db.pool.wait.duration" {
					pool[m.Name] = p.GetAsDouble()
				} else {
					key := m.Name
					if state := attr(p.Attributes, "db.client.connection.state").GetStringValue(); state != "" {
						key += "/" + state
					}
					pool[key] = float64(p.GetAsInt())
				}
			}
		}
		for _, p := range m.GetHistogram().GetDataPoints() {
			status := attr(p.Attributes, "http.response.status_code").GetIntValue()
			if m.Name == "http.server.request.duration" && (status == 401 || status == 403) {
				deniedRequests += p.Count
			}

			if m.Name == "shoowcase.mcp.tool.duration" && attr(p.Attributes, "shoowcase.outcome").GetStringValue() == "conflict" && p.Count == 1 {
				toolConflict = true
			}
			if m.Name == "shoowcase.mcp.tool.duration" && attr(p.Attributes, "shoowcase.outcome").GetStringValue() == "internal" && p.Count == 1 {
				toolFailure = true
			}
			if m.Name == "http.server.request.duration" && attr(p.Attributes, "http.response.status_code").GetIntValue() == 503 && p.Count > 0 {
				httpFailure = true
			}
		}
	}
	if pool["db.client.connection.count/idle"] != 2 || pool["db.client.connection.count/used"] != 3 ||
		pool["db.client.connection.max"] != 16 || pool["shoowcase.db.pool.waits"] != 5 || pool["shoowcase.db.pool.wait.duration"] != .75 {
		t.Fatalf("unexpected exported database pool samples: %v", pool)
	}
	if deniedRequests != 12 {
		t.Fatalf("expected all 12 denied requests counted in metrics, got %d", deniedRequests)
	}
	if !toolFailure || !toolConflict || !httpFailure {
		t.Fatal("missing outcome or HTTP failure histogram")
	}
}

func TestConfigurationIsOptInAndRejectsUnsafeTransport(t *testing.T) {
	cleanEnv(t)
	rt, err := telemetry.New(t.Context(), "test", "http://localhost", nil)
	if err != nil {
		t.Fatal(err)
	}
	h := http.NewServeMux()
	if rt.Handler(h) != h {
		t.Error("enabled middleware without a collector")
	}
	for _, url := range []string{"http://collector.example", "https://user:pass@collector.example", "https://collector.example?q=private", "grpc://collector.example"} {
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", url)
		if _, err := telemetry.New(t.Context(), "test", "http://localhost", nil); err == nil || strings.Contains(err.Error(), url) {
			t.Fatal("expected sanitized error for invalid endpoint")
		}
	}
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://collector.example")
	t.Setenv("OTEL_EXPORTER_OTLP_INSECURE", "true")
	if _, err = telemetry.New(t.Context(), "test", "http://localhost", nil); err == nil {
		t.Fatal("allowed TLS downgrade")
	}
	t.Setenv("OTEL_SDK_DISABLED", "true")
	rt, err = telemetry.New(t.Context(), "test", "http://localhost", nil)
	if err != nil || rt.Handler(h) != h {
		t.Fatal("SDK kill switch did not disable export")
	}
}

func TestTrustedIngressAndSingleSignalOverTLS(t *testing.T) {
	cleanEnv(t)
	var batches atomic.Int32
	collector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/private/traces" || r.Header.Get("Authorization") != "Bearer collector-only" {
			t.Error("incorrect collector path or authentication")
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var req ctrace.ExportTraceServiceRequest
		if err = proto.Unmarshal(data, &req); err != nil {
			t.Error(err)
			return
		}
		if len(req.GetResourceSpans()) != 1 || bytes.Contains(data, []byte(marker)) {
			t.Error("unexpected trace payload")
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		batches.Add(1)
	}))
	defer collector.Close()
	cert := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: collector.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", collector.URL+"/private/traces")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_CERTIFICATE", cert)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS", "authorization=Bearer%20collector-only")
	t.Setenv("SHOOWCASE_OTEL_TRUST_TRACEPARENT", "true")
	rt, err := telemetry.New(t.Context(), "test", "https://control.example", nil)
	if err != nil {
		t.Fatal(err)
	}
	h := rt.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sc := trace.SpanContextFromContext(r.Context())
		if sc.TraceID().String() != parentID || sc.TraceState().Len() != 0 || baggage.FromContext(r.Context()).Len() != 0 {
			t.Error("did not isolate trusted parent from baggage and vendor tracestate")
		}
	}))
	r := httptest.NewRequest("GET", "https://control.example/readyz", nil)
	r.Header.Set("Traceparent", traceparent)
	r.Header.Set("Tracestate", "tenant="+marker)
	r.Header.Set("Baggage", "tenant="+marker)
	h.ServeHTTP(httptest.NewRecorder(), r)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err = rt.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if batches.Load() != 1 {
		t.Fatalf("expected one trace batch, got %d", batches.Load())
	}
}

func TestInvalidHeadersAndCAFailWithoutDisclosingValues(t *testing.T) {
	cleanEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "https://collector.example/v1/traces")
	for _, key := range []string{"HEADERS", "CERTIFICATE"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_"+key, marker)
			_, err := telemetry.New(t.Context(), "test", "https://control.example", nil)
			if err == nil || strings.Contains(err.Error(), marker) {
				t.Fatal("expected generic startup error")
			}
		})
	}
}
