package telemetry

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/felixge/httpsnoop"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

type runtimeKey struct{}

// route returns fixed templates only. Hostnames, file paths and even unknown
// HTTP methods may contain tenant data or arbitrary high-cardinality values.
func (rt *Runtime) route(r *http.Request) (route, surface string) {
	control := strings.EqualFold(r.Host, rt.controlHost)
	surface = "control"
	if !control {
		surface = "content"
	}
	p := r.URL.Path
	switch p {
	case "/auth/login", "/auth/callback", "/auth/finish", "/auth/logout":
		return p, surface
	}
	if !control {
		return "/{path...}", surface
	}
	switch p {
	case "/", "/quickstart", "/quickstart/redeem", "/quickstart/install.sh", "/quickstart/install.ps1", "/keys", "/admin", "/account", "/mcp", "/approve", "/upload", "/sites",
		"/api/audit", "/metrics", "/healthz", "/readyz", "/ui.css", "/console.js",
		"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp":
		return p, surface
	}
	if suffix, ok := strings.CutPrefix(p, "/sites/"); ok {
		_, op, _ := strings.Cut(suffix, "/")
		switch op {
		case "":
			return "/sites/{site}", surface
		case "edit", "delete", "upload", "import", "publish", "access", "rollback":
			return "/sites/{site}/" + op, surface
		}
	}
	return "", surface
}

func methodName(method string) string {
	switch method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE":
		return method
	default:
		return "_OTHER"
	}
}

// Handler preserves optional writer interfaces used by streaming MCP. Nothing
// derived from headers (except explicitly trusted traceparent), bodies, error
// strings, queries, host labels or dynamic URL segments is recorded.
func (rt *Runtime) Handler(next http.Handler) http.Handler {
	if len(rt.stops) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if rt.trustParent {
			ctx = propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{"traceparent": r.Header.Get("Traceparent")})
		}
		route, surface := rt.route(r)
		method := methodName(r.Method)
		name := method
		if name == "_OTHER" {
			name = "HTTP"
		}
		attrs := []attribute.KeyValue{semconv.HTTPRequestMethodKey.String(method), attribute.String("shoowcase.surface", surface)}
		if route != "" {
			name += " " + route
			attrs = append(attrs, semconv.HTTPRoute(route))
		}
		ctx, span := rt.tracer.Start(ctx, name, trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(attrs...))
		ctx = context.WithValue(ctx, runtimeKey{}, rt)
		active := metric.WithAttributes(attrs[0], attrs[1])
		rt.active.Add(ctx, 1, active)
		captured := httpsnoop.Metrics{Code: http.StatusOK}
		complete := false
		defer func() {
			// Do not recover/rethrow a panic: net/http remains responsible for
			// closing the connection, and the panic value is never exported.
			rt.active.Add(ctx, -1, active)
			outcome := ""
			if !complete {
				outcome = "interrupted"
			} else {
				attrs = append(attrs, semconv.HTTPResponseStatusCode(captured.Code))
				if captured.Code >= 500 {
					outcome = strconv.Itoa(captured.Code)
				}
			}
			if outcome != "" {
				attrs = append(attrs, semconv.ErrorTypeKey.String(outcome))
				span.SetStatus(codes.Error, "")
				rt.eventLog(ctx, log.SeverityError, "shoowcase.http.server.error", attrs)
			} else if complete && (captured.Code == http.StatusUnauthorized || captured.Code == http.StatusForbidden) && rt.admitDenial(surface, captured.Code) {
				rt.eventLog(ctx, log.SeverityWarn, "shoowcase.http.access_denied", attrs)
			}
			span.SetAttributes(attrs...)
			rt.duration.Record(ctx, captured.Duration.Seconds(), metric.WithAttributes(attrs...))
			span.End()
		}()
		captured.CaptureMetrics(w, func(w http.ResponseWriter) { next.ServeHTTP(w, r.WithContext(ctx)) })
		complete = true
	})
}

// Up to four warning records per minute per replica, with constant state
// independent of the number of callers, IPs and workspaces. Metrics count all.
func (rt *Runtime) admitDenial(surface string, status int) bool {
	if rt.logger == nil {
		return false
	}
	bucket := 0
	if surface == "content" {
		bucket += 2
	}
	if status == http.StatusForbidden {
		bucket++
	}
	last := &rt.denials[bucket]
	now := time.Now().Unix()
	prev := last.Load()
	return now-prev >= 60 && last.CompareAndSwap(prev, now)
}

func (rt *Runtime) eventLog(ctx context.Context, severity log.Severity, event string, attrs []attribute.KeyValue) {
	if rt.logger == nil {
		return
	}
	var rec log.Record
	rec.SetTimestamp(time.Now())
	rec.SetSeverity(severity)
	rec.SetEventName(event)
	rec.SetBody(attribute.StringValue(event))
	rec.AddAttributes(attrs...)
	rt.logger.Emit(ctx, rec)
}

// StartTool accepts a tool name from the server's registered definitions only.
// Callers supply a bounded outcome, never an error message or MCP arguments.
func StartTool(ctx context.Context, registeredName string) (context.Context, func(outcome string)) {
	rt, ok := ctx.Value(runtimeKey{}).(*Runtime)
	if !ok {
		return ctx, func(string) {}
	}
	name := attribute.String("gen_ai.tool.name", registeredName)
	ctx, span := rt.tracer.Start(ctx, "tools/call "+registeredName, trace.WithAttributes(name))
	start := time.Now()
	return ctx, func(outcome string) {
		switch outcome {
		case "ok", "invalid_argument", "unauthorized", "forbidden", "not_found", "conflict", "rate_limited", "quota_exceeded", "approval_required":
		default:
			outcome = "internal"
		}
		attrs := []attribute.KeyValue{name, attribute.String("shoowcase.outcome", outcome)}
		if outcome != "ok" {
			attrs = append(attrs, semconv.ErrorTypeKey.String(outcome))
			span.SetStatus(codes.Error, "")
			if outcome == "internal" {
				rt.eventLog(ctx, log.SeverityError, "shoowcase.mcp.tool.error", attrs)
			}
		}
		span.SetAttributes(attrs...)
		rt.toolDuration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attrs...))
		span.End()
	}
}
