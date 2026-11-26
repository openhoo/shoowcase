// Package telemetry exports bounded, content-free operational signals over OTLP/HTTP.
// Tenant audit records are written by the domain store independently of this package.
package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	mnoop "go.opentelemetry.io/otel/metric/noop"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	tnoop "go.opentelemetry.io/otel/trace/noop"
)

const scope = "github.com/openhoo/shoowcase"

type Runtime struct {
	controlHost            string
	trustParent            bool
	tracer                 trace.Tracer
	logger                 log.Logger
	active                 metric.Int64UpDownCounter
	duration, toolDuration metric.Float64Histogram
	stops                  []func(context.Context) error
	denials                [4]atomic.Int64 // last log second per fixed (surface, 401/403) bucket
}

// New configures only signals with an explicit endpoint. Configurations that
// request an unsupported exporter or could downgrade a remote connection fail startup.
func New(ctx context.Context, version, controlURL string, poolStats func() sql.DBStats) (_ *Runtime, err error) {
	rt := &Runtime{}
	disabled, err := boolEnv("OTEL_SDK_DISABLED")
	if err != nil || disabled {
		return rt, err
	}
	var enabled [3]bool
	for i, kind := range []string{"TRACES", "METRICS", "LOGS"} {
		if enabled[i], err = enabledSignal(kind); err != nil {
			return nil, err
		}
	}
	if !enabled[0] && !enabled[1] && !enabled[2] {
		return rt, nil
	}
	u, err := url.Parse(controlURL)
	if err != nil || u.Host == "" {
		return nil, errors.New("telemetry requires a valid control origin")
	}
	rt.controlHost = u.Host
	rt.trustParent, err = boolEnv("SHOOWCASE_OTEL_TRUST_TRACEPARENT")
	if err != nil {
		return nil, err
	}

	// SDK errors may include collector response bodies, transport URLs or
	// misconfigured header values. Emit a bounded diagnostic without their text.
	diag := &sdkErrors{}
	otel.SetErrorHandler(diag)
	otel.SetLogger(logr.New(diag))
	res, err := resource.New(ctx, resource.WithService(), resource.WithTelemetrySDK(),
		resource.WithAttributes(semconv.ServiceName("shoowcase"), semconv.ServiceVersion(version)),
		resource.WithFromEnv())
	if err != nil {
		return nil, errors.New("invalid OpenTelemetry resource configuration")
	}
	defer func() {
		if err != nil {
			d, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = rt.Shutdown(d)
		}
	}()
	rt.tracer = tnoop.NewTracerProvider().Tracer(scope)
	var meters metric.MeterProvider = mnoop.NewMeterProvider()
	if enabled[0] {
		exp, e := otlptracehttp.New(ctx)
		if e != nil {
			return nil, errors.New("cannot initialize OTLP trace exporter")
		}
		tp := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(exp))
		rt.tracer = tp.Tracer(scope)
		rt.stops = append(rt.stops, tp.Shutdown)
	}
	if enabled[1] {
		exp, e := otlpmetrichttp.New(ctx)
		if e != nil {
			return nil, errors.New("cannot initialize OTLP metric exporter")
		}
		mp := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp)))
		meters = mp
		rt.stops = append(rt.stops, mp.Shutdown)
	}
	if enabled[2] {
		exp, e := otlploghttp.New(ctx)
		if e != nil {
			return nil, errors.New("cannot initialize OTLP log exporter")
		}
		lp := sdklog.NewLoggerProvider(sdklog.WithResource(res), sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)))
		rt.logger = lp.Logger(scope)
		rt.stops = append(rt.stops, lp.Shutdown)
	}
	m := meters.Meter(scope)
	if enabled[1] && poolStats != nil {
		if err = observePool(m, poolStats); err != nil {
			return nil, err
		}
	}
	rt.active, err = m.Int64UpDownCounter("http.server.active_requests", metric.WithUnit("{request}"), metric.WithDescription("HTTP requests currently being served."))
	if err != nil {
		return nil, err
	}
	buckets := metric.WithExplicitBucketBoundaries(.005, .01, .025, .05, .075, .1, .25, .5, .75, 1, 2.5, 5, 7.5, 10)
	rt.duration, err = m.Float64Histogram("http.server.request.duration", metric.WithUnit("s"), metric.WithDescription("Time spent serving HTTP requests."), buckets)
	if err != nil {
		return nil, err
	}
	rt.toolDuration, err = m.Float64Histogram("shoowcase.mcp.tool.duration", metric.WithUnit("s"), metric.WithDescription("Duration and outcome of registered MCP tool calls."), buckets)
	if err == nil && diag.seen.Load() {
		return nil, errors.New("OpenTelemetry SDK rejected exporter or provider configuration")
	}
	return rt, err
}

