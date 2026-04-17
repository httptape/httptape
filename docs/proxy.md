# Proxy Mode

Proxy mode forwards requests to a real upstream, caches responses in a two-tier system, and falls back to cached responses when the upstream is unavailable. It combines recording and replay into a single transparent `http.RoundTripper`.

## When to use proxy vs record vs serve

| Mode | Use case |
|------|----------|
| **`record`** | Capture a fixed set of fixtures from an upstream API. Stop recording, then switch to `serve`. |
| **`serve`** | Replay a known set of fixtures. No upstream needed. Deterministic and offline. |
| **`proxy`** | Develop against a live upstream with automatic fallback. Best for frontend dev where the backend may be flaky, slow, or occasionally offline. |

Use `proxy` when you want the benefits of a live upstream (fresh data, real behavior) with the safety net of cached responses. Use `record` + `serve` when you want fully deterministic, offline replay.

## How it works

The `Proxy` is an `http.RoundTripper` that implements a two-tier caching strategy:

```
Request
  |
  v
Forward to upstream
  |
  +-- Success --> Save raw tape to L1 (memory)
  |                Save redacted tape to L2 (disk)
  |                Return real response
  |
  +-- Failure --> Look up L1 (raw, in-session cache)
                   |
                   +-- L1 hit --> Return cached response (X-Httptape-Source: l1-cache)
                   |
                   +-- L1 miss --> Look up L2 (redacted, persistent cache)
                                   |
                                   +-- L2 hit --> Return cached response (X-Httptape-Source: l2-cache)
                                   |
                                   +-- L2 miss --> Return original error
```

### L1: In-memory cache (raw)

- Backed by a `MemoryStore`
- Contains unsanitized (raw) responses -- best fidelity within a session
- Lost when the process exits
- Checked first during fallback (lowest latency, best data quality)

### L2: Disk cache (redacted)

- Backed by a `FileStore`
- Contains redacted responses (secrets stripped, PII faked)
- Persists across restarts
- Safe to commit to version control
- Checked second during fallback

### X-Httptape-Source header

When a response comes from cache, the proxy adds an `X-Httptape-Source` header:

| Value | Meaning |
|-------|---------|
| `l1-cache` | Response came from in-memory cache (raw, current session) |
| `l2-cache` | Response came from disk cache (redacted, persistent) |
| (absent) | Response came from the real upstream |

This makes it easy to see in browser dev tools or logs whether a response is live or cached.

## Go API

### Basic usage

```go
l1 := httptape.NewMemoryStore()
l2, _ := httptape.NewFileStore(httptape.WithDirectory("./cache"))

proxy := httptape.NewProxy(l1, l2)

client := &http.Client{Transport: proxy}
resp, err := client.Get("https://api.example.com/users")
// If upstream is reachable: real response, cached to L1 + L2
// If upstream is down: cached response from L1 or L2
```

### With redaction

```go
sanitizer := httptape.NewPipeline(
    httptape.RedactHeaders("Authorization", "Cookie"),
    httptape.RedactBodyPaths("$.password"),
    httptape.FakeFields("my-seed", "$.user.email"),
)

proxy := httptape.NewProxy(l1, l2,
    httptape.WithProxySanitizer(sanitizer),
)
```

The redaction pipeline is applied only to L2 writes. L1 always stores raw responses for best within-session fidelity.

### Constructor

```go
func NewProxy(l1, l2 Store, opts ...ProxyOption) *Proxy
```

Both `l1` and `l2` must be non-nil. Panics on nil stores.

### Options

| Option | Signature | Default |
|--------|-----------|---------|
| `WithProxyTransport` | `WithProxyTransport(rt http.RoundTripper)` | `http.DefaultTransport` |
| `WithProxySanitizer` | `WithProxySanitizer(s Sanitizer)` | no-op Pipeline |
| `WithProxyMatcher` | `WithProxyMatcher(m Matcher)` | `DefaultMatcher()` |
| `WithProxyRoute` | `WithProxyRoute(route string)` | `""` |
| `WithProxyOnError` | `WithProxyOnError(fn func(error))` | nil |
| `WithProxyFallbackOn` | `WithProxyFallbackOn(fn func(error, *http.Response) bool)` | transport errors only |

### Fallback on 5xx

By default, the proxy only falls back on transport errors (connection refused, DNS failure, timeout). To also fall back on 5xx responses from the upstream:

```go
proxy := httptape.NewProxy(l1, l2,
    httptape.WithProxyFallbackOn(func(err error, resp *http.Response) bool {
        if err != nil {
            return true
        }
        return resp != nil && resp.StatusCode >= 500
    }),
)
```

## CLI

