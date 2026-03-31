<p align="center">
  <img src="assets/logo.png" alt="httptape logo" width="300">
</p>

<h3 align="center">record, redact, replay</h3>

<p align="center">
  HTTP traffic recording, sanitization, and replay for Go.
</p>

---

httptape is an embeddable Go library that captures HTTP request/response pairs,
sanitizes sensitive data on write, and replays them as a mock server. Think
WireMock, but native Go, embeddable, and with sanitization built into the core.

## Why httptape?

- **WireMock requires Java** — separate process, 200 MB+ memory, can't embed in a Go binary
- **Go mocking libraries** (`gock`, `httpmock`) only work inside test code — no standalone server, no recording, no fixture management
- **Nobody does sanitization** — existing tools record raw traffic including secrets and PII. httptape sanitizes on write — sensitive data never hits disk

## Install

```bash
go get github.com/httptape/httptape
```

## Quick start

### Record

Wrap any `http.RoundTripper` to capture traffic:

```go
store := httptape.NewMemoryStore()

rec := httptape.NewRecorder(store,
    httptape.WithRoute("github-api"),
)
defer rec.Close()

client := &http.Client{Transport: rec}
resp, err := client.Get("https://api.github.com/users/octocat")
// Tape is automatically saved to store
```

### Replay

Serve recorded fixtures as a mock HTTP server:

```go
srv := httptape.NewServer(store)
ts := httptest.NewServer(srv)
defer ts.Close()

// Requests to ts.URL are matched against recorded tapes
resp, err := http.Get(ts.URL + "/users/octocat")
```

### Match

Control how requests are matched to recorded tapes:

```go
srv := httptape.NewServer(store,
    httptape.WithMatcher(httptape.NewCompositeMatcher(
        httptape.MatchMethod(),
        httptape.MatchPath(),
        httptape.MatchQueryParams(),
        httptape.MatchBodyHash(),
    )),
)
```

### Store

Choose your storage backend:

```go
// In-memory (for tests)
mem := httptape.NewMemoryStore()

// Filesystem (for fixtures)
fs := httptape.NewFileStore(httptape.WithDirectory("./testdata/fixtures"))
```

## Key design decisions

| Decision | Choice | Reason |
|---|---|---|
| Dependencies | stdlib only | Zero transitive deps for embedders |
| Sanitization | On write | Sensitive data never touches disk |
| Faking | HMAC-SHA256 | Deterministic — same input always produces the same fake |
| Fixtures | JSON | Human-readable, easy to inspect and edit |
| Storage | Pluggable | `MemoryStore` for tests, `FileStore` for persistence |
| Recording | Async by default | Non-blocking, minimal hot-path overhead |
| Matching | Composable | Start simple, add specificity as needed |

## License

[Apache 2.0](LICENSE)
