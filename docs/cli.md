# CLI Reference

httptape includes a standalone CLI binary for HTTP traffic recording, sanitization, and replay. It is a thin wrapper over the httptape library.

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

The server uses `DefaultMatcher` (method + path matching) and loads fixtures from the specified directory. It shuts down gracefully on SIGINT/SIGTERM.

**Example:**

```bash
httptape serve --fixtures ./testdata/fixtures --port 9090 --fallback-status 502
```

### record

Proxy requests to an upstream server, record and sanitize responses.

```bash
httptape record --upstream <url> --fixtures <dir> [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--upstream` | (required) | Upstream URL (e.g., `https://api.example.com`) |
| `--fixtures` | (required) | Path to fixture directory |
| `--config` | (none) | Path to sanitization config JSON |
| `--port` | `8081` | Listen port |

The recorder starts a reverse proxy on the specified port. All requests are forwarded to the upstream, and responses are recorded (with optional sanitization) to the fixtures directory.

The upstream URL must include the scheme and host (e.g., `https://api.example.com`).

**Example:**

```bash
httptape record \
  --upstream https://api.github.com \
  --fixtures ./fixtures \
  --config sanitize.json \
  --port 8081
```

Then point your application at `http://localhost:8081` instead of the real API. All traffic is recorded and sanitized.

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

The `serve` and `record` commands handle SIGINT and SIGTERM for graceful shutdown:

1. Stop accepting new connections
2. Wait up to 5 seconds for in-flight requests
3. In record mode, flush pending recordings
4. Exit

## Typical workflow

```bash
# 1. Record traffic from a real API
httptape record \
  --upstream https://api.example.com \
  --fixtures ./fixtures \
  --config sanitize.json

# 2. Export the fixtures
httptape export --fixtures ./fixtures --output fixtures.tar.gz

# 3. Import on another machine / in CI
httptape import --fixtures ./ci-fixtures --input fixtures.tar.gz

# 4. Serve the fixtures as a mock
httptape serve --fixtures ./ci-fixtures --port 8081
```

## See also

- [Config](config.md) -- sanitization config file format
- [Docker](docker.md) -- running httptape in containers
- [Import/Export](import-export.md) -- programmatic API