```bash
httptape proxy --upstream https://api.example.com \
    --fixtures ./cache \
    --config redact.json \
    --port 8081
```

| Flag | Default | Description |
|------|---------|-------------|
| `--upstream` | (required) | Upstream URL (e.g., `https://api.example.com`) |
| `--fixtures` | (required) | Path to fixture directory for L2 cache |
| `--config` | (none) | Path to redaction config JSON (applied to L2 writes only) |
| `--port` | `8081` | Listen port |
| `--cors` | `false` | Enable CORS headers |
| `--fallback-on-5xx` | `false` | Also fall back on 5xx responses from upstream |

The L1 cache is always an in-memory store managed internally. The `--fixtures` directory is the L2 (persistent, redacted) cache.

## Docker

```bash
docker run --rm \
  -v ./cache:/fixtures \
  -v ./redact.json:/config/config.json:ro \
  -p 8081:8081 \
  ghcr.io/vibewarden/httptape:latest \
  proxy --upstream https://api.example.com \
        --fixtures /fixtures \
        --config /config/config.json \
        --port 8081
```

### Docker Compose: frontend + proxy

```yaml
services:
  api-proxy:
    image: ghcr.io/vibewarden/httptape:latest
    command:
      - proxy
      - --upstream
      - https://api.staging.example.com
      - --fixtures
      - /fixtures
      - --config
      - /config/config.json
      - --port
      - "3001"
      - --cors
    ports:
      - "3001:3001"
    volumes:
      - ./cache:/fixtures
      - ./redact.json:/config/config.json:ro

  frontend:
    build:
      context: ./frontend
    ports:
      - "3000:3000"
    environment:
      VITE_API_URL: http://localhost:3001
    depends_on:
      - api-proxy
```

When the staging API is up, the frontend gets live data. When it is down, the proxy serves cached responses transparently.

## Example: frontend development with fallback

A typical frontend development workflow using proxy mode:

1. Start the proxy pointing at your staging/development API
2. Build your UI -- all requests go through the proxy to the real API
3. Responses are cached to disk (L2) with secrets redacted
4. If the API goes down (network issue, deployment, VPN disconnect), the proxy serves cached responses
5. When the API comes back, fresh responses are served and the cache is updated

```bash
# Start proxy with redaction
httptape proxy \
    --upstream https://staging-api.example.com \
    --fixtures ./api-cache \
    --config redact.json \
    --port 3001 \
    --cors

# In another terminal, start your frontend
cd frontend && npm run dev
# Frontend at localhost:3000 calls proxy at localhost:3001
```

Check the `X-Httptape-Source` response header in browser dev tools to see whether each response is live or cached.

## SSE passthrough and fallback

SSE (`text/event-stream`) responses pass through proxy mode transparently. When the upstream is reachable:

1. The SSE stream is forwarded to the caller unchanged.
2. A background goroutine parses the stream into discrete `SSEEvent` entries with timing metadata.
3. When the stream ends, the parsed events are saved to L1 (raw) and L2 (redacted, if a sanitizer is configured).

When the upstream is unavailable, the proxy falls back to cached SSE tapes. L2 fallback synthesizes a streaming response from the stored events using the configured timing mode.

### Proxy SSE timing for fallback

Control the replay timing of cached SSE responses with `WithProxySSETiming`:

```go
proxy := httptape.NewProxy(l1, l2,
    httptape.WithProxySSETiming(httptape.SSETimingInstant()), // default
)
```

The default is `SSETimingInstant()` -- cached SSE responses are delivered as fast as possible. This is appropriate because proxy fallback is a resilience mechanism, not a simulation tool. Use `SSETimingRealtime()` or `SSETimingAccelerated(N)` if you need realistic streaming behavior during fallback.

### SSE redaction in proxy mode

SSE event redaction is applied only to L2 writes (the persistent, disk-backed cache). L1 always stores raw events for within-session fidelity.

```go
sanitizer := httptape.NewPipeline(
    httptape.RedactHeaders("Authorization"),
    httptape.RedactSSEEventData("$.choices[*].delta.content"),
    httptape.FakeSSEEventData("my-seed", "$.user.email"),
)
proxy := httptape.NewProxy(l1, l2, httptape.WithProxySanitizer(sanitizer))
```

## Thread safety

`Proxy` is safe for concurrent use by multiple goroutines. `RoundTrip` may be called from multiple goroutines simultaneously.

## See also

- [Recording](recording.md) -- one-shot recording without fallback
- [Replay](replay.md) -- offline replay from fixtures
- [Redaction](sanitization.md) -- configuring the redaction pipeline
- [CLI](cli.md) -- all CLI commands and flags
- [Docker](docker.md) -- container usage
- [API Reference](api-reference.md) -- full type signatures
