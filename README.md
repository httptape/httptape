<p align="center">
  <img src="https://raw.githubusercontent.com/VibeWarden/httptape/main/assets/logo.png" alt="httptape logo" width="300">
</p>

<h3 align="center">Record, Redact, Replay</h3>

<p align="center">
  HTTP traffic recording, redaction, and replay.<br>
  <strong>Embeddable Go library · CLI · Docker · Testcontainers</strong>
</p>

<p align="center">
  <a href="https://pkg.go.dev/github.com/VibeWarden/httptape"><img src="https://pkg.go.dev/badge/github.com/VibeWarden/httptape.svg" alt="Go Reference"></a>
  <a href="https://github.com/VibeWarden/httptape/actions/workflows/test.yml"><img src="https://github.com/VibeWarden/httptape/actions/workflows/test.yml/badge.svg?branch=main" alt="Tests"></a>
  <a href="https://scorecard.dev/viewer/?uri=github.com/VibeWarden/httptape"><img src="https://api.scorecard.dev/projects/github.com/VibeWarden/httptape/badge" alt="OpenSSF Scorecard"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-blue.svg" alt="License"></a>
  <a href="https://hub.docker.com/r/tibtof/httptape"><img src="https://img.shields.io/docker/image-size/tibtof/httptape/latest?label=docker" alt="Docker Image Size"></a>
</p>

---

httptape captures HTTP request/response pairs, redacts sensitive data on write,
and replays them as a mock server. Think WireMock, but with a 3 MB Docker image,
an embeddable Go library, and a redaction pipeline built into the core.

**The 3 Rs:**

1. **Record** -- capture real HTTP traffic via a transparent `http.RoundTripper`
2. **Redact** -- strip secrets and PII on write, before anything touches disk
3. **Replay** -- serve recorded fixtures as a deterministic mock server

## Why httptape?

- **WireMock requires Java** -- separate process, 200 MB+ memory, can't embed in a Go binary
- **Go mocking libraries** (`gock`, `httpmock`) only work inside test code -- no standalone server, no recording, no fixture management
- **json-server / Mockoon** -- no recording, no redaction, manual fixture writing only
- **Nobody does redaction** -- existing tools record raw traffic including secrets and PII. httptape redacts on write -- sensitive data never hits disk

## Use cases

### Integration testing
Record real API interactions once, replay forever. Deterministic CI without live API credentials.

```go
store := httptape.NewMemoryStore()
rec := httptape.NewRecorder(store, httptape.WithSanitizer(sanitizer))
defer rec.Close()

client := &http.Client{Transport: rec}
// ... hit real APIs, fixtures are recorded and redacted ...

srv := httptape.NewServer(store)
ts := httptest.NewServer(srv)
// ... replay against ts.URL in your tests ...
```

### Frontend-first development
Use httptape as a mock backend while building your UI -- no real backend needed.

```bash
# Hand-write fixtures or record from a staging API
httptape record --upstream https://staging-api.example.com \
    --fixtures ./mocks --config redact.json

# Serve as a mock backend for your frontend
httptape serve --fixtures ./mocks --port 3001
```

Your frontend on `localhost:3000` hits httptape on `localhost:3001`. Edit JSON fixture files, and the next request picks up the changes -- instant hot-reload.

### Production traffic capture
Record a sample of live traffic, safely redacted:

```bash
docker run -v ./fixtures:/fixtures -v ./config.json:/config/config.json \
    tibtof/httptape record \
    --upstream https://api.internal:8080 \
    --fixtures /fixtures --config /config/config.json
```

Sensitive data (secrets, PII) is redacted before it touches disk. Export redacted fixtures for dev/CI use.

### Fallback proxy
Use proxy mode for frontend development with automatic fallback to cached responses when the backend is unavailable:

```bash
httptape proxy --upstream https://api.example.com \
    --fixtures ./cache --config redact.json
```

When the upstream is reachable, requests are forwarded and responses are cached in two tiers (L1 in-memory, L2 on disk). When the upstream is down, httptape transparently serves cached responses. See [Proxy Mode](docs/proxy.md) for details.

