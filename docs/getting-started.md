# Getting Started

Record, Redact, and Replay HTTP traffic in 5 minutes.

## Prerequisites

- Go 1.22 or later (for the Go library)
- Or: Docker (for CLI/Docker usage with any language)
- `go get github.com/VibeWarden/httptape`

## Step 1: Record HTTP traffic

Wrap any `http.RoundTripper` with a `Recorder` to capture traffic:

```go
package main

import (
    "fmt"
    "net/http"

    "github.com/VibeWarden/httptape"
)

func main() {
    // Create a file-backed store for persistent fixtures
    store, err := httptape.NewFileStore(httptape.WithDirectory("./fixtures"))
    if err != nil {
        panic(err)
    }

    // Create a recorder that wraps http.DefaultTransport
    rec := httptape.NewRecorder(store,
        httptape.WithRoute("github-api"),
    )
    defer rec.Close() // Always close to flush pending recordings

    // Use it as a normal http.Client
    client := &http.Client{Transport: rec}
    resp, err := client.Get("https://api.github.com/users/octocat")
    if err != nil {
        panic(err)
    }
    defer resp.Body.Close()

    fmt.Println("Recorded:", resp.StatusCode)
    // A JSON fixture file is now saved in ./fixtures/
}
```

After running this, check `./fixtures/` -- you will see a JSON file containing the full request and response.

## Step 2: Replay recorded traffic

Serve the recorded fixtures as a mock HTTP server:

```go
package main

import (
    "fmt"
    "net/http"
    "net/http/httptest"

    "github.com/VibeWarden/httptape"
)

func main() {
    store, err := httptape.NewFileStore(httptape.WithDirectory("./fixtures"))
    if err != nil {
        panic(err)
    }

    srv := httptape.NewServer(store)
    ts := httptest.NewServer(srv)
    defer ts.Close()

    // Requests are matched against recorded tapes by method + path
    resp, err := http.Get(ts.URL + "/users/octocat")
    if err != nil {
        panic(err)
    }
    defer resp.Body.Close()

    fmt.Println("Replayed:", resp.StatusCode) // 200
}
```

## Step 3: Redact sensitive data

Strip secrets and PII before anything touches disk:

```go
package main

import (
    "net/http"

    "github.com/VibeWarden/httptape"
)

func main() {
    store, err := httptape.NewFileStore(httptape.WithDirectory("./fixtures"))
    if err != nil {
        panic(err)
    }

    // Build a sanitization pipeline
    sanitizer := httptape.NewPipeline(
        httptape.RedactHeaders(),                              // redact Authorization, Cookie, etc.
        httptape.RedactBodyPaths("$.password", "$.ssn"),       // redact specific body fields
        httptape.FakeFields("my-project-seed", "$.user.email", "$.user.id"), // deterministic fakes
    )

    rec := httptape.NewRecorder(store,
        httptape.WithRoute("users-api"),
        httptape.WithSanitizer(sanitizer),
    )
    defer rec.Close()

    client := &http.Client{Transport: rec}
    client.Post("https://api.example.com/users", "application/json",
        nil, // your request body here
    )

    // The fixture file now has:
    // - Authorization header replaced with "[REDACTED]"
    // - $.password replaced with "[REDACTED]"
    // - $.user.email replaced with "user_a1b2c3d4@example.com" (deterministic)
    // - $.user.id replaced with a deterministic number
}
```

## Step 4: Use in tests

The most common pattern -- record once, replay in every test run:

```go
package myapi_test

import (
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/VibeWarden/httptape"
)

func TestUserAPI(t *testing.T) {
    store, err := httptape.NewFileStore(
        httptape.WithDirectory("testdata/fixtures"),
    )
    if err != nil {
        t.Fatal(err)
    }

    srv := httptape.NewServer(store)
    ts := httptest.NewServer(srv)
    defer ts.Close()

    // Point your API client at the mock server
    resp, err := http.Get(ts.URL + "/users/octocat")
    if err != nil {
        t.Fatal(err)
    }

    if resp.StatusCode != 200 {
        t.Errorf("expected 200, got %d", resp.StatusCode)
    }
}
```

## Using in-memory store for tests

For tests that don't need persistent fixtures:

```go
func TestWithMemoryStore(t *testing.T) {
    store := httptape.NewMemoryStore()

    // Record
    rec := httptape.NewRecorder(store, httptape.WithRoute("test"))
    client := &http.Client{Transport: rec}
    client.Get("https://api.example.com/data")
    rec.Close()

    // Replay
    srv := httptape.NewServer(store)
    ts := httptest.NewServer(srv)
    defer ts.Close()

    resp, _ := http.Get(ts.URL + "/data")
    // assert on resp...
    _ = resp
}
```

## Next steps

- [Recording](recording.md) -- async/sync modes, sampling, body size limits
- [Redaction](sanitization.md) -- full guide to redaction and faking
- [Proxy Mode](proxy.md) -- forward to upstream with fallback-to-cache
- [Matching](matching.md) -- control how requests match recorded tapes
- [CLI](cli.md) -- use httptape as a standalone tool (any language)
