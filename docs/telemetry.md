# OpenTelemetry

Shoowcase exports HTTP and MCP traces, duration histograms and correlated operational logs to an OTLP/HTTP collector. No endpoint is configured by default. An unavailable collector does not block a request or change readiness. Workspace audit history is written separately in the database; it does not depend on a collector, sampling or batch delivery.

The legacy control-host `/metrics` scrape endpoint is independent of OTLP. It exposes four process counters. Production disables it unless `SHOOWCASE_METRICS_WORKSPACE` names the exact operator workspace ID; only an effective admin API token from that workspace may scrape it. Local `-dev` defaults to its own workspace.

The [illustrated Grafana guide](grafana.md) shows how to graph the OTLP metrics for HTTP requests, MCP calls and database pools. It includes a disposable local stack and instructions for an existing Grafana installation.

## Connect a collector

Configure a collector with traces, metrics and logs pipelines listening for OTLP/HTTP. Set these variables on each Shoowcase replica and restart it:

```sh
OTEL_EXPORTER_OTLP_ENDPOINT=https://collector.example.org:4318
OTEL_SERVICE_NAME=shoowcase
OTEL_RESOURCE_ATTRIBUTES=deployment.environment.name=production
OTEL_TRACES_SAMPLER=parentbased_traceidratio
OTEL_TRACES_SAMPLER_ARG=0.10
```

The common endpoint is a base URL; the SDK appends `/v1/traces`, `/v1/metrics` and `/v1/logs`. To export just one signal, set only its full URL, for example `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=https://collector.example.org:4318/v1/traces`. Alternatively, configure a common endpoint and set unwanted signals to `none` with `OTEL_{TRACES,METRICS,LOGS}_EXPORTER`. The supported wire protocol is `http/protobuf`. Set `OTEL_SDK_DISABLED=true` to disable the integration entirely. The sample Compose deployment passes through the common settings; use a Compose override to add signal-specific settings or certificate mounts.

Use HTTPS for collectors reached over a network. HTTP is accepted only for a loopback collector, such as a sidecar that shares the pod network namespace. Supply collector authentication using `OTEL_EXPORTER_OTLP_HEADERS` through your secret manager. A private issuer CA can be mounted read-only and selected with `OTEL_EXPORTER_OTLP_CERTIFICATE`. For mutual TLS provide both `OTEL_EXPORTER_OTLP_CLIENT_CERTIFICATE` and `OTEL_EXPORTER_OTLP_CLIENT_KEY`. Collector endpoints must contain no user info, query or fragment.

All three signals carry the service name, application version, SDK identity and a generated service instance ID for the running process. Use resource attributes for deployment metadata only. Every configured attribute is sent to the collector.

## What reaches the collector

| Signal | Fields and interpretation |
| --- | --- |
| HTTP server spans | Method, fixed route template when known, control/content surface, final HTTP status. 5xx and interrupted requests are marked as errors. An interrupted handler has no fabricated HTTP status. |
| `http.server.request.duration` | Histogram in seconds, with the same bounded attributes. Its count is the request count. Control-host `/readyz` and `/healthz` can be filtered separately. |
| `http.server.active_requests` | In-flight requests by method and surface. |
| Database pool metrics | `db.client.connection.count` (used and idle) and `db.client.connection.max` show pool occupancy. `shoowcase.db.pool.waits` and `shoowcase.db.pool.wait.duration` are cumulative counts and seconds spent waiting for connections. Monitor their rates per replica to detect contention. All use the fixed pool name `store`; collecting them does not query the database. |
| MCP child spans and `shoowcase.mcp.tool.duration` | Registered tool name, duration and bounded outcome. Use the histogram count by outcome to distinguish a denied operation, invalid arguments, approval requests and internal failures even when the HTTP response is 200. |
| OTLP logs | HTTP 5xx, interrupted requests, internal tool failures, and warnings for HTTP 401/403. Each event includes its operation and outcome and, when available, the IDs of the active span and trace. General process messages still go to stderr. |

The application does not attach tenant IDs, subjects, subdomains, request or response bodies, input arguments, credentials, error messages, IP addresses, raw paths, queries, headers or stack traces. Input paths become templates (for example `/sites/{site}/edit`); unknown control routes omit the route and content paths share one template. The telemetry route classifier is deliberately closed: add an explicit template when exposing new control routes.

Requests start their own traces by default. To join upstream traces, first configure the only reachable ingress to remove client tracing headers and inject its own trusted W3C `traceparent`. Then set `SHOOWCASE_OTEL_TRUST_TRACEPARENT=true`. Baggage and tracestate are never extracted. Trusted parents can influence parent-based sampling.

HTTP denial warnings contain no identity. They are rate limited to one warning per minute for each combination of status (401, 403) and surface (control, content) on each replica. Metrics still count every denial. Use the durable tenant audit feed for attributable administrative and authorization events; an operational warning alone does not identify a user or tenant.

## Verify and operate

1. Start with all traces sampled in a test environment (`OTEL_TRACES_SAMPLER=always_on`). Request `/readyz`; find a server span named `GET /readyz` for the deployed service version. Make a registered MCP tool call and find its child span.
2. Wait one metric export interval (default 60 s) and check the request-duration histogram. A failed request in a test environment produces an OTLP log that links to its trace. The automated integration test sends synthetic MCP failures to a local collector and checks that a tenant canary is absent from all three wire payloads.
3. Choose production sampling and capacity according to traffic. Sampling affects traces; metrics and operational error logs are independent. Monitor service latency, 5xx ratio, MCP internal errors and the collector's queue/refusal/drop metrics. The SDK logs a rate-limited generic warning to stderr on export problems; collector logs provide the transport-side diagnosis.
4. Traces and logs use bounded asynchronous batches. On shutdown, HTTP drains before the providers flush against a shared time limit. Export can lose data during sustained outages, overload or forced termination. Use the tenant audit feed for access reviews and the collector's own retention and access controls for operational data.