## Install

**Go library:**
```bash
go get github.com/VibeWarden/httptape
```

**CLI:**
```bash
go install github.com/VibeWarden/httptape/cmd/httptape@latest
```

**Docker** (~3 MB, multi-arch):
```bash
docker pull tibtof/httptape
```

## Quick start

### Record

```go
store := httptape.NewMemoryStore()
rec := httptape.NewRecorder(store, httptape.WithRoute("github-api"))
defer rec.Close()

client := &http.Client{Transport: rec}
resp, err := client.Get("https://api.github.com/users/octocat")
// Tape is automatically saved to store
```

### Redact

Strip secrets and fake PII -- on write, before anything hits disk:

```go
sanitizer := httptape.NewPipeline(
    httptape.RedactHeaders("Authorization", "Cookie"),
    httptape.RedactBodyPaths("$.card.number", "$.ssn"),
    httptape.FakeFields("my-seed", "$.email", "$.user_id"),
)
rec := httptape.NewRecorder(store, httptape.WithSanitizer(sanitizer))
```

Or declaratively via JSON config:

```json
{
  "version": "1",
  "rules": [
    {"action": "redact_headers", "headers": ["Authorization", "Cookie"]},
    {"action": "redact_body", "paths": ["$.card.number", "$.ssn"]},
    {"action": "fake", "seed": "my-seed", "paths": ["$.email", "$.user_id"]}
  ]
}
```

### Replay

```go
srv := httptape.NewServer(store)
ts := httptest.NewServer(srv)
defer ts.Close()

resp, err := http.Get(ts.URL + "/users/octocat")
```

### Match

Composable matching with weighted scoring:

```go
srv := httptape.NewServer(store,
    httptape.WithMatcher(httptape.NewCompositeMatcher(
        httptape.MatchMethod(),      // score: 1
        httptape.MatchPath(),        // score: 2
        httptape.MatchHeaders("Accept", "application/json"), // score: 3
        httptape.MatchQueryParams(), // score: 4
        httptape.MatchBodyHash(),    // score: 8
    )),
)
```

### Store

```go
// In-memory (for tests)
mem := httptape.NewMemoryStore()

// Filesystem (for fixtures)
fs := httptape.NewFileStore(httptape.WithDirectory("./testdata/fixtures"))
```

### Proxy (fallback-to-cache)

```go
l1 := httptape.NewMemoryStore()
l2, _ := httptape.NewFileStore(httptape.WithDirectory("./cache"))

proxy := httptape.NewProxy(l1, l2,
    httptape.WithProxySanitizer(sanitizer),
)
client := &http.Client{Transport: proxy}
// Upstream reachable: real response returned, cached in L1 + L2
// Upstream down: cached response returned transparently
```

### Proxy health endpoints (opt-in)

The proxy can expose a small technical surface for operators and downstream UIs to react to upstream state changes in real time. Off by default; opt in with `--health-endpoint` (CLI) or `WithProxyHealthEndpoint()` (library):

```bash
httptape proxy --upstream https://api.example.com --fixtures ./cache \
    --health-endpoint --upstream-probe-interval 2s
```

```bash
# JSON snapshot
curl http://localhost:8081/__httptape/health
# {"state":"live","upstream_url":"https://api.example.com","probe_interval_ms":2000,"since":"2026-04-16T10:00:00Z","last_probed_at":"2026-04-16T10:00:02Z"}

# SSE stream — one event on connect, one per state transition
curl -N http://localhost:8081/__httptape/health/stream
# retry: 2000
#
# data: {"state":"live", ... }
#
# data: {"state":"l1-cache", ... }
```

`state` mirrors the existing `X-Httptape-Source` header (`live`, `l1-cache`, `l2-cache`). With both flags absent, no endpoints are mounted and no probe goroutine is started — behavior is byte-for-byte unchanged.

### Import / Export

