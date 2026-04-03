# httptape

HTTP traffic recording, sanitization, and replay for Go.

httptape is an embeddable Go library that captures HTTP request/response pairs, sanitizes sensitive data on write, and replays them as a mock server. Think WireMock, but native Go, embeddable, and with sanitization built into the core.

## Why httptape?

**WireMock requires Java.** Separate process, 200 MB+ memory, can't embed in a Go binary.

**Go mocking libraries** (`gock`, `httpmock`) only work inside test code. No standalone server, no recording, no fixture management.

**Nobody does sanitization.** Existing tools record raw traffic including secrets and PII. Sanitization is always an afterthought. httptape sanitizes on write -- sensitive data never hits disk.

## Key features

- **Record** -- wrap any `http.RoundTripper` to capture real HTTP traffic
- **Sanitize on write** -- redact headers, body fields, or replace values with deterministic fakes before storage
- **Replay** -- serve recorded fixtures as a mock `http.Handler`
- **Composable matching** -- match requests by method, path, query params, headers, body hash, or fuzzy body fields
- **Pluggable storage** -- in-memory for tests, filesystem for fixtures, or implement your own `Store`
- **Import/export** -- share fixture bundles as `tar.gz` archives between environments
- **Zero dependencies** -- stdlib only, no transitive deps for embedders
- **CLI and Docker** -- use as a standalone proxy for recording and replay

## Install

```bash
go get github.com/VibeWarden/httptape
```

Requires Go 1.22 or later.

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

## Documentation

| Page | Description |
|------|-------------|
| [Getting Started](getting-started.md) | Record, replay, and sanitize in 5 minutes |
| [Recording](recording.md) | Recorder options, async/sync, sampling, body limits |
| [Replay](replay.md) | Server options, fallback behavior, request handling |
| [Sanitization](sanitization.md) | RedactHeaders, RedactBodyPaths, FakeFields, Pipeline |
| [Matching](matching.md) | All matchers, CompositeMatcher, score weights |
| [Storage](storage.md) | MemoryStore, FileStore, custom Store implementations |
| [Import/Export](import-export.md) | ExportBundle, ImportBundle, selective filters |
| [Config](config.md) | Declarative JSON configuration for sanitization |
| [CLI](cli.md) | serve, record, export, import commands |
| [Docker](docker.md) | Container usage, docker-compose, volumes |
| [Testcontainers](testcontainers.md) | Go Testcontainers module for integration tests |
| [API Reference](api-reference.md) | All exported types, functions, and options |

## License

[Apache 2.0](https://github.com/VibeWarden/httptape/blob/main/LICENSE)
