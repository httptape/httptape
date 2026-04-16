# Replay

The `Server` is an `http.Handler` that replays recorded HTTP interactions. It receives incoming requests, finds a matching `Tape` via a `Matcher`, and writes the recorded response.

## Basic usage

```go
store, _ := httptape.NewFileStore(httptape.WithDirectory("./fixtures"))

srv := httptape.NewServer(store)
ts := httptest.NewServer(srv)
defer ts.Close()

// Requests to ts.URL are matched against recorded tapes
resp, _ := http.Get(ts.URL + "/users/octocat")
```

## Constructor

```go
func NewServer(store Store, opts ...ServerOption) *Server
```

The `store` parameter is required and must not be nil (panics if nil).

## Options

### WithMatcher

```go
httptape.WithMatcher(matcher)
```

Sets the `Matcher` used to find tapes for incoming requests. If not set, `DefaultMatcher()` is used, which matches by HTTP method and URL path.

See [Matching](matching.md) for all available matchers.

```go
srv := httptape.NewServer(store,
    httptape.WithMatcher(httptape.NewCompositeMatcher(
        httptape.MatchMethod(),
        httptape.MatchPath(),
        httptape.MatchQueryParams(),
    )),
)
```

### WithFallbackStatus

```go
httptape.WithFallbackStatus(502)
```

Sets the HTTP status code returned when no tape matches the incoming request. Defaults to 404.

### WithFallbackBody

```go
httptape.WithFallbackBody([]byte(`{"error": "no mock found"}`))
```

Sets the response body returned when no tape matches. Defaults to `"httptape: no matching tape found"`.

### WithOnNoMatch

```go
httptape.WithOnNoMatch(func(r *http.Request) {
    log.Printf("unmatched request: %s %s", r.Method, r.URL.Path)
})
```

Sets a callback invoked when no tape matches an incoming request. The callback runs before the fallback response is written. Must be safe for concurrent use.

This is useful for debugging which requests are not being matched during test development.

### WithCORS

```go
func WithCORS() ServerOption
```

Enables permissive CORS headers (`Access-Control-Allow-Origin: *`) on every replayed response and short-circuits `OPTIONS` preflight requests with 204. Intended for local development where a frontend dev server (e.g., `localhost:3000`) calls the mock backend (e.g., `localhost:3001`). Opt-in only.

```go
srv := httptape.NewServer(store, httptape.WithCORS())
```

### WithDelay

```go
func WithDelay(d time.Duration) ServerOption
```

Adds a fixed delay before every response. The delay is applied after matching but before writing the response. If the request context is cancelled during the delay (e.g., the client disconnects), `ServeHTTP` returns immediately without writing. A zero or negative duration is a no-op.

```go
srv := httptape.NewServer(store, httptape.WithDelay(200*time.Millisecond))
```

### WithErrorRate

```go
func WithErrorRate(rate float64) ServerOption
```

Causes a fraction of requests to return `500 Internal Server Error` with an `X-Httptape-Error: simulated` header instead of the recorded response. `rate` must be between `0.0` and `1.0` inclusive (`0.0` disables error simulation, `1.0` fails every request). Panics if `rate` is outside `[0.0, 1.0]`.

```go
srv := httptape.NewServer(store, httptape.WithErrorRate(0.1)) // 10% failure rate
```

### WithReplayHeaders

```go
func WithReplayHeaders(key, value string) ServerOption
```

Injects a header into every replayed response, applied after tape matching. Overrides any header with the same key from the recorded tape. May be called multiple times to set multiple headers. Useful for environment-specific tokens, correlation IDs, or cache-control values.

```go
srv := httptape.NewServer(store,
    httptape.WithReplayHeaders("X-Request-ID", "test-run-1"),
    httptape.WithReplayHeaders("Cache-Control", "no-store"),
)
```

## How replay works

For each incoming request, the `Server`:

1. Calls `Store.List` with an empty filter to retrieve all tapes
2. Passes the request and all tapes to the `Matcher`
3. If a match is found, writes the tape's response headers, status code, and body
4. If no match is found, calls `OnNoMatch` (if set) and writes the fallback response

The `Content-Length` header from the recorded response is removed and re-calculated by `net/http` to ensure it matches the actual body (which may have been modified by sanitization).

## Using with httptest

The most common pattern for tests:

```go
func TestMyAPI(t *testing.T) {
    store, err := httptape.NewFileStore(
        httptape.WithDirectory("testdata/fixtures"),
    )
    if err != nil {
        t.Fatal(err)
    }

    srv := httptape.NewServer(store,
        httptape.WithOnNoMatch(func(r *http.Request) {
            t.Errorf("unmatched: %s %s", r.Method, r.URL.Path)
        }),
    )
    ts := httptest.NewServer(srv)
    defer ts.Close()

    // Point your code at ts.URL instead of the real API
    client := NewAPIClient(ts.URL)
    user, err := client.GetUser("octocat")
    if err != nil {
        t.Fatal(err)
    }
    // assert on user...
}
```

## Using as a standalone server

The `Server` implements `http.Handler`, so it can be used with any HTTP server:

```go
store, _ := httptape.NewFileStore(httptape.WithDirectory("./fixtures"))
srv := httptape.NewServer(store)

log.Println("Mock server on :8081")
http.ListenAndServe(":8081", srv)
```

For standalone use, consider the [CLI](cli.md) which wraps this pattern.

## Thread safety

`Server` is safe for concurrent use. All fields are immutable after construction. `ServeHTTP` can be called from multiple goroutines simultaneously.

## Performance note

The server calls `Store.List` on every request, resulting in an O(n) scan over all tapes. This is acceptable for test usage with small fixture sets (up to a few hundred tapes). For large fixture sets, consider scoping fixtures by route.

## See also

- [Matching](matching.md) -- control how requests are matched to tapes
- [Storage](storage.md) -- where tapes are loaded from
- [CLI](cli.md) -- standalone serve mode
