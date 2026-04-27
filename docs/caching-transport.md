# CachingTransport

CachingTransport is an `http.RoundTripper` that provides transparent, store-backed caching for HTTP requests. On cache hit, it returns the recorded response without contacting upstream. On cache miss, it forwards to upstream, records the response (with optional sanitization), and returns it.

CachingTransport is the library primitive for cache-through-upstream logic. Use it when you want to embed httptape's caching behavior into any Go application via a standard `http.Client`.

## When to use CachingTransport

| Use case | Solution |
|----------|----------|
| Embed cache-through-upstream in your Go app | **CachingTransport** |
| Two-tier L1/L2 cache with CLI integration | [Proxy](proxy.md) (composes CachingTransport internally since v0.13.1) |
| Record-only (audit, capture) | [Recorder](recording.md) |
| Replay-only (mock server) | [Server](replay.md) |

CachingTransport is the right choice when:
- You want zero-cost demo hosting: record real LLM responses once, replay infinitely for every demo visitor.
- You are building an egress proxy and want transparent caching with sanitization.
- You need single-flight dedup (concurrent identical requests share one upstream call).
- You want a single-store model (simpler than Proxy's L1/L2 split).

## Not an RFC 7234 cache

CachingTransport does **not** honor `Cache-Control`, `Vary`, or any other HTTP caching headers. It is a tape-match layer: identical requests (as defined by the configured Matcher) always get identical recorded responses. This is deliberate -- LLM APIs return `no-store`, and overriding that is the primary use case.

## Basic usage

```go
store := httptape.NewMemoryStore()

ct := httptape.NewCachingTransport(http.DefaultTransport, store)

client := &http.Client{Transport: ct}
resp, err := client.Get("https://api.example.com/users")
// First call: upstream is contacted, response is cached.
// Second call: response is returned from cache (upstream is not contacted).
```

## With sanitization

```go
store, _ := httptape.NewFileStore(httptape.WithDirectory("./cache"))

sanitizer := httptape.NewPipeline(
    httptape.RedactHeaders("Authorization", "Cookie"),
    httptape.RedactBodyPaths("$.password"),
    httptape.FakeFields("my-seed", "$.user.email"),
)

ct := httptape.NewCachingTransport(http.DefaultTransport, store,
    httptape.WithCacheSanitizer(sanitizer),
    httptape.WithCacheRoute("users-api"),
)

client := &http.Client{Transport: ct}
```

Sanitization is applied to tapes before store persistence. The response returned to the caller is always the original, unsanitized response.

## Constructor

```go
func NewCachingTransport(upstream http.RoundTripper, store Store, opts ...CachingOption) *CachingTransport
```

Both `upstream` and `store` must be non-nil. Panics on nil (constructor guard).

### Default configuration

| Setting | Default |
|---------|---------|
| Matcher | method + path + body_hash |
| Sanitizer | no-op Pipeline |
| Cache filter | 2xx responses only |
| Single-flight | enabled |
| Max body size | 10 MiB |
| SSE recording | enabled |
| Stale fallback | disabled |
| Upstream timeout | 0 (no timeout) |
| Route | `""` |
| OnError | no-op |

## Options

### WithCacheMatcher

```go
httptape.WithCacheMatcher(matcher)
```

Sets the `Matcher` used to identify equivalent requests. Default: method + path + body hash (appropriate for APIs where the body determines the response, such as LLM completions).

### WithCacheSanitizer

```go
httptape.WithCacheSanitizer(sanitizer)
```

Sets the sanitization pipeline applied to recorded tapes before store persistence. The caller always receives the original, unsanitized response.

### WithCacheFilter

```go
httptape.WithCacheFilter(func(resp *http.Response) bool {
    return resp.StatusCode >= 200 && resp.StatusCode < 300
})
```

Controls which upstream responses are cached. Only responses for which the function returns true are persisted. Default: 2xx responses only. To cache all responses:

```go
httptape.WithCacheFilter(func(resp *http.Response) bool { return true })
```

### WithCacheSingleFlight

```go
httptape.WithCacheSingleFlight(true)  // default
httptape.WithCacheSingleFlight(false) // disable
```

Controls single-flight deduplication of concurrent cache misses. When enabled, concurrent requests with the same match key (method + path + body hash) share a single upstream call. Each waiter receives an independent response with its own body reader.

For SSE responses, the first caller gets the live tee'd stream. Concurrent callers wait for the stream to complete, then get the cached tape.

### WithCacheMaxBodySize

```go
httptape.WithCacheMaxBodySize(1 << 20)  // 1 MiB
httptape.WithCacheMaxBodySize(0)        // no limit
```

Sets the maximum request body size in bytes for cache participation. Requests whose body exceeds this limit bypass the cache entirely (forwarded to upstream, response not recorded). Default: 10 MiB.

### WithCacheRoute

```go
httptape.WithCacheRoute("llm-api")
```

Labels all tapes created by this transport with a route name. Only tapes with a matching route are considered during cache lookup.

### WithCacheOnError

```go
httptape.WithCacheOnError(func(err error) {
    log.Printf("caching transport: %v", err)
})
```

Sets a callback invoked when a non-fatal error occurs (store failure, body read failure on the record path). Non-fatal errors do not affect the response returned to the caller.

### WithCacheSSERecording

```go
httptape.WithCacheSSERecording(true)  // default
httptape.WithCacheSSERecording(false) // disable
```

Controls whether SSE (Server-Sent Events) stream recording is enabled. When enabled, SSE responses on the miss path are tee'd to the caller while events are accumulated and persisted as a tape. See [SSE recording](#sse-recording) below.

### WithCacheLookupDisabled

```go
httptape.WithCacheLookupDisabled()
```

Disables the cache hit path entirely. Every request is treated as a miss: forwarded to upstream, recorded via the sanitization pipeline (if configured), and returned. Single-flight dedup, SSE tee, and sanitization remain active.

The configured Matcher is still used by stale fallback (`WithCacheUpstreamDownFallback`), so the two options compose: disable the hit path but still serve stale tapes when upstream is down.

Useful when the embedder owns its own hit-path logic (e.g., Proxy uses an L1 store consulted before this transport runs) and wants CachingTransport's other cross-cutting concerns without the cache lookup it would otherwise perform.

Unlike using a never-matching Matcher, this option skips `Store.List` entirely on the hot path, avoiding unnecessary I/O.

### WithCacheUpstreamDownFallback

```go
httptape.WithCacheUpstreamDownFallback(true)
```

Enables stale-response fallback when upstream is unreachable or returns a transport error on a cache miss. When enabled, CachingTransport searches the store for the best-matching tape and returns it with an `X-Httptape-Stale: true` header. When disabled (default), transport errors are propagated to the caller.

This is useful for demo hosting (upstream flakiness should not break the demo) but wrong for integration tests (which should see the real failure).

### WithCacheUpstreamTimeout

```go
httptape.WithCacheUpstreamTimeout(5 * time.Second)
```

Sets a timeout for upstream requests on cache miss. When set, the request context is wrapped with a deadline before forwarding. On timeout, the stale-fallback path is entered (if enabled). Default: 0 (no timeout; the caller's `http.Client` timeout dominates).

## How it works

### Cache hit

```
Request
  |
  v
Read request body, compute body hash
  |
  v
Store.List -> Matcher.Match -> HIT
  |
  v
Synthesize response from tape
  |
  v
Return response (upstream not contacted)
```

### Cache miss

```
Request
  |
  v
Read request body, compute body hash
  |
  v
Store.List -> Matcher.Match -> MISS
  |
  v
[Single-flight dedup if enabled]
  |
  v
Forward to upstream
  |
  +-- Success --> Read response body
  |                Check cacheFilter
  |                Build tape, sanitize, Store.Save
  |                Return response
  |
  +-- Error --> [Stale fallback if enabled]
                  |
                  +-- Stale hit --> Return cached response
                  |                 (X-Httptape-Stale: true)
                  |
                  +-- Stale miss --> Propagate error
```

## SSE recording

When the upstream responds with `Content-Type: text/event-stream` and SSE recording is enabled, CachingTransport detects the SSE stream automatically:

1. The upstream response body is wrapped in an `sseRecordingReader`.
2. Bytes pass through to the caller unchanged (streaming, not buffered).
3. A background goroutine parses the stream into discrete `SSEEvent` entries with timing metadata.
4. When the stream ends cleanly (upstream sends EOF), the tape is persisted.
5. If the client disconnects mid-stream, the partial tape is discarded.

On subsequent requests with the same match key, the cached SSE tape is returned as a piped stream with events emitted back-to-back (instant timing). This matches the `http.RoundTripper` contract where callers expect responses quickly.

### SSE with redaction

```go
sanitizer := httptape.NewPipeline(
    httptape.RedactHeaders("Authorization"),
    httptape.RedactSSEEventData("$.choices[*].delta.content"),
    httptape.FakeSSEEventData("my-seed", "$.user.email"),
)

ct := httptape.NewCachingTransport(upstream, store,
    httptape.WithCacheSanitizer(sanitizer),
)
```

## Stale fallback

When `WithCacheUpstreamDownFallback(true)` is set and the upstream fails (transport error or timeout):

1. CachingTransport searches the store for a matching tape.
2. If a match is found, it is returned with `X-Httptape-Stale: true` header.
3. If no match is found, the original transport error is propagated.

The `X-Httptape-Stale: true` header signals to callers that the response is from a stale cache, not from upstream.

### Combining with upstream timeout

```go
ct := httptape.NewCachingTransport(upstream, store,
    httptape.WithCacheUpstreamDownFallback(true),
    httptape.WithCacheUpstreamTimeout(3 * time.Second),
)
```

If the upstream does not respond within 3 seconds, CachingTransport falls back to any cached tape that matches the request.

## Error handling

| Failure mode | Caller sees | Notes |
|---|---|---|
| Request body exceeds maxBodySize | Upstream response (bypass cache) | No recording |
| Request body read fails | Error (wrapped) | Cannot proceed |
| Store.List fails on cache lookup | Upstream response (miss path) | onError called |
| Matcher finds no match (cache miss) | Upstream response | Normal miss flow |
| Upstream transport error (miss) | Error or stale response if staleFallback | See stale fallback |
| Upstream returns non-2xx (filtered) | Upstream response (not cached) | Response returned, not recorded |
| Upstream response body read fails | Upstream response (partial) | onError called, not cached |
| Store.Save fails (record path) | Upstream response | onError called |
| SSE stream truncated (client disconnect) | Partial stream | Partial tape discarded |

Non-fatal errors are reported via the `WithCacheOnError` callback. They never affect the response returned to the caller.

## CachingTransport vs Proxy

| Feature | CachingTransport | Proxy |
|---------|-----------------|-------|
| Store model | Single store | L1 (memory) + L2 (disk) |
| Single-flight dedup | Yes | Yes (via composed CachingTransport) |
| Stale fallback | Yes (opt-in) | Yes (L1 then L2) |
| Health endpoint | No | No |
| CLI integration | No | Yes (`httptape proxy`) |
| Use case | Library embedding | CLI-oriented caching proxy |

Proxy composes CachingTransport internally (since v0.13.1). L1 pre-check + fallback logic live at the Proxy layer; L2 cache + stale fallback + SSE tee live in CachingTransport. CachingTransport remains usable as a standalone single-store primitive for library embedding.

## Full example: zero-cost demo hosting

```go
package main

import (
    "fmt"
    "log"
    "net/http"
    "time"

    "github.com/VibeWarden/httptape"
)

func main() {
    store, err := httptape.NewFileStore(httptape.WithDirectory("./demo-fixtures"))
    if err != nil {
        log.Fatal(err)
    }

    sanitizer := httptape.NewPipeline(
        httptape.RedactHeaders("Authorization", "X-Api-Key"),
        httptape.FakeFields("demo-seed", "$.user.email", "$.user.name"),
    )

    ct := httptape.NewCachingTransport(http.DefaultTransport, store,
        httptape.WithCacheSanitizer(sanitizer),
        httptape.WithCacheRoute("demo"),
        httptape.WithCacheUpstreamDownFallback(true),
        httptape.WithCacheUpstreamTimeout(5 * time.Second),
        httptape.WithCacheOnError(func(err error) {
            log.Printf("cache: %v", err)
        }),
    )

    client := &http.Client{Transport: ct}

    // First call: hits upstream, records response.
    resp, err := client.Get("https://api.example.com/demo/data")
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println("Status:", resp.StatusCode)

    // Second call: served from cache (upstream not contacted).
    resp, err = client.Get("https://api.example.com/demo/data")
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println("Status:", resp.StatusCode)
}
```

## Thread safety

CachingTransport is safe for concurrent use by multiple goroutines. `RoundTrip` may be called from multiple goroutines simultaneously.

## Known limitations

- **Single-flight waiters do not observe context cancellation.** When single-flight is enabled (default), waiters blocked on a shared upstream call do not exit early if their request context is cancelled. The waiter goroutine remains blocked until the leader's upstream call completes. This is a consequence of using `sync.WaitGroup` (not context-aware) for waiter coordination. To work around this, either disable single-flight (`WithCacheSingleFlight(false)`) or set `WithCacheUpstreamTimeout` to bound the leader's wait.

## See also

- [Proxy Mode](proxy.md) -- two-tier L1/L2 caching with CLI integration
- [Recording](recording.md) -- record-only (no replay)
- [Replay](replay.md) -- replay-only (no upstream)
- [Redaction](sanitization.md) -- configuring the sanitization pipeline
- [API Reference](api-reference.md) -- full type signatures
