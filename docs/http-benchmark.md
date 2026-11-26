# Authenticated HTTP delivery benchmark

On November 26, 2026, a local development build served one 32 KiB workspace page at **13,040 full responses/second**. Four clients sent 5,000 requests per case using persistent HTTP/1.1 connections. Both client and server ran on the same Apple M4 Max, with macOS 26.6.2, Go 1.27.4, Python 3.15.2 and a temporary SQLite database.

| Response | Requests | Wall time | Requests/s | p50 | p95 |
| --- | ---: | ---: | ---: | ---: | ---: |
| Authenticated GET, 200 with 32,768 bytes | 5,000 | 0.383 s | 13,040 | 0.264 ms | 0.508 ms |
| Authenticated GET with matching ETag, 304 and no body | 5,000 | 0.310 s | 16,150 | 0.230 ms | 0.406 ms |

## Repeat the run

From the repository root:

```sh
make build
python3 scripts/bench-http.py --binary ./bin/shoowcase --requests 5000 --concurrency 4
```

The script starts the binary on loopback with a new data directory, creates a page through MCP, publishes it with workspace access, checks that an anonymous request redirects to sign-in, and follows the development sign-in flow. Each worker uses the issued site session cookie and warms its connection with 15 untimed requests. Workers start measurement together. The script checks the status, ETag and body for every request and reports the nearest-rank median and 95th percentile from sending the request through reading its complete response. The run measured here used development binary SHA-256 `dccf3d51299357ea1167b74e30c098f4fb3011c31ee98e1b34a693fe0aa8d62d`.

The conditional case sends `If-None-Match` explicitly to exercise the server path; the normal delivery policy is `private, no-store`. These warm, single-page numbers include Python client overhead and exclude TLS, network latency, reverse proxies, production PostgreSQL, cold data, writes and external identity-provider sign-in. They provide a local regression check, not a production capacity estimate or service commitment.