```go
// Export redacted fixtures as a portable bundle
r, _ := httptape.ExportBundle(ctx, store,
    httptape.WithRoutes("stripe-api"),
    httptape.WithSince(time.Now().Add(-24*time.Hour)),
)

// Import on another machine
httptape.ImportBundle(ctx, store, r)
```

## CLI

```bash
httptape serve   --fixtures ./mocks --port 8081
httptape record  --upstream https://api.example.com --fixtures ./mocks --config redact.json
httptape proxy   --upstream https://api.example.com --fixtures ./cache --config redact.json
httptape export  --fixtures ./mocks --output bundle.tar.gz
httptape import  --fixtures ./mocks --input bundle.tar.gz
```

## Docker

Pull from either registry — the same image is published to both on every
release:

```bash
docker pull tibtof/httptape           # Docker Hub
docker pull ghcr.io/vibewarden/httptape   # GHCR
```

Examples below use `tibtof/httptape`; substitute `ghcr.io/vibewarden/httptape`
freely.

```bash
# Replay mode
docker run -v ./mocks:/fixtures -p 8081:8081 tibtof/httptape serve --fixtures /fixtures

# Record mode (with redaction)
docker run -v ./mocks:/fixtures -v ./config.json:/config/config.json -p 8081:8081 \
    tibtof/httptape record --upstream https://api.example.com \
    --fixtures /fixtures --config /config/config.json

# Proxy mode (with fallback-to-cache)
docker run -v ./cache:/fixtures -v ./config.json:/config/config.json -p 8081:8081 \
    tibtof/httptape proxy --upstream https://api.example.com \
    --fixtures /fixtures --config /config/config.json
```

## Testcontainers

```go
import httptapetest "github.com/VibeWarden/httptape/testcontainers"

container, err := httptapetest.RunContainer(ctx,
    httptapetest.WithFixturesDir("./testdata/fixtures"),
)
defer container.Terminate(ctx)

// container.BaseURL() returns the mock server URL
resp, _ := http.Get(container.BaseURL() + "/api/users")
```

## How it compares

| Feature | httptape | WireMock | json-server | MSW | gock |
|---|---|---|---|---|---|
| Embeddable in Go | **yes** | no (Java) | no (Node) | no (browser) | yes |
| Standalone server | **yes** | yes | yes | no | no |
| Docker | **3 MB** | 200 MB+ | 50 MB+ | n/a | n/a |
| Recording | **yes** | yes | no | no | no |
| Redaction on write | **yes** | no | no | no | no |
| Deterministic faking | **yes** | no | no | no | no |
| Proxy with fallback | **yes** | no | no | no | no |
| Frontend mock backend | **yes** | yes | yes | yes (browser) | no |
| Fixture import/export | **yes** | partial | no | no | no |
| Dependencies | **zero** | JVM | npm | npm | 1 |

## Key design decisions

| Decision | Choice | Reason |
|---|---|---|
| Dependencies | stdlib only | Zero transitive deps for embedders |
| Redaction | On write | Sensitive data never touches disk |
| Faking | HMAC-SHA256 | Deterministic -- same input always produces the same fake |
| Fixtures | JSON | Human-readable, easy to inspect and edit |
| Storage | Pluggable | `MemoryStore` for tests, `FileStore` for persistence |
| Recording | Async by default | Non-blocking, minimal hot-path overhead |
| Matching | Composable | Start simple, add specificity as needed |
| Proxy | L1/L2 caching | Raw in-memory for session, redacted on disk for persistence |

## Documentation

- [Getting Started](docs/getting-started.md)
- [Recording](docs/recording.md) · [Replay](docs/replay.md) · [Redaction](docs/sanitization.md)
- [Proxy Mode](docs/proxy.md) · [Matching](docs/matching.md) · [Storage](docs/storage.md)
- [Import/Export](docs/import-export.md) · [JSON Config](docs/config.md)
- [CLI Reference](docs/cli.md) · [Docker](docs/docker.md) · [Testcontainers](docs/testcontainers.md)
- [API Reference](docs/api-reference.md)

## License

[Apache 2.0](LICENSE)