func boolEnv(name string) (bool, error) {
	switch strings.ToLower(os.Getenv(name)) {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	default:
		return false, fmt.Errorf("%s must be true or false", name)
	}
}

func enabledSignal(kind string) (bool, error) {
	exp := os.Getenv("OTEL_" + kind + "_EXPORTER")
	switch exp {
	case "none":
		return false, nil
	case "", "otlp":
	default:
		return false, fmt.Errorf("OTEL_%s_EXPORTER must be otlp or none", kind)
	}
	global := "OTEL_EXPORTER_OTLP_"
	specific := global + kind + "_"
	value := func(key string) string {
		if v := os.Getenv(specific + key); v != "" {
			return v
		}
		return os.Getenv(global + key)
	}
	endpoint := value("ENDPOINT")
	if endpoint == "" {
		if exp == "otlp" {
			return false, fmt.Errorf("OTLP %s export requires a collector endpoint", kind)
		}
		return false, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		(u.Scheme != "https" && !(u.Scheme == "http" && (net.ParseIP(u.Hostname()).IsLoopback() || u.Hostname() == "localhost"))) {
		return false, fmt.Errorf("invalid OTLP %s endpoint: use HTTPS (HTTP only on loopback), without user info, query or fragment", kind)
	}
	switch strings.ToLower(value("INSECURE")) {
	case "", "false":
	case "true":
		if u.Scheme != "http" {
			return false, fmt.Errorf("refusing insecure OTLP %s transport for HTTPS endpoint", kind)
		}
	default:
		return false, fmt.Errorf("OTLP %s insecure setting must be true or false", kind)
	}
	for _, prefix := range []string{global, specific} {
		cert, key := os.Getenv(prefix+"CLIENT_CERTIFICATE"), os.Getenv(prefix+"CLIENT_KEY")
		if (cert == "") != (key == "") {
			return false, fmt.Errorf("OTLP %s requires both a client certificate and key for mTLS", kind)
		}
	}
	if kind == "TRACES" {
		switch strings.ToLower(os.Getenv("OTEL_TRACES_SAMPLER")) {
		case "", "always_on", "always_off", "parentbased_always_on", "parentbased_always_off":
		case "traceidratio", "parentbased_traceidratio":
			if raw := os.Getenv("OTEL_TRACES_SAMPLER_ARG"); raw != "" {
				f, e := strconv.ParseFloat(raw, 64)
				if e != nil || math.IsNaN(f) || f < 0 || f > 1 {
					return false, errors.New("OTEL_TRACES_SAMPLER_ARG must be a ratio between 0 and 1")
				}
			}
		default:
			return false, errors.New("unsupported OTEL_TRACES_SAMPLER")
		}
	}
	if protocol := value("PROTOCOL"); protocol != "" && protocol != "http/protobuf" {
		return false, fmt.Errorf("OTLP %s requires http/protobuf", kind)
	}
	return true, nil
}

// Shutdown flushes all signals concurrently against the caller's single deadline.
func (r *Runtime) Shutdown(ctx context.Context) error {
	var wg sync.WaitGroup
	var failed atomic.Bool
	for _, stop := range r.stops {
		wg.Go(func() {
			if stop(ctx) != nil {
				failed.Store(true)
			}
		})
	}
	wg.Wait()
	if failed.Load() {
		return errors.New("OpenTelemetry shutdown did not flush all signals")
	}
	return nil
}

type sdkErrors struct {
	last atomic.Int64
	seen atomic.Bool
}

// Some SDK components report invalid headers or certificate configuration via
// logr instead of ErrorHandler. Both paths share the same redaction and limit.
func (e *sdkErrors) Init(logr.RuntimeInfo)               {}
func (e *sdkErrors) Enabled(int) bool                    { return false }
func (e *sdkErrors) Info(int, string, ...any)            {}
func (e *sdkErrors) Error(err error, _ string, _ ...any) { e.Handle(err) }
func (e *sdkErrors) WithValues(...any) logr.LogSink      { return e }
func (e *sdkErrors) WithName(string) logr.LogSink        { return e }

func (e *sdkErrors) Handle(error) {
	e.seen.Store(true)
	now := time.Now().Unix()
	prev := e.last.Load()
	if now-prev >= 60 && e.last.CompareAndSwap(prev, now) {
		slog.Warn("OpenTelemetry SDK reported an error; check collector health, credentials and exporter configuration")
	}
}
