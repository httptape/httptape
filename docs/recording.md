# Recording

The `Recorder` is an `http.RoundTripper` that wraps an inner transport and captures every HTTP request/response pair as a `Tape`. It is the entry point for all recording in httptape.

## Basic usage

```go
store := httptape.NewMemoryStore()
rec := httptape.NewRecorder(store)
defer rec.Close()

client := &http.Client{Transport: rec}
resp, err := client.Get("https://api.example.com/users")
// resp is the real response -- recording is transparent
```

The `Recorder` never modifies the response returned to the caller. The only observable side effect is that `resp.Body` is fully read into memory and replaced with a new reader (so both the caller and the recorder can access the body).

## Constructor

```go
func NewRecorder(store Store, opts ...RecorderOption) *Recorder
```

The `store` parameter is required and must not be nil (panics if nil). All other behavior is configured via options.

## Options

### WithTransport

```go
httptape.WithTransport(rt http.RoundTripper)
```

Sets the inner transport. Defaults to `http.DefaultTransport`. Use this to wrap a custom transport or chain with other middleware.

### WithRoute

```go
httptape.WithRoute("users-api")
```

Labels all tapes produced by this recorder with a route name. Routes are used for:
- Logical grouping of fixtures
- Filtering during [export](import-export.md)
- Scoped matching with [MatchRoute](matching.md#matchroute)

### WithSanitizer

```go
httptape.WithSanitizer(sanitizer)
```

Sets a `Sanitizer` to transform tapes before persistence. If nil, a no-op pipeline is used. See [Sanitization](sanitization.md) for details.

### WithAsync

```go
httptape.WithAsync(true)  // default
httptape.WithAsync(false) // synchronous writes
```

Controls whether tapes are written asynchronously (via a buffered channel and background goroutine) or synchronously (inline during `RoundTrip`).

**Async mode (default):** Tapes are sent to a buffered channel. A background goroutine drains the channel and writes to the store. `RoundTrip` returns immediately after sending to the channel. If the channel is full, the tape is dropped and `OnError` is called.

**Sync mode:** Tapes are written to the store directly inside `RoundTrip`. Store errors are reported via `OnError` but never affect the HTTP response.

### WithBufferSize

```go
httptape.WithBufferSize(2048)
```

Sets the channel buffer size for async mode. Defaults to 1024. Ignored in sync mode. Values less than 1 are set to 1.

### WithSampling

```go
httptape.WithSampling(0.01) // record 1% of requests
httptape.WithSampling(1.0)  // record everything (default)
httptape.WithSampling(0.0)  // record nothing
```

Sets a probabilistic sampling rate. Values are clamped to [0.0, 1.0]. When a request is not sampled, it is passed through to the inner transport without recording.

This is useful for production traffic capture where recording every request would be too expensive.

### WithMaxBodySize

```go
httptape.WithMaxBodySize(1 << 20) // 1 MB limit
```

Sets the maximum body size in bytes for both request and response bodies. Bodies exceeding this limit are truncated, and the `Truncated` flag is set on the recorded request/response. The `OriginalBodySize` field records the pre-truncation size. A value of 0 (default) means no limit.

### WithSkipRedirects

```go
httptape.WithSkipRedirects(true)
```

When enabled, intermediate 3xx redirect responses are not recorded. Only the final non-redirect response is stored. This is useful when the `http.Client` follows redirects automatically -- each redirect hop produces a separate `RoundTrip` call, and recording all of them clutters the fixture set.

### WithOnError

```go
httptape.WithOnError(func(err error) {
    log.Printf("recording error: %v", err)
})
```

Sets a callback for async write errors, body truncation warnings, and other non-fatal errors. The callback is invoked from the background goroutine (async mode) or inline (sync mode), so it must be safe for concurrent use. Defaults to a no-op.

## Closing the recorder

```go
rec.Close()
```

Always call `Close` when recording is complete. In async mode, `Close` flushes all pending tapes from the channel and waits for the background goroutine to finish. In sync mode, `Close` is a no-op. `Close` is idempotent -- safe to call multiple times.

After `Close` returns, any further `RoundTrip` calls pass through to the inner transport without recording.

## Thread safety

`Recorder` is safe for concurrent use. Multiple goroutines can call `RoundTrip` simultaneously. `Close` must be called exactly once when recording is complete (though calling it multiple times is safe due to `sync.Once`).

## Full example

```go
store, _ := httptape.NewFileStore(httptape.WithDirectory("./fixtures"))

sanitizer := httptape.NewPipeline(
    httptape.RedactHeaders(),
    httptape.RedactBodyPaths("$.password"),
)

rec := httptape.NewRecorder(store,
    httptape.WithRoute("payments-api"),
    httptape.WithSanitizer(sanitizer),
    httptape.WithAsync(true),
    httptape.WithBufferSize(2048),
    httptape.WithSampling(0.1),       // 10% sampling
    httptape.WithMaxBodySize(5<<20),   // 5 MB body limit
    httptape.WithSkipRedirects(true),
    httptape.WithOnError(func(err error) {
        log.Printf("recorder: %v", err)
    }),
)
defer rec.Close()

client := &http.Client{Transport: rec}
// Use client normally...
```

## See also

- [Sanitization](sanitization.md) -- configure what gets redacted
- [Storage](storage.md) -- where tapes are stored
- [Config](config.md) -- declarative sanitizer configuration
