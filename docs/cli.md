# CLI Reference

httptape includes a standalone CLI binary for HTTP traffic recording, redaction, and replay. It is a thin wrapper over the httptape library and works with any language or framework via HTTP.

## Install

```bash
go install github.com/VibeWarden/httptape/cmd/httptape@latest
```

Or use the [Docker image](docker.md).

## Commands

### serve

Replay recorded fixtures as a mock HTTP server.

```bash
httptape serve --fixtures ./fixtures [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--fixtures` | (required) | Path to fixture directory |
| `--port` | `8081` | Listen port |
| `--fallback-status` | `404` | HTTP status when no tape matches |
| `--cors` | `false` | Enable CORS headers (Access-Control-Allow-Origin: *) |
| `--delay` | `0` | Fixed delay before every response (e.g., `200ms`, `1s`) |
| `--error-rate` | `0` | Fraction of requests that return 500 (0.0-1.0) |
| `--replay-header` | (none) | Header to inject into responses (`Key=Value`, repeatable) |
| `--sse-timing` | (none) | SSE replay timing mode: `realtime`, `instant`, `accelerated=<factor>`. When unset, the library default (`realtime`) is used. |
| `--config` | (none) | Path to redaction config JSON. Accepted but not currently used by `serve` (reserved for future use). |

The server uses `DefaultMatcher` (method + path matching) and loads fixtures from the specified directory. It shuts down gracefully on SIGINT/SIGTERM.

**Example:**

```bash
httptape serve --fixtures ./testdata/fixtures --port 9090 --fallback-status 502
```

### record

Proxy requests to an upstream server, record and redact responses.

```bash
httptape record --upstream <url> --fixtures <dir> [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--upstream` | (required) | Upstream URL (e.g., `https://api.example.com`) |
| `--fixtures` | (required) | Path to fixture directory |
| `--config` | (none) | Path to redaction config JSON |
| `--port` | `8081` | Listen port |
| `--cors` | `false` | Enable CORS headers |
| `--tls-cert` | (none) | Path to PEM client certificate for mTLS. See [TLS](tls.md). |
| `--tls-key` | (none) | Path to PEM client private key for mTLS. See [TLS](tls.md). |
| `--tls-ca` | (none) | Path to PEM CA certificate(s) for upstream verification. See [TLS](tls.md). |
| `--tls-insecure` | `false` | Skip TLS verification (development only). See [TLS](tls.md). |

The recorder starts a reverse proxy on the specified port. All requests are forwarded to the upstream, and responses are recorded (with optional redaction) to the fixtures directory.

The upstream URL must include the scheme and host (e.g., `https://api.example.com`).

**Example:**

```bash
httptape record \
  --upstream https://api.github.com \
  --fixtures ./fixtures \
  --config redact.json \
  --port 8081
```

Then point your application at `http://localhost:8081` instead of the real API. All traffic is recorded and redacted.

### proxy

Forward requests to an upstream server with two-tier caching and automatic fallback. See [Proxy Mode](proxy.md) for a full guide.

```bash
httptape proxy --upstream <url> --fixtures <dir> [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--upstream` | (required) | Upstream URL (e.g., `https://api.example.com`) |
| `--fixtures` | (required) | Path to fixture directory for L2 (persistent) cache |
| `--config` | (none) | Path to redaction config JSON (applied to L2 writes only) |
| `--port` | `8081` | Listen port |
| `--cors` | `false` | Enable CORS headers |
| `--fallback-on-5xx` | `false` | Also fall back on 5xx responses from upstream |
| `--sse-timing` | (none) | SSE replay timing mode for L2 fallback: `realtime`, `instant`, `accelerated=<factor>`. When unset, the library default (`instant`) is used. |
| `--tls-cert` | (none) | Path to PEM client certificate for mTLS. See [TLS](tls.md). |
| `--tls-key` | (none) | Path to PEM client private key for mTLS. See [TLS](tls.md). |
| `--tls-ca` | (none) | Path to PEM CA certificate(s) for upstream verification. See [TLS](tls.md). |
| `--tls-insecure` | `false` | Skip TLS verification (development only). See [TLS](tls.md). |

When the upstream is reachable, requests are forwarded and responses are cached:

- **L1 (memory):** raw, unsanitized responses for best within-session fidelity
- **L2 (disk):** redacted responses that persist across restarts

When the upstream is unreachable (or returns 5xx with `--fallback-on-5xx`), the proxy serves cached responses. The `X-Httptape-Source` header indicates whether a response came from `l1-cache`, `l2-cache`, or the real upstream (header absent).

**Example:**

```bash
httptape proxy \
  --upstream https://api.staging.example.com \
  --fixtures ./cache \
  --config redact.json \
  --port 3001 \
  --cors \
  --fallback-on-5xx
```

### export

Export fixtures to a `tar.gz` bundle.

```bash
httptape export --fixtures <dir> [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--fixtures` | (required) | Path to fixture directory |
| `--output` | stdout | Output file path |
| `--routes` | (none) | Comma-separated route filter |
| `--methods` | (none) | Comma-separated HTTP method filter |
| `--since` | (none) | RFC 3339 timestamp filter |

**Examples:**

```bash
# Export all fixtures to a file
httptape export --fixtures ./fixtures --output bundle.tar.gz

# Export only GET requests to the users-api route
httptape export --fixtures ./fixtures --output users.tar.gz \
  --routes users-api --methods GET

# Export fixtures recorded after a specific date
httptape export --fixtures ./fixtures --output recent.tar.gz \
  --since 2024-01-01T00:00:00Z

# Export to stdout (pipe to another tool)
httptape export --fixtures ./fixtures | gzip -d | tar -tf -
```

### import

Import fixtures from a `tar.gz` bundle.

```bash
httptape import --fixtures <dir> [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--fixtures` | (required) | Path to fixture directory |
| `--input` | stdin | Input file path |

Existing fixtures with the same ID are overwritten. Fixtures not in the bundle are left untouched.

**Examples:**

```bash
# Import from a file
httptape import --fixtures ./fixtures --input bundle.tar.gz

# Import from stdin
cat bundle.tar.gz | httptape import --fixtures ./fixtures
```

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Usage error (bad flags, missing required args) |
| 2 | Runtime error (server failure, store error) |

## Signal handling

The `serve`, `record`, and `proxy` commands handle SIGINT and SIGTERM for graceful shutdown:

1. Stop accepting new connections
2. Wait up to 5 seconds for in-flight requests
3. In record mode, flush pending recordings
4. Exit

## Typical workflow

```bash
# 1. Record traffic from a real API (with redaction)
httptape record \
  --upstream https://api.example.com \
  --fixtures ./fixtures \
  --config redact.json

# 2. Export the fixtures
httptape export --fixtures ./fixtures --output fixtures.tar.gz

# 3. Import on another machine / in CI
httptape import --fixtures ./ci-fixtures --input fixtures.tar.gz

# 4. Serve the fixtures as a mock
httptape serve --fixtures ./ci-fixtures --port 8081
```

## See also

- [Proxy Mode](proxy.md) -- full guide to proxy mode with L1/L2 caching
- [Config](config.md) -- redaction config file format
- [Docker](docker.md) -- running httptape in containers
- [Import/Export](import-export.md) -- programmatic API
