# httptape

**Record, Redact, Replay** -- HTTP traffic recording, redaction, and replay.

httptape captures HTTP request/response pairs, redacts sensitive data on write, and replays them as a mock server. It ships as an embeddable Go library, a standalone CLI, and a minimal Docker image.

## The 3 Rs

1. **Record** -- wrap any `http.RoundTripper` to capture real HTTP traffic, or use the CLI/Docker to record from an upstream API
2. **Redact** -- strip secrets, PII, and credentials on write via a configurable redaction pipeline, before anything touches disk
3. **Replay** -- serve recorded fixtures as a deterministic mock `http.Handler`, or run a standalone mock server via CLI/Docker

## Why httptape?

**WireMock requires Java.** Separate process, 200 MB+ memory, can't embed in a Go binary.

**Go mocking libraries** (`gock`, `httpmock`) only work inside test code. No standalone server, no recording, no fixture management.

**Nobody does redaction.** Existing tools record raw traffic including secrets and PII. Redaction is always an afterthought. httptape redacts on write -- sensitive data never hits disk.

## Key features

- **Record** -- wrap any `http.RoundTripper` to capture real HTTP traffic
- **Redact on write** -- strip headers, body fields, or replace values with deterministic fakes before storage
- **Replay** -- serve recorded fixtures as a mock `http.Handler`
- **Proxy with fallback** -- forward to upstream with automatic fallback to cached responses when the backend is down
- **Composable matching** -- match requests by method, path, query params, headers, body hash, or fuzzy body fields
- **Pluggable storage** -- in-memory for tests, filesystem for fixtures, or implement your own `Store`
- **Import/export** -- share fixture bundles as `tar.gz` archives between environments
- **Zero dependencies** -- stdlib only, no transitive deps for embedders
- **CLI and Docker** -- use as a standalone proxy for recording, replaying, and caching

## Install

=== "Go library"

    ```bash
    go get github.com/VibeWarden/httptape
    ```

=== "CLI"

    ```bash
    go install github.com/VibeWarden/httptape/cmd/httptape@latest
    ```

=== "Docker"

    ```bash
    docker pull ghcr.io/vibewarden/httptape:latest
    ```

Requires Go 1.22 or later for the library/CLI. Docker works with any platform.

## Quick example

```go
package main

import (
    "fmt"
    "net/http"
    "net/http/httptest"

    "github.com/VibeWarden/httptape"
)

func main() {
    store := httptape.NewMemoryStore()

    // Record
    rec := httptape.NewRecorder(store, httptape.WithRoute("github"))
    client := &http.Client{Transport: rec}
    client.Get("https://api.github.com/users/octocat")
    rec.Close()

    // Replay
    srv := httptape.NewServer(store)
    ts := httptest.NewServer(srv)
    defer ts.Close()

    resp, _ := http.Get(ts.URL + "/users/octocat")
    fmt.Println(resp.StatusCode) // 200
}
```

Or using the CLI:

```bash
# Record from a real API (with redaction)
httptape record --upstream https://api.github.com --fixtures ./mocks --config redact.json

# Replay as a mock server
httptape serve --fixtures ./mocks --port 8081

# Proxy with automatic fallback
httptape proxy --upstream https://api.github.com --fixtures ./cache
```

## Documentation

| Page | Description |
|------|-------------|
| [Getting Started](getting-started.md) | Record, redact, and replay in 5 minutes |
| [Recording](recording.md) | Recorder options, async/sync, sampling, body limits |
| [Replay](replay.md) | Server options, fallback behavior, request handling |
| [Redaction](sanitization.md) | RedactHeaders, RedactBodyPaths, FakeFields, Pipeline |
| [Proxy Mode](proxy.md) | L1/L2 caching, fallback behavior, frontend dev |
| [Matching](matching.md) | All matchers, CompositeMatcher, score weights |
| [Storage](storage.md) | MemoryStore, FileStore, custom Store implementations |
| [Import/Export](import-export.md) | ExportBundle, ImportBundle, selective filters |
| [Config](config.md) | Declarative JSON configuration for redaction |
| [CLI](cli.md) | serve, record, proxy, export, import commands |
| [Docker](docker.md) | Container usage, docker-compose, volumes |
| [Testcontainers](testcontainers.md) | Go Testcontainers module for integration tests |
| [API Reference](api-reference.md) | All exported types, functions, and options |

## License

[Apache 2.0](https://github.com/VibeWarden/httptape/blob/main/LICENSE)
