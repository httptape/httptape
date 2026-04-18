# httptape — Decisions Log

This file is the living record of all architectural decisions.
Updated by the architect agent (ADRs).
Never delete entries — mark superseded decisions as `Superseded by ADR-N`.

---

## Locked decisions (from project inception)

| # | Decision | Status |
|---|---|---|
| L-01 | Language: Go | Locked |
| L-02 | License: Apache 2.0 | Locked |
| L-03 | Architecture: Hexagonal (ports & adapters) within single package | Locked |
| L-04 | Dependencies: stdlib only for v1 | Locked |
| L-05 | Sanitization: on write, not on export | Locked |
| L-06 | Deterministic faking: HMAC-SHA256 with configurable seed | Locked |
| L-07 | Fixture format: JSON | Locked |
| L-08 | Storage: pluggable interface (Store) | Locked |
| L-09 | Matching: progressive (exact first, fuzzy/regex later) | Locked |
| L-10 | Recording: async by default via buffered channel | Locked |
| L-11 | No init(), no package-level mutable state, no panics | Locked |
| L-12 | Functional options pattern for all public constructors | Locked |
| L-13 | 90% test coverage target, stdlib testing only | Locked |

---

## ADRs

---

### ADR-1: Core types and storage interface

**Date**: 2026-03-30
**Issue**: #27
**Status**: Accepted

#### Context

httptape needs its foundational data types and storage abstraction before any other
feature (recorder, server, matcher, sanitizer) can be built. This ADR defines the
core value types (`Tape`, `RecordedReq`, `RecordedResp`), the `Store` port interface,
and the two initial adapter implementations (`MemoryStore`, `FileStore`).

All types must be pure data structures with zero I/O. The `Store` interface is the
primary hexagonal port for persistence. Implementations are adapters.

#### Decision

##### Core types (`tape.go`)

```go
package httptape

import (
    "net/http"
    "time"
)

// Tape represents a single recorded HTTP interaction (request + response pair).
// It is a pure value type with no I/O.
type Tape struct {
    // ID uniquely identifies this tape. Generated as a UUID v4 string on creation.
    ID string `json:"id"`

    // Route is a logical grouping label (e.g., "users-api", "auth-service").
    // Used by FileStore for directory partitioning and by matchers for scoping.
    Route string `json:"route"`

    // RecordedAt is the UTC timestamp when the interaction was captured.
    RecordedAt time.Time `json:"recorded_at"`

    // Request is the recorded HTTP request.
    Request RecordedReq `json:"request"`

    // Response is the recorded HTTP response.
    Response RecordedResp `json:"response"`
}

// RecordedReq captures the essential parts of an HTTP request for matching and replay.
type RecordedReq struct {
    // Method is the HTTP method (GET, POST, etc.).
    Method string `json:"method"`

    // URL is the full request URL as a string.
    URL string `json:"url"`

    // Headers contains the request headers. Only non-sensitive headers are stored
    // after sanitization (handled by the sanitizer, not by this type).
    Headers http.Header `json:"headers"`

    // Body is the full request body bytes. May be nil for bodiless requests.
    Body []byte `json:"body"`

    // BodyHash is a hex-encoded SHA-256 hash of the original request body.
    // Used for matching without comparing full bodies.
    BodyHash string `json:"body_hash"`
}

// RecordedResp captures the essential parts of an HTTP response for replay.
type RecordedResp struct {
    // StatusCode is the HTTP response status code.
    StatusCode int `json:"status_code"`

    // Headers contains the response headers.
    Headers http.Header `json:"headers"`

    // Body is the full response body bytes.
    Body []byte `json:"body"`
}
```

Helper functions in `tape.go`:

```go
// NewTape creates a new Tape with a generated ID and the current UTC timestamp.
// Route, Request, and Response must be populated by the caller after creation,
// or passed via functional options in the future recorder API.
func NewTape(route string, req RecordedReq, resp RecordedResp) Tape

// BodyHashFromBytes computes the hex-encoded SHA-256 hash of the given bytes.
// Returns an empty string if b is nil or empty.
func BodyHashFromBytes(b []byte) string
```

`NewTape` generates the ID using `crypto/rand` to produce a UUID v4 string
(16 random bytes formatted as `xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx`). This
avoids any external UUID dependency. The implementation is a small unexported
helper `newUUID() string`.

##### Store interface (`store.go`)

```go
package httptape

import "context"

// Filter controls which tapes are returned by Store.List.
type Filter struct {
    // Route filters tapes by route. Empty string means no filter (return all).
    Route string

    // Method filters tapes by HTTP method. Empty string means no filter.
    Method string
}

// Store persists and retrieves recorded HTTP interactions.
// It is the primary hexagonal port for persistence.
//
// All methods accept a context.Context for cancellation and deadline support.
// Implementations must respect context cancellation.
type Store interface {
    // Save persists a tape. If a tape with the same ID already exists,
    // it is overwritten (upsert semantics).
    Save(ctx context.Context, tape Tape) error

    // Load retrieves a single tape by ID.
    // Returns a non-nil error wrapping ErrNotFound if the tape does not exist.
    Load(ctx context.Context, id string) (Tape, error)

    // List returns all tapes matching the given filter.
    // An empty filter returns all tapes. Returns an empty slice (not nil) if
    // no tapes match.
    List(ctx context.Context, filter Filter) ([]Tape, error)

    // Delete removes a tape by ID.
    // Returns a non-nil error wrapping ErrNotFound if the tape does not exist.
    Delete(ctx context.Context, id string) error
}
```

Sentinel errors in `store.go`:

```go
import "errors"

// ErrNotFound is returned when a requested tape does not exist in the store.
var ErrNotFound = errors.New("httptape: tape not found")
```

Callers use `errors.Is(err, httptape.ErrNotFound)` to check.

##### MemoryStore (`store_memory.go`)

```go
// MemoryStore is an in-memory Store implementation. It is safe for concurrent use.
// Intended primarily for testing, but usable in production for ephemeral recordings.
type MemoryStore struct {
    mu    sync.RWMutex
    tapes map[string]Tape // keyed by Tape.ID
}

// MemoryStoreOption configures a MemoryStore.
type MemoryStoreOption func(*MemoryStore)

// NewMemoryStore creates a new empty MemoryStore.
func NewMemoryStore(opts ...MemoryStoreOption) *MemoryStore
```

Concurrency: `sync.RWMutex` — `Save`/`Delete` take write lock, `Load`/`List` take
read lock. `Save` and `Load` deep-copy the `Tape` (copy headers map and body slices)
to prevent aliasing between caller and store internals.

##### FileStore (`store_file.go`)

```go
// FileStore is a filesystem-backed Store implementation. Each tape is persisted
// as a single JSON file. Safe for concurrent use within a single process.
type FileStore struct {
    dir string // base directory for fixtures
    mu  sync.RWMutex
}

// FileStoreOption configures a FileStore.
type FileStoreOption func(*FileStore)

// WithDirectory sets the base directory for fixture storage.
// If not set, defaults to "fixtures" in the current working directory.
func WithDirectory(dir string) FileStoreOption

// NewFileStore creates a new FileStore. The base directory is created if it
// does not exist (with mode 0o755).
func NewFileStore(opts ...FileStoreOption) (*FileStore, error)
```

**Directory structure:**

```
<base-dir>/<tape-id>.json
```

Each tape is stored as `<tape-id>.json` in the base directory. The tape ID is a
UUID, so it is safe for use as a filename. The Route field is stored inside the
JSON, not encoded in the directory structure — this keeps the filesystem layout
simple and avoids path-sanitization edge cases. Filtering by route is done by
scanning and deserializing files.

Note: The issue mentions `fixtures/<route>/<method>_<path_hash>.json` but this
creates complications: route strings need sanitization for filesystem safety,
renames become multi-directory moves, and listing requires recursive traversal.
Since v1 is not expected to handle millions of fixtures, a flat layout keyed by
tape ID is simpler and safer. If hierarchical storage becomes necessary, it can
be added as an option in a future ADR.

**File format:** Each JSON file contains a single `Tape` struct serialized with
`json.MarshalIndent` (2-space indent) for human readability. The `Body` fields
in `RecordedReq` and `RecordedResp` are `[]byte`, which `encoding/json`
automatically base64-encodes/decodes.

**Atomic writes:** `Save` writes to a temporary file in the same directory, then
renames to the target path. This prevents partial writes on crash.

**Error wrapping:** All errors are wrapped with context:
- `fmt.Errorf("httptape: filestore save %s: %w", id, err)`
- `fmt.Errorf("httptape: filestore load %s: %w", id, err)`

For `Load` and `Delete`, if the file does not exist (`os.IsNotExist`), the error
wraps `ErrNotFound`.

#### File layout

| File | Contents | New/Modified |
|---|---|---|
| `tape.go` | `Tape`, `RecordedReq`, `RecordedResp`, `NewTape`, `BodyHashFromBytes`, unexported `newUUID` | New |
| `store.go` | `Store` interface, `Filter` type, `ErrNotFound` sentinel | New |
| `store_memory.go` | `MemoryStore`, `MemoryStoreOption`, `NewMemoryStore` | New |
| `store_memory_test.go` | Table-driven tests for all `MemoryStore` methods | New |
| `store_file.go` | `FileStore`, `FileStoreOption`, `WithDirectory`, `NewFileStore` | New |
| `store_file_test.go` | Table-driven tests for all `FileStore` methods | New |

#### Sequence: Save and Load flow

1. Caller creates a `Tape` via `NewTape(route, req, resp)` — ID and timestamp auto-generated.
2. Caller calls `store.Save(ctx, tape)`.
3. **MemoryStore**: acquires write lock, deep-copies tape into internal map, releases lock.
4. **FileStore**: acquires write lock, marshals tape to JSON, writes to temp file, renames to `<id>.json`, releases lock.
5. Caller calls `store.Load(ctx, id)`.
6. **MemoryStore**: acquires read lock, looks up by ID, deep-copies result, releases lock. Returns `ErrNotFound` if missing.
7. **FileStore**: acquires read lock, reads `<id>.json`, unmarshals, releases lock. Returns `ErrNotFound` if file missing.

#### Error cases

| Operation | Error condition | Behavior |
|---|---|---|
| `Save` | Context cancelled | Return `ctx.Err()` (wrapped) |
| `Save` (FileStore) | Disk full / permission denied | Return wrapped `os.` error |
| `Save` (FileStore) | JSON marshal failure | Return wrapped error (should not happen with valid types) |
| `Load` | Tape ID not found | Return error wrapping `ErrNotFound` |
| `Load` (FileStore) | Corrupt JSON file | Return wrapped unmarshal error |
| `List` | No tapes match filter | Return empty slice `[]Tape{}`, nil error |
| `Delete` | Tape ID not found | Return error wrapping `ErrNotFound` |
| `Delete` (FileStore) | Permission denied | Return wrapped `os.` error |
| `NewFileStore` | Cannot create base directory | Return error |

#### Test strategy

**`store_memory_test.go`** — table-driven tests:
- `TestMemoryStore_Save` — save a tape, verify it can be loaded back
- `TestMemoryStore_Load_NotFound` — load non-existent ID, verify `errors.Is(err, ErrNotFound)`
- `TestMemoryStore_List_All` — save several tapes, list with empty filter, verify all returned
- `TestMemoryStore_List_ByRoute` — save tapes with different routes, filter by route
- `TestMemoryStore_List_ByMethod` — filter by HTTP method
- `TestMemoryStore_Delete` — save then delete, verify load returns `ErrNotFound`
- `TestMemoryStore_Delete_NotFound` — delete non-existent ID, verify `ErrNotFound`
- `TestMemoryStore_Save_Overwrite` — save same ID twice, verify second version persisted
- `TestMemoryStore_Isolation` — save a tape, mutate the original, verify store copy unchanged

**`store_file_test.go`** — table-driven tests using `t.TempDir()`:
- Same logical test cases as MemoryStore (both implement the same interface)
- `TestFileStore_NewFileStore_CreatesDir` — verify base directory created
- `TestFileStore_AtomicWrite` — verify temp file is used (check no partial files on error)
- `TestFileStore_JSONFormat` — save a tape, read raw file, verify valid JSON with expected fields
- `TestFileStore_Persistence` — save with one FileStore instance, load with a new instance pointing to the same directory

**Shared test helper** (unexported, in one of the test files):
- `makeTape(route, method, url string) Tape` — creates a minimal valid tape for testing

Both test files use `context.Background()` for the context parameter.

#### Consequences

- **Simple flat file layout**: Choosing `<id>.json` over route-partitioned directories
  simplifies the implementation but means `List` with a route filter must scan all files.
  Acceptable for v1 volumes. Can be revisited if performance requires it.
- **Value semantics for Tape**: Using `Tape` (not `*Tape`) in the Store interface
  avoids nil pointer issues and makes immutability intent clear. The trade-off is
  copying on Save/Load, but tapes are small (KB range).
- **Deep copy in MemoryStore**: Required to prevent callers from mutating stored data
  through retained references to header maps or body slices.
- **UUID v4 without external dep**: Small internal implementation using `crypto/rand`.
  Sufficient for tape IDs. Not suitable for high-throughput ID generation, but tape
  creation is not a hot path.
- **context.Context on all Store methods**: Follows Go best practices for I/O interfaces.
  MemoryStore can largely ignore it, but FileStore benefits from cancellation support.

---

### ADR-2: Recorder — RoundTripper wrapper

**Date**: 2026-03-30
**Issue**: #28
**Status**: Accepted

#### Context

httptape needs a transparent `http.RoundTripper` wrapper that intercepts HTTP calls,
captures request/response pairs as `Tape` values, and persists them to a `Store`. This
is the primary entry point for recording traffic. The recorder must be invisible to the
caller — it must not modify the request or response in any observable way.

Key requirements from the issue and locked decisions:
- Async recording by default (L-10): the hot path (RoundTrip) must not block on store writes.
- Functional options (L-12) for all configuration.
- No panics (L-11), no global state, stdlib only (L-04).
- Must accept a future `Sanitizer` interface without implementing it (sanitization is a later milestone).

#### Decision

##### Types

**Recorder** (`recorder.go`):

```go
// Recorder is an http.RoundTripper that transparently records HTTP interactions
// to a Store. It delegates the actual HTTP call to an inner transport and captures
// the request/response pair as a Tape.
//
// By default, recording is asynchronous — tapes are sent to a buffered channel
// and a background goroutine drains them to the store. Call Close to flush
// pending recordings and release resources.
type Recorder struct {
    transport  http.RoundTripper   // inner transport to delegate to
    store      Store               // where to persist tapes
    route      string              // logical route label for all tapes produced
    sanitizer  Sanitizer           // optional; may be nil (no-op if nil)
    async      bool                // true = non-blocking writes via channel
    sampleRate float64             // 0.0–1.0; 1.0 = record everything
    randFloat  func() float64     // returns [0.0, 1.0); injectable for testing
    bufSize    int                 // channel buffer size (only used when async=true)
    onError    func(error)         // callback for async write errors; defaults to no-op

    // async internals
    tapeCh  chan Tape              // buffered channel for async mode
    done    chan struct{}          // closed when background goroutine exits
    closeOnce sync.Once           // ensures Close is idempotent
}
```

**Sanitizer** placeholder interface (`recorder.go`, above Recorder):

```go
// Sanitizer transforms a Tape before it is persisted, redacting or faking
// sensitive data. The Sanitizer implementation is defined in a later milestone;
// this interface is declared here so the Recorder can accept it.
type Sanitizer interface {
    // Sanitize transforms the given tape in place and returns it.
    // Implementations must not return a nil Tape for a non-nil input.
    Sanitize(Tape) Tape
}
```

Note: The `Sanitizer` interface is deliberately minimal — a single method that
transforms a Tape. It is declared in `recorder.go` rather than a separate file
because the Recorder is its only consumer in v1. If the Sanitizer grows, it can
be moved to `sanitizer.go` in a future ADR.

**RecorderOption** (`recorder.go`):

```go
// RecorderOption configures a Recorder.
type RecorderOption func(*Recorder)
```

##### Functions and methods

**Constructor:**

```go
// NewRecorder creates a new Recorder wrapping the given transport.
// If transport is nil, http.DefaultTransport is used.
//
// By default:
//   - async mode is enabled with a buffer size of 1024
//   - sample rate is 1.0 (record every request)
//   - route is "" (empty — caller should set via WithRoute)
//   - no sanitizer (tapes are stored as-is)
//   - errors during async writes are silently discarded
//
// The caller must call Close when done to flush pending recordings.
func NewRecorder(store Store, opts ...RecorderOption) *Recorder
```

The first required argument is `Store` (not transport) because the store is the
essential dependency — without it, recording has nowhere to go. The inner transport
is set via `WithTransport` or defaults to `http.DefaultTransport`.

**Functional options:**

```go
// WithTransport sets the inner http.RoundTripper. Defaults to http.DefaultTransport.
func WithTransport(rt http.RoundTripper) RecorderOption

// WithRoute sets the route label applied to all tapes created by this recorder.
func WithRoute(route string) RecorderOption

// WithSanitizer sets a Sanitizer to transform tapes before persistence.
func WithSanitizer(s Sanitizer) RecorderOption

// WithSampling sets the probabilistic sampling rate. Must be in [0.0, 1.0].
// 1.0 means record every request (default). 0.0 means record nothing.
// Values outside [0.0, 1.0] are clamped.
func WithSampling(rate float64) RecorderOption

// WithAsync controls whether recording is asynchronous (default: true).
// When true, tapes are sent to a buffered channel and written by a background
// goroutine. When false, tapes are written synchronously inside RoundTrip.
func WithAsync(enabled bool) RecorderOption

// WithBufferSize sets the channel buffer size for async mode. Defaults to 1024.
// Ignored when async is disabled. Must be >= 1; values < 1 are set to 1.
func WithBufferSize(size int) RecorderOption

// WithOnError sets a callback invoked when an async store write fails.
// The callback is called from the background goroutine, so it must be
// safe for concurrent use. Defaults to a no-op (errors are discarded).
func WithOnError(fn func(error)) RecorderOption
```

**RoundTrip method (implements http.RoundTripper):**

```go
// RoundTrip executes the HTTP request via the inner transport, captures the
// request and response as a Tape, and writes it to the store (synchronously
// or asynchronously depending on configuration).
//
// The request and response are never modified. The response body is fully read
// into memory and replaced with a new io.ReadCloser so the caller sees the
// complete body. This is the only observable side effect: the original
// response.Body is consumed and replaced.
//
// If recording fails (e.g., store error in synchronous mode), the original
// response is still returned — recording errors never affect the caller's
// HTTP flow.
//
// Sampling: if a random float is >= sampleRate, the request is passed through
// without recording.
func (r *Recorder) RoundTrip(req *http.Request) (*http.Response, error)
```

**Close method:**

```go
// Close flushes all pending asynchronous recordings and waits for the
// background goroutine to finish. It is safe to call multiple times.
// After Close returns, no more tapes will be written.
//
// In synchronous mode, Close is a no-op.
func (r *Recorder) Close() error
```

Close returns error to satisfy common closer patterns, but always returns nil
in the current design. The signature leaves room for future cleanup that could fail.

##### Body capture strategy

The response body must be fully read so it can be stored in the Tape, but the
caller also needs to read it. The approach:

1. Read the entire `resp.Body` into a `[]byte` using `io.ReadAll`.
2. Close the original `resp.Body`.
3. Replace `resp.Body` with `io.NopCloser(bytes.NewReader(bodyBytes))`.
4. Use the `[]byte` for the Tape's `RecordedResp.Body`.

For the request body, the same drain-and-replace strategy is used:

1. If `req.Body` is non-nil, read it into `[]byte` using `io.ReadAll`.
2. Close the original `req.Body`.
3. Replace `req.Body` with `io.NopCloser(bytes.NewReader(bodyBytes))` so the
   inner transport can still read it.
4. Also set `req.GetBody` to return a new reader over the same bytes (supports retries).

The request body is captured *before* calling the inner transport's RoundTrip.
The response body is captured *after*.

##### Async mode internals

When `async` is true, `NewRecorder` starts a background goroutine:

```
NewRecorder:
    r.tapeCh = make(chan Tape, r.bufSize)
    r.done = make(chan struct{})
    go r.drain()

drain():
    for tape := range r.tapeCh {
        err := r.store.Save(context.Background(), tape)
        if err != nil && r.onError != nil {
            r.onError(err)
        }
    }
    close(r.done)

RoundTrip (async path):
    select {
    case r.tapeCh <- tape:
        // sent
    default:
        // channel full — drop tape, call onError if set
        if r.onError != nil {
            r.onError(fmt.Errorf("httptape: recorder buffer full, tape dropped"))
        }
    }

Close():
    r.closeOnce.Do(func() {
        close(r.tapeCh)
        <-r.done
    })
```

Key behaviors:
- The channel send in RoundTrip is **non-blocking** (`select` with `default`). If
  the buffer is full, the tape is dropped and `onError` is called. This ensures the
  hot path never blocks.
- `Close()` closes the channel, signaling the drain goroutine to finish. It then
  waits for the goroutine to complete (`<-r.done`), ensuring all buffered tapes are
  flushed.
- `Close()` is idempotent via `sync.Once`.

##### Synchronous mode

When `async` is false, `RoundTrip` calls `r.store.Save(req.Context(), tape)`
directly. If Save returns an error, it is **not** returned to the caller (recording
errors must not affect the HTTP flow). Instead, if `onError` is set, it is called.
The `tapeCh` and `done` channels are nil and the drain goroutine is not started.

##### Sampling

Before doing any body capture work, `RoundTrip` checks:

```go
if r.sampleRate < 1.0 && r.randFloat() >= r.sampleRate {
    return r.transport.RoundTrip(req)
}
```

This is an early return — if the request is not sampled, no body capture occurs
and the request is passed straight through with zero overhead.

The `randFloat` field defaults to `rand.Float64` (from `math/rand/v2` with no seed,
using the auto-seeded global source). It is injectable for deterministic testing.

##### Sanitizer integration

After building the Tape but before sending it to the store (or channel), the
recorder applies the sanitizer if non-nil:

```go
tape := NewTape(r.route, req, resp)
if r.sanitizer != nil {
    tape = r.sanitizer.Sanitize(tape)
}
// then send to store or channel
```

#### File layout

| File | Contents | New/Modified |
|---|---|---|
| `recorder.go` | `Sanitizer` interface, `Recorder` struct, `RecorderOption` type, `NewRecorder`, `WithTransport`, `WithRoute`, `WithSanitizer`, `WithSampling`, `WithAsync`, `WithBufferSize`, `WithOnError`, `RoundTrip`, `Close`, unexported `drain` method | New |
| `recorder_test.go` | Table-driven tests using `net/http/httptest` | New |

No existing files are modified. The Recorder depends on types from `tape.go` and
`store.go` (ADR-1), which will exist by the time this is implemented.

#### Sequence: Recording flow (async, default)

1. Caller creates a `Recorder` via `NewRecorder(store, opts...)`.
2. `NewRecorder` applies defaults, applies options, starts the drain goroutine.
3. Caller sets `client.Transport = recorder`.
4. Caller calls `client.Do(req)` (or `client.Get`, etc.).
5. `Recorder.RoundTrip(req)` is called.
6. Sampling check: if `randFloat() >= sampleRate`, delegate to inner transport directly (skip to step 13).
7. Capture request body: read `req.Body` into `[]byte`, replace `req.Body` with a new reader.
8. Call `r.transport.RoundTrip(req)` — the actual HTTP call.
9. If the inner transport returns an error, return the error immediately (no tape created for failed transports).
10. Capture response body: read `resp.Body` into `[]byte`, replace `resp.Body` with a new reader.
11. Build a `Tape` via `NewTape(r.route, recordedReq, recordedResp)`.
12. Apply sanitizer if non-nil: `tape = r.sanitizer.Sanitize(tape)`.
13. Send tape to `r.tapeCh` (non-blocking). If full, drop and call `onError`.
14. Return `resp, nil` to the caller.
15. Background goroutine receives tape from channel, calls `store.Save(ctx, tape)`.
16. When caller is done, calls `recorder.Close()`.
17. `Close` closes `tapeCh`, goroutine drains remaining tapes, closes `done`.
18. `Close` blocks until `done` is closed, then returns.

#### Error cases

| Scenario | Behavior |
|---|---|
| Inner transport returns error | Return error to caller. No tape created. |
| Request body read fails | Return error to caller. No tape created. Do not call inner transport. |
| Response body read fails | Return response with error to caller. No tape created. Response body is left in whatever state it was. |
| Store.Save fails (async) | Call `onError` callback if set. Tape is lost. Caller is unaffected. |
| Store.Save fails (sync) | Call `onError` callback if set. Return original response to caller. Recording error is not propagated. |
| Channel full (async) | Tape is dropped. Call `onError` if set. Caller is unaffected. |
| Close called multiple times | Second and subsequent calls are no-ops (sync.Once). |
| RoundTrip called after Close | In async mode, the channel is closed, so the send will either panic or be skipped. To handle this safely, RoundTrip checks a closed flag (atomic bool set in Close) and falls back to synchronous save or skip. |
| nil Store passed to NewRecorder | NewRecorder should require a non-nil store. Return a Recorder that passes through without recording (defensive). Document this in godoc. |

**Handling RoundTrip-after-Close:** An `atomic.Bool` field `closed` is set to true
inside `Close()` before closing the channel. `RoundTrip` checks this flag; if true,
it skips recording entirely and delegates directly to the inner transport.

```go
type Recorder struct {
    // ... other fields ...
    closed atomic.Bool
}

// In RoundTrip:
if r.closed.Load() {
    return r.transport.RoundTrip(req)
}

// In Close:
r.closeOnce.Do(func() {
    r.closed.Store(true)
    close(r.tapeCh)
    <-r.done
})
```

#### Test strategy

**`recorder_test.go`** — all tests use `net/http/httptest.NewServer` as the upstream server and `MemoryStore` as the store.

| Test | What it verifies |
|---|---|
| `TestRecorder_BasicRecording` | Wrap a transport, make a GET request, verify a tape is saved to the store with correct method, URL, status code, response body. |
| `TestRecorder_RequestBodyCapture` | POST with a body, verify the tape's request body and body hash are correct, and the upstream server received the full body. |
| `TestRecorder_TransparentPassthrough` | Verify the response returned to the caller is byte-identical to what the upstream server sent (status, headers, body). |
| `TestRecorder_AsyncMode` | Make a request, verify the tape appears in the store after calling Close (not necessarily before). |
| `TestRecorder_SyncMode` | Create recorder with `WithAsync(false)`, make a request, verify the tape is in the store immediately after RoundTrip returns. |
| `TestRecorder_Sampling` | Inject a deterministic `randFloat` that returns 0.5. Set sampling to 0.3. Verify request is NOT recorded. Set sampling to 0.7. Verify request IS recorded. |
| `TestRecorder_SamplingZero` | Sampling at 0.0 records nothing. |
| `TestRecorder_SamplingOne` | Sampling at 1.0 records everything (default). |
| `TestRecorder_GracefulShutdown` | Make multiple requests, call Close, verify all tapes are in the store. |
| `TestRecorder_CloseIdempotent` | Call Close twice, verify no panic. |
| `TestRecorder_TransportError` | Inner transport returns an error (e.g., connection refused). Verify error is returned to caller, no tape is saved. |
| `TestRecorder_OnErrorCallback` | Use a store that returns errors on Save. Verify onError callback is invoked. |
| `TestRecorder_BufferFull` | Set buffer size to 1, block the store's Save (e.g., with a channel), fill the buffer, verify onError is called with buffer-full message. |
| `TestRecorder_NilTransportDefaultsToDefault` | Create recorder without WithTransport, verify it uses http.DefaultTransport. |
| `TestRecorder_Route` | Set route via WithRoute, verify tapes have the correct route. |
| `TestRecorder_RoundTripAfterClose` | Call Close, then RoundTrip. Verify request still works (passthrough) but no tape is recorded. |

**Test helpers** (unexported, in `recorder_test.go`):
- `newTestServer(handler http.HandlerFunc) *httptest.Server` — creates a test HTTP server.
- `waitForTapes(store *MemoryStore, count int, timeout time.Duration) ([]Tape, error)` — polls the store until the expected number of tapes appear or timeout. Needed for async tests.

#### Consequences

- **Response body fully buffered**: The entire response body is read into memory.
  This is acceptable for typical API responses (KB to low-MB range). For very large
  responses (streaming, file downloads), this could be problematic. A future ADR
  could add a `WithMaxBodySize` option to cap body capture. For v1, this is documented
  as a known limitation.
- **Request body consumed and replaced**: The request body is read before forwarding.
  This means the original `req.Body` is consumed, but since `http.RoundTripper`
  implementations are expected to handle this (and we replace it), this is standard
  practice (see `httputil.DumpRequest`).
- **Non-blocking channel send**: In async mode, if the store is slow and the channel
  fills up, tapes are dropped. This is a deliberate trade-off: recording must never
  slow down the application. The `onError` callback lets users detect drops.
- **Sanitizer as interface on Recorder**: Declaring the `Sanitizer` interface in
  `recorder.go` couples the interface to its consumer. This follows the Go convention
  of defining interfaces where they are used, not where they are implemented. When
  the sanitizer implementation is built (Milestone 2), it will implement this interface
  without importing the recorder.
- **Store as required argument**: Making `Store` the first argument to `NewRecorder`
  (not an option) makes it impossible to create a recorder that silently discards
  everything. This is intentional — explicit is better than implicit.
- **math/rand/v2 for sampling**: Uses the auto-seeded global source. No need for
  a custom seed since sampling does not need reproducibility in production. The
  `randFloat` field allows deterministic testing.

---

### ADR-3: Server — Mock HTTP handler

**Date**: 2026-03-30
**Issue**: #29
**Status**: Accepted

#### Context

httptape needs a mock HTTP server that replays recorded fixtures. The server
receives an incoming HTTP request, finds a matching `Tape` in the `Store`, and
writes the recorded response back to the caller. This is the replay counterpart
to the `Recorder` (ADR-2).

The server must:
- Implement `http.Handler` so it can be used with `httptest.NewServer`, embedded
  in existing mux routers, or run standalone via `http.ListenAndServe`.
- Delegate matching logic to a `Matcher` interface (defined here, implemented in
  issue #30) so matching strategies are pluggable.
- Return a configurable fallback response when no tape matches.
- Follow all locked decisions: stdlib only, functional options, no panics, no
  global state, single flat package.

#### Decision

##### Matcher interface (`server.go`)

The `Matcher` interface is declared in `server.go` because the Server is its
primary consumer in v1. If the Matcher grows in complexity or gains additional
consumers, it can be extracted to `matcher.go` in a future ADR (issue #30 will
provide the implementation).

```go
// Matcher selects a Tape from a list of candidates that best matches
// the incoming HTTP request. Implementations define the matching strategy
// (exact, fuzzy, regex, etc.).
type Matcher interface {
    // Match returns the best-matching Tape for the given request.
    // If no tape matches, it returns false as the second return value.
    // The candidates slice is never nil but may be empty.
    // Implementations must not modify the request or the candidate tapes.
    Match(req *http.Request, candidates []Tape) (Tape, bool)
}
```

Design notes on the `Match` signature:
- Accepts `*http.Request` (not `RecordedReq`) so matchers have access to the
  full stdlib request including URL parsing, Host, TLS state, etc.
- Accepts `[]Tape` candidates rather than querying the Store directly. This
  keeps the Matcher pure (no I/O) and lets the Server control store access,
  filtering, and caching.
- Returns `(Tape, bool)` rather than `(Tape, error)` because "no match" is not
  an error condition — it is a normal outcome. If a matcher encounters an
  internal error, it should return `(Tape{}, false)` and log via its own
  mechanism. This keeps the Server's error handling simple.

##### MatcherFunc adapter (`server.go`)

```go
// MatcherFunc is an adapter to allow the use of ordinary functions as Matchers.
type MatcherFunc func(req *http.Request, candidates []Tape) (Tape, bool)

// Match calls f(req, candidates).
func (f MatcherFunc) Match(req *http.Request, candidates []Tape) (Tape, bool) {
    return f(req, candidates)
}
```

This follows the `http.HandlerFunc` pattern from the stdlib and lets callers
provide a simple function instead of implementing the full interface.

##### ExactMatcher default (`server.go`)

> **Note (ADR-4):** `ExactMatcher` is no longer the constructor default.
> `DefaultMatcher()` (from ADR-4) replaced it as the server default. `ExactMatcher`
> remains available for backward compatibility. Both now live in `matcher.go`.

A minimal built-in matcher is provided so the Server is usable out of the box
without requiring issue #30's full matcher implementation:

```go
// ExactMatcher is a simple Matcher that matches requests by HTTP method and
// URL path. It returns the first candidate whose method and URL path are
// equal to the incoming request's method and URL path.
//
// This is intentionally minimal. For advanced matching (headers, body,
// query params, regex), use the matchers from issue #30.
func ExactMatcher() Matcher {
    return MatcherFunc(func(req *http.Request, candidates []Tape) (Tape, bool) {
        for _, t := range candidates {
            if t.Request.Method == req.Method && t.Request.URL == req.URL.Path {
                return t, true
            }
        }
        return Tape{}, false
    })
}
```

Note: `ExactMatcher` compares `t.Request.URL` (stored as a string in the Tape)
against `req.URL.Path`. The stored URL in a Tape is the full URL string, but
for mock server use the relevant part is typically the path. The full matcher
from issue #30 will handle URL parsing, query string matching, and host
comparison properly. `ExactMatcher` is a pragmatic default.

##### Server struct (`server.go`)

```go
// Server is an http.Handler that replays recorded HTTP interactions.
// It receives incoming requests, finds a matching Tape via a Matcher,
// and writes the recorded response. If no match is found, it returns
// a configurable fallback status code.
//
// Server is safe for concurrent use.
type Server struct {
    store          Store
    matcher        Matcher
    fallbackStatus int            // HTTP status when no tape matches
    fallbackBody   []byte         // response body when no tape matches
    onNoMatch      func(*http.Request) // optional callback when no tape matches
}
```

Fields explained:
- `store`: required dependency — where tapes live.
- `matcher`: required dependency — how to find the right tape. Defaults to
  `DefaultMatcher()` if not provided via options (updated by ADR-4).
- `fallbackStatus`: HTTP status code returned when no tape matches. Defaults
  to `http.StatusNotFound` (404). Configurable via `WithFallbackStatus`.
- `fallbackBody`: body bytes written in the fallback response. Defaults to
  `[]byte("httptape: no matching tape found")`. Configurable via
  `WithFallbackBody`.
- `onNoMatch`: optional callback invoked when no tape matches, before the
  fallback response is written. Useful for logging or debugging. The callback
  receives the unmatched request. Must be safe for concurrent use.

##### ServerOption (`server.go`)

```go
// ServerOption configures a Server.
type ServerOption func(*Server)
```

##### Constructor

```go
// NewServer creates a new Server that replays tapes from the given store.
//
// By default:
//   - matcher is DefaultMatcher() (matches by method + URL path with scoring;
//     updated by ADR-4, was ExactMatcher() originally)
//   - fallback status is 404 Not Found
//   - fallback body is "httptape: no matching tape found"
//   - no onNoMatch callback
//
// The store must not be nil. Passing a nil store is a programming error
// and will panic.
func NewServer(store Store, opts ...ServerOption) *Server
```

`Store` is the first required argument (same pattern as `NewRecorder` in ADR-2).
The matcher is an option because a sensible default exists.

##### Functional options

```go
// WithMatcher sets the Matcher used to find tapes for incoming requests.
// If not set, DefaultMatcher() is used (updated by ADR-4).
func WithMatcher(m Matcher) ServerOption

// WithFallbackStatus sets the HTTP status code returned when no tape matches
// the incoming request. Defaults to 404.
func WithFallbackStatus(code int) ServerOption

// WithFallbackBody sets the response body returned when no tape matches.
// Defaults to "httptape: no matching tape found".
func WithFallbackBody(body []byte) ServerOption

// WithOnNoMatch sets a callback invoked when no tape matches an incoming
// request. The callback receives the unmatched request and is called before
// the fallback response is written. It must be safe for concurrent use.
func WithOnNoMatch(fn func(*http.Request)) ServerOption
```

Note on naming: these option functions are in the `ServerOption` namespace
(they return `ServerOption`). The `WithMatcher` name does not collide with
`WithTransport` or `WithRoute` from ADR-2 because Go's type system
distinguishes `ServerOption` from `RecorderOption`. However, for clarity
in godoc, each option's documentation states which constructor it applies to.

##### ServeHTTP method (implements http.Handler)

```go
// ServeHTTP handles an incoming HTTP request by finding a matching tape
// and writing the recorded response. If no tape matches, it writes the
// configured fallback response.
//
// The method is safe for concurrent use.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request)
```

##### Sequence: Request replay flow

1. `ServeHTTP` is called with an incoming request.
2. If `s.store` is nil (defensive — only if NewServer was called with nil),
   write 500 with body `"httptape: server misconfigured (nil store)"` and return.
3. Call `s.store.List(r.Context(), Filter{})` to retrieve all tapes.
   - If `List` returns an error, write 500 with body
     `"httptape: store error"` and return. Do not leak internal error details
     to the HTTP response.
4. Call `s.matcher.Match(r, tapes)` with the incoming request and the
   candidate tapes.
5. If `Match` returns `false` (no match):
   a. If `s.onNoMatch` is non-nil, call `s.onNoMatch(r)`.
   b. Write `s.fallbackStatus` and `s.fallbackBody` to the response.
   c. Return.
6. If `Match` returns a tape:
   a. Copy the tape's `Response.Headers` to `w.Header()`.
   b. Call `w.WriteHeader(tape.Response.StatusCode)`.
   c. Write `tape.Response.Body` to `w`.
   d. Return.

Step 6 detail — header copying: iterate over `tape.Response.Headers` and set
each header on the response writer. Use `w.Header().Set(key, value)` for
single-value headers. For multi-value headers (multiple values for the same
key), use `w.Header()[key] = values` to copy the slice directly. This ensures
recorded multi-value headers (like `Set-Cookie`) are replayed faithfully.

Step 3 optimization note: listing all tapes on every request is acceptable for
v1. For production use with large stores, a future ADR could add:
- A `WithFilter` option to scope the Server to a specific route/method
- Caching with TTL
- Direct integration with the Matcher to push filtering into the Store query

For v1, simplicity wins — the Server is primarily used in tests with small
fixture sets.

##### Handling nil store (defensive)

Rather than panicking, if `store` is nil, `NewServer` returns a valid `*Server`
whose `ServeHTTP` always writes a 500 error. This follows the "no panics" rule.

```go
func NewServer(store Store, opts ...ServerOption) *Server {
    s := &Server{
        store:          store,
        matcher:        ExactMatcher(),
        fallbackStatus: http.StatusNotFound,
        fallbackBody:   []byte("httptape: no matching tape found"),
    }
    for _, opt := range opts {
        opt(s)
    }
    return s
}
```

The nil-store check happens in `ServeHTTP`, not in the constructor, so the
constructor always returns a usable (if degraded) server.

#### File layout

| File | Contents | New/Modified |
|---|---|---|
| `server.go` | `Matcher` interface, `MatcherFunc` adapter, `ExactMatcher` function, `Server` struct, `ServerOption` type, `NewServer`, `WithMatcher`, `WithFallbackStatus`, `WithFallbackBody`, `WithOnNoMatch`, `ServeHTTP` | New |
| `server_test.go` | Table-driven tests using `net/http/httptest` and `MemoryStore` | New |

No existing files are modified. The Server depends on types from `tape.go` and
`store.go` (ADR-1).

#### Error cases

| Scenario | Behavior |
|---|---|
| Nil store passed to NewServer | Server responds with 500 on every request. No panic. |
| Store.List returns error | Respond with 500 and generic error body. Do not leak internal error details. |
| Matcher returns no match | Call onNoMatch callback (if set), then write fallbackStatus and fallbackBody. |
| Matcher returns a match | Write recorded response (headers, status, body). |
| Tape response body is nil | Write headers and status code with empty body. |
| Tape response headers are nil | Write status code and body with no additional headers. |
| Context cancelled during List | Store.List returns context error. Server writes 500. |
| Response write fails (client disconnect) | Ignored — nothing the server can do. Standard http.Handler behavior. |

#### Test strategy

**`server_test.go`** — all tests use `httptest.NewServer` (or `httptest.NewRecorder` for handler-level tests) and `MemoryStore`.

| Test | What it verifies |
|---|---|
| `TestServer_BasicReplay` | Store a tape, send matching request, verify response status code, headers, and body match the tape. |
| `TestServer_ResponseHeaders` | Store a tape with multi-value headers (e.g., `Set-Cookie`), verify all header values are replayed. |
| `TestServer_NoMatch_DefaultFallback` | Send a request that matches no tape. Verify 404 status and default fallback body. |
| `TestServer_NoMatch_CustomFallback` | Configure custom fallback status (503) and body. Verify they are returned on no match. |
| `TestServer_NoMatch_Callback` | Set onNoMatch callback, send unmatched request, verify callback was called with the correct request. |
| `TestServer_NilStore` | Create server with nil store. Send a request. Verify 500 response. |
| `TestServer_StoreError` | Use a store that returns an error from List. Send a request. Verify 500 response. |
| `TestServer_CustomMatcher` | Provide a custom MatcherFunc that matches on a custom header. Verify it is used instead of ExactMatcher. |
| `TestServer_ExactMatcher_MethodMismatch` | Store a GET tape, send a POST to the same path. Verify no match (404). |
| `TestServer_ExactMatcher_PathMismatch` | Store a tape for `/foo`, send request to `/bar`. Verify no match (404). |
| `TestServer_EmptyStore` | Store has no tapes. Send a request. Verify 404 fallback. |
| `TestServer_NilResponseBody` | Store a tape with nil response body. Verify response is written with empty body and correct status. |
| `TestServer_ConcurrentRequests` | Send multiple concurrent requests using goroutines. Verify no races (run with `-race`). |
| `TestServer_MatcherFunc` | Verify `MatcherFunc` adapter correctly delegates to the wrapped function. |
| `TestServer_WithHTTPTestServer` | Use `httptest.NewServer` with the Server as handler. Make real HTTP calls via `http.Client`. Verify end-to-end replay. |

**Test helpers** (unexported, in `server_test.go`):
- `storeTape(t *testing.T, store *MemoryStore, method, path string, status int, body string) Tape` — creates and saves a minimal tape to the store. Returns the saved tape for assertions.
- `failStore` — a Store implementation whose `List` always returns an error. Used for error-path testing.

All tests use `context.Background()` for the context parameter. Concurrent
tests use `t.Parallel()`. Race condition tests require `go test -race`.

#### Consequences

- **Matcher interface declared in server.go**: This follows the Go convention of
  defining interfaces at the point of use. When issue #30 implements advanced
  matchers, they will satisfy this interface without importing server-specific
  code. If the Matcher interface needs to be shared with other consumers in the
  future, it can be moved to its own file.
- **Full store scan on every request**: `ServeHTTP` calls `Store.List` with an
  empty filter on every request. This is simple but O(n) in the number of tapes.
  Acceptable for v1 where the server is used in tests with small fixture sets
  (typically <100 tapes). A future optimization could add a `WithFilter` option
  to scope the server to a specific route, or cache tapes with a TTL.
- **ExactMatcher is intentionally minimal**: It only matches on method + path.
  This is sufficient for basic use cases and serves as the default. Advanced
  matching (headers, query params, body, regex) is deferred to issue #30.
- **Fallback response is configurable**: Callers can customize both the status
  code and body for unmatched requests. The default 404 is sensible for test
  usage. For integration testing, a caller might prefer 502 (Bad Gateway) to
  distinguish "no tape" from "tape says 404".
- **onNoMatch callback enables observability**: Without this, callers have no
  way to know which requests are failing to match. This is critical for
  debugging fixture sets. The callback runs synchronously before the fallback
  response is written, so it can inspect the request but should not block.
- **No caching or indexing**: Every request triggers a full store scan and
  matcher run. This is the simplest correct implementation. Performance
  optimization is deferred to when it is needed (YAGNI).
- **MatcherFunc adapter**: Following `http.HandlerFunc` precedent, this lets
  users write quick inline matchers for tests without defining a named type.

---

### ADR-4: Matcher — Composable request-to-fixture matching

**Date**: 2026-03-30
**Issue**: #30
**Status**: Accepted

#### Context

The mock `Server` (ADR-3) delegates request matching to a `Matcher` interface
declared in `server.go`. ADR-3 provides only a minimal `ExactMatcher` that
compares method and URL path. Issue #30 requires a composable, extensible
matching system where users can combine match criteria (method, path, route,
query params, body hash) and where more specific matches win when multiple
tapes could match.

The `Matcher` interface from ADR-3 is:

```go
type Matcher interface {
    Match(req *http.Request, candidates []Tape) (Tape, bool)
}
```

This ADR designs the concrete implementations that live in `matcher.go`.

Key constraints:
- Single flat package, stdlib only (L-03, L-04).
- No panics, no global state (L-11).
- Progressive matching: simple cases stay simple, composable for power users (L-09).
- The `Matcher` interface stays in `server.go` (ADR-3). This ADR adds
  implementations in `matcher.go`.

#### Decision

##### Design overview: Criteria + Scoring

Rather than a chain-of-responsibility or decorator pattern, matching uses a
**criteria + scoring** approach. A `CompositeMatcher` holds a list of
`MatchCriterion` functions. For each candidate tape, it evaluates all criteria.
Each criterion returns a score (0 = no match, positive = match with weight).
If any criterion returns 0, the candidate is eliminated. The candidate with the
highest total score wins.

This design:
- Is simple to understand and test (each criterion is an independent function).
- Supports progressive matching: `DefaultMatcher` uses method + path. Power
  users add criteria for body hash, query params, etc.
- Naturally handles priority: more criteria matched = higher score. Criteria
  can also assign different weights to express priority.

##### MatchCriterion type (`matcher.go`)

```go
// MatchCriterion evaluates how well a candidate Tape matches an incoming
// request for a single dimension (method, path, body, etc.).
//
// It returns a score:
//   - 0 means the candidate does not match on this dimension (eliminates it).
//   - A positive value means the candidate matches, with higher values
//     indicating a stronger/more specific match.
//
// Implementations must not modify the request or the candidate tape.
type MatchCriterion func(req *http.Request, candidate Tape) int
```

Using `int` rather than `float64` for scores keeps things simple and avoids
floating-point comparison issues. Scores are added, so relative weights between
criteria are expressed as integer multiples.

##### Built-in criteria (`matcher.go`)

```go
// MatchMethod returns a MatchCriterion that requires the HTTP method to match.
// Returns score 1 on match, 0 on mismatch.
func MatchMethod() MatchCriterion

// MatchPath returns a MatchCriterion that requires the URL path to match.
// It compares the incoming request's URL.Path against the path component of
// the tape's stored URL. Returns score 2 on match, 0 on mismatch.
//
// The tape's URL is stored as a full URL string (e.g., "https://api.example.com/users?page=1").
// MatchPath parses it with url.Parse and compares only the Path component.
// If the tape's URL cannot be parsed, the criterion returns 0.
func MatchPath() MatchCriterion

// MatchRoute returns a MatchCriterion that requires the tape's Route field
// to equal a specific value. Returns score 1 on match, 0 on mismatch.
// If route is empty, the criterion always returns 1 (matches any tape).
func MatchRoute(route string) MatchCriterion

// MatchQueryParams returns a MatchCriterion that requires all query parameters
// from the incoming request to be present in the tape's stored URL with the
// same values. Extra parameters in the tape are allowed (subset match).
// Returns score 4 on match, 0 on mismatch.
//
// If the incoming request has no query parameters, this criterion always
// returns 4 (vacuously true — all zero params match).
//
// The tape's URL is parsed with url.Parse to extract query parameters.
// If parsing fails, the criterion returns 0.
func MatchQueryParams() MatchCriterion

// MatchBodyHash returns a MatchCriterion that requires the SHA-256 hash of
// the incoming request's body to match the tape's BodyHash field.
// Returns score 8 on match, 0 on mismatch.
//
// If the tape's BodyHash is empty (e.g., a GET request with no body), and
// the incoming request also has no body (or empty body), this is a match.
// If the tape has a BodyHash but the request has no body (or vice versa),
// this is a mismatch.
//
// The request body is read fully, then restored (replaced with a new reader
// over the same bytes) so subsequent handlers or criteria can read it again.
func MatchBodyHash() MatchCriterion
```

**Score weights rationale:**

| Criterion | Score | Rationale |
|---|---|---|
| `MatchMethod` | 1 | Low specificity — many tapes share a method. |
| `MatchPath` | 2 | More specific than method alone. |
| `MatchRoute` | 1 | Scoping label, similar specificity to method. |
| `MatchQueryParams` | 4 | Significantly narrows candidates. |
| `MatchBodyHash` | 8 | Most specific — uniquely identifies a request body. |

These weights form a natural priority: a tape matching on method + path + body
hash (score 11) always beats a tape matching on method + path only (score 3).
The weights are powers-of-two-ish to ensure each higher-specificity criterion
dominates all lower ones combined (body hash alone outscores method + path +
query params).

##### CompositeMatcher (`matcher.go`)

```go
// CompositeMatcher evaluates a list of MatchCriterion functions against
// candidate tapes and returns the highest-scoring match. If all criteria
// return a positive score for a candidate, the candidate's total score is
// the sum of all criterion scores. If any criterion returns 0 for a
// candidate, that candidate is eliminated.
//
// If no candidates survive all criteria, CompositeMatcher returns (Tape{}, false).
// If multiple candidates have the same highest score, the first one in the
// candidates slice wins (stable ordering).
type CompositeMatcher struct {
    criteria []MatchCriterion
}

// NewCompositeMatcher creates a CompositeMatcher with the given criteria.
// At least one criterion must be provided. If no criteria are given,
// the matcher matches nothing (returns false for all requests).
func NewCompositeMatcher(criteria ...MatchCriterion) *CompositeMatcher

// Match implements the Matcher interface.
func (m *CompositeMatcher) Match(req *http.Request, candidates []Tape) (Tape, bool)
```

Implementation of `Match`:

```
func (m *CompositeMatcher) Match(req *http.Request, candidates []Tape) (Tape, bool) {
    bestScore := 0
    bestIdx := -1

    for i, tape := range candidates {
        total := 0
        eliminated := false
        for _, criterion := range m.criteria {
            score := criterion(req, tape)
            if score == 0 {
                eliminated = true
                break
            }
            total += score
        }
        if eliminated {
            continue
        }
        if total > bestScore {
            bestScore = total
            bestIdx = i
        }
    }

    if bestIdx < 0 {
        return Tape{}, false
    }
    return candidates[bestIdx], true
}
```

Key behaviors:
- **Short-circuit elimination**: As soon as any criterion returns 0, the
  candidate is skipped. This avoids unnecessary work (e.g., no need to read
  the body hash if the method already mismatches).
- **Stable ordering**: On tie, the first candidate in the slice wins. The
  Server controls candidate ordering via `Store.List`.
- **Empty criteria**: If `m.criteria` is empty, no scores accumulate, so
  `bestScore` stays 0 and `bestIdx` stays -1, returning `false`. This is
  safe — a matcher with no criteria matches nothing.

##### DefaultMatcher (`matcher.go`)

```go
// DefaultMatcher returns a Matcher that matches on HTTP method and URL path.
// This covers the most common use case (exact method + path matching) and is
// the recommended default for the Server.
//
// It is equivalent to NewCompositeMatcher(MatchMethod(), MatchPath()).
func DefaultMatcher() *CompositeMatcher {
    return NewCompositeMatcher(MatchMethod(), MatchPath())
}
```

`DefaultMatcher` replaces ADR-3's `ExactMatcher` as the recommended default.
`ExactMatcher` remains in `server.go` for backward compatibility — it is a
simpler implementation that does not use the scoring system. Users who want
composability should use `DefaultMatcher` or build their own `CompositeMatcher`.

The `Server`'s default matcher (set in `NewServer`) should be updated to use
`DefaultMatcher()` instead of `ExactMatcher()` when `matcher.go` is
implemented. This change is made in the same PR as the matcher implementation.

##### MatchBodyHash body handling

`MatchBodyHash` needs to read the request body to compute its hash, but must
leave the body readable for any subsequent processing. The implementation:

```go
func MatchBodyHash() MatchCriterion {
    return func(req *http.Request, candidate Tape) int {
        // Compute hash of incoming request body.
        var reqHash string
        if req.Body != nil {
            bodyBytes, err := io.ReadAll(req.Body)
            if err != nil {
                return 0
            }
            req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
            reqHash = BodyHashFromBytes(bodyBytes)
        }

        // Compare with tape's stored hash.
        if candidate.Request.BodyHash == "" && reqHash == "" {
            return 8 // both empty — match
        }
        if candidate.Request.BodyHash == reqHash {
            return 8
        }
        return 0
    }
}
```

Note: The body is read and restored on every candidate evaluation. To avoid
reading the body N times (once per candidate), the `CompositeMatcher.Match`
method could pre-read the body once before iterating. However, this would
complicate the `MatchCriterion` interface by requiring a pre-processed
request context. For v1, body reading per candidate is acceptable because:
- `MatchBodyHash` is only used when the user opts in (not in `DefaultMatcher`).
- The body is typically small (KB range for API requests).
- After the first read, subsequent reads are from the in-memory
  `bytes.NewReader` buffer, so only the first read hits the original body.

Actually, a more careful analysis: after the first candidate's criterion call
reads the body and replaces `req.Body` with a `bytes.NewReader`, all subsequent
candidates' calls will read from that in-memory reader. But `bytes.NewReader`
does not reset its read position automatically. Each call to `io.ReadAll` will
drain it and replace it, but the replacement creates a new `bytes.NewReader`
at position 0. So the pattern works correctly: each call reads from position 0,
drains it, and replaces it with a fresh reader.

##### URL parsing in MatchPath and MatchQueryParams

The tape's `RecordedReq.URL` is a full URL string (e.g.,
`"https://api.example.com/users?page=1"`). Both `MatchPath` and
`MatchQueryParams` need to parse this to extract components. Each criterion
parses the URL independently using `url.Parse`. If parsing fails, the
criterion returns 0 (no match).

For v1 this duplication is acceptable. A future optimization could pre-parse
tape URLs, but that would require modifying the `Tape` type or adding a
caching layer, which is out of scope.

#### File layout

| File | Contents | New/Modified |
|---|---|---|
| `matcher.go` | `MatchCriterion` type, `MatchMethod`, `MatchPath`, `MatchRoute`, `MatchQueryParams`, `MatchBodyHash`, `CompositeMatcher` struct, `NewCompositeMatcher`, `CompositeMatcher.Match`, `DefaultMatcher` | New |
| `matcher_test.go` | Table-driven tests for all criteria and CompositeMatcher | New |
| `server.go` | Update `NewServer` default to use `DefaultMatcher()` instead of `ExactMatcher()` | Modified |

The `Matcher` interface and `MatcherFunc` adapter remain in `server.go` (ADR-3).
The `ExactMatcher` function remains in `server.go` for backward compatibility.

#### Sequence: Request matching flow

1. `Server.ServeHTTP` calls `Store.List` to get candidate tapes.
2. `Server.ServeHTTP` calls `matcher.Match(req, candidates)`.
3. `CompositeMatcher.Match` iterates over candidates.
4. For each candidate, it evaluates all criteria in order:
   a. `MatchMethod`: compare `req.Method` with `candidate.Request.Method`.
   b. `MatchPath`: parse `candidate.Request.URL`, compare `.Path` with `req.URL.Path`.
   c. (Optional) `MatchQueryParams`: parse both URLs, compare query params.
   d. (Optional) `MatchBodyHash`: read request body, compute hash, compare.
5. If any criterion returns 0, skip this candidate (short-circuit).
6. Sum all criterion scores for surviving candidates.
7. Return the candidate with the highest total score.
8. If no candidates survive, return `(Tape{}, false)`.

#### Error cases

| Scenario | Behavior |
|---|---|
| No candidates provided (empty slice) | Return `(Tape{}, false)`. |
| No criteria in CompositeMatcher | Return `(Tape{}, false)` for all requests (nothing can score > 0). |
| Tape URL cannot be parsed | `MatchPath` and `MatchQueryParams` return 0 for that candidate. Other candidates still evaluated. |
| Request body read fails in MatchBodyHash | Return 0 for that candidate. Body left in whatever state. |
| Multiple candidates with same score | First candidate in slice wins (stable). |
| All candidates eliminated | Return `(Tape{}, false)`. |
| MatchRoute with empty route string | Criterion always returns 1 (matches any tape — acts as a no-op filter). |
| MatchQueryParams with no query params in request | Returns 4 (vacuously true — all zero params are present in any tape). |
| MatchBodyHash with nil body in both request and tape | Returns 8 (both empty — match). |
| MatchBodyHash with nil body in request but non-empty hash in tape | Returns 0 (mismatch). |
| MatchBodyHash with body in request but empty hash in tape | Returns 0 (mismatch). |

#### Test strategy

**`matcher_test.go`** — all tests are table-driven.

**Individual criterion tests:**

| Test | What it verifies |
|---|---|
| `TestMatchMethod` | Returns 1 when methods match (case-sensitive), 0 when they differ. |
| `TestMatchPath` | Returns 2 when paths match, 0 when they differ. Tests with full URLs in tape (scheme, host, path, query). |
| `TestMatchPath_UnparsableURL` | Tape URL is garbage, returns 0. |
| `TestMatchRoute` | Returns 1 when route matches, 0 when it differs. |
| `TestMatchRoute_EmptyFilter` | `MatchRoute("")` returns 1 for any tape (no-op). |
| `TestMatchQueryParams` | Returns 4 when all request query params are in the tape. Tests subset matching (extra tape params ok). |
| `TestMatchQueryParams_NoRequestParams` | Returns 4 (vacuously true). |
| `TestMatchQueryParams_Missing` | Tape is missing a query param from the request, returns 0. |
| `TestMatchQueryParams_UnparsableURL` | Tape URL is garbage, returns 0. |
| `TestMatchBodyHash_Match` | Request body hash matches tape hash, returns 8. |
| `TestMatchBodyHash_Mismatch` | Different bodies, returns 0. |
| `TestMatchBodyHash_BothEmpty` | No body in request, empty hash in tape, returns 8. |
| `TestMatchBodyHash_RequestEmpty_TapeNot` | No request body but tape has hash, returns 0. |
| `TestMatchBodyHash_RequestNotEmpty_TapeEmpty` | Request has body but tape hash is empty, returns 0. |
| `TestMatchBodyHash_BodyRestored` | After criterion runs, request body is still readable (read it again and get same bytes). |

**CompositeMatcher tests:**

| Test | What it verifies |
|---|---|
| `TestCompositeMatcher_DefaultMatcher` | Method + path matching selects the correct tape from multiple candidates. |
| `TestCompositeMatcher_NoCandidates` | Empty candidates slice returns `(Tape{}, false)`. |
| `TestCompositeMatcher_NoCriteria` | No criteria configured, returns `(Tape{}, false)`. |
| `TestCompositeMatcher_NoMatch` | All candidates eliminated, returns `(Tape{}, false)`. |
| `TestCompositeMatcher_Priority` | Two tapes match on method+path. One also matches on body hash. The body-hash tape wins (higher score). |
| `TestCompositeMatcher_StableOrdering` | Two tapes with identical scores. First one in slice is returned. |
| `TestCompositeMatcher_ShortCircuit` | A criterion that always returns 0 prevents later criteria from being evaluated (verified via a tracking criterion). |
| `TestCompositeMatcher_FullComposition` | Build a matcher with all five criteria. Verify it selects the most specific tape from a mixed set. |

**Integration test with DefaultMatcher:**

| Test | What it verifies |
|---|---|
| `TestDefaultMatcher_BasicMatch` | GET /users matches a tape recorded for GET /users. |
| `TestDefaultMatcher_MethodMismatch` | GET /users does not match a tape recorded for POST /users. |
| `TestDefaultMatcher_PathMismatch` | GET /users does not match a tape recorded for GET /accounts. |
| `TestDefaultMatcher_MultipleMatches` | Multiple tapes for GET /users with different query params — first one wins (DefaultMatcher does not check query params). |

All tests use `httptest.NewRequest` to construct incoming requests and
manually construct `Tape` values for candidates. No `Store` or `Server`
dependency is needed — the Matcher is tested in isolation.

#### Consequences

- **Criteria + scoring is simple and extensible**: New criteria (regex path,
  header matching, fuzzy body) can be added in future issues as additional
  `MatchCriterion` functions without modifying existing code. Users compose
  them into a `CompositeMatcher`.
- **Score weights are hardcoded**: The scores (1, 2, 1, 4, 8) are baked into
  each criterion function. This is intentional for v1 — it avoids configuration
  complexity. If users need custom weights, a `WithWeight(criterion, weight)`
  wrapper can be added later.
- **MatchCriterion is a function type, not an interface**: This follows the
  pattern of `http.HandlerFunc` and keeps criterion implementations concise.
  A criterion is just a function — no struct, no constructor overhead.
- **Body read per candidate in MatchBodyHash**: After the first read, the body
  is an in-memory buffer, so repeated reads are cheap. For v1 with small
  fixture sets this is fine. A pre-read optimization can be added if profiling
  shows it matters.
- **DefaultMatcher replaces ExactMatcher as the recommended default**: The
  `Server` constructor will use `DefaultMatcher()`. `ExactMatcher()` remains
  available but is no longer the suggested starting point. The behavioral
  difference is minimal (both match method + path) but `DefaultMatcher` uses
  the scoring system, making it composable.
- **URL parsing per criterion**: Each criterion parses the tape URL
  independently. This is O(criteria * candidates) parsing. Acceptable for v1
  fixture set sizes. Can be optimized with a pre-parse step if needed.
- **MatchRoute takes a fixed route string**: Unlike the other criteria that
  compare request properties against tape properties, `MatchRoute` takes a
  fixed route string as a parameter. This is because the incoming HTTP request
  has no "route" concept — routes are a recording-time label. The caller
  configures the matcher with the route they want to scope to.

---

### ADR-5: Header redaction sanitizer

**Date**: 2026-03-30
**Issue**: #31
**Status**: Accepted

#### Context

httptape's core differentiator is sanitize-on-write: sensitive data is redacted
before it ever touches disk, making recorded fixtures safe to commit to version
control. The `Sanitizer` interface was declared in `recorder.go` (ADR-2) as a
forward-looking placeholder:

```go
type Sanitizer interface {
    Sanitize(Tape) Tape
}
```

Issue #31 requires the first concrete implementation: header redaction. Header
values for sensitive headers (Authorization, Cookie, Set-Cookie, X-Api-Key, etc.)
must be replaced with `[REDACTED]` in both request and response headers before
the tape is persisted.

This ADR must:
- Implement the existing `Sanitizer` interface from `recorder.go` — no changes
  to the interface itself.
- Apply redaction to both request and response headers.
- Use case-insensitive header name matching (per HTTP spec / `http.Header`
  canonical form).
- Return a sanitized copy of the Tape, not mutate the original.
- Be composable — this is the foundation for #32 (body redaction) and #33
  (deterministic faking). The design must support stacking multiple sanitization
  strategies.
- Follow all locked decisions: stdlib only, functional options, no panics, no
  global state.

#### Decision

##### Design overview: Pipeline of SanitizeFunc

A `HeaderSanitizer` is too narrow for composability. Instead, we introduce a
general-purpose `Pipeline` type that holds an ordered list of sanitization
functions. Each function transforms a Tape and returns the result. The pipeline
implements the `Sanitizer` interface by applying all functions in order.

Header redaction is the first built-in `SanitizeFunc` provided via the
`RedactHeaders` constructor. Future sanitization strategies (#32 body redaction,
#33 deterministic faking) will be additional `SanitizeFunc` values added to the
same pipeline.

##### SanitizeFunc type (`sanitizer.go`)

```go
// SanitizeFunc is a function that transforms a Tape as part of a sanitization
// pipeline. Each function receives a Tape and returns a (possibly modified)
// copy. Implementations must not mutate the input Tape — they must copy any
// fields they modify.
type SanitizeFunc func(Tape) Tape
```

This follows the same pattern as `MatchCriterion` (ADR-4) — a function type
rather than an interface, keeping implementations concise.

##### Pipeline type (`sanitizer.go`)

```go
// Pipeline is a composable Sanitizer that applies an ordered sequence of
// SanitizeFunc transformations to a Tape. It implements the Sanitizer
// interface declared in recorder.go.
//
// Pipeline is safe for concurrent use — it is immutable after construction.
type Pipeline struct {
    funcs []SanitizeFunc
}
```

##### NewPipeline constructor (`sanitizer.go`)

```go
// NewPipeline creates a Pipeline with the given sanitization functions.
// Functions are applied in order: the output of each function is the input
// to the next.
//
// If no functions are provided, the pipeline is a no-op (returns tapes
// unchanged).
func NewPipeline(funcs ...SanitizeFunc) *Pipeline {
    cp := make([]SanitizeFunc, len(funcs))
    copy(cp, funcs)
    return &Pipeline{funcs: cp}
}
```

The constructor copies the input slice to prevent the caller from mutating
the pipeline's internals after construction.

##### Pipeline.Sanitize method (`sanitizer.go`)

```go
// Sanitize applies all sanitization functions in order to the given tape
// and returns the result. It implements the Sanitizer interface.
func (p *Pipeline) Sanitize(t Tape) Tape {
    for _, fn := range p.funcs {
        t = fn(t)
    }
    return t
}
```

##### Redacted constant (`sanitizer.go`)

```go
// Redacted is the replacement value used for redacted header values.
const Redacted = "[REDACTED]"
```

##### DefaultSensitiveHeaders (`sanitizer.go`)

```go
// DefaultSensitiveHeaders is the predefined set of HTTP header names that
// commonly contain sensitive data. These headers are redacted by default
// when using RedactHeaders without explicit header names.
//
// The set includes:
//   - Authorization — bearer tokens, basic auth credentials
//   - Cookie — session tokens, tracking identifiers
//   - Set-Cookie — server-set session tokens
//   - X-Api-Key — API key authentication
//   - Proxy-Authorization — proxy authentication credentials
//   - X-Forwarded-For — client IP addresses (PII)
var DefaultSensitiveHeaders = []string{
    "Authorization",
    "Cookie",
    "Set-Cookie",
    "X-Api-Key",
    "Proxy-Authorization",
    "X-Forwarded-For",
}
```

This is a `var` (not `const`) because Go does not support constant slices.
It is exported so users can inspect or extend it. Since the `RedactHeaders`
function copies the header names into its closure, mutating
`DefaultSensitiveHeaders` after calling `RedactHeaders` has no effect on
existing pipelines.

Design note: The issue specifies Authorization, Cookie, Set-Cookie, and
X-Api-Key. We add Proxy-Authorization and X-Forwarded-For as commonly
sensitive headers. These are safe additions — redacting a header that is not
present is a no-op.

##### RedactHeaders function (`sanitizer.go`)

```go
// RedactHeaders returns a SanitizeFunc that replaces the values of the
// specified HTTP headers with "[REDACTED]" in both request and response
// headers.
//
// Header name matching is case-insensitive (per HTTP spec). Internally,
// header names are canonicalized using http.CanonicalHeaderKey before
// comparison.
//
// If no header names are provided, DefaultSensitiveHeaders is used.
//
// The returned function does not mutate the input Tape — it clones the
// header maps before modification.
//
// Example:
//
//     // Redact default sensitive headers:
//     sanitizer := NewPipeline(RedactHeaders())
//
//     // Redact specific headers:
//     sanitizer := NewPipeline(RedactHeaders("Authorization", "X-Custom-Secret"))
//
//     // Use with Recorder:
//     recorder := NewRecorder(store, WithSanitizer(
//         NewPipeline(RedactHeaders()),
//     ))
func RedactHeaders(names ...string) SanitizeFunc
```

Implementation details:

```go
func RedactHeaders(names ...string) SanitizeFunc {
    if len(names) == 0 {
        names = DefaultSensitiveHeaders
    }

    // Build a set of canonical header names for O(1) lookup.
    sensitive := make(map[string]struct{}, len(names))
    for _, name := range names {
        sensitive[http.CanonicalHeaderKey(name)] = struct{}{}
    }

    return func(t Tape) Tape {
        t.Request.Headers = redactHeaderMap(t.Request.Headers, sensitive)
        t.Response.Headers = redactHeaderMap(t.Response.Headers, sensitive)
        return t
    }
}
```

##### redactHeaderMap helper (unexported, `sanitizer.go`)

```go
// redactHeaderMap returns a copy of the given headers with all values of
// sensitive headers replaced with Redacted. If headers is nil, returns nil.
func redactHeaderMap(headers http.Header, sensitive map[string]struct{}) http.Header {
    if headers == nil {
        return nil
    }
    result := headers.Clone()
    for name := range result {
        if _, ok := sensitive[http.CanonicalHeaderKey(name)]; ok {
            redacted := make([]string, len(result[name]))
            for i := range redacted {
                redacted[i] = Redacted
            }
            result[name] = redacted
        }
    }
    return result
}
```

Key behaviors:
- **Clone before modify**: `headers.Clone()` creates a deep copy of the header
  map. This ensures the original Tape's headers are never mutated.
- **Preserve multi-value headers**: If a header has multiple values (e.g.,
  multiple `Set-Cookie` entries), each value is replaced individually. The
  number of values is preserved so the structure remains consistent.
- **Canonical key comparison**: `http.CanonicalHeaderKey` is used on both the
  sensitive set (at construction time) and during iteration. Since `http.Header`
  stores keys in canonical form, the `range` loop yields canonical keys, and
  the lookup matches correctly.
- **Nil safety**: If the input headers map is nil, return nil (not an empty map).

##### Copy semantics for Tape

The `Sanitize` method and `SanitizeFunc` contract require returning a copy, not
mutating the input. For header redaction, the critical fields that are
reference types in Tape are:

- `Tape.Request.Headers` — `http.Header` (map): cloned in `redactHeaderMap`.
- `Tape.Response.Headers` — `http.Header` (map): cloned in `redactHeaderMap`.
- `Tape.Request.Body` — `[]byte`: not modified by header redaction; Go's value
  semantics for the Tape struct mean the slice header is copied but the
  underlying array is shared. This is safe because header redaction does not
  touch body bytes.
- `Tape.Response.Body` — `[]byte`: same as above.

Since `Tape` is a value type (not a pointer), passing it through a function
automatically copies all non-reference fields. The `RedactHeaders` function
only needs to explicitly clone the header maps it modifies. Body slices are
safe to share because they are not modified.

Future `SanitizeFunc` implementations that modify body bytes (#32) must copy
the body slice before mutating it.

##### Integration with Recorder

The `Pipeline` type implements `Sanitizer` (it has a `Sanitize(Tape) Tape`
method), so it plugs directly into the existing `WithSanitizer` option on
`Recorder`:

```go
recorder := NewRecorder(store,
    WithSanitizer(NewPipeline(
        RedactHeaders(), // redact default sensitive headers
    )),
)
```

No changes to `recorder.go` are needed. The `Sanitizer` interface remains
in `recorder.go` as declared in ADR-2.

#### File layout

| File | Contents | New/Modified |
|---|---|---|
| `sanitizer.go` | `SanitizeFunc` type, `Pipeline` struct, `NewPipeline`, `Pipeline.Sanitize`, `Redacted` constant, `DefaultSensitiveHeaders` variable, `RedactHeaders` function, unexported `redactHeaderMap` helper | New |
| `sanitizer_test.go` | Table-driven tests for `RedactHeaders`, `Pipeline`, and `redactHeaderMap` | New |

No existing files are modified. The `Sanitizer` interface stays in `recorder.go`.

#### Sequence: Sanitization flow

1. User creates a `Pipeline` via `NewPipeline(RedactHeaders())`.
2. User passes the pipeline to the Recorder via `WithSanitizer(pipeline)`.
3. `Recorder.RoundTrip` captures a request/response pair and builds a `Tape`.
4. `Recorder.RoundTrip` calls `pipeline.Sanitize(tape)`.
5. `Pipeline.Sanitize` iterates over its `SanitizeFunc` list.
6. `RedactHeaders`'s returned function is called with the Tape:
   a. Clone `tape.Request.Headers`, replace sensitive header values with `[REDACTED]`.
   b. Clone `tape.Response.Headers`, replace sensitive header values with `[REDACTED]`.
   c. Return the modified Tape (with cloned headers).
7. The sanitized Tape is sent to the Store (via channel or synchronous save).
8. The original `http.Response` returned to the caller is unaffected.

#### Error cases

| Scenario | Behavior |
|---|---|
| No header names passed to `RedactHeaders` | Uses `DefaultSensitiveHeaders`. Not an error. |
| Header name not present in tape | No-op for that header. The tape is returned unchanged for that header. |
| Nil request headers in tape | `redactHeaderMap` returns nil. No error. |
| Nil response headers in tape | `redactHeaderMap` returns nil. No error. |
| Empty pipeline (no funcs) | `Sanitize` returns the tape unchanged. No error. |
| Tape with multi-value header | Each value is replaced with `[REDACTED]`. Count preserved. |
| Non-canonical header name in `RedactHeaders` call | Canonicalized via `http.CanonicalHeaderKey`. Works correctly. |
| Concurrent calls to `Pipeline.Sanitize` | Safe — `Pipeline` is immutable after construction and `Sanitize` operates on value copies. |

#### Test strategy

**`sanitizer_test.go`** — all tests are table-driven.

**RedactHeaders tests:**

| Test | What it verifies |
|---|---|
| `TestRedactHeaders_SingleHeader` | Tape with `Authorization: Bearer token123` is redacted to `Authorization: [REDACTED]`. |
| `TestRedactHeaders_MultipleHeaders` | Redact both `Authorization` and `Cookie` in the same tape. Both are replaced. |
| `TestRedactHeaders_DefaultHeaders` | Call `RedactHeaders()` with no arguments. Verify all `DefaultSensitiveHeaders` are redacted in both request and response. |
| `TestRedactHeaders_CustomHeaders` | Call `RedactHeaders("X-Custom-Secret")`. Verify only `X-Custom-Secret` is redacted; `Authorization` is left intact. |
| `TestRedactHeaders_CaseInsensitive` | Call `RedactHeaders("authorization")` (lowercase). Verify it matches `Authorization` header in the tape. |
| `TestRedactHeaders_HeaderNotPresent` | Redact `Authorization` on a tape that has no `Authorization` header. Verify tape is returned unchanged. |
| `TestRedactHeaders_MultiValueHeader` | Tape with `Set-Cookie: a=1` and `Set-Cookie: b=2`. Both values replaced with `[REDACTED]`. |
| `TestRedactHeaders_BothRequestAndResponse` | Tape has `Authorization` in both request and response headers. Verify both are redacted. |
| `TestRedactHeaders_NilHeaders` | Tape with nil request headers and nil response headers. No panic, tape returned as-is. |
| `TestRedactHeaders_PreservesOtherHeaders` | Tape has `Content-Type` and `Authorization`. After redaction, `Content-Type` is unchanged. |
| `TestRedactHeaders_DoesNotMutateOriginal` | Create a tape, sanitize it, verify the original tape's headers are unchanged (copy semantics). |
| `TestRedactHeaders_PreservesBody` | Verify request and response bodies are not affected by header redaction. |

**Pipeline tests:**

| Test | What it verifies |
|---|---|
| `TestPipeline_Empty` | Empty pipeline returns tape unchanged. |
| `TestPipeline_SingleFunc` | Pipeline with one `RedactHeaders` func works correctly. |
| `TestPipeline_MultipleFuncs` | Pipeline with two funcs. First redacts `Authorization`, second redacts `Cookie`. Verify both are redacted in the output. |
| `TestPipeline_Ordering` | Pipeline applies functions in order. Use a func that reads a header value, then a func that redacts it. Verify the first func sees the un-redacted value. |
| `TestPipeline_ImplementsSanitizer` | Verify `*Pipeline` satisfies the `Sanitizer` interface (compile-time check via `var _ Sanitizer = (*Pipeline)(nil)`). |

**Test helpers** (unexported, in `sanitizer_test.go`):
- `makeTapeWithHeaders(reqHeaders, respHeaders http.Header) Tape` — creates a
  minimal Tape with the given request and response headers.

All tests use `context.Background()` where needed. No `Store`, `Recorder`, or
`Server` dependencies — the sanitizer is tested in isolation.

#### Consequences

- **Pipeline is the composition mechanism**: All sanitization strategies (#31
  header redaction, #32 body redaction, #33 deterministic faking) will be
  `SanitizeFunc` values composed into a `Pipeline`. This gives users full
  control over ordering and which sanitizations to apply.
- **SanitizeFunc is a function type, not an interface**: Consistent with
  `MatchCriterion` (ADR-4). Simple to implement, no ceremony.
- **Copy semantics are the caller's responsibility**: Each `SanitizeFunc` must
  clone any reference-type fields it modifies. For header redaction, this means
  cloning the header maps. For future body redaction, it means copying the body
  slice. This is documented in the `SanitizeFunc` godoc.
- **DefaultSensitiveHeaders is a var, not a const**: Go limitation. The var is
  exported for inspection but mutating it after pipeline construction has no
  effect on existing pipelines (because `RedactHeaders` copies the names into
  a closure). This is safe.
- **No changes to recorder.go**: The `Sanitizer` interface declared in ADR-2
  is satisfied by `*Pipeline` without modification. This validates the original
  interface design.
- **http.Header canonical form**: Using `http.CanonicalHeaderKey` ensures
  case-insensitive matching per the HTTP spec. Since `http.Header.Clone()`
  preserves canonical keys, the comparison is straightforward.
- **Multi-value header handling**: Each value in a multi-value header is
  replaced individually. This preserves the header structure (important for
  headers like `Set-Cookie` where the count of values may be meaningful to
  downstream tools).
- **Redacted constant is exported**: Users may need to check for redacted
  values in tests (e.g., `assert tape.Request.Headers.Get("Authorization") ==
  httptape.Redacted`). The constant also ensures consistency across future
  sanitization functions.

---

### ADR-6: Body path redaction sanitizer

**Date**: 2026-03-30
**Issue**: #32
**Status**: Accepted

#### Context

ADR-5 introduced the `Pipeline` / `SanitizeFunc` composition mechanism and
the first built-in sanitizer (`RedactHeaders`). Issue #32 extends sanitization
to JSON request and response bodies: callers specify JSONPath-like field paths,
and the matching values are replaced with type-aware redacted placeholders
before the tape is persisted.

Key constraints carried forward from ADR-5 and the project constitution:

- stdlib only -- no external JSONPath library.
- `SanitizeFunc` contract: return a copy, never mutate the input.
- No panics -- graceful degradation for non-JSON bodies.
- Composable: `RedactBodyPaths` is another `SanitizeFunc` that slots into
  the existing `Pipeline` alongside `RedactHeaders`.

#### Decision

##### Supported path syntax

A minimal JSONPath-like subset, sufficient for the vast majority of API
body redaction needs:

| Syntax | Meaning | Example |
|---|---|---|
| `$.field` | Top-level field | `$.api_key` |
| `$.nested.field` | Nested object field | `$.user.email` |
| `$.array[*].field` | Field in every array element | `$.users[*].ssn` |
| `$.a[*].b.c` | Nested field inside array elements | `$.items[*].meta.secret` |
| `$.a.b[*].c[*].d` | Multiple wildcard segments | `$.data.rows[*].tags[*].value` |

Unsupported (out of scope for v1):

- Recursive descent (`$..field`)
- Array index access (`$.array[0]`)
- Filter expressions (`$.array[?(@.active)]`)
- Bracket notation for field names (`$['field']`)

Paths that do not match any location in the body are silently ignored (no
error, no modification). This matches the behavior of `RedactHeaders` when
a specified header is not present.

##### Path parsing

Paths are parsed at construction time (when `RedactBodyPaths` is called),
not on every `Sanitize` invocation. This avoids repeated work and allows
early validation.

A path is split into a sequence of **segments**:

```go
// segment represents one step in a body path.
type segment struct {
    // key is the object field name to traverse.
    key string
    // wildcard is true when this segment includes [*], meaning "iterate all
    // array elements" before descending into key.
    wildcard bool
}
```

Parsing rules:

1. Path must start with `$.`. The `$` prefix and leading dot are stripped.
2. The remaining string is split on `.`, yielding raw tokens.
3. Each token is checked for a `[*]` suffix:
   - If present: `wildcard = true`, `key` = token with `[*]` stripped.
   - If absent: `wildcard = false`, `key` = token as-is.
4. Empty keys (e.g., `$..foo`, `$.foo.`) are rejected -- `RedactBodyPaths`
   silently skips invalid paths (no error, no panic). This keeps the API
   simple and forgiving.
5. Tokens containing `[` without a matching `[*]` suffix (e.g., `$.a[0]`)
   are also rejected silently.

The parsed representation is stored in the closure returned by
`RedactBodyPaths`, so parsing happens once.

```go
// parsedPath is a pre-parsed body redaction path.
type parsedPath struct {
    segments []segment
}

// parsePath parses a JSONPath-like string into a parsedPath.
// Returns ok=false if the path is invalid or unsupported.
func parsePath(path string) (parsedPath, bool)
```

##### Type-aware redaction values

When a path matches a value in the JSON body, the replacement depends on
the JSON type of the existing value:

| JSON type | Replacement | Go representation after `json.Unmarshal` |
|---|---|---|
| string | `"[REDACTED]"` | `string` -- replaced with `Redacted` constant |
| number | `0` | `float64` -- replaced with `float64(0)` |
| boolean | `false` | `bool` -- replaced with `false` |
| null | `null` (unchanged) | `nil` -- left as-is (null has no sensitive data) |
| object | not redacted | `map[string]any` -- left as-is (redact leaf fields, not containers) |
| array | not redacted | `[]any` -- left as-is (use `[*]` to reach into arrays) |

Rationale: Redacting containers (objects/arrays) with a scalar would break
the JSON schema, potentially causing replay failures. Callers should target
leaf fields. Null values carry no sensitive information.

##### RedactBodyPaths function (`sanitizer.go`)

```go
// RedactBodyPaths returns a SanitizeFunc that redacts fields within JSON
// request and response bodies at the specified paths.
//
// Paths use a JSONPath-like syntax:
//   - $.field             -- top-level field
//   - $.nested.field      -- nested field access
//   - $.array[*].field    -- field within each element of an array
//
// Redacted values are type-aware: strings become "[REDACTED]", numbers
// become 0, booleans become false. Null values, objects, and arrays are
// left unchanged (target leaf fields for redaction).
//
// If the body is not valid JSON, it is left unchanged (no error).
// If a path does not match any field in the body, it is silently skipped.
// Invalid or unsupported paths are silently ignored.
//
// The returned function does not mutate the input Tape -- it copies the
// body byte slices before modification.
//
// Example:
//
//     sanitizer := NewPipeline(
//         RedactHeaders(),
//         RedactBodyPaths("$.password", "$.user.ssn", "$.tokens[*].value"),
//     )
func RedactBodyPaths(paths ...string) SanitizeFunc
```

##### Implementation strategy

The implementation uses `encoding/json` from the stdlib to unmarshal the
body into `any` (which yields `map[string]any` for objects, `[]any` for
arrays, `float64` for numbers, `string` for strings, `bool` for booleans,
and `nil` for null). This generic representation allows traversal and
modification without knowing the concrete schema.

```go
func RedactBodyPaths(paths ...string) SanitizeFunc {
    // Parse all paths at construction time.
    var parsed []parsedPath
    for _, p := range paths {
        if pp, ok := parsePath(p); ok {
            parsed = append(parsed, pp)
        }
    }

    return func(t Tape) Tape {
        t.Request.Body = redactBodyFields(t.Request.Body, parsed)
        t.Response.Body = redactBodyFields(t.Response.Body, parsed)
        return t
    }
}
```

##### redactBodyFields helper (unexported, `sanitizer.go`)

```go
// redactBodyFields unmarshals the body as JSON, applies all path
// redactions, and re-marshals the result. If the body is nil, empty,
// or not valid JSON, it is returned unchanged.
func redactBodyFields(body []byte, paths []parsedPath) []byte
```

Algorithm:

1. If `body` is nil or empty, return it unchanged.
2. Attempt `json.Unmarshal(body, &data)` where `data` is `any`.
3. If unmarshal fails (not JSON), return body unchanged.
4. For each parsed path, call `redactAtPath(data, path.segments)`.
5. `json.Marshal(data)` the modified structure back to bytes.
6. If marshal fails (should not happen since we only modify leaf values
   to valid JSON types), return original body unchanged.

The re-marshaled output uses compact JSON (no indentation). This is
acceptable because:
- Fixtures are machine-generated, not hand-edited.
- Whitespace differences do not affect functionality.
- Consistent formatting makes byte-level comparison deterministic.

##### redactAtPath recursive helper (unexported, `sanitizer.go`)

```go
// redactAtPath recursively traverses the JSON structure following the
// given segments and redacts the leaf value. It modifies the data
// in-place (caller must ensure data is a fresh copy).
func redactAtPath(data any, segments []segment)
```

Traversal logic:

1. If `segments` is empty, return (nothing to do).
2. Take the first segment `seg` and the remaining `rest`.
3. If `seg.wildcard`:
   a. `data` must be a `map[string]any` containing key `seg.key`.
   b. The value at `seg.key` must be a `[]any`.
   c. For each element in the array:
      - If `rest` is empty: this is invalid (wildcard at leaf means the
        target is the array element itself, which is a container -- skip).
      - Otherwise: recurse with `redactAtPath(element, rest)`.
   d. If the value is not an array or the key is missing, skip silently.
4. If not `seg.wildcard`:
   a. `data` must be a `map[string]any`.
   b. If `rest` is empty: this is the leaf. Replace `data[seg.key]` with
      the type-appropriate redacted value (using `redactValue`).
   c. If `rest` is not empty: recurse with `redactAtPath(data[seg.key], rest)`.
   d. If `data` is not a map or key is missing, skip silently.

##### redactValue helper (unexported, `sanitizer.go`)

```go
// redactValue returns a type-aware redacted replacement for the given
// JSON value. Strings become "[REDACTED]", numbers become 0, booleans
// become false. Nil, objects, and arrays are returned unchanged.
func redactValue(v any) any {
    switch v.(type) {
    case string:
        return Redacted
    case float64:
        return float64(0)
    case bool:
        return false
    default:
        // nil, map[string]any, []any -- leave unchanged.
        return v
    }
}
```

##### Copy semantics for body bytes

ADR-5 noted that future body redaction must copy the body slice before
mutation. The `redactBodyFields` function satisfies this naturally:
`json.Unmarshal` creates a fresh data structure, and `json.Marshal`
produces a new `[]byte`. The original body slice is never written to.

If the body is not JSON (unmarshal fails), the original slice is returned
as-is, which is safe because no modification occurs.

##### BodyHash recalculation

After body redaction, `Request.BodyHash` becomes stale (it was computed
from the original body). The `RedactBodyPaths` function does **not**
recalculate the hash. Rationale:

- The hash is used for **matching** (finding a recorded fixture that
  matches an incoming request). Matching happens against the sanitized
  tape, so the hash in the stored fixture should reflect the redacted
  body.
- However, recalculating the hash inside `RedactBodyPaths` would couple
  body redaction to the hash computation concern. Instead, the hash
  should be recalculated after the full sanitization pipeline completes,
  in the Recorder (which already has access to `BodyHashFromBytes`).
- For now, `RedactBodyPaths` updates the body bytes and leaves the hash
  unchanged. A future ADR (or modification to the Recorder) will address
  hash recalculation after the full pipeline. This is acceptable because
  the Recorder can simply recalculate the hash after calling
  `pipeline.Sanitize(tape)`.

**Update**: To keep the Tape internally consistent after sanitization,
`RedactBodyPaths` **will** recalculate `Request.BodyHash` if the request
body was modified (i.e., was valid JSON and at least one path was
applicable). This is a simple call to `BodyHashFromBytes` and keeps the
sanitizer self-contained:

```go
return func(t Tape) Tape {
    newReqBody := redactBodyFields(t.Request.Body, parsed)
    if !bytes.Equal(newReqBody, t.Request.Body) {
        t.Request.Body = newReqBody
        t.Request.BodyHash = BodyHashFromBytes(newReqBody)
    } else {
        t.Request.Body = newReqBody
    }
    t.Response.Body = redactBodyFields(t.Response.Body, parsed)
    return t
}
```

##### Integration with Pipeline

`RedactBodyPaths` returns a `SanitizeFunc`, so it composes with
`RedactHeaders` and any future sanitizers in a `Pipeline`:

```go
sanitizer := NewPipeline(
    RedactHeaders(),                                           // #31
    RedactBodyPaths("$.password", "$.user.ssn", "$.tokens[*].value"), // #32
    // FakeHeaders("X-Request-Id"),                            // #33 (future)
)
recorder := NewRecorder(store, WithSanitizer(sanitizer))
```

Ordering note: `RedactHeaders` and `RedactBodyPaths` operate on
independent fields (headers vs. body), so their relative order does not
matter. However, by convention, header sanitization comes first.

#### File layout

| File | Contents | New/Modified |
|---|---|---|
| `sanitizer.go` | Add `RedactBodyPaths` function, `parsePath`, `parsedPath`, `segment` types, `redactBodyFields`, `redactAtPath`, `redactValue` helpers | Modified |
| `sanitizer_test.go` | Add table-driven tests for `RedactBodyPaths` | Modified |

No new files are created. All new code is added to `sanitizer.go` and its
test file, consistent with ADR-5's file layout.

#### Error cases

| Scenario | Behavior |
|---|---|
| No paths passed to `RedactBodyPaths` | Returns a no-op `SanitizeFunc`. Tape unchanged. |
| Invalid path syntax (e.g., `foo.bar`, missing `$`) | Path silently skipped. Other valid paths still applied. |
| Unsupported path syntax (e.g., `$..field`, `$[0]`) | Path silently skipped. |
| Path does not match any field in body | Body unchanged for that path. No error. |
| Body is nil or empty | Body returned unchanged. No error. |
| Body is not valid JSON (e.g., plain text, XML) | Body returned unchanged. No error. |
| Path targets an object or array value | Value left unchanged (only leaf scalars are redacted). |
| Path targets a null value | Value left unchanged (null carries no sensitive data). |
| Body is valid JSON but a scalar (e.g., `"hello"`, `42`) | No field to match. Body returned unchanged. |
| Array element is not an object (e.g., `[1, 2, 3]` with path `$[*].field`) | Elements silently skipped. |
| `json.Marshal` fails after redaction | Original body returned unchanged (defensive). |
| Concurrent calls to the returned `SanitizeFunc` | Safe -- parsed paths are read-only, `json.Unmarshal`/`Marshal` operate on local variables. |

#### Test strategy

**`sanitizer_test.go`** -- all tests are table-driven, added alongside
existing ADR-5 tests.

**RedactBodyPaths tests:**

| Test | Input body | Path(s) | Expected |
|---|---|---|---|
| `TestRedactBodyPaths_TopLevelString` | `{"api_key":"secret"}` | `$.api_key` | `{"api_key":"[REDACTED]"}` |
| `TestRedactBodyPaths_TopLevelNumber` | `{"balance":1234.56}` | `$.balance` | `{"balance":0}` |
| `TestRedactBodyPaths_TopLevelBool` | `{"active":true}` | `$.active` | `{"active":false}` |
| `TestRedactBodyPaths_NestedField` | `{"user":{"email":"a@b.c"}}` | `$.user.email` | `{"user":{"email":"[REDACTED]"}}` |
| `TestRedactBodyPaths_ArrayWildcard` | `{"users":[{"ssn":"123"},{"ssn":"456"}]}` | `$.users[*].ssn` | `{"users":[{"ssn":"[REDACTED]"},{"ssn":"[REDACTED]"}]}` |
| `TestRedactBodyPaths_MultiplePaths` | `{"a":"x","b":1}` | `$.a`, `$.b` | `{"a":"[REDACTED]","b":0}` |
| `TestRedactBodyPaths_MissingPath` | `{"foo":"bar"}` | `$.nonexistent` | `{"foo":"bar"}` (unchanged) |
| `TestRedactBodyPaths_NonJSONBody` | `plain text body` | `$.field` | `plain text body` (unchanged) |
| `TestRedactBodyPaths_NilBody` | `nil` | `$.field` | `nil` (unchanged) |
| `TestRedactBodyPaths_EmptyBody` | `[]byte{}` | `$.field` | `[]byte{}` (unchanged) |
| `TestRedactBodyPaths_NullValue` | `{"token":null}` | `$.token` | `{"token":null}` (unchanged) |
| `TestRedactBodyPaths_ObjectValue` | `{"data":{"nested":"val"}}` | `$.data` | `{"data":{"nested":"val"}}` (unchanged, object not redacted) |
| `TestRedactBodyPaths_ArrayValue` | `{"items":[1,2]}` | `$.items` | `{"items":[1,2]}` (unchanged, array not redacted) |
| `TestRedactBodyPaths_BothRequestAndResponse` | req+resp both JSON | `$.secret` | Both bodies redacted |
| `TestRedactBodyPaths_DoesNotMutateOriginal` | `{"a":"b"}` | `$.a` | Original tape body unchanged |
| `TestRedactBodyPaths_BodyHashRecalculated` | `{"pw":"x"}` | `$.pw` | `Request.BodyHash` matches hash of redacted body |
| `TestRedactBodyPaths_InvalidPath` | `{"a":"b"}` | `foo.bar` | Body unchanged (invalid path skipped) |
| `TestRedactBodyPaths_DeepNested` | `{"a":{"b":{"c":"s"}}}` | `$.a.b.c` | `{"a":{"b":{"c":"[REDACTED]"}}}` |
| `TestRedactBodyPaths_NestedArrayWildcard` | `{"d":{"rows":[{"v":"s"}]}}` | `$.d.rows[*].v` | `{"d":{"rows":[{"v":"[REDACTED]"}]}}` |
| `TestRedactBodyPaths_NoPaths` | `{"a":"b"}` | (none) | Body unchanged (no-op) |

**parsePath tests (unexported, tested indirectly):**

The `parsePath` function is tested indirectly through `RedactBodyPaths`
tests. Invalid paths (missing `$` prefix, empty segments, unsupported
bracket syntax) are covered by the `InvalidPath` test case and similar
edge cases.

#### Consequences

- **Stdlib-only JSON path traversal**: By implementing a minimal path
  parser rather than importing a JSONPath library, we maintain the
  zero-dependency constraint. The supported syntax covers the most common
  redaction needs. Users who need recursive descent or filter expressions
  can implement a custom `SanitizeFunc`.
- **Re-marshaling changes whitespace**: Bodies are re-marshaled with
  `json.Marshal`, which produces compact JSON. If the original body had
  indentation or specific formatting, it will be lost. This is acceptable
  for machine-generated fixtures.
- **Body hash stays consistent**: `Request.BodyHash` is recalculated
  after body redaction, keeping the tape internally consistent for
  matching purposes.
- **Type-aware redaction preserves schema**: Replacing strings with
  strings, numbers with numbers, and booleans with booleans means the
  redacted JSON still has the same schema. This prevents replay failures
  where a consumer expects a specific type.
- **Silent skip for non-JSON**: Libraries often record non-JSON bodies
  (file uploads, form data, protobuf). Silently skipping redaction for
  these cases is the safest default -- it avoids false errors and lets
  the body pass through unchanged.
- **Containers are not redacted**: Targeting objects or arrays with a
  redaction path leaves them unchanged. This is intentional -- replacing
  a nested structure with a scalar would break the JSON schema. Users
  should target leaf fields.
- **Future extensibility**: The `segment`-based path representation can
  be extended to support recursive descent (`..`) or index access
  (`[0]`) in future ADRs without changing the public API.

---

### ADR-7: Deterministic faking (HMAC-SHA256)

**Date**: 2026-03-30
**Issue**: #33
**Status**: Accepted

#### Context

ADR-5 introduced the `Pipeline` / `SanitizeFunc` composition mechanism and
`RedactHeaders`. ADR-6 added `RedactBodyPaths` for body field redaction.
Both replace values with static placeholders (`[REDACTED]`, `0`, `false`),
which destroys referential integrity: a user ID that appears in multiple
fixtures becomes `[REDACTED]` everywhere, losing the relationship between
them.

Issue #33 requires **deterministic faking**: given the same input value and
the same project seed, the output is always the same fake, regardless of
which fixture it appears in. This preserves cross-fixture consistency
(e.g., the same email in fixture A and fixture B becomes the same fake
email in both).

The locked decision L-06 mandates HMAC-SHA256 with a configurable seed.

Key constraints:

- stdlib only: `crypto/hmac`, `crypto/sha256`, `encoding/hex` -- no
  external libraries.
- `SanitizeFunc` contract: return a copy, never mutate the input.
- No panics -- graceful degradation for non-JSON bodies.
- Composable: `FakeFields` is another `SanitizeFunc` that slots into the
  existing `Pipeline` alongside `RedactHeaders` and `RedactBodyPaths`.
- Reuse the path parsing infrastructure (`parsePath`, `parsedPath`,
  `segment`) from ADR-6.

#### Decision

##### Design overview

`FakeFields` is a `SanitizeFunc` constructor that accepts a project seed
and a list of JSONPath-like field paths. For each matched leaf value in the
JSON body, it computes an HMAC-SHA256 of the original value using the seed
as the key, then produces a type-appropriate fake derived from the hash.

The seed is passed as the first argument to `FakeFields`. A higher-level
`WithSeed` functional option on `Pipeline` is also provided for ergonomic
configuration.

##### Faking strategies by detected type

When a path matches a leaf value, the faking strategy depends on the
detected type of the **original value** (not just the JSON type):

| Detected type | Detection rule | Fake format | Example |
|---|---|---|---|
| Email | String contains exactly one `@` with non-empty local and domain parts | `user_<hex8>@example.com` | `user_a1b2c3d4@example.com` |
| UUID | String matches `xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx` hex pattern (8-4-4-4-12) | Deterministic UUID v5 (bytes from HMAC, version/variant bits set) | `a1b2c3d4-e5f6-5789-a012-b3c4d5e6f7a8` |
| Numeric ID | JSON number (`float64`) | Deterministic positive integer in range [1, 2^31-1] derived from first 4 bytes of HMAC | `1234567890` |
| Generic string | Any string that does not match email or UUID patterns | `fake_<hex8>` | `fake_a1b2c3d4` |
| Boolean | JSON boolean | Left unchanged (booleans are not sensitive) |  |
| Null | JSON null | Left unchanged (null carries no information) |  |
| Object/Array | Container types | Left unchanged (target leaf fields) |  |

The `<hex8>` notation means the first 8 hex characters (4 bytes) of the
HMAC-SHA256 output. This provides 4 billion unique values, which is
sufficient for fixture deduplication. Using a short prefix keeps faked
values readable.

##### HMAC-SHA256 computation

```go
// computeHMAC returns the HMAC-SHA256 of the given message using the
// provided key. Both key and message are strings; the HMAC operates on
// their UTF-8 byte representations.
func computeHMAC(key, message string) []byte {
    mac := hmac.New(sha256.New, []byte(key))
    mac.Write([]byte(message))
    return mac.Sum(nil)
}
```

The input to the HMAC is the **string representation** of the original
value:

- For strings: the string value itself (e.g., `"alice@corp.com"`).
- For numbers: the `strconv.FormatFloat(v, 'f', -1, 64)` representation
  (e.g., `"42"`, `"3.14"`). Using `'f'` format with `-1` precision
  ensures consistent representation.

The key is the project seed string. Same seed + same input = same HMAC =
same fake output. Different seeds produce different fakes.

##### Type detection functions (unexported, `sanitizer.go`)

```go
// isEmail returns true if s looks like an email address: contains exactly
// one '@' with non-empty parts on both sides. This is a heuristic, not a
// full RFC 5322 parser.
func isEmail(s string) bool {
    at := strings.IndexByte(s, '@')
    return at > 0 && at < len(s)-1 && strings.Count(s, "@") == 1
}
```

```go
// isUUID returns true if s matches the UUID format:
// 8-4-4-4-12 hex characters separated by hyphens.
func isUUID(s string) bool {
    if len(s) != 36 {
        return false
    }
    for i, c := range s {
        switch i {
        case 8, 13, 18, 23:
            if c != '-' {
                return false
            }
        default:
            if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
                return false
            }
        }
    }
    return true
}
```

##### Faking functions (unexported, `sanitizer.go`)

```go
// fakeEmail generates a deterministic fake email from the HMAC hash.
// Format: user_<first 8 hex chars>@example.com
func fakeEmail(hash []byte) string {
    return "user_" + hex.EncodeToString(hash[:4]) + "@example.com"
}
```

```go
// fakeUUID generates a deterministic UUID v5-style value from the HMAC hash.
// It takes the first 16 bytes, sets version=5 and variant=RFC4122,
// then formats as standard UUID string.
func fakeUUID(hash []byte) string {
    var buf [16]byte
    copy(buf[:], hash[:16])
    buf[6] = (buf[6] & 0x0f) | 0x50 // version 5
    buf[8] = (buf[8] & 0x3f) | 0x80 // variant RFC 4122
    return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
        buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}
```

```go
// fakeNumericID generates a deterministic positive integer from the HMAC
// hash. Uses the first 4 bytes interpreted as big-endian uint32, masked
// to [1, 2^31-1] to ensure a positive non-zero int.
func fakeNumericID(hash []byte) float64 {
    n := uint32(hash[0])<<24 | uint32(hash[1])<<16 | uint32(hash[2])<<8 | uint32(hash[3])
    n = (n & 0x7FFFFFFF) // clear sign bit → [0, 2^31-1]
    if n == 0 {
        n = 1 // avoid zero
    }
    return float64(n)
}
```

```go
// fakeString generates a deterministic fake string from the HMAC hash.
// Format: fake_<first 8 hex chars>
func fakeString(hash []byte) string {
    return "fake_" + hex.EncodeToString(hash[:4])
}
```

##### fakeValue dispatcher (unexported, `sanitizer.go`)

```go
// fakeValue returns a deterministic fake replacement for the given JSON
// value. The fake is derived from the HMAC-SHA256 of the value's string
// representation using the provided seed.
//
// Faking strategies:
//   - Email string → user_<hash>@example.com
//   - UUID string → deterministic UUID v5
//   - float64 → deterministic positive integer
//   - Generic string → fake_<hash_prefix>
//   - bool, nil, objects, arrays → returned unchanged
func fakeValue(v any, seed string) any {
    switch val := v.(type) {
    case string:
        h := computeHMAC(seed, val)
        if isEmail(val) {
            return fakeEmail(h)
        }
        if isUUID(val) {
            return fakeUUID(h)
        }
        return fakeString(h)
    case float64:
        h := computeHMAC(seed, strconv.FormatFloat(val, 'f', -1, 64))
        return fakeNumericID(h)
    default:
        // bool, nil, map[string]any, []any -- leave unchanged.
        return v
    }
}
```

##### fakeAtPath recursive helper (unexported, `sanitizer.go`)

This mirrors `redactAtPath` from ADR-6 but calls `fakeValue` instead of
`redactValue` at the leaf:

```go
// fakeAtPath recursively traverses the JSON structure following the
// given segments and replaces the leaf value with a deterministic fake.
// It modifies the data in-place (caller must ensure data is a fresh
// copy from json.Unmarshal).
func fakeAtPath(data any, segments []segment, seed string) {
    if len(segments) == 0 {
        return
    }

    seg := segments[0]
    rest := segments[1:]

    obj, ok := data.(map[string]any)
    if !ok {
        return
    }

    val, exists := obj[seg.key]
    if !exists {
        return
    }

    if seg.wildcard {
        arr, ok := val.([]any)
        if !ok {
            return
        }
        if len(rest) == 0 {
            // Wildcard at leaf targets array elements (containers) -- skip.
            return
        }
        for _, elem := range arr {
            fakeAtPath(elem, rest, seed)
        }
        return
    }

    // Not a wildcard segment.
    if len(rest) == 0 {
        // Leaf: apply deterministic faking.
        obj[seg.key] = fakeValue(val, seed)
        return
    }

    // Intermediate: recurse deeper.
    fakeAtPath(val, rest, seed)
}
```

##### fakeBodyFields helper (unexported, `sanitizer.go`)

```go
// fakeBodyFields unmarshals the body as JSON, applies all path-based
// faking, and re-marshals the result. If the body is nil, empty, or
// not valid JSON, it is returned unchanged.
func fakeBodyFields(body []byte, paths []parsedPath, seed string) []byte {
    if len(body) == 0 {
        return body
    }

    var data any
    if err := json.Unmarshal(body, &data); err != nil {
        return body
    }

    for _, p := range paths {
        fakeAtPath(data, p.segments, seed)
    }

    result, err := json.Marshal(data)
    if err != nil {
        return body
    }
    return result
}
```

##### FakeFields function (`sanitizer.go`)

```go
// FakeFields returns a SanitizeFunc that replaces field values in JSON
// request and response bodies with deterministic fakes derived from
// HMAC-SHA256.
//
// The seed is a project-level secret used as the HMAC key. The same seed
// and input value always produce the same fake output, preserving
// cross-fixture consistency. Different seeds produce different fakes.
//
// Paths use the same JSONPath-like syntax as RedactBodyPaths:
//   - $.field             -- top-level field
//   - $.nested.field      -- nested field access
//   - $.array[*].field    -- field within each element of an array
//
// Faking strategies are determined by the detected type of each value:
//   - Email (string with @): user_<hash>@example.com
//   - UUID (8-4-4-4-12 hex): deterministic UUID v5
//   - Number (float64): deterministic positive integer
//   - Generic string: fake_<hash_prefix>
//   - Booleans, nulls, objects, arrays: left unchanged
//
// If the body is not valid JSON, it is left unchanged (no error).
// If a path does not match any field in the body, it is silently skipped.
// Invalid or unsupported paths are silently ignored.
//
// The returned function does not mutate the input Tape -- it copies the
// body byte slices before modification.
//
// Example:
//
//     sanitizer := NewPipeline(
//         RedactHeaders(),
//         FakeFields("my-project-seed",
//             "$.user.email",
//             "$.user.id",
//             "$.tokens[*].value",
//         ),
//     )
func FakeFields(seed string, paths ...string) SanitizeFunc {
    // Parse all paths at construction time.
    var parsed []parsedPath
    for _, p := range paths {
        if pp, ok := parsePath(p); ok {
            parsed = append(parsed, pp)
        }
    }

    return func(t Tape) Tape {
        newReqBody := fakeBodyFields(t.Request.Body, parsed, seed)
        if !bytes.Equal(newReqBody, t.Request.Body) {
            t.Request.Body = newReqBody
            t.Request.BodyHash = BodyHashFromBytes(newReqBody)
        } else {
            t.Request.Body = newReqBody
        }
        t.Response.Body = fakeBodyFields(t.Response.Body, parsed, seed)
        return t
    }
}
```

##### WithSeed functional option on Pipeline

The issue specifies a `WithSeed(seed string)` functional option. Since the
seed is consumed by `FakeFields` (not by `Pipeline` itself), and `Pipeline`
is a generic function list, the cleanest approach is to make `WithSeed` a
`PipelineOption` that stores the seed on the `Pipeline` for convenience,
and provide a helper method.

However, this would complicate `Pipeline` (which is currently a simple
function list). A simpler approach that preserves the existing design:

**`WithSeed` is a package-level helper that returns a `SanitizeFunc`
partial application wrapper.** But this is awkward since `FakeFields`
already takes the seed.

**Final decision**: `WithSeed` is not a separate option -- the seed is
passed directly to `FakeFields` as its first argument. This is the
simplest API and avoids hidden state on the Pipeline. The issue's mention
of `WithSeed` is satisfied by `FakeFields`'s seed parameter. The usage
pattern is:

```go
sanitizer := NewPipeline(
    RedactHeaders(),
    FakeFields("my-project-seed", "$.user.email", "$.tokens[*].value"),
)
```

If a caller wants to configure the seed separately from the paths (e.g.,
from environment variables), they simply pass it as a variable:

```go
seed := os.Getenv("HTTPTAPE_SEED")
sanitizer := NewPipeline(
    FakeFields(seed, "$.user.email"),
)
```

This is idiomatic Go and avoids unnecessary indirection.

##### Imports

`FakeFields` requires these additional stdlib imports in `sanitizer.go`:

```go
import (
    "crypto/hmac"
    "crypto/sha256"
    "encoding/hex"
    "fmt"
    "strconv"
)
```

All are stdlib. No external dependencies.

##### Copy semantics

Same approach as ADR-6: `json.Unmarshal` creates a fresh data structure,
`json.Marshal` produces new `[]byte`. The original body slice is never
written to. `Request.BodyHash` is recalculated when the request body
changes, keeping the tape internally consistent.

##### Ordering in the Pipeline

`FakeFields` operates on body fields, same as `RedactBodyPaths`. If both
are used in the same pipeline, the order matters:

- `RedactBodyPaths` then `FakeFields`: Redacted fields (now `[REDACTED]`)
  would be faked as generic strings. This is wrong -- the redacted
  placeholder would be the HMAC input, not the original value.
- `FakeFields` then `RedactBodyPaths`: Faked fields would then be
  redacted. This defeats the purpose of faking.

**Rule**: `FakeFields` and `RedactBodyPaths` should not target the same
paths. If a field should be faked, use `FakeFields`. If it should be
redacted (destroyed), use `RedactBodyPaths`. The documentation makes this
clear. If both target the same path, the last one in the pipeline wins.

#### File layout

| File | Contents | New/Modified |
|---|---|---|
| `sanitizer.go` | Add `FakeFields` function, `fakeBodyFields`, `fakeAtPath`, `fakeValue`, `computeHMAC`, `fakeEmail`, `fakeUUID`, `fakeNumericID`, `fakeString`, `isEmail`, `isUUID` helpers | Modified |
| `sanitizer_test.go` | Add table-driven tests for `FakeFields` | Modified |

No new files are created. All new code is added to `sanitizer.go` and its
test file.

#### Error cases

| Scenario | Behavior |
|---|---|
| Empty seed string | HMAC still works (empty key). Produces deterministic output. No error. |
| No paths passed to `FakeFields` | Returns a no-op `SanitizeFunc`. Tape unchanged. |
| Invalid path syntax | Path silently skipped. Other valid paths still applied. |
| Path does not match any field in body | Body unchanged for that path. No error. |
| Body is nil or empty | Body returned unchanged. No error. |
| Body is not valid JSON | Body returned unchanged. No error. |
| Path targets a boolean value | Value left unchanged (booleans are not sensitive). |
| Path targets a null value | Value left unchanged. |
| Path targets an object or array | Value left unchanged (target leaf fields). |
| Value is a string but not email/UUID | Faked as generic string (`fake_<hex8>`). |
| Value is a number with decimals (e.g., 3.14) | Faked as deterministic integer (decimal information is used in HMAC input but output is integer). |
| Concurrent calls to the returned `SanitizeFunc` | Safe -- parsed paths and seed are read-only, `json.Unmarshal`/`Marshal` and `hmac.New` operate on local variables. |

#### Test strategy

**`sanitizer_test.go`** -- all tests are table-driven, added alongside
existing ADR-5 and ADR-6 tests.

**FakeFields tests:**

| Test | Description |
|---|---|
| `TestFakeFields_Determinism` | Same input + same seed produces identical output across multiple calls |
| `TestFakeFields_CrossFixtureConsistency` | Same value in different Tapes (different IDs) produces the same fake |
| `TestFakeFields_DifferentSeeds` | Same input with different seeds produces different fakes |
| `TestFakeFields_Email` | String containing `@` is faked as `user_<hex8>@example.com` |
| `TestFakeFields_UUID` | UUID string is faked as deterministic UUID v5 format |
| `TestFakeFields_NumericID` | float64 value is faked as deterministic positive integer |
| `TestFakeFields_GenericString` | Non-email, non-UUID string is faked as `fake_<hex8>` |
| `TestFakeFields_BoolUnchanged` | Boolean values pass through unchanged |
| `TestFakeFields_NullUnchanged` | Null values pass through unchanged |
| `TestFakeFields_ObjectUnchanged` | Object values pass through unchanged |
| `TestFakeFields_ArrayWildcard` | `$.items[*].email` fakes each array element's email |
| `TestFakeFields_NestedPath` | `$.user.email` traverses nested objects correctly |
| `TestFakeFields_NonJSONBody` | Non-JSON body returned unchanged |
| `TestFakeFields_NilBody` | Nil body returned unchanged |
| `TestFakeFields_EmptyBody` | Empty body returned unchanged |
| `TestFakeFields_MissingPath` | Path that doesn't match leaves body unchanged |
| `TestFakeFields_InvalidPath` | Invalid path syntax silently skipped |
| `TestFakeFields_NoPaths` | No paths = no-op, tape unchanged |
| `TestFakeFields_BodyHashRecalculated` | `Request.BodyHash` updated after faking request body |
| `TestFakeFields_BothRequestAndResponse` | Both request and response bodies are faked |
| `TestFakeFields_DoesNotMutateOriginal` | Original tape body bytes unchanged after faking |
| `TestFakeFields_PipelineComposition` | `FakeFields` composes with `RedactHeaders` in a Pipeline |

**isEmail tests (tested indirectly via FakeFields):**

- `"alice@example.com"` -- detected as email
- `"not-an-email"` -- not detected
- `"@missing-local"` -- not detected
- `"missing-domain@"` -- not detected
- `"two@@signs"` -- not detected

**isUUID tests (tested indirectly via FakeFields):**

- `"550e8400-e29b-41d4-a716-446655440000"` -- detected as UUID
- `"550E8400-E29B-41D4-A716-446655440000"` -- detected (uppercase)
- `"not-a-uuid"` -- not detected
- `"550e8400-e29b-41d4-a716"` -- not detected (too short)
- `"550e8400-e29b-41d4-a716-44665544000g"` -- not detected (non-hex)

#### Consequences

- **Cross-fixture consistency preserved**: The same email address in
  fixture A and fixture B becomes the same `user_<hash>@example.com` in
  both, preserving referential integrity.
- **Deterministic output**: Given the same seed and input, the output is
  always identical. This means fixtures are stable across re-recordings
  (assuming the upstream API returns the same data).
- **Type-preserving fakes**: Emails remain valid email-shaped strings,
  UUIDs remain valid UUID-shaped strings, numbers remain numbers. This
  prevents replay failures where consumers expect specific formats.
- **Seed management is the caller's responsibility**: The seed must be
  consistent across all recordings in a project. If the seed changes, all
  faked values change. This is by design -- it allows rotating fakes.
- **HMAC security**: HMAC-SHA256 is a one-way function. Given the fake
  output, an attacker cannot recover the original value without the seed.
  However, the truncated hex prefix (8 chars / 4 bytes) means brute-force
  is feasible for short inputs. This is acceptable because the goal is
  privacy in fixtures, not cryptographic secrecy.
- **No external dependencies**: `crypto/hmac` and `crypto/sha256` are
  stdlib. The zero-dependency constraint is maintained.
- **Reuses path infrastructure**: `parsePath`, `parsedPath`, and `segment`
  from ADR-6 are reused without modification. `fakeAtPath` mirrors
  `redactAtPath` structurally, only differing at the leaf action.
- **Re-marshaling changes whitespace**: Same consequence as ADR-6. Bodies
  are re-marshaled with compact JSON.
- **Decimal numbers become integers**: A number like `3.14` is faked as a
  deterministic integer (e.g., `1234567890`). The decimal precision is
  lost in the fake. This is acceptable because the faked value is not
  meant to be arithmetically meaningful -- it just needs to be a valid
  number that is deterministic.

---

### ADR-8: Sanitizer integration with Recorder — mandatory sanitization

**Date**: 2026-03-30
**Issue**: #34
**Status**: Accepted

#### Context

The Recorder already accepts a `Sanitizer` via the `WithSanitizer` option and
applies it before persisting tapes (ADR-2). However, the sanitizer field
defaults to `nil`, and the application is guarded by `if r.sanitizer != nil`.
This means a caller who forgets to configure a sanitizer silently records raw
(unsanitized) fixtures — violating httptape's core guarantee that sensitive
data never touches disk (L-05).

Issue #34 requires:
1. Sanitization is mandatory: the Recorder always has a sanitizer.
2. If no sanitizer is explicitly provided, a no-op `Pipeline` (zero funcs) is
   used — fixtures are stored without modification, but the code path is
   exercised.
3. The sanitizer operates on a copy of the Tape; the caller's HTTP response is
   completely unaffected.
4. Integration tests prove the full flow: record with sanitizer, verify
   redaction in store; record without explicit sanitizer, verify no-op behavior.

#### Decision

##### 1. Default no-op sanitizer in NewRecorder

`NewRecorder` initializes the `sanitizer` field to `NewPipeline()` (a Pipeline
with zero funcs, which is a no-op). This happens in the defaults block before
options are applied, so `WithSanitizer` can still override it.

```go
func NewRecorder(store Store, opts ...RecorderOption) *Recorder {
    if store == nil {
        panic("httptape: NewRecorder requires a non-nil Store")
    }

    r := &Recorder{
        transport:  http.DefaultTransport,
        store:      store,
        sanitizer:  NewPipeline(), // default no-op sanitizer
        async:      true,
        sampleRate: 1.0,
        randFloat:  rand.Float64,
        bufSize:    1024,
    }

    for _, opt := range opts {
        opt(r)
    }

    // ... rest unchanged
}
```

##### 2. Remove nil-guard in RoundTrip

The conditional `if r.sanitizer != nil` is replaced with an unconditional call:

```go
// Apply sanitizer (always present — defaults to no-op Pipeline).
tape = r.sanitizer.Sanitize(tape)
```

This guarantees the sanitization code path is always exercised, which means:
- No silent bypass if the caller forgets `WithSanitizer`.
- The Tape passed to the store is always the output of `Sanitize`, even if
  the sanitizer is a no-op.

##### 3. WithSanitizer nil-guard

`WithSanitizer` should guard against a nil argument. If called with `nil`, it
sets the sanitizer to `NewPipeline()` (no-op) rather than allowing a nil value
that would cause a panic in RoundTrip:

```go
func WithSanitizer(s Sanitizer) RecorderOption {
    return func(r *Recorder) {
        if s == nil {
            r.sanitizer = NewPipeline()
            return
        }
        r.sanitizer = s
    }
}
```

##### 4. Godoc updates

- `NewRecorder` godoc: change "no sanitizer (tapes are stored as-is)" to
  "default no-op sanitizer (tapes are stored as-is unless WithSanitizer
  configures redaction)".
- `Recorder.sanitizer` field comment: change "optional; may be nil" to
  "always set; defaults to no-op Pipeline".
- `WithSanitizer` godoc: add note that passing nil results in a no-op Pipeline.

##### 5. Integration tests (recorder_test.go)

Add the following integration tests to `recorder_test.go`:

**`TestRecorder_Integration_SanitizerRedactsFixtures`**:
1. Start an `httptest.Server` that returns a JSON response containing a
   sensitive header (`Authorization`) and a JSON body with a `$.password`
   field.
2. Create a `MemoryStore` and a `Recorder` with:
   - `WithSanitizer(NewPipeline(RedactHeaders("Authorization"), RedactBodyPaths("$.password")))`
   - `WithAsync(false)` (synchronous for deterministic assertions)
3. Execute a request through the recorder.
4. Verify the HTTP response returned to the caller is **unmodified** (original
   Authorization header value, original body bytes).
5. Load the tape from the store.
6. Verify the tape's request `Authorization` header is `[REDACTED]`.
7. Verify the tape's response body `$.password` field is `[REDACTED]`.

**`TestRecorder_Integration_DefaultNoOpSanitizer`**:
1. Start an `httptest.Server` returning a JSON response with known content.
2. Create a `Recorder` with **no** `WithSanitizer` option.
3. Execute a request.
4. Load the tape from the store.
5. Verify the tape contents match the original request/response exactly (no
   modification — confirming the no-op Pipeline was used).

**`TestRecorder_Integration_CallerResponseUnmodified`**:
1. Configure a recorder with a sanitizer that aggressively modifies the tape
   (e.g., replaces all header values, redacts body fields).
2. Execute a request and capture the `*http.Response`.
3. Verify that `resp.StatusCode`, `resp.Header`, and `resp.Body` are identical
   to what the upstream server sent — proving sanitization only affects the
   tape copy, not the live response.

##### 6. Existing test updates

- `TestNewRecorder_Defaults`: update assertion from `if rec.sanitizer != nil`
  to verify `rec.sanitizer` is a non-nil `*Pipeline` (the default no-op).
- `TestRecorder_RoundTrip_WithSanitizer`: no changes needed (already tests
  explicit sanitizer override).

#### File layout

| File | Contents | New/Modified |
|---|---|---|
| `recorder.go` | Default no-op sanitizer in `NewRecorder`, remove nil-guard in `RoundTrip`, nil-guard in `WithSanitizer`, godoc updates | Modified |
| `recorder_test.go` | Three new integration tests, updated default assertion | Modified |

#### Consequences

- **Sanitization is always active**: No way to create a Recorder that bypasses
  the sanitization code path. This closes the gap where a forgotten
  `WithSanitizer` call could leak sensitive data to disk.
- **No-op is cheap**: `NewPipeline()` with zero funcs iterates an empty slice
  — effectively zero overhead.
- **Backwards compatible**: Callers who already use `WithSanitizer` see no
  change. Callers who do not use it now get a no-op Pipeline instead of nil —
  behavior is identical (fixtures stored as-is), but the invariant is
  enforced.
- **Nil safety**: `WithSanitizer(nil)` no longer causes a nil-pointer panic
  in `RoundTrip` — it falls back to the no-op Pipeline.
- **Test coverage**: Integration tests verify the end-to-end flow from HTTP
  call through sanitization to store persistence, covering the acceptance
  criteria in issue #34.

---

### ADR-9: Bundle export (tar.gz)

**Date**: 2026-03-30
**Issue**: #35
**Status**: Accepted

#### Context

Teams need to share recorded fixtures across environments, CI pipelines, and
developer machines. A standardized, portable export format enables fixture
distribution without requiring access to the original store. The issue asks for
a tar.gz bundle containing a manifest and all fixture files.

Key constraints from locked decisions:
- stdlib only (L-04): must use `archive/tar` and `compress/gzip` from the standard library.
- Single flat package (L-03): the export function lives in `bundle.go`.
- No panics (L-11), functional options (L-12).
- The `Store` interface (L-08) already provides `List` which is sufficient to enumerate all tapes.

The central design question is whether `Export` should be a method on the `Store`
interface or a standalone function.

#### Decision

##### Standalone function, not a Store method

`Export` is implemented as a **standalone package-level function** that accepts
any `Store`, rather than adding a method to the `Store` interface. Rationale:

1. **Interface Segregation**: The `Store` interface is the hexagonal port for
   CRUD persistence. Export is an orthogonal concern — it is a read-only
   operation that composes on top of `List` + JSON serialization. Adding it to
   `Store` would force every adapter implementation to carry export logic that
   is identical across adapters.
2. **No breaking change**: Adding a method to `Store` would break all existing
   implementations (including any third-party adapters). A standalone function
   avoids this entirely.
3. **Single implementation**: Because export only uses the public `Store.List`
   method, a single function works for `MemoryStore`, `FileStore`, and any
   future adapter with zero duplication.

##### Public API (`bundle.go`)

```go
package httptape

import (
    "archive/tar"
    "compress/gzip"
    "context"
    "encoding/json"
    "io"
    "time"
)

// Manifest describes the contents and metadata of an exported bundle.
// It is serialized as the first entry (manifest.json) in the tar.gz archive.
type Manifest struct {
    // ExportedAt is the UTC timestamp when the bundle was created.
    ExportedAt time.Time `json:"exported_at"`

    // FixtureCount is the total number of fixture files in the bundle.
    FixtureCount int `json:"fixture_count"`

    // Routes is the deduplicated, sorted list of route labels present
    // in the exported fixtures.
    Routes []string `json:"routes"`

    // SanitizerConfig is an optional human-readable summary of the
    // sanitizer configuration that was active when the fixtures were
    // recorded. Empty string if unknown or not applicable.
    SanitizerConfig string `json:"sanitizer_config,omitempty"`
}

// ExportOption configures an ExportBundle call.
type ExportOption func(*exportConfig)

// exportConfig holds resolved options for ExportBundle.
type exportConfig struct {
    sanitizerConfig string
}

// WithSanitizerConfig attaches a human-readable sanitizer configuration
// summary to the bundle manifest.
func WithSanitizerConfig(summary string) ExportOption {
    return func(cfg *exportConfig) {
        cfg.sanitizerConfig = summary
    }
}

// ExportBundle exports all tapes from the given store as a tar.gz archive.
// The returned io.Reader streams the archive — it is not buffered entirely
// in memory. The caller must read the reader to completion or cancel the
// context to release resources.
//
// Bundle layout:
//
//     manifest.json          — bundle metadata (see Manifest type)
//     fixtures/<id>.json     — one file per tape, JSON-encoded
//
// The function uses Store.List with an empty filter to enumerate all tapes.
// Fixture files are named by tape ID and placed in a flat fixtures/ directory.
func ExportBundle(ctx context.Context, s Store, opts ...ExportOption) (io.Reader, error)
```

##### Streaming via io.Pipe

`ExportBundle` uses `io.Pipe` to stream the tar.gz output:

1. Call `s.List(ctx, Filter{})` to load all tapes up front. This is necessary
   to compute the manifest (fixture count, route list) before writing the
   first tar entry. Since tapes are small (KB range) and v1 is not expected
   to handle millions of fixtures, loading all tapes into a slice is acceptable.
2. Create an `io.Pipe` — the `io.PipeWriter` is passed to a goroutine, the
   `io.PipeReader` is returned to the caller.
3. In the goroutine:
   a. Create `gzip.NewWriter(pw)` wrapping the pipe writer.
   b. Create `tar.NewWriter(gw)` wrapping the gzip writer.
   c. Build the `Manifest` from the loaded tapes (deduplicate and sort routes,
      count fixtures, set `ExportedAt` to `time.Now().UTC()`).
   d. Marshal the manifest to JSON, write it as `manifest.json` tar entry.
   e. For each tape: marshal to indented JSON, write as `fixtures/<id>.json`
      tar entry.
   f. Close the tar writer, gzip writer, and pipe writer (in that order).
   g. If any error occurs, close the pipe writer with the error so the reader
      surfaces it.

The goroutine checks `ctx.Err()` before writing each entry so the caller can
cancel a long-running export.

##### Tar entry details

Each tar entry uses the following header fields:

| Field | Value |
|---|---|
| `Name` | `manifest.json` or `fixtures/<tape-id>.json` |
| `Mode` | `0o644` |
| `Size` | `int64(len(jsonBytes))` |
| `ModTime` | `time.Now().UTC()` for manifest; `tape.RecordedAt` for fixtures |
| `Typeflag` | `tar.TypeReg` |

No directory entries are written — tar readers handle implicit directories.

##### Manifest generation

```go
func buildManifest(tapes []Tape, cfg exportConfig) Manifest {
    routeSet := make(map[string]struct{})
    for _, t := range tapes {
        if t.Route != "" {
            routeSet[t.Route] = struct{}{}
        }
    }
    routes := make([]string, 0, len(routeSet))
    for r := range routeSet {
        routes = append(routes, r)
    }
    sort.Strings(routes)

    return Manifest{
        ExportedAt:      time.Now().UTC(),
        FixtureCount:    len(tapes),
        Routes:          routes,
        SanitizerConfig: cfg.sanitizerConfig,
    }
}
```

If there are zero tapes, `Routes` is an empty slice (not nil) and
`FixtureCount` is 0. The bundle is still valid — it contains only
`manifest.json`.

##### Error handling

| Condition | Behavior |
|---|---|
| `Store.List` fails | Return `nil, err` immediately (before creating pipe) |
| Context cancelled during tar writing | Goroutine detects `ctx.Err()`, closes pipe with error |
| JSON marshal failure | Close pipe with error (caller sees it on Read) |
| Tar/gzip write failure | Close pipe with error |
| Caller abandons reader without reading | Goroutine blocks on pipe write, eventually caller GCs the reader; pipe writer closes with `io.ErrClosedPipe` — goroutine exits cleanly |

All errors are wrapped with `fmt.Errorf("httptape: export: %w", err)`.

#### File layout

| File | Contents | New/Modified |
|---|---|---|
| `bundle.go` | `Manifest` type, `ExportOption`, `WithSanitizerConfig`, `ExportBundle`, unexported `buildManifest`, unexported `exportConfig` | New |
| `bundle_test.go` | Table-driven tests for ExportBundle | New |

#### Test strategy (`bundle_test.go`)

All tests use `MemoryStore` (since export is store-agnostic and MemoryStore
requires no filesystem setup). Tests use `context.Background()`.

- **`TestExportBundle_WithFixtures`**: Save 3 tapes across 2 routes to a
  MemoryStore. Call `ExportBundle`. Decompress the tar.gz reader. Verify:
  - `manifest.json` exists and is valid JSON with correct `fixture_count` (3),
    sorted `routes`, and a non-zero `exported_at`.
  - `fixtures/<id>.json` exists for each tape ID. Unmarshal each and verify
    it matches the original tape.

- **`TestExportBundle_Empty`**: Export from an empty MemoryStore. Verify the
  bundle contains only `manifest.json` with `fixture_count: 0` and empty
  `routes`.

- **`TestExportBundle_ManifestRoutes`**: Save tapes with duplicate routes.
  Verify `routes` in manifest is deduplicated and sorted.

- **`TestExportBundle_WithSanitizerConfig`**: Export with
  `WithSanitizerConfig("headers: Authorization, Cookie")`. Verify manifest
  contains the summary string.

- **`TestExportBundle_ContextCancel`**: Create a store with tapes, cancel
  the context before reading the export stream. Verify an error is returned
  (either from ExportBundle directly if List fails, or from reading the
  io.Reader).

- **`TestExportBundle_ValidGzip`**: Read the entire export output through
  `gzip.NewReader` to verify it is a valid gzip stream, then through
  `tar.NewReader` to verify valid tar structure.

Helper: `saveTestTapes(t *testing.T, s Store, tapes ...Tape)` — saves all
tapes to the store using `context.Background()`.

#### Sequence: Export flow

1. Caller calls `ExportBundle(ctx, store, opts...)`.
2. `ExportBundle` calls `store.List(ctx, Filter{})` to load all tapes.
3. If List returns an error, return `nil, err`.
4. Create `io.Pipe()`.
5. Launch goroutine:
   a. Build manifest from tapes.
   b. Create gzip writer wrapping pipe writer.
   c. Create tar writer wrapping gzip writer.
   d. Write `manifest.json` entry.
   e. For each tape, write `fixtures/<id>.json` entry.
   f. Close tar writer, gzip writer, pipe writer.
6. Return pipe reader to caller.
7. Caller reads from pipe reader — data flows as goroutine writes.

#### Consequences

- **No Store interface change**: Existing implementations and any third-party
  adapters are unaffected. Export composes on the public List method.
- **Streaming output**: Using `io.Pipe` means the full archive is never
  buffered in memory (though all tapes are loaded into a slice for manifest
  computation — acceptable for v1 volumes).
- **Tapes loaded up front**: The entire tape list is loaded before streaming
  begins. This is a deliberate trade-off: computing the manifest requires
  knowing the fixture count and route list. For very large stores, a future
  ADR could introduce a two-pass approach or a streaming manifest at the end
  of the archive.
- **Flat fixtures/ directory**: All fixture files are placed directly under
  `fixtures/` keyed by tape ID, mirroring the FileStore layout. No route-based
  subdirectories — this keeps the format simple and avoids filename
  sanitization issues.
- **Bundle is self-describing**: The manifest provides enough metadata for a
  future import function to validate the bundle without inspecting every
  fixture file.
- **Foundation for import**: ADR-9 covers export only. Import (issue #35
  story 3.2) will be a separate ADR that reads the same bundle format.

---

### ADR-10: Bundle import (tar.gz)

**Date**: 2026-03-30
**Issue**: #36
**Status**: Accepted

#### Context

ADR-9 established the tar.gz export format with a `manifest.json` entry followed
by `fixtures/<id>.json` entries. Teams now need the inverse operation: importing a
bundle into any `Store` implementation so that fixtures recorded elsewhere can be
used for local testing and CI replay.

Key constraints carried forward:
- stdlib only (L-04): `archive/tar`, `compress/gzip`, `encoding/json`.
- Single flat package (L-03): import lives in `bundle.go` alongside export.
- No panics (L-11), no interface changes (same rationale as ADR-9).
- Store already provides `Save` with upsert semantics — perfect for merge.

Design questions:
1. Standalone function vs Store method? (same as ADR-9 — standalone)
2. Validation: what must be checked before fixtures are persisted?
3. Merge strategy: how to handle ID collisions with existing fixtures?
4. Memory model: buffer everything or stream-and-save?

#### Decision

##### Standalone function, not a Store method

For the same reasons as ADR-9 (interface segregation, no breaking change, single
implementation), import is a **standalone package-level function**:

```go
// ImportBundle imports tapes from a tar.gz bundle into the given store.
// The bundle must have been produced by ExportBundle (see ADR-9 for the format).
//
// Merge strategy: fixtures in the bundle overwrite any existing fixtures with
// the same ID in the store. Fixtures already in the store whose IDs are not
// present in the bundle are left untouched.
//
// The entire bundle is validated before any fixtures are persisted. If the
// manifest is missing, malformed, or any fixture fails JSON unmarshalling,
// ImportBundle returns an error and the store is not modified.
func ImportBundle(ctx context.Context, s Store, r io.Reader) error
```

No functional options are needed for v1. The function signature is intentionally
simple — future options (e.g., dry-run, conflict callback) can be added via an
`ImportOption` variadic parameter without breaking the API by adding it as a
trailing `...ImportOption` argument.

##### Two-phase approach: validate then persist

ImportBundle operates in two phases:

**Phase 1 — Read and validate (no store mutations):**

1. Wrap `r` in `gzip.NewReader`, then `tar.NewReader`.
2. Iterate through all tar entries. For each entry:
   - If `Name == "manifest.json"`: unmarshal into `Manifest`. Record that the
     manifest was found. Validate required fields: `FixtureCount >= 0`,
     `ExportedAt` is not zero.
   - If `Name` matches `fixtures/*.json`: unmarshal into `Tape`. Validate that
     the tape has a non-empty `ID`, non-empty `Request.Method`, and non-empty
     `Request.URL`. Collect into a `[]Tape` slice.
   - Other entries (directories, unknown files): silently skip. This provides
     forward compatibility if future bundle versions add new entry types.
3. After iteration, validate:
   - Manifest was found (error if missing).
   - `manifest.FixtureCount` matches the number of fixture entries actually
     found (error if mismatch — indicates a truncated or corrupt bundle).

If any validation fails, return an error immediately. The store is untouched.

**Phase 2 — Persist:**

4. For each validated tape, call `s.Save(ctx, tape)`. Since `Save` has upsert
   semantics (see ADR-1), this naturally overwrites existing fixtures with the
   same ID and leaves other fixtures untouched.
5. Check `ctx.Err()` before each `Save` call for cancellation support.
6. If any `Save` fails, return the error immediately. Note: this means a partial
   import is possible if the store fails mid-way. This is acceptable for v1
   because:
   - `Save` upsert is idempotent — re-importing the same bundle is safe.
   - A transactional all-or-nothing import would require a `Store` transaction
     API that does not exist and is out of scope.

##### Fixture validation rules

Each fixture extracted from the bundle must pass these checks before phase 2:

| Field | Rule | Error |
|---|---|---|
| `ID` | Non-empty string | `"httptape: import: fixture has empty ID"` |
| `Request.Method` | Non-empty string | `"httptape: import: fixture %s has empty request method"` |
| `Request.URL` | Non-empty string | `"httptape: import: fixture %s has empty request URL"` |

These are minimal structural checks — they verify the fixture is usable for
matching and replay without being overly strict. Fields like `Route`,
`RecordedAt`, `Headers`, `Body` are optional by nature.

##### Manifest validation rules

| Field | Rule | Error |
|---|---|---|
| Presence | `manifest.json` must exist in the archive | `"httptape: import: missing manifest.json"` |
| JSON format | Must unmarshal into `Manifest` | `"httptape: import: invalid manifest: ..."` |
| `FixtureCount` | Must match the number of `fixtures/*.json` entries found | `"httptape: import: manifest declares %d fixtures but bundle contains %d"` |

The `ExportedAt`, `Routes`, and `SanitizerConfig` fields are informational and
not validated beyond JSON unmarshalling. This avoids brittle checks on metadata
that has no impact on import correctness.

##### Memory model

The entire bundle is read into memory (as a `[]Tape` slice) during phase 1.
This mirrors the export design (ADR-9) where all tapes are loaded into a slice
for manifest computation. For v1 volumes this is acceptable. A streaming
validate-and-persist approach would be more memory-efficient but would prevent
the all-or-nothing validation guarantee of phase 1.

##### Size guard

To prevent denial-of-service from maliciously large bundles, individual tar
entries are limited to 50 MB using `io.LimitReader`. This is far above any
reasonable fixture size while still protecting against zip-bomb-style attacks.
The limit is defined as an unexported constant `maxEntrySize`.

##### Error handling

| Condition | Behavior |
|---|---|
| `r` is not valid gzip | Return `"httptape: import: %w"` wrapping gzip error |
| Tar read error | Return `"httptape: import: %w"` wrapping tar error |
| Missing `manifest.json` | Return `"httptape: import: missing manifest.json"` |
| Manifest JSON invalid | Return `"httptape: import: invalid manifest: %w"` |
| Fixture JSON invalid | Return `"httptape: import: invalid fixture %q: %w"` (using entry name) |
| Fixture validation fails | Return specific message (see table above) |
| Fixture count mismatch | Return `"httptape: import: manifest declares %d fixtures but bundle contains %d"` |
| Context cancelled | Return `"httptape: import: %w"` wrapping context error |
| `Store.Save` fails | Return `"httptape: import: save tape %s: %w"` |

All errors are wrapped with the `"httptape: import:"` prefix for consistency
with the export error convention from ADR-9.

##### Implementation sketch

```go
const maxEntrySize = 50 << 20 // 50 MB

func ImportBundle(ctx context.Context, s Store, r io.Reader) error {
    gr, err := gzip.NewReader(r)
    if err != nil {
        return fmt.Errorf("httptape: import: %w", err)
    }
    defer gr.Close()

    tr := tar.NewReader(gr)

    var manifest *Manifest
    var tapes []Tape

    for {
        hdr, err := tr.Next()
        if err == io.EOF {
            break
        }
        if err != nil {
            return fmt.Errorf("httptape: import: %w", err)
        }

        if err := ctx.Err(); err != nil {
            return fmt.Errorf("httptape: import: %w", err)
        }

        lr := io.LimitReader(tr, maxEntrySize)

        switch {
        case hdr.Name == "manifest.json":
            var m Manifest
            if err := json.NewDecoder(lr).Decode(&m); err != nil {
                return fmt.Errorf("httptape: import: invalid manifest: %w", err)
            }
            manifest = &m

        case isFixtureEntry(hdr.Name):
            var t Tape
            if err := json.NewDecoder(lr).Decode(&t); err != nil {
                return fmt.Errorf("httptape: import: invalid fixture %q: %w", hdr.Name, err)
            }
            if err := validateFixture(t); err != nil {
                return err
            }
            tapes = append(tapes, t)
        }
        // Unknown entries are silently skipped.
    }

    // Phase 1 validation
    if manifest == nil {
        return fmt.Errorf("httptape: import: missing manifest.json")
    }
    if manifest.FixtureCount != len(tapes) {
        return fmt.Errorf("httptape: import: manifest declares %d fixtures but bundle contains %d",
            manifest.FixtureCount, len(tapes))
    }

    // Phase 2 persist
    for _, t := range tapes {
        if err := ctx.Err(); err != nil {
            return fmt.Errorf("httptape: import: %w", err)
        }
        if err := s.Save(ctx, t); err != nil {
            return fmt.Errorf("httptape: import: save tape %s: %w", t.ID, err)
        }
    }

    return nil
}

// isFixtureEntry reports whether the tar entry name matches the fixture pattern.
func isFixtureEntry(name string) bool {
    return strings.HasPrefix(name, "fixtures/") && strings.HasSuffix(name, ".json")
}

// validateFixture checks that a tape has the minimum required fields.
func validateFixture(t Tape) error {
    if t.ID == "" {
        return fmt.Errorf("httptape: import: fixture has empty ID")
    }
    if t.Request.Method == "" {
        return fmt.Errorf("httptape: import: fixture %s has empty request method", t.ID)
    }
    if t.Request.URL == "" {
        return fmt.Errorf("httptape: import: fixture %s has empty request URL", t.ID)
    }
    return nil
}
```

#### File layout

| File | Contents | New/Modified |
|---|---|---|
| `bundle.go` | Add `ImportBundle`, unexported `isFixtureEntry`, `validateFixture`, `maxEntrySize` | Modified |
| `bundle_test.go` | Add import tests (see test strategy below) | Modified |

#### Test strategy (`bundle_test.go`)

All tests use `MemoryStore` and `context.Background()`.

- **`TestImportBundle_IntoEmptyStore`**: Create a bundle (via `ExportBundle`
  from a MemoryStore with 3 tapes). Import into a fresh empty MemoryStore.
  Verify all 3 tapes are present with correct data.

- **`TestImportBundle_MergeOverwrite`**: Pre-populate a MemoryStore with tape A
  and tape B. Create a bundle containing tape A (modified response) and tape C.
  Import. Verify: tape A has the new response (overwritten), tape B still exists
  (untouched), tape C exists (new).

- **`TestImportBundle_MalformedGzip`**: Pass `strings.NewReader("not gzip")`.
  Verify a non-nil error is returned.

- **`TestImportBundle_InvalidManifest`**: Construct a tar.gz with a
  `manifest.json` entry containing invalid JSON. Verify error.

- **`TestImportBundle_MissingManifest`**: Construct a tar.gz with fixture
  entries but no `manifest.json`. Verify error mentions missing manifest.

- **`TestImportBundle_FixtureCountMismatch`**: Construct a bundle where
  `manifest.FixtureCount` does not match the actual number of fixture entries.
  Verify error.

- **`TestImportBundle_InvalidFixture`**: Construct a bundle with a fixture
  entry containing invalid JSON. Verify error.

- **`TestImportBundle_EmptyBundle`**: Export from an empty store (0 fixtures),
  import into another empty store. Verify no error and store remains empty.

- **`TestImportBundle_RoundTrip`**: Save N tapes to store A, export, import
  into store B. List all from both stores and compare — all tapes must be
  identical (deep equal on the Tape structs).

- **`TestImportBundle_ContextCancellation`**: Pass an already-cancelled context.
  Verify error wraps `context.Canceled`.

#### Consequences

- **No Store interface change**: Import composes on the public `Save` method,
  same philosophy as ADR-9 for export.
- **Validate-then-persist**: The two-phase design means a corrupt bundle never
  partially modifies the store. The trade-off is that all fixtures must fit in
  memory — acceptable for v1 volumes.
- **Partial import on Store.Save failure**: If the store itself fails mid-save,
  some fixtures may already be persisted. This is acceptable because `Save` is
  idempotent (upsert) — re-importing the same bundle is safe and will converge
  to the correct state.
- **Forward compatible**: Unknown tar entries are silently skipped, allowing
  future bundle versions to add metadata files without breaking older importers.
- **Round-trip fidelity**: Export and import use the same JSON serialization of
  `Tape`, so round-tripping preserves all fields exactly. This is verified by
  the round-trip test.

### ADR-11: Selective export (filter by route, method, time)

**Date**: 2026-03-30
**Issue**: #37
**Status**: Accepted

#### Context

`ExportBundle` (ADR-9) exports all fixtures from a store. Teams often need to
share only a subset of fixtures — for a specific route, HTTP method, or time
range — to keep bundles focused and small. Issue #37 asks for functional options
that filter tapes before bundling.

Key constraints:
- Functional options pattern (L-12): new filters are `ExportOption` values.
- Backward compatible: no filters = export all (current behavior preserved).
- stdlib only (L-04), no panics (L-11).

Two design questions arise:
1. Should selective export reuse the existing `Filter` type from `store.go`?
2. Where does filtering happen — at the `Store.List` call or as a post-filter
   on the returned tapes?

#### Decision

##### Filter in `exportConfig`, applied as post-filter on tapes

Filtering is performed **in ExportBundle after `Store.List` returns**, not by
passing a richer `Filter` to `Store.List`. Rationale:

1. **No Store interface change**: The existing `Filter` type supports a single
   `Route` (string) and a single `Method` (string). The issue requires
   multi-route filtering (`WithRoutes("stripe", "s3")`) and time-range
   filtering (`WithSince`), neither of which `Filter` supports. Extending
   `Filter` with slice fields and a `Since` timestamp would change the contract
   for all `Store` adapter implementations — a breaking change we want to avoid
   in v1.
2. **Separation of concerns**: `Store.List` is a persistence query. Export
   filtering is a bundle-assembly concern. Keeping them separate follows the
   hexagonal principle — the export function composes on top of the port
   without changing it.
3. **Simplicity**: Post-filtering a slice of tapes is trivial and efficient for
   v1 volumes (KBs, not millions). No index or query optimization needed.

The trade-off is that `Store.List` loads all tapes and the export function
discards non-matching ones. This is acceptable for v1 where fixture counts are
modest. If performance becomes a concern with very large stores, a future ADR
can add richer query support to `Store.List`.

##### New `ExportOption` constructors

Three new option constructors are added to `bundle.go`:

```go
// WithRoutes filters the export to include only tapes whose Route field
// matches one of the specified route names. If no routes are specified,
// this option is a no-op. Route matching is exact (case-sensitive).
func WithRoutes(routes ...string) ExportOption {
    return func(cfg *exportConfig) {
        cfg.routes = routes
    }
}

// WithMethods filters the export to include only tapes whose HTTP method
// matches one of the specified methods. Methods are compared case-insensitively
// (normalized to uppercase). If no methods are specified, this option is a no-op.
func WithMethods(methods ...string) ExportOption {
    return func(cfg *exportConfig) {
        normalized := make([]string, len(methods))
        for i, m := range methods {
            normalized[i] = strings.ToUpper(m)
        }
        cfg.methods = normalized
    }
}

// WithSince filters the export to include only tapes recorded at or after
// the given timestamp. The zero value of time.Time disables this filter.
func WithSince(t time.Time) ExportOption {
    return func(cfg *exportConfig) {
        cfg.since = t
    }
}
```

##### Extended `exportConfig`

```go
type exportConfig struct {
    sanitizerConfig string
    routes          []string  // nil = no route filter
    methods         []string  // nil = no method filter; stored uppercase
    since           time.Time // zero = no time filter
}
```

##### Filtering logic

A new unexported function `filterTapes` applies all configured filters:

```go
// filterTapes returns the subset of tapes matching the export filters.
// All non-nil/non-zero filters are AND-ed: a tape must pass every active
// filter to be included. If no filters are set, all tapes are returned.
func filterTapes(tapes []Tape, cfg exportConfig) []Tape {
    if len(cfg.routes) == 0 && len(cfg.methods) == 0 && cfg.since.IsZero() {
        return tapes
    }

    result := make([]Tape, 0, len(tapes))
    for _, t := range tapes {
        if !matchesRouteFilter(t, cfg.routes) {
            continue
        }
        if !matchesMethodFilter(t, cfg.methods) {
            continue
        }
        if !cfg.since.IsZero() && t.RecordedAt.Before(cfg.since) {
            continue
        }
        result = append(result, t)
    }
    return result
}

// matchesRouteFilter returns true if the tape's route matches any of the
// specified routes, or if routes is empty (no filter).
func matchesRouteFilter(t Tape, routes []string) bool {
    if len(routes) == 0 {
        return true
    }
    for _, r := range routes {
        if t.Route == r {
            return true
        }
    }
    return false
}

// matchesMethodFilter returns true if the tape's HTTP method matches any of
// the specified methods, or if methods is empty (no filter).
// Methods in the slice are expected to already be uppercase.
func matchesMethodFilter(t Tape, methods []string) bool {
    if len(methods) == 0 {
        return true
    }
    m := strings.ToUpper(t.Request.Method)
    for _, allowed := range methods {
        if m == allowed {
            return true
        }
    }
    return false
}
```

##### Integration into ExportBundle

The only change to `ExportBundle` is inserting a `filterTapes` call between
`Store.List` and the goroutine that writes the archive:

```go
func ExportBundle(ctx context.Context, s Store, opts ...ExportOption) (io.Reader, error) {
    var cfg exportConfig
    for _, opt := range opts {
        opt(&cfg)
    }

    tapes, err := s.List(ctx, Filter{})
    if err != nil {
        return nil, fmt.Errorf("httptape: export: %w", err)
    }

    tapes = filterTapes(tapes, cfg) // <-- new line

    pr, pw := io.Pipe()
    go func() {
        err := writeBundle(ctx, pw, tapes, cfg)
        pw.CloseWithError(err)
    }()
    return pr, nil
}
```

`writeBundle` and `buildManifest` require zero changes — they already operate
on whatever slice of tapes they receive. The manifest will naturally reflect the
filtered set (correct `FixtureCount`, only matching `Routes`).

##### Method normalization

`WithMethods` normalizes input to uppercase at option-construction time. The
`matchesMethodFilter` function also uppercases the tape's method before
comparison. This ensures case-insensitive matching consistent with HTTP
semantics (RFC 9110 section 9.1: methods are case-sensitive by spec, but
real-world clients sometimes vary case; normalizing to uppercase is the
pragmatic choice).

##### Edge cases

| Scenario | Behavior |
|---|---|
| No filters provided | All tapes exported (backward compatible) |
| `WithRoutes()` with empty args | No-op (nil slice, filter skipped) |
| `WithMethods()` with empty args | No-op (nil slice, filter skipped) |
| `WithSince(time.Time{})` | Zero time, filter skipped |
| All filters active, nothing matches | Valid empty bundle (manifest with 0 fixtures) |
| Same option applied twice | Last one wins (standard functional options behavior) |
| `WithRoutes("a")` + `WithMethods("GET")` | AND-ed: tape must be route "a" AND method GET |

#### File layout

| File | Contents | New/Modified |
|---|---|---|
| `bundle.go` | `WithRoutes`, `WithMethods`, `WithSince`, extended `exportConfig`, `filterTapes`, `matchesRouteFilter`, `matchesMethodFilter`; one new line in `ExportBundle` | Modified |
| `bundle_test.go` | New test cases for selective export | Modified |

#### Test strategy (`bundle_test.go`)

All tests use `MemoryStore` with `context.Background()`. Each test saves a
known set of tapes, calls `ExportBundle` with specific options, decompresses
the result, and verifies the manifest and fixture set.

- **`TestExportBundle_WithRoutes_Single`**: Save tapes across routes "stripe",
  "s3", "auth". Export with `WithRoutes("stripe")`. Verify only stripe fixtures
  in bundle, manifest `FixtureCount` and `Routes` correct.

- **`TestExportBundle_WithRoutes_Multiple`**: Export with
  `WithRoutes("stripe", "s3")`. Verify both routes present, "auth" excluded.

- **`TestExportBundle_WithMethods`**: Save GET and POST tapes. Export with
  `WithMethods("GET")`. Verify only GET fixtures exported.

- **`TestExportBundle_WithSince`**: Save tapes with `RecordedAt` at T-1h and
  T+1h relative to a cutoff. Export with `WithSince(cutoff)`. Verify only the
  T+1h tape is included.

- **`TestExportBundle_CombinedFilters`**: Save tapes with various route/method
  combinations. Export with `WithRoutes("stripe")` and `WithMethods("POST")`.
  Verify only tapes matching both criteria are exported.

- **`TestExportBundle_FilterMatchesNothing`**: Export with
  `WithRoutes("nonexistent")`. Verify valid empty bundle with `FixtureCount: 0`
  and empty `Routes`.

- **`TestExportBundle_WithMethodsCaseInsensitive`**: Save a tape with method
  "GET". Export with `WithMethods("get")`. Verify the tape is included.

#### Consequences

- **No Store interface change**: Filtering is entirely within the export
  function. All existing `Store` implementations work without modification.
- **Backward compatible**: Zero options = current behavior (export all).
- **Composable**: Filters AND together naturally. Adding new filter dimensions
  in the future (e.g., `WithBefore(time.Time)`, `WithURLPattern(string)`) is a
  matter of adding a field to `exportConfig` and a clause to `filterTapes`.
- **Memory overhead**: All tapes are loaded then filtered. Acceptable for v1.
  A future optimization could pass filters down to `Store.List` if stores grow
  large.
- **Manifest accuracy**: Because filtering happens before `buildManifest`, the
  manifest always accurately reflects the filtered set with no extra logic.

---

### ADR-12: MatchPathRegex — Regex path matching criterion

**Date**: 2026-03-30
**Issue**: #38
**Status**: Accepted

#### Context

Exact path matching (`MatchPath`, ADR-4) does not work for parameterized REST
APIs. A fixture recorded for `/users/123/orders` cannot serve a replay request
for `/users/456/orders` because the path segments differ. Issue #38 requests a
regex-based path matching criterion so a single recorded fixture can match
multiple parameterized URLs.

The existing matcher infrastructure from ADR-4 is designed for exactly this kind
of extension: add a new `MatchCriterion` function, assign it a score weight, and
users compose it into a `CompositeMatcher`. No changes to the `Matcher` interface,
`CompositeMatcher`, or existing criteria are needed.

Key constraints:
- Stdlib only (L-04). Go's `regexp` package is stdlib.
- No panics (L-11). Invalid regex must be reported as an error, not a panic.
- Progressive matching (L-09). Regex is opt-in; `DefaultMatcher` is unchanged.
- Functional options pattern (L-12). `MatchPathRegex` follows the same pattern
  as existing criteria constructors.

#### Decision

##### MatchPathRegex constructor

```go
// MatchPathRegex returns a MatchCriterion that matches the incoming request's
// URL path against a compiled regular expression, and also verifies that the
// candidate tape's stored URL path matches the same expression. This ensures
// that only tapes belonging to the same "path family" as the request are
// considered matches.
//
// The pattern is compiled once at construction time using regexp.Compile.
// If the pattern is invalid, MatchPathRegex returns a non-nil error and a
// nil MatchCriterion. Callers must check the error before using the criterion.
//
// Returns score 1 on match, 0 on mismatch.
//
// Usage: use MatchPathRegex as a replacement for MatchPath when regex matching
// is desired, not alongside it. If MatchPath is also present in the same
// CompositeMatcher, candidates that do not exact-match will be eliminated by
// MatchPath (score 0) regardless of the regex result.
//
// Example:
//
//	criterion, err := MatchPathRegex(`^/users/\d+/orders$`)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	matcher := NewCompositeMatcher(MatchMethod(), criterion)
func MatchPathRegex(pattern string) (MatchCriterion, error)
```

##### Why return (MatchCriterion, error) instead of panicking

All existing criterion constructors (`MatchMethod`, `MatchPath`, etc.) return
`MatchCriterion` directly with no error. They cannot fail because they take no
user-controlled input that could be invalid. `MatchPathRegex` takes a regex
pattern string that can be syntactically invalid. The project constitution (L-11)
prohibits panics. Therefore `MatchPathRegex` must return an error.

This is a deliberate break from the constructor signature pattern of other
criteria. The alternative — accepting a pre-compiled `*regexp.Regexp` — was
considered but rejected because:
- It leaks the `regexp` type into the public API for callers who just want to
  pass a string pattern.
- It shifts the compilation responsibility to the caller, who might forget to
  handle the compile error or might compile with `MustCompile` (which panics).
- The `(MatchCriterion, error)` return is idiomatic Go and clearly communicates
  that construction can fail.

##### Regex compilation: at construction time, not per call

The regex is compiled once via `regexp.Compile` when `MatchPathRegex` is called.
The resulting `*regexp.Regexp` is captured in the closure returned as the
`MatchCriterion`. This means:
- Invalid patterns fail fast at configuration time, not at request time.
- No repeated compilation overhead per request or per candidate.
- The `*regexp.Regexp` is safe for concurrent use (Go's regexp is goroutine-safe
  after compilation), so the criterion can be shared across goroutines.

##### Matching logic

The criterion performs two checks:

1. **Request path check**: `re.MatchString(req.URL.Path)` — does the incoming
   request's URL path match the pattern?
2. **Tape path check**: Parse `candidate.Request.URL` with `url.Parse`, then
   `re.MatchString(parsed.Path)` — does the tape's stored URL path match the
   same pattern?

Both must match for the criterion to return a positive score. This dual-match
design ensures that:
- A tape recorded for `/users/123/orders` matches a request for
  `/users/456/orders` when the pattern is `^/users/\d+/orders$` (both paths
  belong to the same "family").
- A tape recorded for `/products/42` does NOT match a request for
  `/users/456/orders` even though the request matches the user-orders pattern
  (the tape path does not match the pattern, so it is eliminated).

If the tape's URL cannot be parsed, the criterion returns 0 (consistent with
`MatchPath` and `MatchQueryParams` behavior from ADR-4).

##### Score weight: 1

| Criterion | Score | Rationale |
|---|---|---|
| `MatchMethod` | 1 | Low specificity — many tapes share a method. |
| `MatchPath` | 2 | Exact path — high specificity. |
| **`MatchPathRegex`** | **1** | **Regex path — lower specificity than exact.** |
| `MatchRoute` | 1 | Scoping label. |
| `MatchQueryParams` | 4 | Significantly narrows candidates. |
| `MatchBodyHash` | 8 | Most specific — uniquely identifies a request body. |

Score 1 is correct because:
- A regex match is inherently less specific than an exact path match (a regex
  can match many paths, an exact match matches exactly one).
- Score 1 ensures that if a user builds two `CompositeMatcher` instances — one
  with `MatchPath` (score 2) and one with `MatchPathRegex` (score 1) — and uses
  a fallback strategy, the exact matcher naturally produces higher scores.
- Score 1 is also appropriate because the regex pattern is user-provided and may
  be broad (e.g., `.*`) or narrow (e.g., `^/users/\d+$`). A fixed low score
  avoids overweighting broad patterns.

Note: `MatchPathRegex` is intended as a **replacement** for `MatchPath` in a
given `CompositeMatcher`, not as an addition alongside it. Using both in the same
`CompositeMatcher` would cause `MatchPath` to eliminate candidates that don't
exact-match, defeating the purpose of regex matching. The godoc on
`MatchPathRegex` documents this usage pattern.

##### Implementation sketch

```go
func MatchPathRegex(pattern string) (MatchCriterion, error) {
    re, err := regexp.Compile(pattern)
    if err != nil {
        return nil, fmt.Errorf("httptape: invalid path regex %q: %w", pattern, err)
    }
    return func(req *http.Request, candidate Tape) int {
        if !re.MatchString(req.URL.Path) {
            return 0
        }
        parsed, err := url.Parse(candidate.Request.URL)
        if err != nil {
            return 0
        }
        if !re.MatchString(parsed.Path) {
            return 0
        }
        return 1
    }, nil
}
```

#### File layout

| File | Contents | New/Modified |
|---|---|---|
| `matcher.go` | `MatchPathRegex` function | Modified — add function and `regexp` + `fmt` imports |
| `matcher_test.go` | Table-driven tests for `MatchPathRegex` | Modified — add test functions |

No other files are modified. `DefaultMatcher` is unchanged.

#### Test strategy

**`matcher_test.go`** — all tests are table-driven, following the patterns
established in ADR-4.

**Individual criterion tests:**

| Test | What it verifies |
|---|---|
| `TestMatchPathRegex_Match` | Pattern `^/users/\d+/orders$` matches request `/users/456/orders` against tape with URL `https://api.example.com/users/123/orders`. Returns 1. |
| `TestMatchPathRegex_RequestNoMatch` | Pattern `^/users/\d+/orders$` does not match request `/products/1`. Returns 0. |
| `TestMatchPathRegex_TapeNoMatch` | Request path matches pattern but tape path does not (different tape). Returns 0. |
| `TestMatchPathRegex_UnparsableTapeURL` | Tape URL is garbage. Returns 0. |
| `TestMatchPathRegex_InvalidPattern` | `MatchPathRegex("[invalid")` returns non-nil error and nil criterion. |
| `TestMatchPathRegex_BroadPattern` | Pattern `.*` matches everything. Returns 1. |
| `TestMatchPathRegex_AnchoredPattern` | Pattern without anchors matches partial paths. Pattern with `^` and `$` matches only full paths. |

**Composition tests:**

| Test | What it verifies |
|---|---|
| `TestCompositeMatcher_RegexPath` | `NewCompositeMatcher(MatchMethod(), regexCriterion)` selects correct tape from multiple candidates with parameterized paths. |
| `TestCompositeMatcher_ExactBeatsRegex` | Two separate matchers: one with `MatchPath` (exact), one with `MatchPathRegex`. Exact matcher scores higher (3 vs 2 for method+path vs method+regex). Demonstrates the priority relationship. |

#### Error cases

| Scenario | Behavior |
|---|---|
| Invalid regex pattern (e.g., `[invalid`) | `MatchPathRegex` returns `(nil, error)`. Error wraps the `regexp.Compile` error with context. |
| Valid pattern, request path does not match | Criterion returns 0. Candidate eliminated. |
| Valid pattern, tape URL cannot be parsed | Criterion returns 0. Candidate eliminated. Other candidates still evaluated. |
| Valid pattern, request matches but tape does not | Criterion returns 0. Candidate eliminated. |
| Empty pattern `""` | Compiles successfully (matches any string). Both request and tape paths match. Returns 1. |

#### Consequences

- **No changes to existing code**: `MatchPathRegex` is a new function added to
  `matcher.go`. No existing functions, types, or interfaces are modified.
  `DefaultMatcher` is unchanged.
- **Idiomatic error handling**: The `(MatchCriterion, error)` return type is a
  deliberate departure from other criterion constructors that cannot fail. This
  is the correct Go idiom for fallible construction.
- **Compile-once semantics**: The regex is compiled once and reused for all
  candidate evaluations. No per-request or per-candidate compilation overhead.
  The compiled `*regexp.Regexp` is goroutine-safe.
- **Dual-match design**: Checking both request and tape paths against the pattern
  ensures tapes are only matched against requests in the same "path family."
  This prevents a regex like `.*` from matching any tape against any request
  (both would match, but the tape must also match the pattern).
- **Score 1 ensures exact > regex**: When comparing `CompositeMatcher` instances,
  one using `MatchPath` (score 2) and one using `MatchPathRegex` (score 1), the
  exact matcher naturally wins. This satisfies the acceptance criteria.
- **Future extension point**: Path parameter extraction (capturing groups for
  response interpolation) is explicitly out of scope per issue #38. The compiled
  `*regexp.Regexp` already supports capturing groups, so a future criterion or
  wrapper could extract matches without changing `MatchPathRegex` itself.
- **Import addition**: `matcher.go` will need `regexp` and `fmt` added to its
  import block. Both are stdlib (L-04 compliant).

---

### ADR-13: MatchHeaders — Header-based matching criterion

**Date**: 2026-03-30
**Issue**: #39
**Status**: Accepted

#### Context

Some APIs differentiate request types using HTTP headers rather than URL paths.
Common examples include API versioning via the `Accept` header
(`application/vnd.api.v2+json`), content negotiation via `Content-Type`, and
feature flags via custom headers (`X-Feature-Flag: new-checkout`). When the same
URL path serves different responses based on headers, the existing matcher
criteria (method, path, query params, body hash) cannot distinguish between them.

Issue #39 requests a `MatchHeaders` criterion that lets users include specific
header key-value pairs in matching criteria.

The existing matcher infrastructure from ADR-4 supports this directly: add a new
`MatchCriterion` function, assign it a score weight, and users compose it into a
`CompositeMatcher`. No changes to the `Matcher` interface, `CompositeMatcher`, or
existing criteria are needed.

Key constraints:
- Stdlib only (L-04). `net/http` provides `http.CanonicalHeaderKey` and
  `http.Header` — both stdlib.
- No panics (L-11). Header key and value are plain strings; no fallible parsing.
- Progressive matching (L-09). Header matching is opt-in; `DefaultMatcher` is
  unchanged.
- HTTP spec compliance: header names are case-insensitive per RFC 7230 section
  3.2. Header values are case-sensitive.

#### Decision

##### MatchHeaders constructor

```go
// MatchHeaders returns a MatchCriterion that requires the specified header to
// be present in both the incoming request and the candidate tape's recorded
// request, with an exact value match.
//
// The header name is canonicalized using http.CanonicalHeaderKey, making it
// case-insensitive per HTTP specification (RFC 7230 section 3.2). The header
// value comparison is exact and case-sensitive.
//
// If the header has multiple values in either the request or the tape, the
// criterion checks whether the specified value appears among them (any-of
// semantics). This handles the common case where a header may be set multiple
// times (e.g., multiple Accept values).
//
// To require multiple headers, add multiple MatchHeaders criteria to the
// CompositeMatcher. They are AND-ed together naturally: if any criterion
// returns 0, the candidate is eliminated.
//
// Returns score 3 on match, 0 on mismatch.
//
// Example:
//
//	matcher := NewCompositeMatcher(
//	    MatchMethod(),
//	    MatchPath(),
//	    MatchHeaders("Accept", "application/vnd.api.v2+json"),
//	    MatchHeaders("X-Feature-Flag", "new-checkout"),
//	)
func MatchHeaders(key, value string) MatchCriterion
```

##### Why a single key-value pair per call (not a map)

The alternative — `MatchHeaders(headers map[string]string)` accepting multiple
key-value pairs at once — was considered. The single-pair design was chosen
because:

- It composes naturally with `CompositeMatcher`. Each criterion is independent
  and scores independently. Users can mix header criteria with other criteria
  freely.
- It is consistent with the existing criterion constructor pattern: each
  constructor returns one `MatchCriterion` for one dimension.
- A map-based constructor would hide the AND semantics inside the function rather
  than relying on the well-documented `CompositeMatcher` elimination behavior.
- Each header criterion contributes its own score (3 per header), so matching
  more headers naturally produces a higher total score. A map-based approach
  would need to decide on a single combined score.

##### Case sensitivity

- **Header name**: Canonicalized via `http.CanonicalHeaderKey` before comparison.
  This means `MatchHeaders("content-type", "application/json")` and
  `MatchHeaders("Content-Type", "application/json")` behave identically. This
  follows RFC 7230 section 3.2 which states header field names are
  case-insensitive.
- **Header value**: Exact, case-sensitive comparison. While some header values
  have case-insensitive semantics (e.g., media type tokens in `Content-Type`),
  the HTTP spec does not mandate case-insensitive values globally. Exact matching
  is the safe default; case-insensitive value matching could be added as a
  separate criterion in the future if needed.

##### Matching logic

The criterion performs two checks:

1. **Request header check**: Look up `http.CanonicalHeaderKey(key)` in
   `req.Header`. Check if `value` appears among the header's values.
2. **Tape header check**: Look up `http.CanonicalHeaderKey(key)` in
   `candidate.Request.Headers`. Check if `value` appears among the header's
   values.

Both must match for the criterion to return a positive score. This dual-match
design (consistent with `MatchPathRegex`, ADR-12) ensures that:
- A tape recorded with `Accept: application/vnd.api.v2+json` matches a request
  with the same header value.
- A tape recorded with `Accept: application/json` does NOT match a request with
  `Accept: application/vnd.api.v2+json`, even though both share the same path.

If the header is absent from either the request or the tape, the criterion
returns 0.

##### Score weight: 3

| Criterion | Score | Rationale |
|---|---|---|
| `MatchMethod` | 1 | Low specificity — many tapes share a method. |
| `MatchPath` | 2 | Exact path — high specificity. |
| `MatchPathRegex` | 1 | Regex path — lower specificity than exact. |
| `MatchRoute` | 1 | Scoping label. |
| **`MatchHeaders`** | **3** | **Per header — moderate specificity.** |
| `MatchQueryParams` | 4 | Significantly narrows candidates. |
| `MatchBodyHash` | 8 | Most specific — uniquely identifies a request body. |

Score 3 is correct because:
- A single header match is more specific than a path match (score 2) but less
  specific than query parameter matching (score 4, which checks all params at
  once).
- Multiple header criteria stack: two headers contribute 6, three contribute 9.
  This naturally increases the total score as more headers are required, which is
  the desired behavior — more constrained matches score higher.
- Score 3 keeps the power-of-two-ish spacing from ADR-4. The progression is
  1 (method) < 2 (path) < 3 (header) < 4 (query) < 8 (body hash).

##### No error return

Unlike `MatchPathRegex` (ADR-12), `MatchHeaders` takes plain string arguments
that cannot be syntactically invalid. There is no fallible parsing step.
Therefore the constructor returns `MatchCriterion` directly, consistent with
`MatchMethod`, `MatchPath`, `MatchRoute`, and `MatchQueryParams`.

##### Any-of semantics for multi-valued headers

HTTP headers can have multiple values (either comma-separated in a single header
line or via repeated header lines). Go's `http.Header` type stores these as a
`[]string` slice. The criterion checks whether the specified `value` appears
anywhere in the slice, rather than requiring it to be the only value. This
handles real-world cases like:

```
Accept: text/html
Accept: application/json
```

where `MatchHeaders("Accept", "application/json")` should match even though
`text/html` is also present.

##### Implementation sketch

```go
func MatchHeaders(key, value string) MatchCriterion {
    canonicalKey := http.CanonicalHeaderKey(key)
    return func(req *http.Request, candidate Tape) int {
        if !headerContains(req.Header, canonicalKey, value) {
            return 0
        }
        if !headerContains(candidate.Request.Headers, canonicalKey, value) {
            return 0
        }
        return 3
    }
}

// headerContains reports whether the header map contains the specified
// canonical key with the specified value among its values.
func headerContains(h http.Header, canonicalKey, value string) bool {
    values := h[canonicalKey]
    for _, v := range values {
        if v == value {
            return true
        }
    }
    return false
}
```

Key points:
- `http.CanonicalHeaderKey` is called once at construction time, not per
  evaluation. This avoids repeated string manipulation during matching.
- Direct map access with the canonical key (`h[canonicalKey]`) is used instead of
  `h.Get(key)` because `Get` only returns the first value, while we need to
  check all values for multi-valued headers.
- The `headerContains` helper is unexported and keeps the matching logic clean.

#### Consequences

- **New file changes**: `matcher.go` gains `MatchHeaders` and `headerContains`.
  No new imports are needed beyond what already exists (`net/http` is already
  imported).
- **No breaking changes**: This is a purely additive change. `DefaultMatcher`
  is not affected. Existing code continues to work unchanged.
- **Test requirements**: `matcher_test.go` must cover: single header match,
  multiple header criteria (AND semantics), case-insensitive header names,
  missing header (no match), wrong header value (no match), and multi-valued
  headers.
- **Progressive matching preserved**: Header matching is opt-in. Users who do
  not need it are unaffected. Users who need it add `MatchHeaders(...)` calls
  to their `CompositeMatcher`.
- **Composability**: `MatchHeaders` composes naturally with all existing criteria.
  A typical advanced matcher might be:
  `NewCompositeMatcher(MatchMethod(), MatchPath(), MatchHeaders("Accept", "..."), MatchQueryParams())`.
- **Future extensions**: If substring or regex header value matching is needed in
  the future, it can be added as a separate criterion (e.g.,
  `MatchHeadersRegex(key, pattern string)`) following the same pattern as
  `MatchPathRegex` (ADR-12). The exact-match default is the safe starting point.

### ADR-14: MatchBodyFuzzy — Field-level body matching criterion

**Date**: 2026-03-30
**Issue**: #40
**Status**: Accepted

#### Context

`MatchBodyHash` (ADR-4) compares the SHA-256 hash of the entire request body.
This is maximally specific but brittle: if the request body contains fields
that vary per invocation — timestamps, nonces, request IDs, CSRF tokens — the
hash will never match a recorded fixture even though the "meaningful" fields
are identical.

Users need a way to say "match these specific body fields and ignore everything
else." This is the body-matching analog of `MatchQueryParams`: compare a
declared subset and ignore the rest.

Key constraints:
- Reuse the existing JSONPath-like path syntax (`$.field`, `$.nested.field`,
  `$.array[*].field`) already established in `sanitizer.go` for
  `RedactBodyPaths` and `FakeFields`.
- Reuse the existing `parsePath`/`parsedPath`/`segment` types from
  `sanitizer.go` to avoid duplicating path-parsing logic.
- Both the incoming request body and the tape's stored `Request.Body` must be
  unmarshaled as JSON for field-level comparison.
- Non-JSON bodies must not cause a panic or error — the criterion should
  return 0 (no match) gracefully, letting other criteria decide.
- Stdlib only (L-03, L-04). No panics (L-11).

#### Decision

##### Public API (`matcher.go`)

```go
// MatchBodyFuzzy returns a MatchCriterion that compares specific fields in
// the JSON request body between the incoming request and the candidate tape.
// Only the fields at the specified paths are compared; all other fields are
// ignored. This is useful when request bodies contain volatile fields
// (timestamps, nonces, request IDs) that vary per invocation.
//
// Paths use the same JSONPath-like syntax as RedactBodyPaths and FakeFields:
//   - $.field             -- top-level field
//   - $.nested.field      -- nested field access
//   - $.array[*].field    -- field within each element of an array
//
// Matching semantics:
//   - Both bodies are unmarshaled as JSON. If either body is not valid JSON,
//     the criterion returns 0 (no match).
//   - For each specified path, the value is extracted from both the request
//     and the tape body. If a path does not exist in both bodies, it is
//     skipped (does not cause a mismatch).
//   - If a path exists in both bodies, the extracted values must be
//     deeply equal (compared via reflect.DeepEqual on the unmarshaled
//     any values). If any compared field differs, the criterion returns 0.
//   - If no paths are provided, or no paths match fields present in both
//     bodies, the criterion returns 0 (no match — nothing to compare means
//     no evidence of a match).
//   - If at least one path matched and all matched fields are equal, the
//     criterion returns its score.
//
// The request body is read fully, then restored (replaced with a new reader
// over the same bytes) so subsequent criteria can read it again.
//
// Invalid or unsupported paths are silently ignored (same as RedactBodyPaths).
//
// Returns score 6 on match, 0 on mismatch.
//
// Example:
//
//	matcher := NewCompositeMatcher(
//	    MatchMethod(),
//	    MatchPath(),
//	    MatchBodyFuzzy("$.action", "$.user.id", "$.items[*].sku"),
//	)
func MatchBodyFuzzy(paths ...string) MatchCriterion
```

##### Score weight: 6

| Criterion | Score | Rationale |
|---|---|---|
| `MatchMethod` | 1 | Low specificity — many tapes share a method. |
| `MatchPath` | 2 | More specific than method alone. |
| `MatchHeaders` | 3 | Narrows by content negotiation / feature flags. |
| `MatchQueryParams` | 4 | Significantly narrows candidates. |
| **`MatchBodyFuzzy`** | **6** | **Partial body specificity — between query params and exact body hash.** |
| `MatchBodyHash` | 8 | Most specific — uniquely identifies the full body. |

Rationale for score 6:
- Fuzzy body matching is more specific than query params (score 4) because it
  inspects the actual request payload, which typically carries richer
  information than URL parameters.
- It is less specific than `MatchBodyHash` (score 8) because it deliberately
  ignores some fields. A full body hash match is stronger evidence.
- Score 6 sits between 4 and 8, preserving the power-of-two-ish spacing from
  ADR-4. Importantly, `MatchBodyFuzzy` alone (6) cannot outscore
  `MatchBodyHash` (8), so an exact body match always wins when both criteria
  are present in the same `CompositeMatcher`.
- Users should choose one of `MatchBodyFuzzy` or `MatchBodyHash`, not both.
  If both are present, `MatchBodyHash` will eliminate candidates whose full
  body differs (score 0), making `MatchBodyFuzzy` redundant. This is safe
  but wasteful. The godoc should note this.

##### Internal implementation (`matcher.go`)

```go
func MatchBodyFuzzy(paths ...string) MatchCriterion {
    // Parse all paths at construction time (reuses parsePath from sanitizer.go).
    var parsed []parsedPath
    for _, p := range paths {
        if pp, ok := parsePath(p); ok {
            parsed = append(parsed, pp)
        }
    }

    return func(req *http.Request, candidate Tape) int {
        if len(parsed) == 0 {
            return 0
        }

        // Read and restore the incoming request body.
        var reqBody []byte
        if req.Body != nil {
            bodyBytes, err := io.ReadAll(req.Body)
            if err != nil {
                return 0
            }
            req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
            reqBody = bodyBytes
        }

        // Unmarshal both bodies.
        var reqData, tapeData any
        if err := json.Unmarshal(reqBody, &reqData); err != nil {
            return 0
        }
        if err := json.Unmarshal(candidate.Request.Body, &tapeData); err != nil {
            return 0
        }

        // Compare specified fields.
        matched := 0
        for _, p := range parsed {
            reqVal, reqOk := extractAtPath(reqData, p.segments)
            tapeVal, tapeOk := extractAtPath(tapeData, p.segments)

            if !reqOk || !tapeOk {
                // Path doesn't exist in one or both — skip, not a mismatch.
                continue
            }

            if !reflect.DeepEqual(reqVal, tapeVal) {
                return 0 // field exists in both but values differ — eliminate.
            }
            matched++
        }

        if matched == 0 {
            return 0 // no fields compared — no evidence of match.
        }
        return 6
    }
}
```

##### New helper: `extractAtPath` (`matcher.go`)

```go
// extractAtPath traverses the JSON structure following the given segments
// and returns the value at the leaf. Returns (value, true) if the path
// exists, or (nil, false) if any segment is missing or the structure does
// not match (e.g., expected object but found array).
//
// For wildcard segments (array[*].field), it collects the matching values
// from all array elements into a []any slice and returns that.
func extractAtPath(data any, segments []segment) (any, bool)
```

Implementation outline:
- Walk the `segments` slice, at each step narrowing `data`.
- For a non-wildcard segment: assert `data` is `map[string]any`, look up
  `seg.key`. If missing, return `(nil, false)`.
- For a wildcard segment: assert `data` is `map[string]any`, look up
  `seg.key`, assert the result is `[]any`, then recurse into each element
  with the remaining segments. Collect the results into a `[]any` for
  comparison. If any element is missing the remaining path, return
  `(nil, false)` (all-or-nothing semantics for arrays).
- At the leaf (no remaining segments), return `(data, true)`.

This helper mirrors `redactAtPath` and `fakeAtPath` from `sanitizer.go` but
extracts rather than mutates. It belongs in `matcher.go` because it is
matching infrastructure, not sanitization.

##### Why `reflect.DeepEqual`

`reflect.DeepEqual` is used to compare the extracted `any` values because:
- JSON unmarshaling produces nested `map[string]any`, `[]any`, `string`,
  `float64`, `bool`, and `nil`. `reflect.DeepEqual` handles all of these
  correctly, including nested structures and arrays.
- It is stdlib (`reflect` package), consistent with the zero-dependency rule.
- The comparison happens on already-unmarshaled data, so the cost is the
  comparison itself, not serialization. For the typical field sizes in API
  bodies (strings, numbers, small objects), this is negligible.
- Alternative: marshal both values back to JSON bytes and compare. This is
  more work, introduces ordering concerns for maps, and is less readable.
  `reflect.DeepEqual` is the idiomatic Go approach here.

##### New import: `reflect`

`matcher.go` will gain `import "reflect"` for `reflect.DeepEqual`. This is
stdlib, so it does not violate the zero-dependency rule.

##### Reuse of `parsePath` / `parsedPath` / `segment`

These types and functions are already defined in `sanitizer.go` and are
unexported (lowercase). Since httptape is a single flat package, they are
directly accessible from `matcher.go` without any refactoring. No code needs
to move.

This is the intended benefit of the single-package architecture: internal
types are shared across files without needing `internal/` sub-packages.

#### Consequences

- **New file changes**: `matcher.go` gains `MatchBodyFuzzy` and the unexported
  `extractAtPath` helper. New imports: `encoding/json`, `reflect`. The existing
  `io` and `bytes` imports are already present (used by `MatchBodyHash`).
- **No breaking changes**: Purely additive. `DefaultMatcher` is unaffected.
  Existing code continues to work unchanged.
- **Test requirements**: `matcher_test.go` must cover:
  - Match on single body field, ignoring other differences.
  - Match on multiple body fields.
  - Match on nested fields (`$.nested.field`).
  - Match on array element fields (`$.array[*].field`).
  - No match when a specified field value differs.
  - Non-JSON body gracefully returns 0 (no match).
  - Empty paths list returns 0.
  - Path exists in request but not in tape (skipped, not mismatch).
  - Path exists in tape but not in request (skipped, not mismatch).
  - Both bodies empty: returns 0 (nothing to compare).
  - Composability: works alongside `MatchMethod`, `MatchPath`, `MatchHeaders`
    in a `CompositeMatcher`.
- **Progressive matching preserved**: Fuzzy body matching is opt-in. Users who
  do not need it are unaffected. Users who need it add
  `MatchBodyFuzzy("$.action", "$.user.id")` to their `CompositeMatcher`.
- **Mutual exclusivity note**: Using both `MatchBodyFuzzy` and `MatchBodyHash`
  in the same `CompositeMatcher` is safe but semantically redundant. If
  `MatchBodyHash` passes (exact match), `MatchBodyFuzzy` will also pass. If
  `MatchBodyHash` fails, the candidate is already eliminated. The godoc for
  `MatchBodyFuzzy` should note this.
- **Performance**: Each evaluation unmarshals both the request body and the
  tape body as JSON. For typical API bodies (a few KB), this is fast. For
  very large bodies, users should prefer `MatchBodyHash` or limit the number
  of candidates via other criteria (method, path) that short-circuit first.
  The `CompositeMatcher` already evaluates criteria in order and short-circuits
  on score 0, so placing `MatchMethod` and `MatchPath` before `MatchBodyFuzzy`
  naturally limits the number of JSON unmarshalings.

---

### ADR-15: Concurrent safety audit

**Date**: 2026-03-30
**Issue**: #41
**Status**: Accepted

#### Context

httptape types will be used in concurrent Go programs: HTTP servers, test suites
with `t.Parallel()`, and production middleware with many goroutines hitting the
same Recorder or Server simultaneously. All public types must be safe for
concurrent use without external synchronization. Issue #41 requires a full audit.

#### Audit results

##### Already safe

1. **MemoryStore** — Uses `sync.RWMutex`. Read operations (`Load`, `List`) take
   `RLock`; write operations (`Save`, `Delete`) take full `Lock`. All values are
   deep-copied on read and write via `deepCopyTape`, preventing aliasing between
   caller-held data and store internals. **Verdict: safe.**

2. **FileStore** — Uses `sync.RWMutex` (same pattern as MemoryStore). Writes use
   atomic temp-file-then-rename. Safe for concurrent use within a single process.
   Not safe for multi-process concurrent access (out of scope per issue #41).
   **Verdict: safe (single-process).**

3. **Server** — All fields (`store`, `matcher`, `fallbackStatus`, `fallbackBody`,
   `onNoMatch`) are set during construction and never mutated afterward. The
   `ServeHTTP` method is read-only with respect to Server's own state; it
   delegates to `Store.List` and `Matcher.Match`, whose safety is guaranteed by
   their own contracts. Each `*http.Request` is distinct per HTTP connection, so
   there is no aliasing of request bodies across goroutines.
   **Verdict: safe (immutable after construction).**

4. **Pipeline (Sanitizer)** — Immutable after construction. `NewPipeline` copies
   the `funcs` slice. `Sanitize` iterates the slice without modification. Each
   `SanitizeFunc` (RedactHeaders, RedactBodyPaths, FakeFields) operates on
   copies of the Tape's data (headers are cloned, bodies are unmarshaled into
   fresh structures). **Verdict: safe.**

5. **CompositeMatcher** — Immutable after construction. The `criteria` slice is
   set once in `NewCompositeMatcher` and never modified. `Match` iterates
   read-only. Individual `MatchCriterion` closures capture only immutable state
   (compiled regexes, pre-parsed paths, constant strings). `MatchBodyHash` and
   `MatchBodyFuzzy` read and restore `req.Body`, but this is safe because each
   HTTP request is processed by one goroutine at a time (standard `net/http`
   contract). **Verdict: safe.**

6. **ExportBundle / ImportBundle** — Package-level functions with no shared
   mutable state. Safety depends entirely on the Store implementation passed in.
   **Verdict: safe (stateless functions).**

7. **Core types (Tape, RecordedReq, RecordedResp)** — Pure value types with no
   methods that mutate shared state. Thread safety is the responsibility of the
   container (Store). **Verdict: safe (pure data).**

##### Gap identified: Recorder close/send race

**Recorder** uses `atomic.Bool` (`closed`) as a guard in `RoundTrip` to avoid
sending on a closed channel. However, there is a **TOCTOU race** between the
`closed.Load()` check and the channel send:

```
Goroutine A (RoundTrip):          Goroutine B (Close):
  if r.closed.Load() → false
                                    r.closed.Store(true)
                                    close(r.tapeCh)     // channel closed
  r.tapeCh <- tape                  // PANIC: send on closed channel
```

The `select` with a `default` case does NOT protect against send-on-closed-channel.
Go's `select` can choose the `case r.tapeCh <- tape` branch even if the channel
was closed between the `closed.Load()` check and the `select` evaluation.

**Fix**: Replace the `atomic.Bool` + `close(tapeCh)` pattern with a
`sync.Mutex`-guarded approach, or use a "poison pill" sentinel (send a zero-value
Tape and close-after-drain). The recommended fix:

```go
// In Recorder, add a dedicated mutex for the close/send coordination:
sendMu sync.Mutex

// In RoundTrip (async branch):
r.sendMu.Lock()
if r.closed.Load() {
    r.sendMu.Unlock()
    // recorder closed — drop tape silently
} else {
    select {
    case r.tapeCh <- tape:
    default:
        if r.onError != nil {
            r.onError(fmt.Errorf("httptape: recorder buffer full, tape dropped"))
        }
    }
    r.sendMu.Unlock()
}

// In Close:
r.closeOnce.Do(func() {
    r.sendMu.Lock()
    r.closed.Store(true)
    close(r.tapeCh)
    r.sendMu.Unlock()
    <-r.done
})
```

This ensures that `close(r.tapeCh)` never happens while another goroutine is
between the `closed` check and the channel send. The mutex is held only briefly
(no I/O under lock).

#### Decision

1. **MemoryStore, FileStore, Server, Pipeline, CompositeMatcher, Bundle functions**:
   already concurrent-safe. No code changes needed — only godoc additions.

2. **Recorder**: Fix the TOCTOU race between `RoundTrip` and `Close` by adding a
   `sendMu sync.Mutex` that coordinates the closed-check-then-send sequence with
   the close-channel sequence. Keep `atomic.Bool` for the fast-path read in the
   non-async code path and for the pre-check before acquiring the mutex.

3. **Godoc guarantees to add**: Every public type must document its concurrency
   contract in its godoc comment. Specifically:
   - `MemoryStore`: "Safe for concurrent use by multiple goroutines."
   - `FileStore`: "Safe for concurrent use by multiple goroutines within a single
     process. Not safe for multi-process concurrent access."
   - `Recorder`: "Safe for concurrent use by multiple goroutines. RoundTrip may
     be called from multiple goroutines simultaneously. Close must be called
     exactly once when recording is complete."
   - `Server`: "Safe for concurrent use by multiple goroutines. All fields are
     immutable after construction."
   - `Pipeline`: "Safe for concurrent use — immutable after construction."
   - `CompositeMatcher`: "Safe for concurrent use — immutable after construction."

4. **Race tests to add** (in the Dev phase):
   - Multiple goroutines calling `MemoryStore.Save` and `MemoryStore.Load`
     concurrently.
   - Multiple goroutines calling `FileStore.Save` and `FileStore.Load`
     concurrently.
   - Multiple goroutines calling `Recorder.RoundTrip` concurrently, then calling
     `Close`.
   - Multiple goroutines calling `Server.ServeHTTP` concurrently.
   - All tests must pass under `go test -race ./...`.

#### Consequences

- **Recorder gains one new field**: `sendMu sync.Mutex`. This is a small addition
  with no API impact.
- **No breaking changes**: All fixes are internal. The public API is unchanged.
- **Performance impact**: The `sendMu` mutex in the async path adds minimal
  overhead — it is held only for the duration of a non-blocking channel send
  (nanoseconds). The fast-path `closed.Load()` check before acquiring the mutex
  avoids contention after Close has been called.
- **Godoc updates**: Every public type gets an explicit concurrency guarantee.
  This is documentation-only and helps users reason about thread safety.
- **Test coverage**: Race tests validate correctness under `-race`. These tests
  are primarily exercised by the CI pipeline (`go test -race ./...`).

---

### ADR-16: Performance benchmark suite

**Date**: 2026-03-30
**Issue**: #42
**Status**: Accepted

#### Context

httptape will be used in two performance-sensitive positions: (1) as a production
middleware via `Recorder` wrapping an `http.RoundTripper`, and (2) as a test mock
via `Server` implementing `http.Handler`. Users need documented performance
characteristics to decide whether httptape is acceptable for their use case.

Issue #42 requests a comprehensive benchmark suite using Go's `testing.B`
framework. The goal is measurement, not optimization -- benchmarks establish
baselines so regressions can be detected and users can make informed decisions.

Performance targets (informational, not hard gates):
- Recorder overhead per request in async mode: **< 50us** added latency
- Server response latency for exact match lookup: **< 1ms**
- Sanitizer throughput for large JSON bodies: measurable at 1KB, 10KB, 100KB
- FileStore write throughput: measurable sequential and concurrent

#### Decision

##### Benchmark organization

Benchmarks live in `*_test.go` files alongside the code they measure, following
Go convention. Each benchmark file covers one component:

| File | Benchmarks |
|---|---|
| `recorder_test.go` | Recorder hot-path overhead |
| `server_test.go` | Server response latency |
| `sanitizer_test.go` | Sanitizer pipeline throughput |
| `store_file_test.go` | FileStore write throughput |
| `matcher_test.go` | Matcher performance with varying fixture counts |

##### Benchmark 1: Recorder overhead (`recorder_test.go`)

Measures the added latency of `Recorder.RoundTrip` beyond the inner transport's
own latency. Uses a no-op `http.RoundTripper` stub that returns a canned response
immediately, so the benchmark isolates recorder overhead (body capture, tape
construction, sanitizer, channel send).

```go
// BenchmarkRecorderRoundTrip_Async measures the overhead of async recording.
// The inner transport is a no-op stub returning a fixed response.
// Target: < 50us per operation.
func BenchmarkRecorderRoundTrip_Async(b *testing.B)

// BenchmarkRecorderRoundTrip_Sync measures sync recording overhead for comparison.
func BenchmarkRecorderRoundTrip_Sync(b *testing.B)
```

Implementation notes:
- The no-op transport stub (`noopTransport`) returns a pre-built `*http.Response`
  with a small body (e.g., `{"ok":true}`) and status 200.
- The `MemoryStore` is used as the store (avoids filesystem noise).
- A no-op sanitizer (default `NewPipeline()`) is used to measure the baseline.
  A separate sub-benchmark adds `RedactHeaders()` to show sanitizer cost.
- The recorder is created once in the benchmark setup; `b.ResetTimer()` is called
  before the loop.
- `b.ReportAllocs()` is called to track allocation overhead.
- After the loop, `recorder.Close()` is called to flush the channel.

Sub-benchmarks:
- `BenchmarkRecorderRoundTrip_Async/NoSanitizer` -- baseline
- `BenchmarkRecorderRoundTrip_Async/WithRedactHeaders` -- adds header redaction
- `BenchmarkRecorderRoundTrip_Async/SmallBody` -- 100-byte request+response body
- `BenchmarkRecorderRoundTrip_Async/MediumBody` -- 10KB request+response body

##### Benchmark 2: Server response latency (`server_test.go`)

Measures the end-to-end latency of `Server.ServeHTTP` from request arrival to
response write completion. Uses `httptest.NewRecorder` (the stdlib test
`ResponseRecorder`, not httptape's `Recorder`) to capture the response.

```go
// BenchmarkServerServeHTTP_ExactMatch measures response latency with exact match.
// Target: < 1ms per operation.
func BenchmarkServerServeHTTP_ExactMatch(b *testing.B)
```

Implementation notes:
- Pre-populate `MemoryStore` with a known tape matching the benchmark request.
- Use `DefaultMatcher()` (method + path).
- Create a fresh `*http.Request` via `httptest.NewRequest` each iteration.
- Use `httptest.NewRecorder()` each iteration to capture the response.
- Vary the number of candidate tapes to show scaling behavior.

Sub-benchmarks:
- `BenchmarkServerServeHTTP_ExactMatch/1tape` -- single tape in store
- `BenchmarkServerServeHTTP_ExactMatch/10tapes` -- 10 tapes, one matching
- `BenchmarkServerServeHTTP_ExactMatch/100tapes` -- 100 tapes, one matching
- `BenchmarkServerServeHTTP_ExactMatch/1000tapes` -- 1000 tapes, one matching

##### Benchmark 3: Sanitizer throughput (`sanitizer_test.go`)

Measures the throughput of the sanitization pipeline on JSON bodies of varying
sizes. This isolates the JSON unmarshal/redact/marshal cycle.

```go
// BenchmarkSanitizer_RedactBodyPaths measures RedactBodyPaths throughput.
func BenchmarkSanitizer_RedactBodyPaths(b *testing.B)

// BenchmarkSanitizer_FakeFields measures FakeFields throughput (includes HMAC).
func BenchmarkSanitizer_FakeFields(b *testing.B)

// BenchmarkSanitizer_FullPipeline measures a realistic pipeline with
// RedactHeaders + RedactBodyPaths + FakeFields combined.
func BenchmarkSanitizer_FullPipeline(b *testing.B)
```

Implementation notes:
- Generate synthetic JSON bodies at construction time for each size tier:
  1KB, 10KB, 100KB. Use nested objects with arrays to exercise the path
  traversal (`$.users[*].email`, `$.users[*].id`).
- Build a `Tape` with the synthetic body for both request and response.
- Call `pipeline.Sanitize(tape)` in the hot loop.
- `b.SetBytes(int64(len(body)))` to report throughput in bytes/sec.
- `b.ReportAllocs()` for allocation tracking.

Sub-benchmarks by body size:
- `BenchmarkSanitizer_RedactBodyPaths/1KB`
- `BenchmarkSanitizer_RedactBodyPaths/10KB`
- `BenchmarkSanitizer_RedactBodyPaths/100KB`
- (Same pattern for FakeFields and FullPipeline.)

Body generation helper:
```go
// generateJSONBody creates a JSON object with `n` user entries, each containing
// email, id, name, and nested tokens array. Returns the JSON bytes and the
// approximate size label.
func generateJSONBody(n int) []byte
```

The helper produces structure like:
```json
{
  "users": [
    {"email": "user0@test.com", "id": 1000, "name": "User 0", "tokens": [{"value": "tok0"}]},
    ...
  ]
}
```

Scale `n` to hit target sizes: ~5 users for 1KB, ~60 users for 10KB, ~600 users
for 100KB.

##### Benchmark 4: FileStore write throughput (`store_file_test.go`)

Measures `FileStore.Save` throughput including JSON marshaling and atomic
file write (temp + rename).

```go
// BenchmarkFileStore_Save measures sequential write throughput.
func BenchmarkFileStore_Save(b *testing.B)

// BenchmarkFileStore_Save_Concurrent measures concurrent write throughput.
func BenchmarkFileStore_Save_Concurrent(b *testing.B)
```

Implementation notes:
- Use `b.TempDir()` for the fixture directory (auto-cleaned).
- Create a new `FileStore` with `WithDirectory(b.TempDir())`.
- Build a template tape once; vary the ID per iteration (using `fmt.Sprintf`
  or `strconv.Itoa`) to avoid overwriting the same file.
- For the concurrent benchmark, use `b.RunParallel` with `testing.PB`.
- `b.ReportAllocs()` for allocation tracking.

Sub-benchmarks:
- `BenchmarkFileStore_Save/SmallTape` -- ~200-byte JSON body
- `BenchmarkFileStore_Save/MediumTape` -- ~10KB JSON body
- `BenchmarkFileStore_Save_Concurrent/SmallTape`
- `BenchmarkFileStore_Save_Concurrent/MediumTape`

##### Benchmark 5: Matcher performance (`matcher_test.go`)

Measures `CompositeMatcher.Match` with varying numbers of candidate tapes.
This is the hot path in `Server.ServeHTTP` -- the matcher must scan all
candidates to find the best match.

```go
// BenchmarkCompositeMatcher_Match measures matching with DefaultMatcher criteria.
func BenchmarkCompositeMatcher_Match(b *testing.B)
```

Implementation notes:
- Pre-generate candidate slices of varying sizes (10, 100, 1000, 5000).
- Place the matching tape at a random position (not always first or last)
  to avoid best-case/worst-case bias. Use a fixed seed for reproducibility.
- Use `DefaultMatcher()` (MatchMethod + MatchPath) as the baseline.
- Add sub-benchmarks with `MatchQueryParams()` and `MatchBodyHash()` to
  show the cost of additional criteria.

Sub-benchmarks:
- `BenchmarkCompositeMatcher_Match/Default/10candidates`
- `BenchmarkCompositeMatcher_Match/Default/100candidates`
- `BenchmarkCompositeMatcher_Match/Default/1000candidates`
- `BenchmarkCompositeMatcher_Match/Default/5000candidates`
- `BenchmarkCompositeMatcher_Match/WithQueryAndBody/100candidates`
- `BenchmarkCompositeMatcher_Match/WithQueryAndBody/1000candidates`

##### Helper types for benchmarks

```go
// noopTransport is an http.RoundTripper that returns a fixed response without
// making any network call. Used to isolate recorder overhead in benchmarks.
type noopTransport struct {
    response *http.Response
}

func (t *noopTransport) RoundTrip(*http.Request) (*http.Response, error) {
    // Return a fresh body reader each call to avoid consumed-body issues.
    body := []byte(`{"ok":true}`)
    return &http.Response{
        StatusCode: t.response.StatusCode,
        Header:     t.response.Header.Clone(),
        Body:       io.NopCloser(bytes.NewReader(body)),
    }, nil
}
```

This type is defined in `recorder_test.go` (unexported, test-only).

##### CI integration

Benchmarks run in CI via `go test -bench=. -benchmem -count=1 ./...`. Results
are informational only -- benchmark failures do not block merges. A future
enhancement could use `benchstat` for regression detection, but that is out of
scope for this issue.

##### No new production code

Per the issue scope, this ADR adds only test files (`*_test.go`). No production
code changes are needed. All benchmark helpers (noopTransport, generateJSONBody)
are unexported and test-only.

#### Consequences

- **Five benchmark files** gain new `Benchmark*` functions. No new files are
  created -- all benchmarks go into existing `*_test.go` files.
- **Reproducible baselines**: `b.ReportAllocs()` and `b.SetBytes()` provide
  stable metrics across runs. Results can be compared using `benchstat`.
- **CI visibility**: Benchmark output appears in CI logs. Developers can spot
  regressions by comparing against previous runs.
- **No performance optimization**: This ADR is measurement-only. If benchmarks
  reveal performance issues, separate issues should be filed for optimization.
- **Memory allocation tracking**: `b.ReportAllocs()` on all benchmarks provides
  data for future allocation optimization work (explicitly out of scope per
  issue #42, but the data will be collected).
- **MemoryStore used for isolation**: Recorder and Server benchmarks use
  `MemoryStore` to avoid filesystem overhead skewing results. FileStore has its
  own dedicated benchmarks that intentionally include filesystem overhead.

---

### ADR-18: Declarative sanitization config (JSON)

**Date**: 2026-03-30
**Issue**: #76
**Status**: Accepted

#### Context

httptape's sanitization pipeline (`Pipeline`, `RedactHeaders`, `RedactBodyPaths`,
`FakeFields`) is currently configurable only via Go code. The distribution milestone
(CLI, Docker, testcontainers) requires a way to express sanitization rules without
writing Go code. A declarative JSON configuration format bridges this gap.

This is the foundational enabler for the distribution milestone:
- Issue #44 (CLI) depends on this to accept a config file.
- Issue #77 (Docker image) depends on CLI.
- Issue #78 (Testcontainers module) depends on Docker.

Key constraints:
- stdlib only: `encoding/json` -- no external JSON schema or config libraries.
- Must map 1:1 to the existing Go API (`RedactHeaders`, `RedactBodyPaths`, `FakeFields`).
- Config types must be exported so users can build configs programmatically.
- Validation must produce clear, actionable error messages.
- The config format must be extensible for future sanitization actions.

#### Decision

##### Config format: rules array

The config uses a `rules` array where each rule has an `action` discriminator
field and action-specific parameters. This is more extensible than a flat
structure: new actions can be added without changing the top-level schema.

```json
{
  "version": "1",
  "rules": [
    {"action": "redact_headers", "headers": ["Authorization", "Cookie"]},
    {"action": "redact_headers"},
    {"action": "redact_body", "paths": ["$.card.number", "$.ssn"]},
    {"action": "fake", "seed": "project-seed", "paths": ["$.email", "$.user_id"]}
  ]
}
```

Format rules:
- `version` is required and must be `"1"`. This allows future schema evolution.
- `rules` is required and must be a non-empty array.
- Each rule must have an `action` field with one of the known values.
- Unknown fields on the top-level config or within rules cause validation errors
  (using `json.Decoder` with `DisallowUnknownFields`).

##### Action definitions

| Action | Required fields | Optional fields | Maps to |
|---|---|---|---|
| `redact_headers` | (none) | `headers` (string array) | `RedactHeaders(headers...)` |
| `redact_body` | `paths` (non-empty string array) | (none) | `RedactBodyPaths(paths...)` |
| `fake` | `seed` (non-empty string), `paths` (non-empty string array) | (none) | `FakeFields(seed, paths...)` |

When `redact_headers` has no `headers` field (or an empty array), it maps to
`RedactHeaders()` which uses `DefaultSensitiveHeaders()`. This matches the Go
API's zero-argument behavior.

For `redact_body`, `paths` is required and must be non-empty -- unlike
`redact_headers` which has sensible defaults, there are no default body paths.

For `fake`, both `seed` and `paths` are required. The seed must be non-empty
(an empty seed produces degenerate HMAC output).

##### Path validation

All paths in `redact_body` and `fake` rules are validated at config load time
using the existing `parsePath` function. Invalid paths cause a validation error
with the specific path and reason. This is stricter than the Go API (which
silently ignores invalid paths) because config files are typically written once
and should fail loudly on typos.

##### Exported Go types (`config.go`)

```go
// Config represents a declarative sanitization configuration.
// It can be loaded from JSON or constructed programmatically.
type Config struct {
    Version string `json:"version"`
    Rules   []Rule `json:"rules"`
}

// Rule represents a single sanitization rule within a Config.
// The Action field determines which other fields are relevant.
type Rule struct {
    Action  string   `json:"action"`
    Headers []string `json:"headers,omitempty"`
    Paths   []string `json:"paths,omitempty"`
    Seed    string   `json:"seed,omitempty"`
}
```

The types are exported so users can build configs programmatically and serialize
them for use with the CLI or Docker.

##### Loading functions (`config.go`)

```go
// LoadConfig reads a JSON sanitization config from r, validates it, and
// returns a Config. It returns an error if the JSON is malformed, contains
// unknown fields, or fails validation.
func LoadConfig(r io.Reader) (*Config, error)

// LoadConfigFile is a convenience wrapper that opens the file at path and
// calls LoadConfig.
func LoadConfigFile(path string) (*Config, error)
```

`LoadConfig` performs two-phase processing:
1. **Parse**: decode JSON with `DisallowUnknownFields`.
2. **Validate**: check version, action names, required fields, path syntax.

##### Pipeline construction (`config.go`)

```go
// BuildPipeline converts the Config into a Pipeline by mapping each Rule
// to the corresponding SanitizeFunc.
func (c *Config) BuildPipeline() *Pipeline
```

This method assumes the config has been validated (via `LoadConfig`). It maps
each rule to a `SanitizeFunc`:
- `redact_headers` -> `RedactHeaders(rule.Headers...)`
- `redact_body` -> `RedactBodyPaths(rule.Paths...)`
- `fake` -> `FakeFields(rule.Seed, rule.Paths...)`

Rules are applied in order, matching the Pipeline's sequential semantics.

##### Validation errors

Validation produces a single error that aggregates all issues found:

```go
// ValidateConfig checks c for structural and semantic errors. It returns
// nil if the config is valid, or an error describing all issues found.
func (c *Config) Validate() error
```

Error messages are human-readable and include the rule index:

```
config validation: rule[0]: unknown action "redact"
config validation: rule[2]: "redact_body" requires non-empty "paths"
config validation: rule[3]: "fake" path "$.": invalid path syntax
```

##### JSON Schema (`config.schema.json`)

A JSON Schema file is provided for IDE validation and documentation. It uses
JSON Schema draft 2020-12 and lives at the repository root. The schema is not
used by the Go code at runtime -- it is a documentation artifact for editors
and CI linting.

##### File organization

All config code goes in `config.go` with tests in `config_test.go`. This
follows the project's single-package layout. The JSON Schema goes in
`config.schema.json` at the repository root.

The package structure entry in CLAUDE.md should be updated to include:
```
config.go            # Declarative JSON config for sanitization pipeline
config_test.go
config.schema.json   # JSON Schema for config file validation (IDE/CI use)
```

#### Consequences

- **CLI enablement**: The CLI (#44) can accept `--config path/to/config.json`
  and call `LoadConfigFile` + `BuildPipeline` without importing any sanitizer
  functions directly.
- **Docker enablement**: The Docker image (#77) can mount a config file and
  pass it to the CLI.
- **Programmatic use**: Go users can build `Config` structs in code, serialize
  them to JSON, and share them across projects.
- **Strict validation**: Config files fail loudly on errors, unlike the Go API
  which silently ignores invalid paths. This is intentional -- config files are
  written by humans and should give immediate feedback.
- **Version field**: The `"version": "1"` field allows future schema evolution
  without breaking existing configs.
- **Extensibility**: New sanitization actions can be added by extending the
  action discriminator. Existing configs remain valid.
- **No runtime schema validation**: The JSON Schema file is for editor tooling
  only. Runtime validation is done in Go code, keeping the stdlib-only constraint.

### ADR-19: Standalone CLI

**Date**: 2026-03-30
**Issue**: #44
**Status**: Accepted
**Depends on**: ADR-18 (Declarative sanitization config)

#### Context

httptape is currently usable only as an embedded Go library. The distribution
milestone requires a standalone CLI binary so that non-Go users (and CI
pipelines, Docker images, testcontainers) can record, replay, and manage
HTTP fixtures without writing Go code.

The CLI must be a thin wrapper over the existing library API -- no business
logic in `cmd/`. It must use stdlib only (`flag` package) to honour the
project's zero-dependency constraint. The core library package must remain
unchanged; all new code lives in `cmd/httptape/`.

#### Decision

##### Command structure

The binary exposes four subcommands via positional argument dispatching
(no third-party CLI frameworks):

```
httptape <command> [flags]

Commands:
  serve    Replay mode — serve recorded fixtures as a mock HTTP server
  record   Proxy mode — forward requests to upstream, record + sanitize responses
  export   Export fixtures to a tar.gz bundle
  import   Import fixtures from a tar.gz bundle
```

Running `httptape` with no arguments or with `-h`/`--help` prints the usage
summary and exits with code 0.

##### `httptape serve`

Starts an HTTP server that replays recorded fixtures.

| Flag | Type | Required | Default | Description |
|---|---|---|---|---|
| `--fixtures` | string | yes | — | Path to fixture directory |
| `--config` | string | no | — | Path to sanitization config JSON (accepted for API uniformity; not used by serve) |
| `--port` | int | no | 8081 | Listen port |
| `--fallback-status` | int | no | 404 | HTTP status when no tape matches |

Implementation:
1. Create `FileStore` with `WithDirectory(fixtures)`.
2. Create `Server` with `WithFallbackStatus(fallbackStatus)`.
3. Bind `http.Server{Addr: ":port", Handler: server}`.
4. Start listening; log the address to stderr.
5. Wait for SIGINT/SIGTERM, then call `http.Server.Shutdown` with a 5-second
   timeout for graceful drain.

The `--config` flag is accepted but ignored in serve mode. This keeps the
flag set uniform across serve/record, which simplifies Docker entrypoints
and documentation. A future enhancement could use the config to validate
that fixtures match expected sanitization rules.

##### `httptape record`

Starts a reverse proxy that forwards requests to an upstream server, records
interactions through `httptape.Recorder`, and applies sanitization.

| Flag | Type | Required | Default | Description |
|---|---|---|---|---|
| `--upstream` | string | yes | — | Upstream URL (scheme + host, e.g. `https://api.example.com`) |
| `--fixtures` | string | yes | — | Path to fixture directory (recordings saved here) |
| `--config` | string | no | — | Path to sanitization config JSON |
| `--port` | int | no | 8081 | Listen port |

Implementation:
1. Parse and validate `--upstream` as a URL (`url.Parse`; must have scheme and host).
2. Create `FileStore` with `WithDirectory(fixtures)`.
3. If `--config` is provided, call `httptape.LoadConfigFile` then `BuildPipeline`.
4. Create `Recorder` with `WithSanitizer(pipeline)` and `WithAsync(true)`.
5. Create `net/http/httputil.ReverseProxy` with:
   - `Rewrite` function that sets the target scheme, host, and path from the
     upstream URL.
   - `Transport` set to the `Recorder` (which implements `http.RoundTripper`).
6. Bind `http.Server{Addr: ":port", Handler: proxy}`.
7. Start listening; log the upstream and local address to stderr.
8. On SIGINT/SIGTERM: call `http.Server.Shutdown` (5s timeout), then
   `recorder.Close()` to flush pending async recordings.

The reverse proxy approach means the CLI acts as a transparent forward proxy:
clients point at `localhost:8081` instead of the real upstream. The Recorder
wraps the transport, so every round-trip is captured and sanitized before
hitting the FileStore.

##### `httptape export`

Exports fixtures from a directory to a tar.gz bundle.

| Flag | Type | Required | Default | Description |
|---|---|---|---|---|
| `--fixtures` | string | yes | — | Path to fixture directory |
| `--output` | string | no | — (stdout) | Output file path; omit for stdout |
| `--routes` | string | no | — | Comma-separated route filter |
| `--methods` | string | no | — | Comma-separated HTTP method filter |
| `--since` | string | no | — | RFC 3339 timestamp filter |

Implementation:
1. Create `FileStore` with `WithDirectory(fixtures)`.
2. Build `[]ExportOption` from flags (`WithRoutes`, `WithMethods`, `WithSince`).
3. Call `ExportBundle(ctx, store, opts...)`.
4. Copy the returned `io.Reader` to the output (file or stdout).
5. Exit 0 on success.

When `--output` is omitted, the tar.gz stream is written to stdout, enabling
piping (`httptape export --fixtures ./dir | ssh remote tar xzf -`).

##### `httptape import`

Imports fixtures from a tar.gz bundle into a directory.

| Flag | Type | Required | Default | Description |
|---|---|---|---|---|
| `--fixtures` | string | yes | — | Path to fixture directory |
| `--input` | string | no | — (stdin) | Input file path; omit for stdin |

Implementation:
1. Create `FileStore` with `WithDirectory(fixtures)`.
2. Open the input (file or stdin).
3. Call `ImportBundle(ctx, store, reader)`.
4. Exit 0 on success.

When `--input` is omitted, the bundle is read from stdin, enabling piping.

##### File layout

```
cmd/
  httptape/
    main.go     # Entry point, subcommand dispatch, signal handling
```

All CLI code lives in a single `main.go` file. The file contains:
- `main()`: subcommand dispatch, top-level error handling, exit codes.
- `runServe(args []string) error`: parse flags, wire up serve mode.
- `runRecord(args []string) error`: parse flags, wire up record mode.
- `runExport(args []string) error`: parse flags, wire up export mode.
- `runImport(args []string) error`: parse flags, wire up import mode.
- `awaitShutdown() <-chan os.Signal`: helper for SIGINT/SIGTERM.

A single file is appropriate because the CLI is a thin wrapper with no
business logic. If it grows beyond ~500 lines, it can be split into
per-command files within the same `main` package.

##### CLI parsing: stdlib `flag` package

Each subcommand creates its own `flag.FlagSet` with `flag.ContinueOnError`
(so we can return errors instead of calling `os.Exit` from within flag
parsing). Subcommand dispatch is a `switch` on `os.Args[1]`.

No third-party CLI frameworks (cobra, urfave/cli, kong). The `flag` package
is sufficient for four subcommands with flat flag sets.

##### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Usage error (bad flags, missing required flags, unknown subcommand) |
| 2 | Runtime error (store failure, upstream unreachable, config invalid) |

`main()` calls `os.Exit` exactly once, at the end, based on the error
returned by the subcommand runner. This ensures deferred functions run.

##### Graceful shutdown

The serve and record commands install a signal handler for SIGINT and
SIGTERM using `signal.NotifyContext`. On signal:

1. `http.Server.Shutdown(ctx)` is called with a 5-second deadline. This
   stops accepting new connections and waits for in-flight requests.
2. For record mode, `recorder.Close()` is called after server shutdown to
   flush any buffered tapes.
3. If the shutdown deadline expires, `http.Server.Close()` is called for
   immediate termination.

##### Logging

All diagnostic output goes to stderr via `log.New(os.Stderr, "httptape: ", 0)`.
No structured logging library — plain text with a prefix. This keeps the
binary small and avoids dependencies. Logged events:

- Server start: address and mode (serve/record).
- Record mode: upstream URL.
- Shutdown initiated.
- Shutdown complete (with flush count for record mode).
- Errors: config load failures, upstream validation, store errors.

Request-level logging is intentionally omitted in v1 to avoid noise. A
future `--verbose` flag could enable per-request logging.

##### Relationship to core library

The CLI imports `httptape` as a regular Go module dependency. It calls only
exported API:
- `NewFileStore`, `WithDirectory`
- `NewServer`, `WithFallbackStatus`
- `NewRecorder`, `WithSanitizer`, `WithAsync`, `WithOnError`
- `LoadConfigFile`, `(*Config).BuildPipeline`
- `ExportBundle`, `ImportBundle`, `WithRoutes`, `WithMethods`, `WithSince`

No changes to the core library are required. The `cmd/httptape/` package
has zero coupling to unexported internals.

##### Build and installation

```bash
go build -o httptape ./cmd/httptape
go install github.com/VibeWarden/httptape/cmd/httptape@latest
```

The `go.mod` module path (`github.com/VibeWarden/httptape`) already supports
this layout. No module path changes are needed.

#### Consequences

- **Distribution unblocked**: The CLI binary enables Docker images (#77) and
  testcontainers (#78) without requiring users to write Go code.
- **Zero library changes**: The core package is untouched. The CLI is a pure
  consumer of the public API, validating that the API is sufficient for
  real-world use.
- **Stdlib only**: No new dependencies. The `flag`, `net/http`,
  `net/http/httputil`, `os/signal`, and `log` packages provide everything
  needed.
- **Simple mental model**: Four subcommands mapping 1:1 to the library's
  four capabilities (replay, record, export, import).
- **Pipe-friendly**: Export writes to stdout by default; import reads from
  stdin by default. This enables Unix-style composition.
- **Graceful shutdown**: In-flight requests complete and async recordings
  flush before exit, preventing data loss.
- **Future extensibility**: Adding flags (e.g., `--verbose`, `--tls-cert`)
  is straightforward with `flag.FlagSet`. Adding subcommands is a new case
  in the dispatch switch.

### ADR-20: Docker Image

**Date**: 2026-03-30
**Issue**: #77
**Status**: Accepted
**Depends on**: ADR-19 (Standalone CLI)

#### Context

The CLI binary (ADR-19) gives us a standalone executable, but running httptape
in CI pipelines, docker-compose stacks, and Kubernetes side-cars still requires
a Go toolchain to build from source. A pre-built Docker image removes that
requirement entirely: pull, mount fixtures, run.

Design constraints:

1. **Minimal image size** (~10-15 MB) — no shell, no OS, fast pulls in CI.
2. **Scratch base** — the binary is statically linked (CGO_ENABLED=0), so we
   need nothing beyond CA certificates for HTTPS upstreams.
3. **CLI is the interface** — the ENTRYPOINT is the httptape binary; Docker
   users pass subcommands and flags exactly as they would on the host.
4. **No new library code** — the Dockerfile and CI workflow are pure
   infrastructure; the core library and the CLI source remain unchanged.

#### Decision

##### Multi-stage Dockerfile

```
# Builder: golang:1.26-alpine
#   - CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w"
#   - Produces a static ~10 MB binary
#
# Final: scratch
#   - COPY ca-certificates.crt (for HTTPS record mode)
#   - COPY httptape binary to /usr/local/bin/httptape
#   - VOLUME ["/fixtures", "/config"]
#   - EXPOSE 8081
#   - ENTRYPOINT ["httptape"]
```

Key choices:

| Choice | Rationale |
|---|---|
| `scratch` over `distroless` | Smaller image, no unnecessary files. The binary is fully static and self-contained. |
| Alpine builder | Smaller download than `golang:1.26`, and `apk` provides `ca-certificates`. |
| `-trimpath -ldflags="-s -w"` | Strip debug info and paths to minimise binary size. |
| `/fixtures` and `/config` volumes | Convention matching the CLI flags `--fixtures` and `--config`. Users mount host dirs here. |
| Port 8081 | Matches the CLI's `--port` default (ADR-19). The issue suggested 8080, but we align with the existing CLI default to avoid confusion. |
| ENTRYPOINT, no CMD | Users supply the subcommand (`serve`, `record`, `export`, `import`) as `docker run` arguments. |

##### Docker Compose example

Two services, `serve` and `record`, demonstrate the sidecar pattern:

- `serve`: mounts `./fixtures` read-only, exposes 8081.
- `record`: mounts `./fixtures` read-write (recordings persist), mounts
  config read-only, requires `--upstream`.

File: `docker-compose.yml` at the repo root (example, not production).

##### GitHub Actions workflow

File: `.github/workflows/docker.yml`

- **Triggers**: push to `main`, tags matching `v*`.
- **Registry**: `ghcr.io/vibewarden/httptape`.
- **Tagging strategy**:
  - Push to `main` -> `latest`
  - Tag `v1.2.3` -> `1.2.3`, `1.2`
- **Caching**: GitHub Actions cache (`type=gha`) for layer reuse.
- Uses `docker/metadata-action` for tag generation, `docker/build-push-action`
  for building and pushing.

##### Deferred items (not in this ADR)

- **Environment variable overrides** (`HTTPTAPE_PORT`, `HTTPTAPE_FIXTURES`,
  `HTTPTAPE_CONFIG`): requires CLI code changes. Will be addressed in a
  follow-up issue if demand materialises. Docker users can pass flags directly.
- **Health check endpoint** (`/healthz`): requires a library-level change to
  the server. Deferred to a separate issue. Docker HEALTHCHECK can use
  `wget`/`curl` once a non-scratch base is considered, or a future `--healthz`
  flag can be added to the CLI.
- **Multi-arch builds** (ARM64): out of scope per the issue; can be added
  later with `docker/setup-qemu-action`.
- **Major version tags** (`v0`, `v1`): semver `major.minor` tags are
  sufficient; single-digit major tags can be added if users request them.

#### Consequences

- **Zero Go toolchain required**: CI and docker-compose users pull a ~12 MB
  image and run immediately.
- **No library changes**: The Docker image is pure packaging. ADR-19's CLI
  binary is the only artifact being containerised.
- **Automated publishing**: Every push to `main` and every semver tag
  produces a fresh image on GHCR with no manual steps.
- **Scratch trade-off**: No shell inside the container means `docker exec sh`
  is impossible. This is acceptable for a single-purpose sidecar; debugging
  can use ephemeral debug containers or a future `distroless/debug` variant.
- **Unblocks #78**: The testcontainers module can reference
  `ghcr.io/vibewarden/httptape` as its container image.

### ADR-21: Testcontainers Module

**Date**: 2026-03-30
**Issue**: #78
**Status**: Accepted
**Depends on**: ADR-20 (Docker Image)

#### Context

The Docker image (ADR-20) enables running httptape as a sidecar, but Go
developers still need manual `docker run` orchestration in integration tests.
The testcontainers-go ecosystem provides a standard pattern: call a
`RunContainer` function, get back a handle with connection details, tear it
down in `defer`. A first-party testcontainers module eliminates boilerplate
and gives users process isolation without leaving the `go test` workflow.

Design constraints:

1. **Separate Go module** — testcontainers-go is an external dependency. The
   main httptape module has a zero-dependency guarantee (CLAUDE.md). A
   separate `go.mod` keeps the dependency graph isolated.
2. **Thin wrapper** — the module configures and starts the container. It does
   not duplicate httptape library logic; the container runs the CLI binary
   from the Docker image.
3. **Functional options** — consistent with the main library's API style
   (functional options for all public constructors).
4. **Docker-only prerequisite** — tests importing this module require a
   running Docker daemon. A build tag allows skipping when Docker is
   unavailable.

#### Decision

##### Module layout

```
testcontainers/
  go.mod              # module github.com/VibeWarden/httptape/testcontainers
  go.sum
  httptape.go         # RunContainer, Container, Option types
  httptape_test.go    # Integration tests (build tag: dockertest)
  options.go          # Functional option implementations
  doc.go              # Package-level documentation
  README.md           # Usage examples and API overview
  testdata/           # Fixtures and config for integration tests
    fixtures/
    config.json
```

The module has its own `go.mod` with `go 1.26` and a dependency on
`github.com/testcontainers/testcontainers-go` (latest stable). It does NOT
depend on `github.com/VibeWarden/httptape` — the module only talks to the
container over HTTP.

##### Public API

```go
package httptape

// RunContainer starts an httptape Docker container and returns a handle.
// The caller must call Container.Terminate to clean up resources.
func RunContainer(ctx context.Context, opts ...Option) (*Container, error)

// Container wraps a running httptape Docker container.
type Container struct {
    testcontainers.Container  // embed for advanced use
}

// BaseURL returns the mapped HTTP base URL (e.g. "http://localhost:32789").
func (c *Container) BaseURL() string

// Endpoint returns host:port for the mapped container port.
func (c *Container) Endpoint(ctx context.Context) (string, error)

// Terminate stops and removes the container.
func (c *Container) Terminate(ctx context.Context) error
```

##### Functional options

| Option | Purpose | Default |
|---|---|---|
| `WithFixturesDir(path)` | Bind-mount a host directory to `/fixtures` in the container. | none (required for serve mode) |
| `WithConfig(cfg httptape.Config)` | Serialise a `Config` struct to JSON and mount it at `/config/config.json`. Requires importing the main module. | none |
| `WithConfigFile(path)` | Bind-mount a host JSON config file to `/config/config.json`. | none |
| `WithPort(port string)` | Set the container's exposed port (e.g. `"9090/tcp"`). | `"8081/tcp"` |
| `WithImage(image string)` | Override the Docker image reference. | `"ghcr.io/vibewarden/httptape:latest"` |
| `WithMode(mode string)` | CLI subcommand: `"serve"` or `"record"`. | `"serve"` |
| `WithTarget(url string)` | Upstream URL for record mode (maps to `--upstream`). | none |

`WithConfig` and `WithConfigFile` are mutually exclusive; supplying both
returns an error from `RunContainer`.

##### Container startup logic

`RunContainer` builds a `testcontainers.ContainerRequest`:

1. **Image**: `ghcr.io/vibewarden/httptape:latest` (overridable via
   `WithImage`).
2. **Cmd**: the CLI subcommand and flags, e.g.
   `["serve", "--fixtures", "/fixtures", "--port", "8081"]`.
   In record mode: `["record", "--fixtures", "/fixtures", "--upstream",
   "<target>", "--config", "/config/config.json", "--port", "8081"]`.
3. **ExposedPorts**: `["8081/tcp"]` (overridable via `WithPort`).
4. **Mounts**: bind mounts for fixtures directory and config file.
5. **WaitingFor**: `wait.ForHTTP("/").WithPort("8081/tcp")` — the serve
   command returns responses as soon as the listener is ready. For a more
   robust check, use `wait.ForListeningPort("8081/tcp")` since the scratch
   image has no health endpoint yet.

The function validates option consistency (e.g., record mode requires
`WithTarget`), starts the container, waits for readiness, and returns the
`Container` handle.

##### Build tag for tests

Integration tests in `httptape_test.go` use:

```go
//go:build dockertest
```

CI runs them with `go test -tags dockertest ./testcontainers/...`. Local
developers without Docker simply omit the tag. This avoids surprising
failures in `go test ./...` from the repo root.

##### Why not a subdirectory inside the main module?

Putting testcontainers code in `testcontainers/` as a sub-package of the
main module would force `testcontainers-go` into the main `go.mod`,
violating the zero-dependency constraint. A separate Go module (with its
own `go.mod`) is the standard Go solution: the main module's dependency
tree is unaffected, and users `go get` only what they need.

#### Consequences

- **No impact on main module**: the testcontainers module has its own
  dependency tree. `go get github.com/VibeWarden/httptape` still pulls
  zero transitive dependencies.
- **Process isolation**: bugs in test fixtures or config cannot crash the
  test process. The container is a separate OS process with its own memory
  space.
- **Language-agnostic potential**: although this module is Go-specific,
  the Docker image it wraps can be used from any language's testcontainers
  library (Java, Python, etc.) without changes.
- **Docker requirement**: tests using this module need a Docker daemon.
  The build tag mitigates CI environments without Docker.
- **Image availability**: the module assumes the Docker image from #77 is
  published to `ghcr.io/vibewarden/httptape`. If the image is not yet
  available, integration tests will fail until #77 is merged and the CI
  workflow publishes the first image.
- **Maintenance surface**: a new `go.mod` means a separate dependency
  update cadence. Dependabot or Renovate should be configured to cover
  `testcontainers/go.mod`.

---

### ADR-22: LoadFixtures convenience functions

**Date**: 2026-03-30
**Issue**: #91
**Status**: Accepted

#### Context

Loading fixture files for test mocking currently requires multiple steps:
creating a `FileStore` or `MemoryStore`, configuring it, and then the store
is ready. For the most common use case — loading pre-existing fixture JSON
files from a directory for replay — this boilerplate is repeated in every
test file. The issue requests a one-liner convenience API.

Two variants are needed:

1. **Filesystem directory**: for tests that read fixtures from `testdata/`
   at runtime. Returns a `*FileStore` pointing at the given directory.
2. **`fs.FS` (embed.FS)**: for self-contained test binaries that embed
   fixtures at compile time. Reads JSON files into a `*MemoryStore` since
   `embed.FS` is read-only and `FileStore` requires a writable directory.

Both functions must recursively walk the directory, decode every `.json`
file as a `Tape`, and return a populated store ready for use with
`NewServer`.

#### Decision

Add two exported convenience functions in a new file `fixtures.go`:

```go
// LoadFixtures creates a FileStore rooted at the given filesystem directory.
// It validates that the directory exists and contains at least parseable JSON
// tape files by performing a read-only scan. The returned FileStore points at
// dir and is immediately usable with NewServer.
//
// All .json files in dir (non-recursive, matching FileStore's flat-directory
// model) are validated as Tape JSON. Files that fail to parse cause an error
// that includes the filename.
func LoadFixtures(dir string) (*FileStore, error)

// LoadFixturesFS reads all .json Tape files from the given fs.FS directory
// (recursive walk) and returns a MemoryStore populated with the decoded tapes.
// This is designed for use with embed.FS for self-contained test binaries.
//
// Files that fail to decode as a Tape cause an error that includes the
// filename. The returned MemoryStore is immediately usable with NewServer.
func LoadFixturesFS(fsys fs.FS, dir string) (*MemoryStore, error)
```

##### Design choices

1. **Return concrete types, not `Store` interface**: `LoadFixtures` returns
   `*FileStore` and `LoadFixturesFS` returns `*MemoryStore`. This follows
   Go best practice of returning concrete types and accepting interfaces.
   Callers who need `Store` can assign to a `Store` variable since both
   types implement the interface.

2. **`LoadFixtures` delegates to `NewFileStore`**: rather than duplicating
   directory creation logic, `LoadFixtures` calls `NewFileStore` with
   `WithDirectory(dir)` and then performs a validation scan. This keeps
   `FileStore` as the single source of truth for filesystem operations.

3. **`LoadFixturesFS` walks recursively**: since `fs.FS` is read-only and
   there is no concern about accidentally writing to nested directories,
   recursive walking is safe and useful for organizing fixtures in
   subdirectories (e.g., by route or service).

4. **`LoadFixtures` scans flat (non-recursive)**: this matches the existing
   `FileStore.List` behavior which reads only the base directory. Introducing
   recursive filesystem walking would be inconsistent with how `FileStore`
   operates.

5. **Validation on load**: both functions validate that every `.json` file
   in scope is a valid `Tape`. This fails fast with a clear error including
   the filename, rather than silently loading invalid data that would cause
   confusing failures later during replay.

6. **New file `fixtures.go`**: these are convenience wrappers that
   orchestrate existing types (`FileStore`, `MemoryStore`). They don't
   belong in `store_file.go` or `store_memory.go` because they span both.
   A dedicated file keeps responsibilities clean.

7. **No functional options**: these are convenience functions — the whole
   point is minimal ceremony. Advanced users who need options can use
   `NewFileStore` / `NewMemoryStore` directly.

8. **Empty directory is not an error**: an empty directory produces an
   empty store. This is a valid use case (e.g., a fresh test setup).

##### File placement

- `fixtures.go` — implementation
- `fixtures_test.go` — table-driven tests covering: happy path, empty
  directory, invalid JSON, nested directories (for `LoadFixturesFS`),
  non-existent directory, `embed.FS`

#### Consequences

- **Reduced boilerplate**: the most common test setup is now a one-liner.
- **No breaking changes**: these are purely additive new exports.
- **stdlib only**: `fs.FS` and `io/fs` are stdlib. No new dependencies.
- **Discoverability**: users searching for "fixtures" or "load" in godoc
  will find these functions immediately.
- **FileStore flat-directory limitation**: `LoadFixtures` does not support
  nested directories, matching `FileStore`'s existing behavior. Users who
  need recursive loading from the real filesystem can use
  `LoadFixturesFS(os.DirFS(dir), ".")` as a workaround.

---

### ADR-23: Go DSL for mock definitions

**Date**: 2026-03-30
**Issue**: #92
**Status**: Accepted

#### Context

Creating a mock HTTP server for tests currently requires constructing Tapes
manually, populating a MemoryStore, and wiring up a Server with httptest.
This boilerplate is repetitive and obscures the intent of the test setup.
Issue #92 requests a fluent Go DSL that lets users declare request/response
stubs inline, similar to WireMock's stubFor() API but idiomatic Go.

The DSL should be a thin convenience layer over existing types (Tape,
MemoryStore, Server, httptest.Server). It must not introduce new matching
semantics or bypass the existing architecture.

#### Decision

Add a fluent mock DSL in a new file `mock.go` with tests in `mock_test.go`.

##### API surface

```go
// MockServer wraps an httptest.Server configured with stub-based tapes.
// It embeds *httptest.Server so callers get URL, Client, and Close for free.
type MockServer struct {
    *httptest.Server
}

// Mock creates an httptest.Server backed by a MemoryStore populated with
// the given stubs. Each Stub is converted to a Tape internally.
// The returned MockServer must be closed by the caller (defer srv.Close()).
func Mock(stubs ...Stub) *MockServer

// Stub represents a single request-response pair for the mock DSL.
type Stub struct {
    method      string
    path        string
    status      int
    body        []byte
    headers     http.Header
}

// Method is a request matcher that captures HTTP method and path.
type Method struct {
    method string
    path   string
}

// Method helpers — each returns a Method value:
func GET(path string) Method
func POST(path string) Method
func PUT(path string) Method
func DELETE(path string) Method
func PATCH(path string) Method
func HEAD(path string) Method

// When starts building a Stub from a Method.
func When(m Method) *StubBuilder

// StubBuilder provides a fluent API for constructing a Stub.
type StubBuilder struct {
    stub Stub
}

// Respond sets the response status code and optional body.
// If no body is provided, the response has an empty body.
func (b *StubBuilder) Respond(status int, body ...Body) *StubBuilder

// WithHeader adds a response header to the stub.
// Can be called multiple times for multiple headers.
func (b *StubBuilder) WithHeader(key, value string) *StubBuilder

// Build finalizes and returns the Stub. Called implicitly by Mock.
func (b *StubBuilder) Build() Stub

// Body represents a response body with optional content type.
type Body struct {
    data        []byte
    contentType string  // auto-set for JSON/Text helpers; empty for Binary
}

// Body helpers:
func JSON(s string) Body    // sets content type to application/json
func Text(s string) Body    // sets content type to text/plain; charset=utf-8
func Binary(b []byte) Body  // no auto content type
```

##### Internal conversion

`Mock` converts each `Stub` to a `Tape` by:
1. Generating a UUID via `newUUID()` for the tape ID.
2. Setting `RecordedAt` to `time.Now().UTC()`.
3. Setting `Request.Method` and `Request.URL` from the Method.
4. Setting `Response.StatusCode`, `Response.Body`, and `Response.Headers`.
5. If a Body helper provides a content type and no explicit Content-Type
   header was set via `WithHeader`, the content type is added automatically.
6. Saving each tape to a `NewMemoryStore()` via `Save`.
7. Creating `NewServer(store)` and wrapping it in `httptest.NewServer`.

##### Design choices

1. **Embed `*httptest.Server`**: callers get `URL`, `Client()`, and `Close()`
   without wrapper methods. This is standard Go practice for test helpers.

2. **`StubBuilder` returns `*StubBuilder`** (pointer receiver): enables
   method chaining. The `Build()` method returns a value `Stub`.

3. **`Mock` accepts `...Stub`**: variadic for clean multi-stub syntax.
   `StubBuilder.Build()` is called implicitly — `Mock` also accepts
   `*StubBuilder` would complicate the API. Instead, `StubBuilder`
   implements a `Build()` that `Mock` calls, but to keep the API minimal,
   `When().Respond().Build()` returns a `Stub` directly, and `Mock` accepts
   `Stub` values.

4. **Body helpers set content type**: `JSON()` auto-adds
   `Content-Type: application/json` if not explicitly overridden. This
   eliminates the most common `WithHeader` call.

5. **No query string or header matching in stubs**: the DSL is for
   defining responses, not for advanced request matching. Stubs use the
   DefaultMatcher (method + path). Users who need advanced matching should
   use the full Tape + CompositeMatcher API.

6. **Path stored as full URL**: internally the tape URL is stored as
   `http://mock<path>` to satisfy the URL parsing that MatchPath performs.
   The actual test server URL is irrelevant for matching since MatchPath
   compares only the path component.

7. **stdlib only**: uses `net/http/httptest` which is part of the Go
   standard library. No new dependencies.

8. **Panics on store errors**: `Mock` panics if `MemoryStore.Save` fails.
   This follows the constructor-panic convention (L-11 exception for
   programming errors) and matches `regexp.MustCompile` precedent.
   `MemoryStore.Save` only fails on context cancellation, which cannot
   happen with `context.Background()`.

##### File placement

- `mock.go` — all DSL types and functions
- `mock_test.go` — table-driven tests covering: single stub, multiple
  stubs, JSON body, text body, binary body, no body (204), custom headers,
  auto content-type from body helper, method helpers (GET/POST/PUT/DELETE/
  PATCH/HEAD)

#### Consequences

- **Reduced test boilerplate**: the most common mock setup is now 3-5 lines
  instead of 15-20.
- **No breaking changes**: purely additive new exports.
- **stdlib only**: no new dependencies.
- **Discoverable**: users searching for "mock" in godoc find `Mock` immediately.
- **Composable with existing API**: `MockServer` embeds `*httptest.Server`,
  so all existing httptest patterns work (custom transport, TLS, etc.).
- **Limited matching**: stubs only support method + path matching. This is
  intentional — the DSL optimizes for the 80% case. Advanced users compose
  Tape + CompositeMatcher directly.


### ADR-24: CORS support, latency simulation, and error simulation

**Date**: 2026-03-30
**Issues**: #94, #95, #96
**Status**: Accepted

#### Context

Milestone 8 (UI-First Dev) focuses on making httptape a first-class mock
backend for frontend development. Three related features are needed:

1. **CORS support (#94)**: Browsers block cross-origin requests from the
   frontend dev server (e.g., `localhost:3000`) to the httptape mock
   (e.g., `localhost:3001`). Without CORS headers, httptape is unusable
   for frontend development.

2. **Latency simulation (#95)**: Real APIs have latency, but mock servers
   respond instantly. Frontend developers cannot test loading states,
   spinners, or timeout handling without simulated delay.

3. **Error simulation (#96)**: Real APIs fail. Without error simulation,
   error handling code paths (retry logic, error toasts, fallback UI)
   go untested during development.

All three features modify Server behavior, are opt-in via ServerOption
functions, and need corresponding CLI flags. They are designed together
to ensure consistent architecture and avoid conflicting middleware
ordering.

#### Decision

Add three new ServerOption functions and corresponding CLI flags. All
implementation goes in `server.go` with tests in `server_test.go`. CLI
changes go in `cmd/httptape/main.go`.

##### Middleware execution order

When multiple features are enabled, they execute in this order within
`ServeHTTP`:

1. **CORS** (first) -- add CORS headers and handle OPTIONS preflight
2. **Error simulation** (second) -- short-circuit with 500 before any work
3. **Delay** (third) -- sleep before writing the real response
4. **Normal replay** (last) -- existing match-and-respond logic

This order is intentional:
- CORS headers must be present even on error responses (browsers need them)
- Error simulation should skip the delay (a simulated 500 returns fast)
- Delay applies only to successful replay responses

##### Feature 1: CORS support (#94)

**New Server fields:**

```go
type Server struct {
    // ... existing fields ...
    cors bool // if true, add CORS headers to all responses
}
```

**New ServerOption:**

```go
// WithCORS enables CORS headers on all responses. When enabled, the
// server adds permissive CORS headers (Access-Control-Allow-Origin: *)
// and handles OPTIONS preflight requests automatically with 204 No Content.
//
// This is intended for local development where the frontend dev server
// (e.g., localhost:3000) calls the mock backend (e.g., localhost:3001).
// It is opt-in only.
func WithCORS() ServerOption {
    return func(s *Server) { s.cors = true }
}
```

**ServeHTTP changes:**

At the top of `ServeHTTP`, before any other logic:

```go
// CORS: add headers to every response if enabled.
if s.cors {
    w.Header().Set("Access-Control-Allow-Origin", "*")
    w.Header().Set("Access-Control-Allow-Methods",
        "GET, POST, PUT, DELETE, PATCH, OPTIONS, HEAD")
    w.Header().Set("Access-Control-Allow-Headers",
        "Content-Type, Authorization, X-Requested-With, Accept")
    w.Header().Set("Access-Control-Max-Age", "86400")

    // Handle OPTIONS preflight: return 204 with CORS headers, no body.
    if r.Method == http.MethodOptions {
        w.WriteHeader(http.StatusNoContent)
        return
    }
}
```

**CLI flag:**

```go
// In runServe:
cors := fs.Bool("cors", false, "Enable CORS headers (Access-Control-Allow-Origin: *)")

// When building ServerOptions:
if *cors {
    serverOpts = append(serverOpts, httptape.WithCORS())
}
```

The `--cors` flag is also added to `runRecord` since the recording proxy
may also serve a frontend during development.

##### Feature 2: Latency simulation (#95)

**New Server fields:**

```go
type Server struct {
    // ... existing fields ...
    delay time.Duration // fixed delay before every response; zero means no delay
}
```

**New ServerOption:**

```go
// WithDelay adds a fixed delay before every response. The delay is
// applied after matching but before writing the response. If the
// request context is cancelled during the delay (e.g., client
// disconnects), ServeHTTP returns immediately without writing.
//
// A zero or negative duration is a no-op.
func WithDelay(d time.Duration) ServerOption {
    return func(s *Server) { s.delay = d }
}
```

**ServeHTTP changes:**

After matching a tape, before writing the response:

```go
// Delay: sleep before writing the response if configured.
if s.delay > 0 {
    timer := time.NewTimer(s.delay)
    defer timer.Stop()
    select {
    case <-timer.C:
        // delay elapsed, proceed to write response
    case <-r.Context().Done():
        // client disconnected during delay, bail out
        return
    }
}
```

The delay is applied only to successful matches. Fallback (no-match)
responses are not delayed -- there is no point in delaying an error
response for a missing fixture.

**Per-fixture delay** (from issue #95 acceptance criteria): The Tape
`Metadata` map (type `map[string]any`, already present in the Tape
struct's JSON representation) can carry a `"delay"` key with a Go
duration string value. When present, it overrides the global delay for
that specific fixture.

```go
// After matching, check per-fixture delay override.
effectiveDelay := s.delay
if tape.Metadata != nil {
    if raw, ok := tape.Metadata["delay"]; ok {
        if ds, ok := raw.(string); ok {
            if parsed, err := time.ParseDuration(ds); err == nil {
                effectiveDelay = parsed
            }
        }
    }
}
```

**CLI flag:**

```go
// In runServe:
delay := fs.Duration("delay", 0, "Fixed delay before every response (e.g., 200ms, 1s)")

// When building ServerOptions:
if *delay > 0 {
    serverOpts = append(serverOpts, httptape.WithDelay(*delay))
}
```

Note: `flag.Duration` parses Go duration strings natively -- no custom
parsing needed.

##### Feature 3: Error simulation (#96)

**New Server fields:**

```go
type Server struct {
    // ... existing fields ...
    errorRate float64              // fraction of requests that return 500 (0.0-1.0)
    randFloat func() float64      // random number generator (injectable for testing)
}
```

**New ServerOptions:**

```go
// WithErrorRate causes a fraction of requests to return 500 Internal
// Server Error with an "X-Httptape-Error: simulated" header instead of
// the recorded response. The rate must be between 0.0 and 1.0 inclusive.
//
// A rate of 0.0 disables error simulation (default). A rate of 1.0
// causes all requests to fail.
//
// Panics if rate is outside [0.0, 1.0]. This is a programming error,
// following the constructor-guard convention (see L-11).
func WithErrorRate(rate float64) ServerOption {
    return func(s *Server) {
        if rate < 0 || rate > 1 {
            panic("httptape: WithErrorRate rate must be between 0.0 and 1.0")
        }
        s.errorRate = rate
    }
}

// withRandFloat overrides the random number generator for testing.
// This is unexported -- only used in tests to make error simulation
// deterministic.
func withRandFloat(fn func() float64) ServerOption {
    return func(s *Server) { s.randFloat = fn }
}
```

**NewServer default for randFloat:**

```go
// In NewServer, after applying options:
if s.randFloat == nil {
    s.randFloat = rand.Float64
}
```

This uses `math/rand.Float64` from the top-level package (which is
auto-seeded since Go 1.20). The injectable `randFloat` field allows
tests to provide a deterministic function.

**ServeHTTP changes:**

After CORS handling, before delay and matching:

```go
// Error simulation: randomly return 500 if error rate is set.
if s.errorRate > 0 && s.randFloat() < s.errorRate {
    if s.cors {
        // CORS headers already set above
    }
    w.Header().Set("X-Httptape-Error", "simulated")
    http.Error(w, "httptape: simulated error", http.StatusInternalServerError)
    return
}
```

**Per-fixture error** (from issue #96 acceptance criteria): When a tape's
Metadata contains an `"error"` key with a map value containing `"status"`
(int) and optionally `"body"` (string), that tape always returns the
specified error instead of the recorded response.

```go
// After matching, check per-fixture error override.
if tape.Metadata != nil {
    if raw, ok := tape.Metadata["error"]; ok {
        if errMap, ok := raw.(map[string]any); ok {
            status := http.StatusInternalServerError
            body := "httptape: fixture error"
            if s, ok := errMap["status"].(float64); ok {
                status = int(s)
            }
            if b, ok := errMap["body"].(string); ok {
                body = b
            }
            w.Header().Set("X-Httptape-Error", "simulated")
            http.Error(w, body, status)
            return
        }
    }
}
```

Note: JSON unmarshaling into `map[string]any` produces `float64` for
numbers, which is why the status is read as `float64` and cast to `int`.

**CLI flag:**

```go
// In runServe:
errorRate := fs.Float64("error-rate", 0, "Fraction of requests that return 500 (0.0-1.0)")

// When building ServerOptions:
if *errorRate > 0 {
    serverOpts = append(serverOpts, httptape.WithErrorRate(*errorRate))
}
```

##### Complete ServeHTTP flow (revised)

```go
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // 1. CORS headers (if enabled).
    if s.cors {
        w.Header().Set("Access-Control-Allow-Origin", "*")
        w.Header().Set("Access-Control-Allow-Methods",
            "GET, POST, PUT, DELETE, PATCH, OPTIONS, HEAD")
        w.Header().Set("Access-Control-Allow-Headers",
            "Content-Type, Authorization, X-Requested-With, Accept")
        w.Header().Set("Access-Control-Max-Age", "86400")
        if r.Method == http.MethodOptions {
            w.WriteHeader(http.StatusNoContent)
            return
        }
    }

    // 2. Random error simulation (if enabled).
    if s.errorRate > 0 && s.randFloat() < s.errorRate {
        w.Header().Set("X-Httptape-Error", "simulated")
        http.Error(w, "httptape: simulated error", http.StatusInternalServerError)
        return
    }

    // 3. Retrieve tapes from store.
    tapes, err := s.store.List(r.Context(), Filter{})
    if err != nil {
        http.Error(w, "httptape: store error", http.StatusInternalServerError)
        return
    }

    // 4. Find a matching tape.
    tape, ok := s.matcher.Match(r, tapes)
    if !ok {
        if s.onNoMatch != nil {
            s.onNoMatch(r)
        }
        w.WriteHeader(s.fallbackStatus)
        w.Write(s.fallbackBody)
        return
    }

    // 5. Per-fixture error override.
    if tape.Metadata != nil {
        if raw, ok := tape.Metadata["error"]; ok {
            if errMap, ok := raw.(map[string]any); ok {
                status := http.StatusInternalServerError
                body := "httptape: fixture error"
                if s, ok := errMap["status"].(float64); ok {
                    status = int(s)
                }
                if b, ok := errMap["body"].(string); ok {
                    body = b
                }
                w.Header().Set("X-Httptape-Error", "simulated")
                http.Error(w, body, status)
                return
            }
        }
    }

    // 6. Delay (global or per-fixture override).
    effectiveDelay := s.delay
    if tape.Metadata != nil {
        if raw, ok := tape.Metadata["delay"]; ok {
            if ds, ok := raw.(string); ok {
                if parsed, err := time.ParseDuration(ds); err == nil {
                    effectiveDelay = parsed
                }
            }
        }
    }
    if effectiveDelay > 0 {
        timer := time.NewTimer(effectiveDelay)
        defer timer.Stop()
        select {
        case <-timer.C:
        case <-r.Context().Done():
            return
        }
    }

    // 7. Write the matched tape's response.
    for key, values := range tape.Response.Headers {
        w.Header()[key] = append([]string(nil), values...)
    }
    w.Header().Del("Content-Length")
    w.WriteHeader(tape.Response.StatusCode)
    w.Write(tape.Response.Body)
}
```

##### CLI changes summary

Both `runServe` and `runRecord` get three new flags:

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--cors` | bool | false | Enable CORS headers |
| `--delay` | duration | 0 | Fixed delay before responses |
| `--error-rate` | float64 | 0.0 | Fraction of requests returning 500 |

The `runServe` function collects `[]httptape.ServerOption` before calling
`httptape.NewServer`, passing all configured options. The `runRecord`
function wraps the reverse proxy handler the same way. Since `runRecord`
uses `httputil.ReverseProxy` as the handler (not `Server`), the CORS and
error-rate middleware for `runRecord` is applied by wrapping the proxy
handler in an `http.HandlerFunc` that applies the same CORS/delay/error
logic. Alternatively, a simpler approach: for `runRecord`, only `--cors`
is relevant (CORS headers on the proxy response). Delay and error-rate
do not make sense for recording mode since you want accurate recordings.
Therefore: `--cors` is added to both `runServe` and `runRecord`, but
`--delay` and `--error-rate` are added to `runServe` only.

##### Metadata field on Tape

The per-fixture features (delay override, error override) require a
`Metadata` field on `Tape`. If `Tape` does not already have this field,
add it:

```go
type Tape struct {
    // ... existing fields ...

    // Metadata holds optional key-value pairs for fixture-level
    // configuration (e.g., delay, error simulation). Not used for
    // matching. Values are preserved through JSON round-trip.
    Metadata map[string]any `json:"metadata,omitempty"`
}
```

The `omitempty` tag ensures existing fixtures without metadata produce
clean JSON. The field is not used by matchers, sanitizers, or the
recorder -- it is consumed only by `Server.ServeHTTP`.

##### New imports in server.go

```go
import (
    "math/rand"
    "net/http"
    "time"
)
```

##### Design choices

1. **All in `server.go`**: these features are Server concerns. They modify
   `ServeHTTP` behavior via functional options. No new files needed.

2. **Inline middleware, not separate handlers**: wrapping `http.Handler`
   in middleware chains is idiomatic Go, but these features are tightly
   coupled to Server internals (e.g., per-fixture metadata requires
   access to the matched tape). Inline checks in `ServeHTTP` are simpler
   and avoid the complexity of passing matched-tape context through
   middleware.

3. **CORS is permissive (`*`)**: this is a dev tool. Granular origin
   control is out of scope for v1 (per issue #94).

4. **`randFloat` injection**: the `withRandFloat` unexported option lets
   tests supply a deterministic function (e.g., always returns 0.0 or
   1.0) without exposing internals. This follows the existing pattern of
   unexported test helpers in the codebase.

5. **`time.NewTimer` + select for delay**: this is the correct way to
   implement a cancellable sleep in Go. Using `time.Sleep` would not
   respect context cancellation.

6. **Error simulation before matching**: random errors skip the
   store.List call entirely. This is both more efficient and more
   realistic (a 500 from an overloaded server happens before request
   processing).

7. **Per-fixture error after matching**: per-fixture errors are
   deterministic overrides, not random. They always fire for the matched
   fixture, regardless of `errorRate`.

8. **CORS on error responses**: CORS headers are set first, before any
   short-circuit. This ensures browsers can read error responses (without
   CORS headers, the browser hides the response body from JavaScript).

##### File placement

- `server.go` -- all three ServerOption functions, updated ServeHTTP
- `server_test.go` -- new test cases for CORS, delay, error rate
- `tape.go` -- add Metadata field if not already present
- `cmd/httptape/main.go` -- new CLI flags

##### Test plan

**CORS tests:**
- CORS headers present when `WithCORS()` enabled
- CORS headers absent when not enabled
- OPTIONS preflight returns 204 with CORS headers
- OPTIONS without CORS returns normal fallback
- CORS headers present on error simulation responses
- CORS headers present on no-match fallback responses

**Delay tests:**
- Response delayed by configured duration (measure with time.Since)
- Zero delay is a no-op (fast response)
- Per-fixture delay overrides global delay
- Context cancellation during delay returns immediately
- Delay not applied to no-match fallback responses

**Error simulation tests:**
- Error rate 0: no errors (randFloat always returns 0.5)
- Error rate 1: all errors (randFloat always returns 0.5)
- Error rate 0.5 with randFloat returning 0.3: error
- Error rate 0.5 with randFloat returning 0.7: no error
- X-Httptape-Error header present on simulated errors
- Per-fixture error always fires regardless of error rate
- Per-fixture error with custom status and body
- WithErrorRate panics on rate < 0 or rate > 1

**Integration tests:**
- All three features combined: CORS + delay + error rate
- CORS headers present on simulated error responses

#### Consequences

- **Frontend-ready mock server**: httptape becomes usable for frontend
  development out of the box with `--cors --delay 200ms`.
- **Realistic testing**: developers can test loading states and error
  handling without modifying fixtures.
- **No breaking changes**: all features are opt-in via new ServerOption
  functions. Existing behavior is unchanged when no options are set.
- **stdlib only**: `math/rand`, `time`, `net/http` are all stdlib.
- **Metadata field**: adding `Metadata` to Tape is a minor schema change.
  Existing fixtures without the field will unmarshal correctly (nil map).
  New fixtures with metadata will have `"metadata": {...}` in JSON.
- **Per-fixture features require manual JSON editing**: there is no DSL
  or CLI for setting per-fixture metadata. This is acceptable for v1 --
  users edit fixture JSON directly.

---

### ADR-25: Replay header injection (WithReplayHeaders)

**Date**: 2026-03-30
**Issue**: #105
**Status**: Accepted

#### Context

When replaying recorded fixtures as a mock server, certain HTTP response
headers may need to differ from what was originally recorded. Common
scenarios include:

- Injecting fresh `Authorization` tokens so downstream clients pass
  validation.
- Adding correlation or trace headers (`X-Request-Id`, `X-Trace-Id`)
  for observability in test environments.
- Overriding `Cache-Control` or `Set-Cookie` headers that are
  environment-specific.

Currently, the only way to achieve this is to edit every fixture file
manually, which is error-prone and pollutes diffs.

#### Decision

Add a new `ServerOption`:

```go
func WithReplayHeaders(key, value string) ServerOption
```

**Library side** (`server.go`):

- A `replayHeaders map[string]string` field is added to the `Server`
  struct.
- Each call to `WithReplayHeaders` inserts (or overwrites) an entry in
  the map.
- In `ServeHTTP`, after copying the matched tape's response headers and
  before removing `Content-Length`, every entry in `replayHeaders` is
  applied via `w.Header().Set(key, value)`. This means replay headers
  override tape headers with the same key.

**CLI side** (`cmd/httptape/main.go`):

- A repeatable `--replay-header` flag is added to the `serve` command.
- Format: `--replay-header "Key=Value"`.
- Multiple flags are allowed:
  `--replay-header "Authorization=Bearer tok" --replay-header "X-Req-Id=123"`.
- The flag is split on the first `=` sign; keys without `=` are rejected
  as a usage error.

##### Design choices

1. **`map[string]string` (single value per key)**: HTTP allows multiple
   values per header key, but the override use case is almost always
   "replace the value". A map keeps the API simple. Users who need
   multi-valued headers can call `WithReplayHeaders` with the
   canonical comma-separated form.

2. **Applied after tape headers, before Content-Length removal**: this
   ensures replay headers always win over recorded values, and
   Content-Length is still recalculated by `net/http` from the actual
   body.

3. **No effect on fallback/error responses**: replay headers are only
   injected on successful tape matches. Fallback (no-match) and
   simulated error responses are not modified — they are synthetic
   responses from httptape itself, not from recorded tapes.

4. **Repeatable CLI flag via `flag.Value` interface**: Go's `flag`
   package supports custom value types. A `repeatableFlag` (string
   slice) collects multiple `--replay-header` invocations.

##### File placement

- `server.go` -- `WithReplayHeaders` option, `replayHeaders` field,
  injection in `ServeHTTP`
- `server_test.go` -- new test cases
- `cmd/httptape/main.go` -- `--replay-header` flag, `repeatableFlag` type

##### Test plan

- Header override: tape has `Authorization: original`, replay header
  sets `Authorization: injected` — response has injected value.
- Multiple overrides: three different replay headers all appear in
  response.
- No override when not set: default server leaves tape headers untouched.

#### Consequences

- **Non-breaking**: `WithReplayHeaders` is a new opt-in option. Existing
  behavior is unchanged when no replay headers are configured.
- **Environment-portable fixtures**: fixtures can be recorded once and
  replayed in different environments by injecting environment-specific
  headers at serve time, without editing fixture files.
- **stdlib only**: no new dependencies.
- **Single-value limitation**: the `map[string]string` design means only
  one value per header key. This is acceptable for the target use cases.
  If multi-value support is needed later, the API can be extended with a
  `WithReplayHeaderValues(key string, values ...string)` option without
  breaking the existing API.

---

### ADR-26: Fallback-to-cache proxy mode with L1/L2 caching

**Date**: 2026-03-30
**Issue**: #108
**Status**: Accepted

#### Context

During development, engineers need to hit the real backend when it is available
but seamlessly fall back to recorded fixtures when it is not (backend down,
offline, CI without credentials). Today httptape forces a binary choice between
`record` mode (always forwards, always records) and `serve` mode (always replays
from disk). There is no hybrid.

Issue #108 proposes a proxy mode that forwards to the real backend, records
successful responses, and falls back to cached tapes on failure. The issue
comments refine this into a two-tier caching architecture:

- **L1 cache** (MemoryStore) -- raw/unsanitized responses, ephemeral,
  lost on process restart.
- **L2 cache** (FileStore) -- sanitized responses, persistent on disk,
  safe to commit to version control.

On fallback, L1 is checked first (preserves real data within the current
session), then L2 (sanitized but still functional). This gives the best
developer experience: during an active session the developer sees consistent
real data even when the backend goes down; on restart, only sanitized fixtures
remain, honoring the core "sensitive data never touches disk" promise.

#### Decision

##### New type: `Proxy` (in `proxy.go`)

A new `Proxy` type is introduced rather than extending the existing `Recorder`.
Rationale:

1. **Single Responsibility**: `Recorder` is a pure recording RoundTripper. Its
   contract is simple: forward request, capture response, save tape, return
   response. Adding fallback logic (match from L1, match from L2, synthesize
   response) would make it do two very different things.

2. **Different dependency set**: `Proxy` needs a `Matcher` (for fallback
   lookups) that `Recorder` does not need. Adding a matcher dependency to
   `Recorder` would confuse its API for non-proxy users.

3. **Different lifecycle**: `Proxy` manages two stores (L1 + L2) with different
   write semantics (raw vs. sanitized). `Recorder` manages one store with one
   write path.

4. **The existing `Recorder` is a building block**: `Proxy` delegates the
   actual HTTP forwarding to an inner `http.RoundTripper` (typically
   `http.DefaultTransport`), not to a `Recorder`. The recording logic in
   `Proxy.RoundTrip` is specialized (dual-write to L1 and L2 with different
   sanitization) and cannot reuse `Recorder.RoundTrip` as-is.

```go
// Proxy is an http.RoundTripper that forwards requests to a real backend,
// records successful responses to a two-tier cache (L1 in-memory, L2 on disk),
// and falls back to cached tapes on transport failure.
//
// On success:
//   - Raw (unsanitized) tape saved to L1 (MemoryStore)
//   - Sanitized tape saved to L2 (FileStore)
//   - Real response returned to caller
//
// On failure:
//   - Match from L1 (raw, best UX within session)
//   - Match from L2 (sanitized, persistent)
//   - If neither matches, return original error
//
// Proxy is safe for concurrent use by multiple goroutines.
type Proxy struct {
    transport  http.RoundTripper  // real backend transport
    l1         Store              // raw/ephemeral (typically *MemoryStore)
    l2         Store              // sanitized/persistent (typically *FileStore)
    sanitizer  Sanitizer          // applied to L2 writes only
    matcher    Matcher            // for fallback lookups
    route      string             // logical route label
    onError    func(error)        // async error callback
    isFallback func(error, *http.Response) bool // determines when to fall back
}

// ProxyOption configures a Proxy.
type ProxyOption func(*Proxy)
```

##### Constructor

```go
// NewProxy creates a new Proxy with the given L1 (ephemeral) and L2 (persistent)
// stores.
//
// Defaults:
//   - transport: http.DefaultTransport
//   - sanitizer: NewPipeline() (no-op)
//   - matcher: DefaultMatcher()
//   - isFallback: transport errors only (not 5xx)
//   - route: ""
//
// Both l1 and l2 must be non-nil. Panics on nil stores (constructor guard
// convention per L-11).
func NewProxy(l1, l2 Store, opts ...ProxyOption) *Proxy
```

Parameter order is `l1, l2` (not `l2, l1`) because the mental model is
"fast cache first, persistent cache second" which matches the fallback
lookup order.

##### ProxyOption functions

```go
// WithProxyTransport sets the inner http.RoundTripper for real backend calls.
func WithProxyTransport(rt http.RoundTripper) ProxyOption

// WithProxySanitizer sets the Sanitizer applied to L2 writes.
// L1 writes are always raw (unsanitized).
func WithProxySanitizer(s Sanitizer) ProxyOption

// WithProxyMatcher sets the Matcher used for fallback lookups.
func WithProxyMatcher(m Matcher) ProxyOption

// WithProxyRoute sets the route label for all tapes.
func WithProxyRoute(route string) ProxyOption

// WithProxyOnError sets the error callback.
func WithProxyOnError(fn func(error)) ProxyOption

// WithProxyFallbackOn sets a custom function that decides whether a given
// transport error and/or HTTP response should trigger fallback.
//
// The function receives:
//   - err: the error from transport.RoundTrip (nil if the call succeeded)
//   - resp: the HTTP response (nil if err is non-nil)
//
// It returns true if fallback should be attempted.
//
// Default: fall back only on transport errors (err != nil).
// Common alternative: also fall back on 5xx responses.
func WithProxyFallbackOn(fn func(err error, resp *http.Response) bool) ProxyOption
```

The option names are prefixed with `Proxy` (e.g., `WithProxyTransport`) to avoid
collision with existing `Recorder` options (`WithTransport`, `WithSanitizer`,
etc.) since all types are in the same package.

##### RoundTrip flow

```go
func (p *Proxy) RoundTrip(req *http.Request) (*http.Response, error) {
    // 1. Capture request body (same pattern as Recorder).
    reqBody := captureRequestBody(req)

    // 2. Forward to real backend.
    resp, err := p.transport.RoundTrip(req)

    // 3. Decide: success or fallback?
    if p.isFallback(err, resp) {
        // 3a. Fallback path.
        return p.fallback(req, err)
    }

    // 4. Success path: capture response body.
    respBody := captureResponseBody(resp)

    // 5. Build raw tape.
    rawTape := p.buildTape(req, reqBody, resp, respBody)

    // 6. Save raw to L1 (synchronous, in-memory, fast).
    if saveErr := p.l1.Save(req.Context(), rawTape); saveErr != nil {
        p.onErrorSafe(saveErr)
    }

    // 7. Sanitize and save to L2 (synchronous).
    sanitizedTape := p.sanitizer.Sanitize(rawTape)
    if saveErr := p.l2.Save(req.Context(), sanitizedTape); saveErr != nil {
        p.onErrorSafe(saveErr)
    }

    // 8. Return real response (with body restored).
    return resp, nil
}
```

Both L1 and L2 saves are **synchronous** in `RoundTrip`. Rationale:
- L1 is `MemoryStore` -- a map write under a mutex, sub-microsecond.
- L2 is `FileStore` -- a file write, but proxy mode is I/O-bound on the
  upstream call anyway. The file write is negligible relative to network latency.
- Synchronous writes guarantee the tape is available for immediate fallback if
  the next request fails (no race between async write and fallback read).
- This avoids the complexity of a drain goroutine and Close() lifecycle in Proxy.

##### Fallback logic

```go
func (p *Proxy) fallback(req *http.Request, originalErr error) (*http.Response, error) {
    ctx := req.Context()

    // Try L1 first (raw, best UX).
    if tape, ok := p.matchFromStore(ctx, req, p.l1); ok {
        return p.tapeToResponse(tape, "l1-cache"), nil
    }

    // Try L2 (sanitized, persistent).
    if tape, ok := p.matchFromStore(ctx, req, p.l2); ok {
        return p.tapeToResponse(tape, "l2-cache"), nil
    }

    // No match in either cache -- return original error.
    return nil, originalErr
}

func (p *Proxy) matchFromStore(ctx context.Context, req *http.Request, store Store) (Tape, bool) {
    tapes, err := store.List(ctx, Filter{})
    if err != nil {
        p.onErrorSafe(err)
        return Tape{}, false
    }
    return p.matcher.Match(req, tapes)
}
```

This reuses the exact same `Store.List` + `Matcher.Match` pattern as
`Server.ServeHTTP` (lines 198-205 of server.go). The matching logic is
not duplicated -- it is delegated to the same `Matcher` interface.

##### Response synthesis from tape

```go
func (p *Proxy) tapeToResponse(tape Tape, source string) *http.Response {
    resp := &http.Response{
        StatusCode: tape.Response.StatusCode,
        Header:     tape.Response.Headers.Clone(),
        Body:       io.NopCloser(bytes.NewReader(tape.Response.Body)),
    }
    // Indicate fallback source.
    resp.Header.Set("X-Httptape-Source", source)
    return resp
}
```

The `X-Httptape-Source` header tells the caller where the response came from:
- `"upstream"` -- not set (absence means upstream; avoids header noise on
  the happy path)
- `"l1-cache"` -- raw in-memory fallback
- `"l2-cache"` -- sanitized on-disk fallback

##### Fallback detection: `isFallback` function

The default `isFallback` triggers only on transport errors (connection refused,
DNS failure, timeout). This is the conservative default -- 5xx responses from
a functioning backend are real responses that the caller may want to handle.

A common alternative for dev workflows is to also fall back on 5xx:

```go
proxy := httptape.NewProxy(l1, l2,
    httptape.WithProxyFallbackOn(func(err error, resp *http.Response) bool {
        if err != nil {
            return true // transport error
        }
        return resp != nil && resp.StatusCode >= 500
    }),
)
```

This is a function rather than a bool flag because real-world needs vary:
some teams want to fall back on specific status codes (502, 503 but not 500),
on specific error types, or on responses missing certain headers.

##### L1 lifecycle: truly ephemeral

L1 (MemoryStore) is not flushed on shutdown. It is truly ephemeral:

- On graceful shutdown, L1 data is lost. This is intentional -- L1 contains
  raw/unsanitized data that must not persist.
- There is no `Close()` method on `Proxy`. The `MemoryStore` has no resources
  to release (no goroutines, no file handles).
- If the caller needs cleanup, they manage the `MemoryStore` lifecycle directly.

##### CLI: `httptape proxy` command

A new `proxy` subcommand is added to `cmd/httptape/main.go`:

```
httptape proxy --upstream URL --fixtures DIR [--config FILE] [--port PORT] [--cors]
```

Flags:
- `--upstream` (required): upstream backend URL
- `--fixtures` (required): directory for L2 (FileStore, sanitized fixtures)
- `--config`: sanitization config JSON (applied to L2 writes only)
- `--port`: listen port (default 8081)
- `--cors`: enable CORS headers
- `--fallback-on-5xx`: also fall back on 5xx responses (default: transport
  errors only)

L1 is always an internal `MemoryStore` -- not configurable via CLI. There is
no reason to persist L1 (it would violate the security model).

Implementation sketch for `runProxy`:

```go
func runProxy(args []string) error {
    // Parse flags (same pattern as runRecord).
    // Create L1 = NewMemoryStore()
    // Create L2 = NewFileStore(WithDirectory(fixtures))
    // Load config -> build pipeline (if --config provided)
    // Create proxy = NewProxy(l1, l2, opts...)
    // Create httputil.ReverseProxy with Transport: proxy
    // Listen, serve, graceful shutdown.
}
```

The reverse proxy setup mirrors `runRecord` -- `httputil.ReverseProxy` with
`Rewrite` to set the upstream URL, and `Transport` set to the `Proxy`
RoundTripper.

##### Go API for embedded use

```go
// Minimal setup:
l1 := httptape.NewMemoryStore()
l2, _ := httptape.NewFileStore(httptape.WithDirectory("fixtures"))
proxy := httptape.NewProxy(l1, l2,
    httptape.WithProxyTransport(http.DefaultTransport),
    httptape.WithProxySanitizer(pipeline),
)
client := &http.Client{Transport: proxy}

// Use client normally -- it records on success, falls back on failure.
resp, err := client.Get("https://api.example.com/users")
```

##### Request body handling for fallback matching

When `RoundTrip` enters the fallback path, the request body has already been
captured (step 1 of the flow). The body is restored on `req` via
`io.NopCloser(bytes.NewReader(reqBody))` before calling `p.fallback(req, err)`,
so the `Matcher` (specifically `MatchBodyHash` or `MatchBodyFuzzy`) can read
it. This follows the same body-capture-and-restore pattern used in
`Recorder.RoundTrip`.

##### Sanitization config guidance

The issue comments note that there should be no `--no-sanitize` flag. Instead,
the documentation should show two sanitization config profiles:

1. **Full sanitization** (for sharing/CI): redacts headers, body PII, fakes
   fields. Cached L2 responses may have `[REDACTED]` in auth headers.

2. **Minimal sanitization** (for proxy/dev): skip header redaction, only
   redact true PII in bodies. Functional headers (Authorization, Content-Type,
   pagination tokens) stay intact so cached responses work as drop-in
   replacements.

Both are achievable with the existing `Pipeline` / `Config` system -- no new
API needed.

#### Alternatives considered

1. **Extend Recorder with `WithFallback(bool)` option**: Rejected. Would
   require adding `Matcher`, dual-store, and fallback logic to `Recorder`,
   violating single responsibility. The `Recorder` contract (always forward,
   always record) would become conditional.

2. **Compose Recorder + Server in a wrapper**: Considered using `Recorder`
   for the success path and `Server` for the fallback path. Rejected because
   `Server` is an `http.Handler`, not a `RoundTripper` -- it writes to
   `http.ResponseWriter`, not returns `*http.Response`. Converting between
   the two would require `httptest.ResponseRecorder` hacks, adding complexity
   without benefit.

3. **Single store with raw + sanitized modes**: Rejected. A single store
   cannot serve both raw (for session-local fallback) and sanitized (for
   persistent/shareable fixtures) simultaneously. The L1/L2 split maps cleanly
   to the existing `MemoryStore`/`FileStore` implementations.

4. **Async writes for L2**: Considered making L2 writes async (like Recorder).
   Rejected for simplicity -- proxy mode is already I/O-bound on upstream
   calls, and synchronous L2 writes guarantee consistency. Can be revisited
   if profiling shows L2 write latency is a problem.

#### Consequences

- **New file**: `proxy.go` (+ `proxy_test.go`) containing the `Proxy` type,
  `ProxyOption` functions, and all proxy logic.
- **Modified file**: `cmd/httptape/main.go` -- add `proxy` subcommand and
  `runProxy` function, update usage text.
- **Non-breaking**: no existing API is modified. `Recorder`, `Server`, and all
  existing options remain unchanged.
- **Security model preserved**: L1 (raw) is in-memory only, never touches disk.
  L2 (sanitized) goes through the full sanitization pipeline before persisting
  to FileStore. The core promise "sensitive data never touches disk" is intact.
- **stdlib only**: no new dependencies. `Proxy` uses `net/http`, `io`,
  `bytes`, `context` -- all stdlib.
- **Testable**: `Proxy` accepts `Store` interfaces for both L1 and L2, and
  `http.RoundTripper` for the transport. All dependencies are injectable. Tests
  can use `MemoryStore` for both tiers and a fake transport.
- **CLI table updated**:

  | Command | Upstream | L1 (memory) | L2 (disk) | Use case |
  |---------|----------|-------------|-----------|----------|
  | `serve` | no | no | read only | Pure replay from sanitized fixtures |
  | `record` | yes | no | write (sanitized) | Capture fixtures for testing |
  | `proxy` | yes | read/write (raw) | read/write (sanitized) | Dev workflow with graceful fallback |

##### Test plan

- **Success path**: transport succeeds -> raw tape in L1, sanitized tape in L2,
  real response returned, no `X-Httptape-Source` header.
- **Fallback to L1**: transport fails -> pre-populated L1 tape returned,
  `X-Httptape-Source: l1-cache` header present.
- **Fallback to L2**: transport fails, L1 empty -> pre-populated L2 tape
  returned, `X-Httptape-Source: l2-cache` header present.
- **No match**: transport fails, both caches empty -> original error returned.
- **L1 before L2**: both caches have a matching tape -> L1 tape is returned
  (not L2).
- **Sanitizer applied to L2 only**: on success, L1 tape has raw headers, L2
  tape has redacted headers.
- **5xx fallback**: `WithProxyFallbackOn` configured for 5xx -> 503 from
  upstream triggers fallback.
- **Custom fallback function**: custom `isFallback` -> only specific error
  types trigger fallback.
- **Request body preserved for matching**: POST with body -> transport fails ->
  `MatchBodyHash` correctly matches against L1/L2 tape.
- **Concurrent safety**: multiple goroutines calling `RoundTrip` simultaneously
  with mixed success/failure -> no races, no panics.
- **CLI integration**: `httptape proxy --upstream ... --fixtures ...` starts
  and proxies correctly.

##### File placement

- `proxy.go` -- `Proxy` type, `ProxyOption` functions, `RoundTrip`,
  `fallback`, `matchFromStore`, `tapeToResponse`, `buildTape`
- `proxy_test.go` -- unit tests for all paths
- `cmd/httptape/main.go` -- `runProxy` function, `proxy` subcommand routing,
  updated usage text


---

### ADR-27: Pluggable Faker interface with format-preserving adapters

**Date**: 2026-03-30
**Issue**: #116
**Status**: Accepted

#### Context

ADR-7 introduced `FakeFields` with hardcoded type detection (email, UUID,
numeric, generic string). While this covers common cases, it falls short
for format-sensitive fields: phone numbers (`+1-555-867-5309` becomes
`fake_a1b2c3d4`), credit card numbers (no Luhn validation), SSNs, dates,
and prefix-bearing API tokens (`sk_test_...`). UI display logic and
client-side validation break when the faked value does not preserve the
structural format of the original.

The existing `fakeValue` function is an auto-detecting dispatcher. Rather
than adding ever more detection heuristics, we need a pluggable system
where users can explicitly assign a faking strategy per field path.

Key constraints from the project constitution:
- Hexagonal architecture: the `Faker` interface is a port; each built-in
  faker is an adapter.
- stdlib only: all implementations use `crypto/hmac`, `crypto/sha256`,
  `encoding/hex`, `strconv`, `fmt`, `time` -- no external libraries.
- Deterministic: same seed + same input = same output, always.
- Backward compatible: `FakeFields` must continue to work unchanged.
- `SanitizeFunc` contract: return a copy, never mutate the input.
- No panics in hot paths.

#### Decision

##### 1. The `Faker` interface (port)

```go
// Faker is a port that produces deterministic fake values from a seed and
// an original value. Implementations must be deterministic: the same seed
// and original value must always produce the same output.
//
// The seed is the project-level HMAC key. The original is the JSON value
// being faked (string, float64, bool, nil, map[string]any, []any).
// The return value replaces the original in the sanitized output.
type Faker interface {
    Fake(seed string, original any) any
}
```

This is placed in `faker.go` alongside all built-in adapter types. The
interface is intentionally minimal (one method) to maximize composability
and keep the implementation burden low for custom fakers.

##### 2. Built-in faker adapters

All adapters are exported struct types implementing `Faker`. Each uses
`computeHMAC(seed, stringRepr)` from `sanitizer.go` to derive
deterministic output from the HMAC hash bytes.

| Type | Behavior | Fields |
|------|----------|--------|
| `RedactedFaker` | Returns `[REDACTED]` for strings, `0` for numbers, `false` for bools | None |
| `FixedFaker` | Returns a caller-supplied constant value | `Value any` |
| `HMACFaker` | `fake_<hex8>` for strings, positive int for numbers (current generic behavior) | None |
| `EmailFaker` | `user_<hex8>@example.com` (current email behavior) | None |
| `PhoneFaker` | Digit-by-digit HMAC replacement preserving original formatting (dashes, spaces, parens, plus signs) | None |
| `CreditCardFaker` | Preserves issuer prefix (first 6 digits), fills middle with HMAC-derived digits, computes valid Luhn check digit | None |
| `NumericFaker` | HMAC-derived decimal digits of specified length | `Length int` |
| `DateFaker` | Deterministic date from epoch offset (2000-01-01 + HMAC-derived days mod ~100 years), formatted with caller-specified Go `time.Format` layout | `Format string` |
| `PatternFaker` | Fills `#` with digits (0-9), `?` with lowercase letters (a-z), literal chars preserved | `Pattern string` |
| `PrefixFaker` | `<prefix><hex8>` | `Prefix string` |
| `AddressFaker` | `<number> <street> <suffix>, <city>, <state> <zip>` with deterministic components drawn from small built-in lists (10 entries each) indexed by HMAC bytes | None |

**Determinism guarantee**: every faker derives its output solely from
`computeHMAC(seed, fmt.Sprintf("%v", original))`. No randomness, no
global state, no time-dependent logic.

**Luhn algorithm for `CreditCardFaker`**: The check digit is computed
using the standard Luhn mod-10 algorithm over the first 15 digits
(6-digit issuer prefix from the original + 9 HMAC-derived digits). This
is a pure arithmetic function -- no external dependencies.

**`PhoneFaker` digit-by-digit replacement**: Each digit in the original
string is replaced with an HMAC-derived digit (0-9). All non-digit
characters (dashes, spaces, parentheses, plus signs) are preserved in
their original positions, maintaining the phone number's formatting.
If the HMAC bytes are exhausted, a new HMAC is computed from the
current output to chain additional bytes.

**`DateFaker` epoch offset generation**: The first 4 HMAC bytes are
interpreted as a uint32 days offset from 2000-01-01, modulo 36525
(~100 years). The resulting date is formatted using the caller-specified
Go `time.Format` layout string (default: "2006-01-02").

**`AddressFaker` built-in data**: A small hardcoded list of 10 street
names, 10 street suffixes, 10 city names, and 50 US state abbreviations. The HMAC bytes
select indices into these lists. The house number is derived from the
first 2 HMAC bytes (range [1-9999]). The zip code is 5 HMAC-derived
digits.

##### 3. `FakeFieldsWith` constructor

```go
// FakeFieldsWith returns a SanitizeFunc that replaces field values in JSON
// request and response bodies with deterministic fakes using explicitly
// assigned Faker implementations per path.
//
// The seed is a project-level secret used as the HMAC key. The fields map
// associates JSONPath-like paths with specific Faker implementations.
//
// This gives callers explicit control over the faking strategy for each
// field, unlike FakeFields which auto-detects the strategy from the value.
func FakeFieldsWith(seed string, fields map[string]Faker) SanitizeFunc
```

Internally, `FakeFieldsWith` parses all map keys as paths at construction
time (same as `FakeFields`), then at execution time traverses the JSON
body and calls `faker.Fake(seed, originalValue)` for each matched leaf.

The traversal logic (`fakeAtPathWith`) mirrors `fakeAtPath` but accepts
a `Faker` instead of calling `fakeValue`. This avoids modifying the
existing `fakeAtPath` function, preserving backward compatibility.

##### 4. Backward compatibility

`FakeFields(seed, paths...)` remains unchanged. Internally it continues
to call `fakeValue`, which is the existing auto-detecting dispatcher.
`fakeValue` is **not** refactored to use the `Faker` interface -- this
avoids any risk of behavioral change for existing users.

A future ADR may refactor `fakeValue` to delegate to the built-in fakers,
but that is out of scope here.

##### 5. JSON config changes

The `Rule` struct gains a new `Fields` field:

```go
type Rule struct {
    Action  string            `json:"action"`
    Headers []string          `json:"headers,omitempty"`
    Paths   []string          `json:"paths,omitempty"`
    Seed    string            `json:"seed,omitempty"`
    Fields  map[string]any    `json:"fields,omitempty"`
}
```

For the `"fake"` action, the config now supports two mutually exclusive
modes:

1. **`paths` mode** (existing): array of paths, auto-detection.
   ```json
   {"action": "fake", "seed": "s", "paths": ["$.email", "$.phone"]}
   ```

2. **`fields` mode** (new): map of path -> faker spec.
   ```json
   {"action": "fake", "seed": "s", "fields": {
     "$.email": "email",
     "$.phone": "phone",
     "$.card.number": "credit_card",
     "$.card.cvv": {"type": "numeric", "length": 3},
     "$.ssn": {"type": "pattern", "pattern": "###-##-####"}
   }}
   ```

**Parsing rules for faker specs**:

- **String shorthand**: maps to a zero-value faker by name.
  | String | Faker |
  |--------|-------|
  | `"redacted"` | `RedactedFaker{}` |
  | `"hmac"` | `HMACFaker{}` |
  | `"email"` | `EmailFaker{}` |
  | `"phone"` | `PhoneFaker{}` |
  | `"credit_card"` | `CreditCardFaker{}` |
  | `"address"` | `AddressFaker{}` |

- **Object form**: `{"type": "<name>", ...params}`. The `type` field
  selects the faker; remaining fields are type-specific parameters.
  | Type | Extra fields | Faker |
  |------|-------------|-------|
  | `"numeric"` | `"length": int` | `NumericFaker{Length: n}` |
  | `"date"` | `"format": string` | `DateFaker{Format: f}` |
  | `"pattern"` | `"pattern": string` | `PatternFaker{Pattern: p}` |
  | `"prefix"` | `"prefix": string` | `PrefixFaker{Prefix: p}` |
  | `"fixed"` | `"value": any` | `FixedFaker{Value: v}` |
  | All shorthands | (none) | Corresponding zero-value faker |

**Validation**: `Validate()` checks:
- `paths` and `fields` are mutually exclusive for a `"fake"` rule.
- At least one of `paths` or `fields` must be present.
- All keys in `fields` must be valid paths (via `parsePath`).
- All faker specs must resolve to known types.
- Required parameters must be present (e.g., `"pattern"` requires
  `"pattern"` field, `"numeric"` requires `"length"` > 0).

**`BuildPipeline`**: when `fields` is present, the rule maps to
`FakeFieldsWith(seed, parsedFakers)` instead of `FakeFields(seed, paths...)`.

##### 6. `config.schema.json` update

The JSON Schema is updated to reflect the new `fields` property on
`"fake"` rules, with `oneOf` constraining the mutual exclusivity of
`paths` vs `fields`. The faker spec schema uses `oneOf` for string
shorthand vs object form.

##### 7. File placement

- **`faker.go`** (new file): `Faker` interface, all built-in faker
  struct types and their `Fake` methods, `FakeFieldsWith` constructor,
  `fakeAtPathWith` traversal, `fakeBodyFieldsWith` body processor, Luhn
  helper. Rationale: `sanitizer.go` is already 579 lines; adding ~400
  lines of faker types would push it past maintainable size. A dedicated
  file keeps the faker port/adapter boundary clean.
- **`faker_test.go`** (new file): unit tests for every faker, including
  Luhn validation for `CreditCardFaker`, format validation for
  `PhoneFaker` and `DateFaker`, determinism tests (same seed+input =
  same output), and `FakeFieldsWith` integration tests.
- **`sanitizer.go`**: no changes. `fakeValue` and `FakeFields` remain
  as-is.
- **`config.go`**: updated `Rule` struct, updated `Validate()` for
  `fields` support, updated `BuildPipeline()` to emit `FakeFieldsWith`
  when `fields` is present, new `parseFakerSpec` helper.
- **`config_test.go`**: new test cases for `fields` mode parsing,
  validation, and pipeline building.
- **`config.schema.json`**: updated schema.

##### 8. Implementation details for key fakers

**`CreditCardFaker.Fake`**:
```
1. Extract issuer prefix (first 6 digits) from original string
   (stripping non-digit chars). If fewer than 6 digits, use "400000".
2. Compute HMAC of the original value.
3. Generate 9 digits from HMAC bytes (each byte mod 10).
4. Concatenate: prefix (6) + generated (9) = 15 digits.
5. Compute Luhn check digit over the 15 digits.
6. Return 16-digit string formatted as "XXXX-XXXX-XXXX-XXXX".
```

**`luhnCheckDigit(digits string) byte`** (unexported helper):
```
Standard Luhn algorithm: iterate digits right-to-left, double every
second digit, subtract 9 if > 9, sum all, check digit = (10 - sum%10) % 10.
```

**`PatternFaker.Fake`**:
```
1. Compute HMAC of the original value.
2. Walk the pattern string char by char:
   - '#' -> next HMAC byte mod 10, rendered as digit char
   - '?' -> next HMAC byte mod 26, rendered as lowercase letter
   - anything else -> literal copy
3. If HMAC bytes exhausted, re-HMAC with the current output as message
   (chaining). In practice, 32 HMAC bytes cover patterns up to 32
   placeholders; longer patterns are extremely rare.
```

**`AddressFaker.Fake`**:
```
1. Compute HMAC of the original value.
2. House number: bytes[0:2] as uint16, mod 9999 + 1.
3. Street: streetNames[bytes[2] % len(streetNames)] + " " +
   streetSuffixes[bytes[3] % len(streetSuffixes)]
4. City: cityNames[bytes[4] % len(cityNames)]
5. State: stateAbbrs[bytes[5] % len(stateAbbrs)]
6. Zip: 5 digits from bytes[6:11], each mod 10.
7. Return "<number> <street>, <city>, <state> <zip>"
```

#### Consequences

##### Positive
- Users gain explicit, fine-grained control over faking strategies per
  field without losing the convenience of auto-detection.
- Format-preserving fakers (phone, credit card, date, pattern) prevent
  client-side validation breakage in recorded fixtures.
- The `Faker` interface enables users to implement custom fakers for
  domain-specific formats.
- Full backward compatibility: `FakeFields` behavior is untouched.
- JSON config gains expressive power without breaking existing configs.

##### Negative
- New file (`faker.go`) adds ~400 lines of code. This is acceptable
  given the number of distinct faker types.
- `AddressFaker` hardcodes US-format addresses. Locale-aware faking is
  explicitly out of scope (per issue #116) and can be addressed in a
  future ADR.
- The `Rule.Fields` field uses `map[string]any` for JSON flexibility,
  requiring runtime type assertions in `parseFakerSpec`. This is a
  pragmatic trade-off for supporting both string shorthand and object
  form in JSON.

##### Risks
- Luhn implementation must be correct. Mitigated by table-driven tests
  with known-good card numbers.
- `PatternFaker` HMAC chaining (for patterns > 32 placeholders) adds
  complexity. Mitigated by the fact that such long patterns are
  exceedingly rare in practice.

##### Migration
- No migration needed. Existing configs using `"paths"` continue to work
  unchanged. New `"fields"` syntax is opt-in.

---

### ADR-28: Health endpoint surface for proxy mode (snapshot + SSE + active probe)

**Date**: 2026-04-16
**Issue**: #121
**Status**: Accepted

#### Context

The `Proxy` (ADR-26) currently signals which tier served a request via the
`X-Httptape-Source` response header (`l1-cache`, `l2-cache`, or absent for live
upstream). This is per-request and reactive: a UI built on top of the proxy can
only learn about an upstream outage (or recovery) by issuing a request and
inspecting the header. Issue #121 introduces a small technical surface so
operators and downstream UIs (notably the `ts-frontend-first` demo) can react to
upstream state changes in real time without polling per-request headers.

The PM has locked in:

- **State model = definition B** ("most-recently-served source"): one state
  machine fed by both real client traffic and a synthetic probe, with values
  mirroring the existing `X-Httptape-Source` semantics (`live`, `l1-cache`,
  `l2-cache`).
- **Backward compatibility is non-negotiable**: with both new flags at
  defaults, proxy behavior is byte-for-byte identical to today — no new
  endpoints mounted, no new goroutines, no new headers, `X-Httptape-Source`
  unchanged.
- `X-Httptape-Source` stays as the per-request ground truth. The new surface
  is additive.
- Library API is required; CLI flags are a thin wrapper.
- Out of scope: auth, rate limiting, metrics, k8s readiness conventions, SSE
  `Last-Event-ID` replay, WebSocket transport, surfacing state in
  serve/record/mock modes, per-visitor outage simulation.

This ADR resolves the open questions left for the architect and pins down the
types, file layout, concurrency model, probe lifecycle, SSE multiplex strategy,
and CLI wiring required to ship.

#### Decision

##### Resolution of open questions

| Question | Decision | Rationale |
|---|---|---|
| Flag names | `--health-endpoint` (bool), `--upstream-probe-interval` (duration) | Match the PM proposal — short, explicit, scoped. `health-endpoint` covers both `/__httptape/health` and `/__httptape/health/stream` because they are one capability with two surfaces. |
| Default probe cadence (when `--health-endpoint` is set and the interval is unset) | **2s** | Trade-off between freshness (UI badges feel real-time) and load (one extra HEAD per 2s is invisible to any real upstream). Matches the PM suggestion. Easy to override per deployment. |
| Probe HTTP method | **HEAD** with **GET fallback** if HEAD returns 405/501 (cached for the lifetime of the proxy after the first GET fallback) | HEAD is the smallest blast radius — no body transfer, no cache pollution at the upstream. But many real backends do not implement HEAD on `/`; once we observe 405 or 501 we sticky-switch to GET. |
| Probe path | **`/`** (configurable via library option, not via CLI in v1) | Most upstreams answer `/`. Library exposes `WithProxyProbePath` so embedders with stricter upstreams can target a known liveness path. CLI flag is held back to keep the surface minimal until there is concrete demand. |
| Probe request shape | Synthetic `*http.Request` constructed with the configured upstream URL, fed through `Proxy.RoundTrip` | Honors the "single state machine" property — the probe takes the exact same code path as a real client request. TLS, sanitizer, fallback, L1/L2 writes all behave identically. |
| SSE back-pressure | **Bounded per-subscriber buffer (size 8) + drop-and-disconnect on overflow** | Keeps the broadcast loop O(N) and lock-free per send. A subscriber whose buffer overflows is removed and its connection is closed (client sees EOF and reconnects via `EventSource` auto-retry). Initial-on-connect event re-seeds state on reconnect, so dropped intermediate events are recoverable from the application's point of view. Documented as a guarantee of the API. |
| JSON field names | `state`, `upstream_url`, `last_probed_at`, `probe_interval_ms`, `since` (RFC 3339 timestamp of the last *transition* into the current state) | Snake_case to match common JSON conventions and the issue's own suggestion. `since` is a small addition (PM listed only the first four as "at least"); it costs nothing and lets a UI render "in state X for Y seconds" without storing local timestamps. |
| SSE event name | **Default (`message`) event, no `event:` line** | EventSource clients receive the same payload either way, but defaulting keeps the payload small (no `event:` line per emit) and avoids a needless tag a frontend would have to listen for explicitly. The architectural value of the named event would be event multiplexing on the same stream — out of scope. |

##### New types

All in a single new file `health.go`:

```go
// SourceState identifies which tier produced the most recent serve through
// the proxy. Values mirror the X-Httptape-Source response header semantics
// established in ADR-26.
type SourceState string

const (
    StateLive    SourceState = "live"
    StateL1Cache SourceState = "l1-cache"
    StateL2Cache SourceState = "l2-cache"
)

// HealthSnapshot is the JSON payload returned by GET /__httptape/health and
// emitted on every SSE event on /__httptape/health/stream. It is the single
// JSON shape all clients see — snapshot and stream agree byte-for-byte at any
// instant.
type HealthSnapshot struct {
    State           SourceState `json:"state"`
    UpstreamURL     string      `json:"upstream_url"`
    LastProbedAt    *time.Time  `json:"last_probed_at,omitempty"` // nil until the first probe completes (or when probing is disabled)
    ProbeIntervalMS int64       `json:"probe_interval_ms"`        // 0 when probing is disabled
    Since           time.Time   `json:"since"`                    // when the proxy last transitioned into the current state
}

// HealthMonitor owns the proxy's "most-recently-served source" state, the SSE
// subscriber set, and the active probe loop. It is created by the Proxy when
// WithProxyHealthEndpoint is enabled. With the option absent, the Proxy holds
// a nil *HealthMonitor and the request path takes a fast no-op branch — this
// preserves byte-for-byte default behavior.
//
// HealthMonitor is safe for concurrent use.
type HealthMonitor struct {
    upstreamURL   string
    interval      time.Duration       // 0 = no probe loop
    probePath     string
    probeMethod   string              // dynamically promoted from HEAD to GET on 405/501
    probeMethodMu sync.Mutex          // guards the HEAD->GET promotion only
    transport     http.RoundTripper   // used by the probe (= the Proxy itself, so probes hit the same resolution path)
    onError       func(error)
    now           func() time.Time    // injectable for tests

    // state machine + subscriber set
    mu          sync.Mutex
    state       SourceState
    since       time.Time
    lastProbed  *time.Time
    subscribers map[*healthSubscriber]struct{}

    // lifecycle
    closeOnce sync.Once
    done      chan struct{} // closed when Close() is called; signals probe loop + handlers to exit
    wg        sync.WaitGroup // tracks the probe loop goroutine
}

// HealthMonitorOption configures a HealthMonitor.
type HealthMonitorOption func(*HealthMonitor)

// healthSubscriber is one connected SSE client. The buffer is bounded; if a
// send would block (buffer full) the broadcast routine drops the subscriber,
// closes the buffer, and the handler goroutine exits and writes EOF to the
// underlying connection.
type healthSubscriber struct {
    ch chan HealthSnapshot // capacity = sseBufferSize (8)
}
```

Constants used internally (unexported):

```go
const (
    sseBufferSize           = 8                       // per-subscriber bounded buffer
    defaultProbeInterval    = 2 * time.Second
    defaultProbePath        = "/"
    defaultProbeMethod      = http.MethodHead
    healthEndpointPath      = "/__httptape/health"
    healthStreamPath        = "/__httptape/health/stream"
    sseRetryBackoffMS       = 2000                    // hint to client via "retry: " field on initial event
)
```

##### New `Proxy` options (in `proxy.go`)

```go
// WithProxyHealthEndpoint enables the health surface on this Proxy. When set,
// Proxy.HealthHandler() returns a non-nil http.Handler that serves
// /__httptape/health and /__httptape/health/stream. The probe loop is started
// the first time the Proxy is mounted (see Proxy.Start) — never at construction
// time, so embedders that build a Proxy without ever serving HTTP do not leak
// goroutines.
//
// With this option absent, Proxy.HealthHandler() returns nil and the request
// path takes a no-op branch when recording state — preserving byte-for-byte
// default behavior.
func WithProxyHealthEndpoint(opts ...HealthMonitorOption) ProxyOption

// WithProxyProbeInterval sets the active probe cadence. Zero disables the
// probe loop (the request path still updates state, but no synthetic probe
// runs). When WithProxyHealthEndpoint is set and this option is absent, the
// default is 2s.
//
// This option is a no-op unless WithProxyHealthEndpoint is also set.
func WithProxyProbeInterval(d time.Duration) ProxyOption

// WithProxyProbePath sets the URL path the active probe targets on the upstream
// (default "/"). No-op unless WithProxyHealthEndpoint is also set.
func WithProxyProbePath(path string) ProxyOption

// WithProxyHealthErrorHandler sets a callback for non-fatal errors inside the
// health surface (probe transport errors that are NOT a state transition,
// SSE write errors, etc.). Defaults to the existing onError callback set by
// WithProxyOnError; if neither is set, errors are swallowed. No-op unless
// WithProxyHealthEndpoint is also set.
func WithProxyHealthErrorHandler(fn func(error)) ProxyOption
```

Note that `WithProxyProbeInterval`, `WithProxyProbePath`, and
`WithProxyHealthErrorHandler` are top-level `ProxyOption`s rather than
`HealthMonitorOption`s passed through `WithProxyHealthEndpoint`. Rationale:
embedders configure the Proxy as one object; they should not have to import a
sub-options type for what feel like proxy-level knobs. The
`HealthMonitorOption` slot inside `WithProxyHealthEndpoint` exists for future
extension (e.g. injecting a clock for tests) without breaking the proxy
options surface.

##### New `Proxy` methods

```go
// HealthHandler returns the http.Handler that serves /__httptape/health and
// /__httptape/health/stream. Returns nil when WithProxyHealthEndpoint was not
// set — callers should mount the handler conditionally.
//
// The handler routes only the two health paths; any other path returns 404.
// Callers compose it into their own mux (see CLI wiring below).
func (p *Proxy) HealthHandler() http.Handler

// Start initializes background workers (currently: the active probe loop).
// Safe to call zero or more times; subsequent calls are no-ops. Must be called
// before serving HTTP if WithProxyHealthEndpoint and a non-zero probe interval
// are set, otherwise the probe loop never runs.
//
// The CLI wires Start() into the proxy command's startup. Library users
// embedding the Proxy choose when (and whether) to call it.
func (p *Proxy) Start()

// Close stops background workers and closes all open SSE subscribers. Safe to
// call zero or more times; idempotent. Returns when all goroutines spawned by
// the Proxy have exited (probe loop drained, broadcast goroutine exited if any).
//
// Close does NOT close the L1/L2 stores — they are owned by the caller.
func (p *Proxy) Close() error
```

`Proxy.Close` is new; ADR-26 did not give `Proxy` a Close method because there
were no background workers to drain. This is the first feature that needs one.
Idempotent + nil-safe so existing embedders that never call it (or call it
twice) are unaffected.

##### `HealthMonitor` methods (mostly unexported, used by `Proxy`)

```go
// NewHealthMonitor constructs a HealthMonitor. transport is the http.RoundTripper
// the active probe will hit — in production this is the Proxy itself, so the
// probe takes the same resolution path as real client traffic. upstreamURL is
// reported in the snapshot. opts may inject a clock or additional knobs.
//
// upstreamURL must be non-empty and transport must be non-nil.
func NewHealthMonitor(upstreamURL string, transport http.RoundTripper, opts ...HealthMonitorOption) *HealthMonitor

// observe is called by the Proxy on every served request (including probe-driven
// ones) with the source the response was served from. It is the only mutator
// of the state machine and the only emitter of SSE events.
//
// observe is a no-op when called on a nil *HealthMonitor — this is the fast
// path for proxies built without WithProxyHealthEndpoint.
func (h *HealthMonitor) observe(src SourceState)

// snapshot returns the current state as a HealthSnapshot value. Safe to call
// concurrently with observe and broadcast.
func (h *HealthMonitor) snapshot() HealthSnapshot

// subscribe registers a new SSE subscriber and returns its receive channel
// plus an unsubscribe func. The channel is closed when the subscriber is
// dropped (either by overflow or by Close).
func (h *HealthMonitor) subscribe() (<-chan HealthSnapshot, func())

// runProbe is the active probe loop. Started exactly once by Proxy.Start.
// Exits when h.done is closed.
func (h *HealthMonitor) runProbe(ctx context.Context)

// ServeHTTP routes /__httptape/health and /__httptape/health/stream; returns
// 404 for any other path. HealthMonitor implements http.Handler so it can be
// mounted directly.
func (h *HealthMonitor) ServeHTTP(w http.ResponseWriter, r *http.Request)

// Close drains the probe loop, closes all subscriber channels, and is
// idempotent.
func (h *HealthMonitor) Close() error
```

Functional option example:

```go
// WithHealthClock injects a clock for tests. Defaults to time.Now.
func WithHealthClock(now func() time.Time) HealthMonitorOption
```

##### File layout

```
httptape/
  health.go              # NEW. SourceState, HealthSnapshot, HealthMonitor,
                         # HealthMonitorOption, NewHealthMonitor, observe,
                         # snapshot, subscribe, runProbe, ServeHTTP, Close,
                         # all unexported helpers and the package-private
                         # constants listed above.
  health_test.go         # NEW. Unit tests (see Test strategy below).
  proxy.go               # MODIFIED. Add Proxy.health *HealthMonitor field,
                         # WithProxyHealthEndpoint / WithProxyProbeInterval /
                         # WithProxyProbePath / WithProxyHealthErrorHandler
                         # options, HealthHandler() / Start() / Close() methods,
                         # and call p.health.observe(src) in RoundTrip /
                         # fallback. observe on a nil receiver is a no-op so
                         # the existing happy paths are unchanged.
  proxy_test.go          # MODIFIED. Add a single "with both flags at default,
                         # no goroutines started, no endpoints mounted" guard
                         # test to lock the backward-compat invariant.
  cmd/httptape/main.go   # MODIFIED. Add --health-endpoint and
                         # --upstream-probe-interval flags to runProxy. Build
                         # ProxyOptions accordingly. If --health-endpoint is
                         # set, mux the health handler under /__httptape/ and
                         # forward everything else to the existing reverse
                         # proxy. Call tapeProxy.Start() and tapeProxy.Close().
  cmd/httptape/main_test.go  # MODIFIED. Smoke test that --health-endpoint
                         # changes the listener surface as expected.
  doc.go                 # MODIFIED. Add a short "Health endpoints" subsection
                         # documenting the surface.
  README.md              # MODIFIED. Add a brief proxy-mode health subsection
                         # mirroring the godoc.
```

No new top-level files beyond `health.go` / `health_test.go`. No `internal/`.
No new dependencies — `net/http` + `http.Flusher` + `encoding/json` +
`time` + `sync` + `context` are all stdlib and already in use.

##### State machine and synchronization

State is a tuple `(state SourceState, since time.Time, lastProbed *time.Time)`
held inside `HealthMonitor` and guarded by a single `sync.Mutex` (`h.mu`).

Single mutator: `HealthMonitor.observe(src)`. Two readers: `snapshot()` (called
by the JSON endpoint and also from inside `observe` to build the SSE event
payload) and `subscribe()` (called by the SSE endpoint).

Why a `sync.Mutex` and not `sync.RWMutex` or `atomic.Pointer`:

- Critical section is tiny (3 string/time field updates + a non-blocking send
  per subscriber).
- Writes are infrequent (one per served request + one per probe tick), reads
  are also infrequent (one per snapshot HTTP call + one per subscribe).
- An `RWMutex` adds memory and complexity for no measurable benefit at this
  request rate.
- `atomic.Pointer[snapshot]` would force a copy-on-write per emit and a
  separate sync mechanism for the subscriber set anyway.

`observe(src)` algorithm:

```
1. Compute the next snapshot: state, since, lastProbed.
   - state = src
   - if src != h.state: since = h.now() (transition); else since = h.since (no-op)
2. Acquire h.mu.
3. Detect transition (src != h.state).
4. Update h.state, h.since.
5. If this call is from the probe loop (signaled via a separate `observeProbe`
   variant), also update h.lastProbed = &now.
6. If a transition occurred, snapshot the subscriber set into a local slice
   (cheap; pointers only).
7. Release h.mu.
8. For each subscriber pointer, attempt a non-blocking send of the snapshot:
       select {
       case sub.ch <- snap:
       default:
           // Buffer full: drop and disconnect.
           h.dropSubscriber(sub)
       }
9. If no transition occurred, skip step 6–8 entirely. (Acceptance criterion:
   no event on confirmation of existing state.)
```

A separate small entrypoint `observeProbe(src)` exists so the request path
(`Proxy.RoundTrip`/`fallback`) can call `observe(src)` without bumping
`lastProbed` (only the probe should update that field), while the probe loop
calls `observeProbe(src)`. Both share the same locking and broadcast logic.

`subscribe()` algorithm:

```
1. ch := make(chan HealthSnapshot, sseBufferSize)
2. sub := &healthSubscriber{ch: ch}
3. Acquire h.mu.
4. Capture current snapshot for initial seed.
5. Add sub to h.subscribers.
6. Release h.mu.
7. Send the initial snapshot non-blocking. (Buffer is fresh and capacity 8 so
   this never blocks — but we still use select/default for paranoia.)
8. Return ch and an unsubscribe closure.
```

`dropSubscriber(sub)` algorithm:

```
1. Acquire h.mu.
2. If sub not in h.subscribers, release lock and return.
3. delete(h.subscribers, sub).
4. close(sub.ch).
5. Release h.mu.
```

Closing the channel from the broadcaster (rather than from the SSE handler)
gives a single owner of the close-channel operation, removing the
"close-of-closed-channel" race entirely. The SSE handler treats `ch` as
read-only and exits when it sees the channel closed.

##### SSE multiplex / back-pressure design

Per-subscriber bounded buffer of size 8, drop-and-disconnect on overflow.

Why 8:

- SSE state events are tiny (~150 bytes JSON). 8 events is < 2 KB of memory
  per subscriber.
- The fastest plausible event rate is one transition per probe interval (~2s).
  A subscriber that cannot drain 8 events in 16+ seconds is effectively dead;
  disconnecting it and letting `EventSource` reconnect is the right answer.

Why drop-and-disconnect over drop-event-only:

- Drop-event-only would let a slow client persistently miss state changes,
  giving them a stale UI badge with no recovery signal.
- Disconnect forces a client reconnect; the new connection's
  initial-on-connect event re-seeds the correct state, so no information is
  lost from the application's perspective.

Why drop-and-disconnect over disconnect-only (no buffer):

- With buffer = 0, the broadcaster would have to do `select { case ch <- snap;
  default: drop }` *and* hold the mutex during the send, since concurrent
  observers could otherwise race to a partial subscriber list. The bounded
  buffer absorbs short pauses (e.g. SSE handler is in the middle of a
  `Flush()` call) and keeps the mutex critical section short.

The SSE handler goroutine logic:

```
1. Verify request is GET. Otherwise 405.
2. Set headers:
     Content-Type: text/event-stream
     Cache-Control: no-cache
     Connection: keep-alive
     X-Accel-Buffering: no   (defeats nginx buffering; harmless without nginx)
3. Type-assert the ResponseWriter to http.Flusher; if it doesn't implement
   Flusher, return 500. (Modern net/http does; httptest does. Defensive.)
4. ch, unsub := h.subscribe()
   defer unsub()
5. Write a "retry: 2000\n\n" line so EventSource clients reconnect quickly.
   Flush.
6. Loop:
     select {
     case <-r.Context().Done():
         // client disconnected
         return
     case <-h.done:
         // proxy shutting down
         return
     case snap, ok := <-ch:
         if !ok {
             // dropped by broadcaster (overflow or Close)
             return
         }
         payload, _ := json.Marshal(snap)
         fmt.Fprintf(w, "data: %s\n\n", payload)
         flusher.Flush()
     }
```

Nothing in this loop holds `h.mu`. The broadcaster holds `h.mu` only to
snapshot the subscriber pointer list, not for the actual sends. Slow flushes
on one subscriber cannot block other subscribers or the request path.

##### Probe lifecycle

Started by `Proxy.Start()`. Stopped by `Proxy.Close()` via `h.done` channel
closure.

```go
func (h *HealthMonitor) runProbe(ctx context.Context) {
    if h.interval <= 0 {
        return
    }
    defer h.wg.Done()

    ticker := time.NewTicker(h.interval)
    defer ticker.Stop()

    for {
        select {
        case <-h.done:
            return
        case <-ticker.C:
            h.runProbeOnce(ctx)
        }
    }
}
```

`runProbeOnce` builds a synthetic `*http.Request` against
`upstreamURL + probePath`, runs it through `h.transport.RoundTrip` (which is
the Proxy), interprets the result, and calls `h.observeProbe(src)`:

- HEAD response with status < 500 and no `X-Httptape-Source` header → `live`.
- Any response with `X-Httptape-Source: l1-cache` → `l1-cache`.
- Any response with `X-Httptape-Source: l2-cache` → `l2-cache`.
- HEAD response with status 405 or 501 → promote `h.probeMethod` to GET (under
  `h.probeMethodMu`), do not call `observeProbe` for this tick (the upstream
  is up but rejected our method — this is not a tier signal).
- Transport error AND response is nil → do not call `observeProbe`. The
  Proxy's own fallback already handled state via `observe` if the request
  produced a tier hit. If the Proxy returned `nil, err` (no cache match), the
  state stays unchanged — which is the correct behavior per definition B
  ("if neither cache matched, state remains whatever it was").
- Always update `h.lastProbed = now()` regardless of outcome (the snapshot
  field reports "did the probe at least *run*"), via a tiny separate call:
  `h.recordProbeAttempt()`.

The probe **uses a context derived from `h.done`**, not the long-lived
`context.Background()` of the Proxy, so a Close immediately cancels any
in-flight probe RoundTrip.

Probe request shape:

```go
req, _ := http.NewRequestWithContext(ctx, h.probeMethod, h.upstreamURL+h.probePath, nil)
req.Header.Set("X-Httptape-Probe", "1")  // diagnostic, not used for routing
```

The `X-Httptape-Probe` header lets operators see probe traffic in upstream
access logs and lets future versions filter probes out of recording (out of
scope here — the probe is recorded normally so it actually feeds L1/L2,
keeping caches warm).

##### Proxy integration

`Proxy.RoundTrip` and `Proxy.fallback` already know which tier they served
from. They each gain a single one-line call:

- Top of the success branch (after the upstream call returns and we are about
  to record): `p.health.observe(StateLive)`.
- Inside `tapeToResponse`, just before returning to the caller: pass the
  source string in and call `p.health.observe(src)` (where `src` is `l1-cache`
  or `l2-cache` mapped to the const).
- If both fallbacks miss and the original error is returned: do **not** call
  `observe`. State stays as-is. This matches the PM's locked-in rule.

`p.health.observe(src)` is a no-op when `p.health == nil`, so when
`WithProxyHealthEndpoint` is not set, this is a single nil-receiver method
call per request — effectively free, and behavior is byte-for-byte identical.

##### CLI flag wiring

In `cmd/httptape/main.go`'s `runProxy`:

```go
healthEndpoint := fs.Bool("health-endpoint", false,
    "Mount /__httptape/health (JSON snapshot) and /__httptape/health/stream (SSE).")
upstreamProbeInterval := fs.Duration("upstream-probe-interval", 0,
    "Active upstream probe cadence. 0 = disabled. When --health-endpoint is set "+
    "and this is unset, defaults to 2s.")
```

After parsing:

```go
if *healthEndpoint {
    proxyOpts = append(proxyOpts, httptape.WithProxyHealthEndpoint())
    interval := *upstreamProbeInterval
    if interval == 0 {
        interval = 2 * time.Second
    }
    proxyOpts = append(proxyOpts, httptape.WithProxyProbeInterval(interval))
} else if *upstreamProbeInterval > 0 {
    return &usageError{fmt.Errorf("--upstream-probe-interval requires --health-endpoint")}
}
```

Routing the listener:

```go
var handler http.Handler = rp
if hh := tapeProxy.HealthHandler(); hh != nil {
    mux := http.NewServeMux()
    mux.Handle("/__httptape/", hh)
    mux.Handle("/", rp)
    handler = mux
}
// CORS wrapper (existing) goes around `handler` after this.
```

The path prefix `/__httptape/` is reserved for httptape's technical surface
and will never be forwarded upstream. (The current proxy has no concept of a
reserved prefix; this is established by this ADR.)

Lifecycle in `runProxy`:

```go
tapeProxy.Start()                 // no-op if no probe configured
defer tapeProxy.Close()           // belt-and-braces; the signal handler also calls it

go func() {
    <-ctx.Done()
    // existing httpServer.Shutdown(...) block
    if err := tapeProxy.Close(); err != nil {
        logger.Printf("proxy close error: %v", err)
    }
}()
```

##### Sequence diagram (text): upstream goes down → probe detects → SSE fires → frontend re-fetches

```
t=0       Frontend connects:  GET /__httptape/health/stream
            -> SSE handler subscribes, sends initial event {state:"live"}
            -> Frontend renders "LIVE" badge.

t=2s      Probe tick #1:
            HealthMonitor.runProbeOnce()
              -> HEAD upstream/  -> 200 OK, no X-Httptape-Source
              -> observeProbe(StateLive)
                   no transition (state already "live") => no SSE event
              -> recordProbeAttempt() updates lastProbed.

t=3s      Upstream goes down (network partition).

t=4s      Probe tick #2:
            HEAD upstream/  -> transport error
            Proxy.fallback runs through L1: hit
              -> tapeToResponse(tape, "l1-cache") returns 200 with
                 X-Httptape-Source: l1-cache
              -> Proxy calls p.health.observe(StateL1Cache)
                   transition live -> l1-cache
                   broadcast: send {state:"l1-cache",since:t=4s,...} to
                   every subscriber's bounded channel.
            runProbeOnce inspects the response, sees l1-cache header, does
            NOT call observeProbe (already observed by the success/fallback
            path); only recordProbeAttempt().

t=4s+ms   SSE handler reads from its channel, writes
            "data: {\"state\":\"l1-cache\",...}\n\n" + flush.
          Frontend's EventSource onmessage fires.
          Frontend re-fetches data, swaps badge to "L1 CACHE".

t=10s     Upstream recovers.

t=12s     Probe tick #5:
            HEAD upstream/  -> 200 OK, no X-Httptape-Source
            Proxy success path: writes L1+L2, calls
              p.health.observe(StateLive)
                transition l1-cache -> live, broadcast.
          SSE handler emits {state:"live",since:t=12s,...}.
          Frontend re-fetches, badge back to "LIVE".

t=...     User hits Ctrl-C. SIGTERM -> ctx cancel.
            Proxy.Close():
              close(h.done)
              probe goroutine's select picks <-h.done, returns; wg.Wait drains.
              broadcaster snapshot of subscribers + close(ch) for each.
              SSE handlers' select picks closed channel, return; HTTP write
              loop unwinds; net/http closes the underlying TCP connection.
              Frontend EventSource sees onclose; auto-retry (per "retry: 2000")
              eventually fails when the listener is gone — frontend handles.
```

##### Error cases

| Where | Symptom | Handling |
|---|---|---|
| `NewHealthMonitor` called with empty upstream URL | Programming error | Panic with `httptape: NewHealthMonitor requires a non-empty upstream URL` (constructor guard, per L-11). |
| `NewHealthMonitor` called with nil transport | Programming error | Panic with `httptape: NewHealthMonitor requires a non-nil transport`. |
| Probe HEAD returns 405/501 | Upstream does not support HEAD on the probe path | Sticky-promote `h.probeMethod` to GET under `h.probeMethodMu`. Skip `observeProbe` for this tick (no tier signal). Subsequent ticks use GET. |
| Probe transport error AND no cached fallback | `Proxy.RoundTrip` returned `nil, err` | `runProbeOnce` swallows the error (passes it to `h.onError` if set) and does NOT call `observeProbe`. State stays at its previous value, matching the locked-in rule "if neither cache matched, state remains whatever it was before that failed serve." |
| Probe transport error WITH cached fallback | Cache hit happened inside the proxy | `Proxy.RoundTrip` already called `observe(StateL1Cache)` or `observe(StateL2Cache)` synchronously. `runProbeOnce` sees the response, does not double-emit. |
| SSE handler called with non-`http.Flusher` writer | Defensive — net/http always satisfies Flusher | Return 500 with body `httptape: streaming not supported`. |
| SSE subscriber buffer full | Slow / dead client | Drop subscriber: `close(sub.ch)`, remove from set. Handler goroutine sees closed channel, returns, net/http closes the connection. Client `EventSource` auto-reconnects and re-seeds via initial event. |
| Health endpoint receives unsupported method (e.g. POST) | Misuse | Return 405 with `Allow: GET`. |
| Health endpoint receives unknown subpath | Misuse | Return 404. The mux only routes `/__httptape/health` and `/__httptape/health/stream`. |
| `Proxy.Close` called twice | Idempotency | `closeOnce.Do(...)` ensures workers are drained exactly once. Returns `nil` on subsequent calls. |
| `Proxy.Start` called twice | Idempotency | `startOnce.Do(...)` ensures the probe goroutine is launched at most once. |
| JSON marshal of `HealthSnapshot` fails | Cannot happen for these field types | Errors-by-default-discarded; the marshal call is paired with a `_ = err` and a comment explaining the unreachable branch. (No panic, no log spam.) |
| `json.NewEncoder(w).Encode(snapshot)` on the snapshot endpoint | Network-side error | Errors are passed to `h.onError` if set. The HTTP response is already partially written; nothing actionable beyond logging. |

##### Test strategy

All in `health_test.go` unless noted. Stdlib `testing` only.
Race-clean (`go test -race ./...`). Coverage target ≥ 90% for `health.go`.

| Test | Pattern | What it verifies |
|---|---|---|
| `TestHealthMonitor_InitialState` | direct | New monitor reports `state=live`, `since` ≈ now, `lastProbed=nil`. |
| `TestHealthMonitor_ObserveTransition` | table-driven | Sequence of `observe` calls produces correct state machine transitions and updates `since` only on transitions. |
| `TestHealthMonitor_ObserveNoEventOnSameState` | direct | Two consecutive `observe(StateLive)` calls produce exactly one initial SSE event (from subscribe), no follow-up events. |
| `TestHealthMonitor_BroadcastFanOut` | direct | 100 concurrent subscribers all receive one transition event. |
| `TestHealthMonitor_SlowSubscriberDropped` | direct | Subscriber that never drains its buffer is dropped after `sseBufferSize+1` transitions; channel is closed; other subscribers are unaffected. |
| `TestHealthMonitor_SubscribeReceivesInitial` | direct | Newly subscribed client immediately receives the current snapshot. |
| `TestHealthMonitor_CloseDrainsSubscribers` | direct | After `Close()`, all subscriber channels are closed and `subscribe` returns. |
| `TestHealthMonitor_CloseStopsProbe` | direct, with a fake transport that records call count | After `Close()`, no further probe RoundTrips occur within 3× the interval. |
| `TestHealthMonitor_ProbeHEADtoGETPromotion` | direct, with a fake transport returning 405 once | After a 405, subsequent probe ticks use GET; `probeMethod` is sticky. |
| `TestHealthMonitor_ProbeFedThroughProxy` | integration-ish, in-memory | A fake upstream first succeeds, then errors; L1 cache is primed; probe drives state from `live` to `l1-cache` and back. SSE subscriber receives both transition events without any real client request between transitions. |
| `TestHealthMonitor_SnapshotJSONShape` | direct | JSON marshals to exactly the documented field set; `last_probed_at` is omitted when nil; `probe_interval_ms` is 0 when probing disabled. |
| `TestHealthMonitor_HTTPSnapshotEndpoint` | `httptest` | `GET /__httptape/health` returns 200, `Content-Type: application/json`, body matches snapshot. |
| `TestHealthMonitor_HTTPStreamEndpoint` | `httptest` + `http.Client` reading body line by line | Stream emits an initial `data:` line within 100ms of subscribe and one more `data:` line per `observe` transition. Test waits with timeouts, never `time.Sleep` of fixed duration alone. |
| `TestHealthMonitor_HTTPStreamGracefulShutdown` | `httptest` | After `Close()`, the stream's body reader sees EOF (not a hung read). |
| `TestHealthMonitor_HTTPMethodNotAllowed` | `httptest` | POST to either endpoint returns 405 with `Allow: GET`. |
| `TestProxy_HealthDisabledByDefault` (in `proxy_test.go`) | direct | `NewProxy(...)` without `WithProxyHealthEndpoint` returns a proxy whose `HealthHandler()` is nil. No goroutines started by `Start()`. |
| `TestProxy_HealthHeaderUnchanged` (in `proxy_test.go`) | direct | With `WithProxyHealthEndpoint` set, `X-Httptape-Source` is still emitted on cache fallbacks. |
| `TestProxy_StartCloseIdempotent` (in `proxy_test.go`) | direct | `Start()` and `Close()` can each be called twice with no panic and no double-spawn. |
| `TestCLI_HealthFlags` (in `cmd/httptape/main_test.go`) | smoke | `--health-endpoint` mounts the endpoint; `--upstream-probe-interval` without `--health-endpoint` returns a usage error. |

A goroutine leak check helper (`goleakish`: count goroutines before/after each
test that involves Close, asserting count returns to baseline) is added at the
top of `health_test.go`. We avoid the `go.uber.org/goleak` dependency — a
30-line stdlib helper using `runtime.NumGoroutine` with retry/backoff is
sufficient for our needs.

##### Backward compatibility verification

The compatibility guarantee is enforced by:

1. **`p.health == nil` fast path**: every new call site
   (`p.health.observe(...)`) is on a nil-receiver-safe method. With the option
   absent, the only added cost is one nil-receiver method call per request,
   which the Go compiler often inlines away entirely.
2. **Test `TestProxy_HealthDisabledByDefault`** (above): explicitly asserts
   `HealthHandler() == nil` and that no goroutine starts.
3. **CLI test**: ensures the proxy mux only diverges from today when the flag
   is set.
4. **Header preservation test**: `X-Httptape-Source` is still written by
   `tapeToResponse`; this is unchanged.

#### Consequences

##### Positive

- Embedders gain a real-time state surface usable from any frontend that
  speaks SSE (`EventSource` is in every browser).
- The "single source of truth fed by both real and probe traffic" property
  means the snapshot endpoint and the SSE stream cannot drift — they read the
  same field protected by the same mutex.
- Probe goes through `Proxy.RoundTrip`, so it benefits from every existing
  proxy feature (TLS, sanitizer, fallback, L1/L2 writes) for free, and
  warming the caches as a side effect is actually a feature, not a bug.
- The default-off design makes this a zero-risk change for users not opting
  in. Existing tests, fixtures, and integration setups are unaffected.
- New `Proxy.Close` and `Proxy.Start` lifecycle methods give us a clean place
  to hang future background workers (e.g. periodic L2 compaction) without
  another API breakage.
- stdlib only. No new dependencies.

##### Negative

- `Proxy` grows new lifecycle methods (`Start`, `Close`). Any embedder that
  builds a `Proxy` and never serves it is fine (Start is a no-op, Close on a
  never-started monitor is a no-op), but anyone using the new options is now
  expected to pair them with `Close()` for clean shutdown. This is documented
  in godoc.
- The bounded-buffer-then-disconnect strategy means a wedged subscriber will
  miss intermediate events between disconnect and reconnect. We mitigate by
  emitting the initial snapshot on every (re)connect, so application-visible
  state is always correct on resume. This is a deliberate trade-off and is
  documented in godoc on the SSE endpoint.
- The probe records into L1/L2. For very low-traffic backends this means the
  cache contains many probe responses. Acceptable: probe responses are
  legitimate cache entries (HEAD/GET on `/`). If this becomes a problem in
  practice, a future ADR can add `WithProxyProbeRecord(false)` to skip
  recording probe traffic.
- Adds a `/__httptape/` reserved path prefix to the proxy mode. Callers who
  happened to be routing real upstream traffic on a path beginning with
  `/__httptape/` (extremely unlikely) would see a behavior change — but only
  when they opt in via `--health-endpoint`. With the flag off, the prefix is
  not reserved.

##### Risks

- Goroutine leak on `Close` if the probe loop or an SSE handler is stuck.
  Mitigated by deriving all blocking operations from contexts/channels we
  explicitly close, and by the dedicated `TestHealthMonitor_CloseStopsProbe`
  and goroutine-count check.
- SSE handler holding the mutex during a slow Flush. Mitigated by design:
  the handler holds no `HealthMonitor` mutex during Flush; it only reads from
  its own channel.
- Panic in user-supplied `onError` propagating into the probe loop. Mitigated
  by wrapping the callback in `defer recover()` and swallowing the panic with
  a log line on stderr (matches the existing pattern in
  `recorder.runDispatcher`).

##### Migration

- No migration needed for existing users. Defaults preserve current behavior.
- Embedders who want the new surface add `httptape.WithProxyHealthEndpoint()`
  (and optionally `WithProxyProbeInterval(...)`) to their `NewProxy` call
  site, and call `Start()` / `Close()` around the lifetime of their HTTP
  server.

---

### ADR-29: Repository polish — community-health files, CI badge, OpenSSF Scorecard

**Date**: 2026-04-16
**Issue**: #129
**Status**: Accepted

#### Context

The httptape repo on GitHub currently scores 50% on `gh api
repos/VibeWarden/httptape/community/profile`: missing `CODE_OF_CONDUCT`,
`CONTRIBUTING`, `SECURITY`, issue templates, and PR template. There is no CI
status badge in the README, and no OpenSSF Scorecard automation. Each gap is
small in isolation; together they make the project look unmaintained to
enterprise reviewers, OSS-list curators, and first-time contributors.

Issue #129 bundles eight related polish deliverables:

1. `SECURITY.md`
2. `CONTRIBUTING.md`
3. `CODE_OF_CONDUCT.md` (Contributor Covenant v2.1, verbatim)
4. `.github/ISSUE_TEMPLATE/bug.yml`
5. `.github/ISSUE_TEMPLATE/feature.yml`
6. `.github/ISSUE_TEMPLATE/config.yml`
7. `.github/PULL_REQUEST_TEMPLATE.md`
8. README CI badge + OpenSSF Scorecard workflow + Scorecard badge

Pre-flight reality check confirmed by the architect:

- `release.yml` runs tests only on tag push; `docker.yml` runs no tests;
  `docs.yml` is docs-only. **No workflow runs `go test ./... -race` on every
  push and PR**, so a CI badge has nothing to point at. A new
  `.github/workflows/test.yml` is therefore part of this issue.
- `gh repo edit` (v2.88.1, current) does **not** expose a
  `--enable-private-vulnerability-reporting` flag (verified via
  `gh repo edit --help`).
- The REST endpoint `PUT /repos/{owner}/{repo}/private-vulnerability-reporting`
  works (architect probed it: `gh api -X PUT
  /repos/VibeWarden/httptape/private-vulnerability-reporting` returned `204
  No Content`). A `GET` on the same endpoint currently returns
  `{"enabled":true}` — PVR is **already enabled** on the repo. This means
  `SECURITY.md` can rely on PVR as the primary reporting channel today, and
  the dev needs no extra runbook step to enable it; we just record the
  REST-API one-liner in `SECURITY.md`'s "How to verify / re-enable" sub-note
  for future reference.

This ADR pins down: file contents skeletons, the `test.yml` workflow shape,
the Scorecard workflow shape (publish on, badge clickable), the README badge
cluster after the change, the chosen reporting mechanism in `SECURITY.md`,
and the PR strategy.

#### Decision

##### Resolution of open questions

| Question | Decision | Rationale |
|---|---|---|
| PR shape (single vs. split) | **Single PR** | All eight deliverables are small, related, and the review surface is mostly templates. PM recommendation; no concrete reason to split. Lets the dev land everything atomically and lets the reviewer verify the community-profile checklist hits 100% in one pass. |
| Reporting mechanism in `SECURITY.md` | **GitHub Private Vulnerability Reporting (PVR)**, primary; `tibtof@gmail.com` as private fallback | PVR is already enabled on the repo (verified). PVR keeps the entire intake on GitHub with built-in advisory drafting and CVE workflow. The email fallback covers reporters who can't or won't use a GitHub account. |
| PVR enable mechanism documented in dev runbook | **`gh api -X PUT /repos/VibeWarden/httptape/private-vulnerability-reporting`** (one-liner, idempotent — re-running on an already-enabled repo is a no-op `204`). The PR description should call this out as informational only since PVR is already on. | `gh repo edit` does not have a `--enable-private-vulnerability-reporting` flag in v2.88.1. The REST endpoint is the supported path. UI fallback (Settings -> Code security -> Private vulnerability reporting -> Enable) documented as the manual alternative for anyone without API token scope. |
| Scorecard publishing | **Publish (`publish_results: true`, `id-token: write`)** | The badge is the deliverable; an unpublished Scorecard run gives a non-clickable badge that points nowhere meaningful. Public repo, Apache 2.0, nothing sensitive in the SARIF — publishing is the standard OSS posture and unlocks `https://scorecard.dev/viewer/?uri=github.com/VibeWarden/httptape` as the badge link. |
| CI badge label | **"Tests"** | More descriptive than bare "CI" (which doesn't indicate what's being tested) and matches the workflow's actual purpose (`go test ./... -race`). Also visually distinguishes from the future possibility of a separate "Lint" or "Build" badge. |
| `test.yml` matrix (Go versions) | **Single version: `go-version-file: go.mod` (= 1.26)** | Matches the existing `release.yml` pattern. Library targets a single Go version per `go.mod`; we are not yet supporting a published version range. Adding a matrix is a separate future issue if we ever ship a `go.mod` with a wider compatibility window. |
| `test.yml` matrix (OS) | **Single OS: `ubuntu-latest`** | Matches `release.yml`. httptape uses only stdlib networking primitives; OS-specific bugs are unlikely. Cross-OS matrix is a separate future issue. |
| Scorecard cron cadence | **Weekly, Saturday 01:30 UTC** (`30 1 * * 6`) | Matches the canonical `ossf/scorecard` repo's own scheduled cadence — proven, off-peak, unlikely to collide with weekday CI volume. |
| Scorecard action pinning | **Pin by full commit SHA + version comment**, matching the canonical `ossf/scorecard` workflow exactly | Scorecard's own checks penalise float-tag (`@v2`) usage. Use the canonical pinned SHAs documented below. |
| Code-scanning SARIF upload step | **Include** (the third step in the canonical example, `github/codeql-action/upload-sarif`) | Adds findings to the repo's Code Scanning dashboard, surfaces failures next to other security alerts. Cost is one extra step per weekly run. |

##### File inventory and content skeletons

The dev writes the full file contents. The skeletons below are normative for
**structure, headings, key phrasing, and links** — the dev fleshes out the
prose but must keep the structure intact. All files are ASCII, no decorative
emoji, hyphens-not-em-dashes (matches the rest of the repo).

###### `SECURITY.md` (repo root)

```markdown
# Security Policy

## Supported versions

httptape is pre-release. The only supported version is the current `main`
branch. Once v0.9.0 ships, this table will be updated to list supported
released versions explicitly.

| Version | Supported          |
|---------|--------------------|
| `main`  | Yes (best-effort)  |
| `v0.x`  | Not yet released   |

## Reporting a vulnerability

**Preferred**: open a GitHub Private Vulnerability Report at
<https://github.com/VibeWarden/httptape/security/advisories/new>.
This keeps the report private until a fix is ready and lets us coordinate a
CVE if applicable.

**Alternative**: email `tibtof@gmail.com` with `[httptape security]` in the
subject line. Please do not open a public issue for security reports.

## Response time (best-effort SLA)

- Acknowledgement: within 7 days
- Initial assessment: within 14 days
- Coordinated disclosure window: typically 90 days from acknowledgement,
  shorter for actively exploited issues, longer if a coordinated upstream
  fix is required

These are best-effort targets, not contractual commitments. httptape is
maintained by a small team.

## Scope: what counts as a security issue in httptape

- **Sanitizer bypass**: any input pattern where a configured redaction or
  faker rule fails to redact data that the rule was meant to cover, causing
  sensitive data to be written to a tape on disk
- **Path traversal in storage**: a tape ID or filename input that causes the
  filesystem store to read or write outside the configured store directory
- **Replay leak**: the mock server returning data from a tape that was
  supposed to be redacted (e.g. recorded after the rule was added but the
  redaction did not apply, or a header rule that did not strip a configured
  header before serve)
- **TLS / certificate handling**: incorrect verification, leak of private
  keys, or downgrade in record/proxy modes
- **Faker collisions**: deterministic-faker output that maps two distinct
  real values to the same fake (which would let a reader correlate a fake
  back to a real value via a known-plaintext pair)

## Out of scope

- Bugs in upstream HTTP services that httptape records — those are the
  upstream's problem
- Denial-of-service via oversized tapes when the user has explicitly opted
  to record them (bound the input on your side)
- Crashes or hangs that require an attacker to control the embedder's Go
  code — that's a code-execution problem, not an httptape problem
- Vulnerabilities in third-party CI Actions used in this repo's workflows —
  report those upstream

## Acknowledgement

We credit reporters in the published advisory unless asked otherwise.
```

Note for the dev: the GitHub Private Vulnerability Reporting setting is
already enabled on the repo (verified by the architect). If it ever needs to
be re-enabled, the one-liner is:

```sh
gh api -X PUT /repos/VibeWarden/httptape/private-vulnerability-reporting
```

The manual UI path is: repo Settings -> Code security -> "Private
vulnerability reporting" -> Enable. **Do not include this verification note
in the published `SECURITY.md`** — it goes in the PR description as a
self-check only.

###### `CONTRIBUTING.md` (repo root)

```markdown
# Contributing to httptape

Thanks for your interest in contributing. This document describes how this
repository is actually maintained today, not how a generic OSS project
might be.

## How issues become PRs in this repo

httptape is built using a four-stage agent pipeline backed by a human
reviewer. The flow is:

1. **PM agent** drafts the issue spec (problem, scope, acceptance criteria,
   non-goals).
2. **Architect agent** reads the spec, makes design decisions, and appends an
   ADR to `decisions.md`.
3. **Dev agent** implements the design exactly as specified.
4. **Reviewer agent** reviews the PR for spec compliance, test coverage, and
   adherence to project rules.
5. The repo owner reviews and merges.

The full pipeline rules live in `CLAUDE.md`. External contributors do not
need to use the agent pipeline — opening a clear issue and a focused PR is
fine. The same coding rules apply either way.

## Hard rules (from `CLAUDE.md`)

- **stdlib only**: no new Go-module dependencies. v1 is committed to a
  zero-transitive-deps surface for embedders.
- **No `init()` functions**: zero side effects on import.
- **No package-level mutable state**: everything via dependency injection or
  functional options.
- **Race-clean tests**: `go test ./... -race` must pass. CI enforces this.
- **godoc on every exported type and function**: no exceptions.
- **Functional options for all public constructors**: see existing
  `WithStore`, `WithMatcher`, etc. patterns.

## Conventional commits

Commit messages and PR titles use these prefixes (matching what's already
in `git log`):

- `feat:`  new feature
- `fix:`   bug fix
- `chore:` maintenance / housekeeping (no behavior change)
- `docs:`  documentation only
- `test:`  test-only changes
- `ci:`    CI/workflow changes

Use the imperative mood: "add X", not "added X" or "adds X".

## Branch naming

- `feat/<issue-number>-<short-slug>` for features
- `fix/<issue-number>-<short-slug>` for bug fixes

Example: `feat/129-repo-polish`, `fix/142-recorder-deadlock`.

## Running tests locally

```sh
go test ./... -race -count=1
```

`-count=1` disables the test cache and ensures every run is fresh. Required
before opening a PR.

## Architectural decisions

All architectural decisions are recorded in `decisions.md`. The "Locked
decisions" table at the top of that file lists choices that are not open
for relitigation in a PR — examples include "stdlib only", "Apache 2.0
license", "JSON fixture format", "sanitize on write".

If you believe a locked decision should change, **open an issue first** with
an ADR-style write-up (Context / Decision / Consequences). Do not send a PR
that quietly changes a locked decision — it will be closed and asked to go
through the issue-first path.

For additions to the architecture (new ports, new adapters, new sanitizer
types), an ADR is appended to `decisions.md` by the architect stage of the
pipeline. External contributors don't need to write the ADR themselves; the
maintainer will. But please flag the architectural intent clearly in your
issue.

## Reporting security issues

See `SECURITY.md`. Do **not** open a public issue for security reports.
```

###### `CODE_OF_CONDUCT.md` (repo root)

Verbatim Contributor Covenant v2.1 from
<https://www.contributor-covenant.org/version/2/1/code_of_conduct.txt>.
The dev copies the full text and substitutes the contact line:

> Community leaders are obligated to respect the privacy and security of the
> reporter of any incident.
>
> ...
>
> Instances of abusive, harassing, or otherwise unacceptable behavior may be
> reported to the community leaders responsible for enforcement at
> **`tibtof@gmail.com`**.

The dev must not edit any other text. The "Attribution" footer must remain
intact (Contributor Covenant requires attribution to retain the license).

###### `.github/ISSUE_TEMPLATE/bug.yml`

YAML form template. Fields:

```yaml
name: Bug report
description: Report a bug in httptape
labels: ["bug"]
body:
  - type: markdown
    attributes:
      value: |
        Thanks for taking the time to file a bug. Please fill in as much
        detail as you can — minimal reproductions are very helpful.

        For security issues, do not file here. See SECURITY.md.
  - type: input
    id: version
    attributes:
      label: httptape version or git SHA
      placeholder: "v0.8.0 or 3c105cd"
    validations:
      required: true
  - type: input
    id: go-version
    attributes:
      label: Go version
      description: Output of `go version`
      placeholder: "go version go1.26.0 darwin/arm64"
    validations:
      required: true
  - type: dropdown
    id: mode
    attributes:
      label: Mode
      options:
        - Library (Go embed)
        - CLI - record
        - CLI - proxy
        - CLI - serve
        - CLI - mock
        - Docker
        - Testcontainers
        - Other (describe in repro)
    validations:
      required: true
  - type: textarea
    id: repro
    attributes:
      label: Reproduction steps
      description: Step-by-step instructions, ideally with a minimal code or CLI snippet.
      render: shell
    validations:
      required: true
  - type: textarea
    id: expected
    attributes:
      label: Expected behavior
    validations:
      required: true
  - type: textarea
    id: actual
    attributes:
      label: Actual behavior
      description: Include error messages, stack traces, or relevant log output.
    validations:
      required: true
  - type: textarea
    id: extra
    attributes:
      label: Anything else?
      description: Optional - minimal repro repo link, redacted fixture, screenshots.
    validations:
      required: false
```

###### `.github/ISSUE_TEMPLATE/feature.yml`

```yaml
name: Feature request
description: Suggest a new feature or enhancement
labels: ["enhancement"]
body:
  - type: markdown
    attributes:
      value: |
        Thanks for the idea. Before filing, please check the existing
        issues and `decisions.md` to make sure this isn't already tracked
        or already decided against.
  - type: textarea
    id: problem
    attributes:
      label: What problem are you trying to solve?
      description: Describe the user-visible problem, not the proposed implementation.
    validations:
      required: true
  - type: textarea
    id: api-sketch
    attributes:
      label: What would the API look like?
      description: A rough Go-code or CLI sketch is enough. Final shape will be decided in design review.
      render: go
    validations:
      required: false
  - type: textarea
    id: alternatives
    attributes:
      label: Alternatives considered
      description: Other approaches you thought about and why they did not fit.
    validations:
      required: false
```

###### `.github/ISSUE_TEMPLATE/config.yml`

```yaml
blank_issues_enabled: false
contact_links:
  - name: Question or how-to
    url: https://github.com/VibeWarden/httptape/discussions
    about: For "how do I do X with httptape?" questions, please use Discussions.
```

###### `.github/PULL_REQUEST_TEMPLATE.md`

```markdown
## Summary

<!-- One or two sentences. What does this PR do and why? -->

## Test plan

- [ ] `go test ./... -race -count=1` passes locally
- [ ] New code has godoc on all exported symbols
- [ ] No new entries in `go.mod` / `go.sum`
- [ ] PR title follows conventional-commit style (`feat:`, `fix:`, `chore:`,
      `docs:`, `test:`, `ci:`)

<!-- Add bespoke test steps or manual verification notes below if needed. -->

## Related

Closes #<issue-number>
```

###### `.github/workflows/test.yml`

```yaml
# .github/workflows/test.yml
#
# Run the full test suite with the race detector on every push to main and
# every PR targeting main. This is the workflow the README "Tests" badge
# points at.

name: Tests

on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

permissions:
  contents: read

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout
        uses: actions/checkout@v4

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      - name: Run tests
        run: go test ./... -race -count=1
```

Notes for the dev:

- The workflow `name:` MUST stay as `Tests` (capital T) because the README
  badge URL embeds it: `https://github.com/VibeWarden/httptape/actions/workflows/test.yml/badge.svg`.
- Do **not** add a Go-version matrix in this PR. Single version, matching
  `go.mod`, matching `release.yml`. A matrix is a future issue if needed.
- Do **not** add a coverage upload step. Coverage tooling is out of scope
  for this issue.

###### `.github/workflows/scorecard.yml`

Adapted from the canonical `ossf/scorecard` workflow at
<https://github.com/ossf/scorecard/blob/main/.github/workflows/scorecard-analysis.yml>.
Pin SHAs as documented there (the dev should fetch the most recent canonical
file at PR time and copy the pinned SHAs verbatim — do not freehand them).

```yaml
# .github/workflows/scorecard.yml
#
# OpenSSF Scorecard analysis. Runs weekly + on pushes to main.
# Results are published to https://api.scorecard.dev so the README badge
# resolves to the public dashboard.
#
# Adapted from the canonical workflow at
# https://github.com/ossf/scorecard/blob/main/.github/workflows/scorecard-analysis.yml

name: Scorecard supply-chain security

on:
  push:
    branches: [main]
  schedule:
    - cron: '30 1 * * 6'  # Weekly, Saturday 01:30 UTC

permissions: read-all

jobs:
  analysis:
    name: Scorecard analysis
    runs-on: ubuntu-latest
    permissions:
      security-events: write   # Upload SARIF to code-scanning
      id-token: write          # OIDC token for publish_results
    steps:
      - name: Checkout
        uses: actions/checkout@<canonical-pinned-sha>  # vX.Y.Z
        with:
          persist-credentials: false

      - name: Run analysis
        uses: ossf/scorecard-action@<canonical-pinned-sha>  # vX.Y.Z
        with:
          results_file: results.sarif
          results_format: sarif
          publish_results: true

      - name: Upload artifact
        uses: actions/upload-artifact@<canonical-pinned-sha>  # vX.Y.Z
        with:
          name: SARIF file
          path: results.sarif
          retention-days: 5

      - name: Upload to code-scanning
        uses: github/codeql-action/upload-sarif@<canonical-pinned-sha>  # vX.Y.Z
        with:
          sarif_file: results.sarif
```

The four pinned SHAs MUST be copied at PR time from
<https://github.com/ossf/scorecard/blob/main/.github/workflows/scorecard-analysis.yml>
so they reflect the latest reviewed canonical pin set. As of this ADR's
writing those are (subject to update at PR time):

- `actions/checkout@de0fac2e4500dabe0009e67214ff5f5447ce83dd  # v6.0.2`
- `ossf/scorecard-action@4eaacf0543bb3f2c246792bd56e8cdeffafb205a  # v2.4.3`
- `actions/upload-artifact@bbbca2ddaa5d8feaa63e36b76fdaad77386f024f  # v7.0.0`
- `github/codeql-action/upload-sarif@38697555549f1db7851b81482ff19f1fa5c4fedc  # v4.34.1`

Restrictions to honor (from Scorecard's "Workflow Restrictions" section):

- No top-level `env:` block, no top-level `defaults:`.
- No workflow-level write permissions (`permissions: read-all` at top is
  required).
- Only the analysis job uses `id-token: write`.
- Job runs on `ubuntu-latest` (a hosted Ubuntu runner). No containers, no
  services, no job-level `env:` or `defaults:`.
- Steps in the analysis job are restricted to the approved list:
  `actions/checkout`, `actions/upload-artifact`,
  `github/codeql-action/upload-sarif`, `ossf/scorecard-action`,
  `step-security/harden-runner`. We use the first four — do not add
  unrelated steps.

##### README badge cluster

Current cluster (lines 13-15 of `README.md`):

1. Go Reference (`pkg.go.dev`)
2. License (Apache 2.0)
3. Docker Image Size (Docker Hub)

After this PR, the cluster has 5 badges in this order:

1. Go Reference (unchanged)
2. **Tests** (new) -> `https://github.com/VibeWarden/httptape/actions/workflows/test.yml`
   - Image: `https://github.com/VibeWarden/httptape/actions/workflows/test.yml/badge.svg?branch=main`
   - Alt: `Tests`
3. **OpenSSF Scorecard** (new) -> `https://scorecard.dev/viewer/?uri=github.com/VibeWarden/httptape`
   - Image: `https://api.scorecard.dev/projects/github.com/VibeWarden/httptape/badge`
   - Alt: `OpenSSF Scorecard`
4. License (unchanged)
5. Docker Image Size (unchanged)

Five is the practical ceiling stated in the spec; we are exactly at it. The
dev MUST NOT add any other badges in this PR.

The Scorecard badge will return a placeholder until the workflow has run at
least once and `publish_results: true` has reported into the dashboard. The
dev should call this out in the PR description: it is expected for the
badge to render as "no data" briefly after merge until the first scheduled
or push-triggered run completes.

##### File layout

New files (8):

- `SECURITY.md`
- `CONTRIBUTING.md`
- `CODE_OF_CONDUCT.md`
- `.github/ISSUE_TEMPLATE/bug.yml`
- `.github/ISSUE_TEMPLATE/feature.yml`
- `.github/ISSUE_TEMPLATE/config.yml`
- `.github/PULL_REQUEST_TEMPLATE.md`
- `.github/workflows/test.yml`
- `.github/workflows/scorecard.yml`

Modified files (1):

- `README.md` — insert Tests + Scorecard badges into the existing badge
  cluster (between Go Reference and License) as specified above. No other
  README changes.

Total: 9 new files, 1 modified file. Zero `.go` files touched. Zero
`go.mod` / `go.sum` changes.

##### Sequence (PR construction)

1. Create branch `chore/129-repo-polish`.
2. Add the eight community-health files first (`SECURITY.md`,
   `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, three issue templates, PR
   template). Commit: `chore: add community-health files (security, contributing, code of conduct, issue and PR templates)`.
3. Add `.github/workflows/test.yml`. Commit: `ci: add test workflow running go test -race on push and PR`.
4. Update `README.md` to add the `Tests` badge. Commit: `docs: add CI status badge to README`.
5. Add `.github/workflows/scorecard.yml`. Commit: `ci: add OpenSSF Scorecard workflow`.
6. Update `README.md` to add the `OpenSSF Scorecard` badge. Commit: `docs: add OpenSSF Scorecard badge to README`.
7. Open PR titled `chore: repository polish — community-health, CI badge, OpenSSF Scorecard`.
8. PR description includes the self-check: paste the output of
   `gh api repos/VibeWarden/httptape/community/profile` showing
   `health_percentage: 100` (run after the branch is pushed and the files
   are visible on the branch — note this output reflects the default
   branch, so the 100% confirmation can only be captured post-merge; the
   PR description should note "post-merge verification deferred").

Five clean commits make the review easy to step through. The PR is
single — that's the decision in the table above.

##### Error cases (what could go wrong)

| Failure | Detection | Mitigation |
|---|---|---|
| `test.yml` fails on first PR run because of an unrelated flake | Red badge on first commit | Re-run; if it fails twice, it is a real bug — fix in this PR or block on a separate fix. Do not merge with red CI. |
| Scorecard SARIF upload fails because `security-events: write` is missing | Workflow logs | Already handled in the YAML — verify before pushing. |
| Scorecard `publish_results` rejected because workflow uses an unapproved step | Scorecard run failure with API rejection message | Stick to the four-step canonical layout. Do not add `step-security/harden-runner` or anything else. |
| README badge URL 404s because workflow filename or `name:` differs from what the badge encodes | Visible broken-image badge after merge | The workflow's filename MUST be `test.yml` and `scorecard.yml`. The badge URL uses the filename, not the `name:` field. The architect specifies both in this ADR — dev must not rename. |
| Scorecard badge shows "no data" forever | Visible after 24h | Means `publish_results` is not reporting. Check the workflow run logs for OIDC errors and re-verify `id-token: write` permission. |
| GitHub rejects a YAML issue template due to syntax | New-issue page errors | Validate locally with `yq` or paste into a draft issue on a fork before merging. Acceptance criterion already requires "valid YAML form syntax". |
| Contributor Covenant text accidentally edited beyond the contact line | Reviewer catches | The dev pastes the verbatim text from <https://www.contributor-covenant.org/version/2/1/code_of_conduct.txt> and only edits the contact line. The reviewer diffs against that source. |
| Community-profile checklist still <100% after merge | `gh api repos/.../community/profile` post-merge | The eight items the API checks for are: Description, README, License, Code of conduct, Contributing, Security policy, Issue templates, PR template. All are covered by this PR (Description and README are pre-existing). 100% is the acceptance criterion. |

##### Test strategy

This issue ships no Go code and no Go tests. "Test" here means
verification:

- **CI green on the PR**: the new `test.yml` runs on the PR itself and must
  pass. This both verifies the existing test suite still passes (sanity
  check, since no `.go` files change) and verifies the new workflow file
  is syntactically valid and works.
- **YAML lint**: dev verifies issue-template YAML by pasting the templates
  into a "New issue" page on a fork or scratch repo before opening the PR.
- **Markdown render**: dev previews `SECURITY.md`, `CONTRIBUTING.md`, and
  `CODE_OF_CONDUCT.md` on GitHub's "view raw / preview" before pushing.
- **Badge URLs resolve**: after the workflows have run at least once on
  `main` (post-merge), both badge images must render. Scorecard badge can
  legitimately show "no data" for up to 24 hours after first run; reviewer
  is aware.
- **Community profile**: `gh api repos/VibeWarden/httptape/community/profile`
  returns `health_percentage: 100` post-merge. Dev includes this in the PR
  description as the final acceptance check.
- **Scorecard score sanity**: post-merge, the dashboard at
  `https://scorecard.dev/viewer/?uri=github.com/VibeWarden/httptape` must
  resolve and show a numeric score. Sub-checks failing is **fine** — fixing
  individual Scorecard checks (Pinned-Dependencies, Branch-Protection,
  etc.) is each a separate future issue. The deliverable here is "the
  badge resolves and the dashboard exists", not "we score 10/10".

Manual smoke test the dev runs before pushing:

```sh
# Validate YAML syntax of the templates and workflows
for f in .github/ISSUE_TEMPLATE/*.yml .github/workflows/test.yml .github/workflows/scorecard.yml; do
  python3 -c "import yaml,sys; yaml.safe_load(open('$f'))" && echo "OK: $f" || echo "FAIL: $f"
done

# Run tests locally to confirm baseline still passes
go test ./... -race -count=1
```

#### Consequences

**Positive**

- GitHub community-profile health score moves from 50% to 100%, removing
  the "early-stage" perception flag.
- README has a real CI signal (Tests badge) — green/red is meaningful and
  links to logs.
- OpenSSF Scorecard badge shows the project takes supply-chain security
  seriously and gives reviewers a one-click view of the security posture.
- Contributors have a documented path: how to file a bug, how to file a
  feature, how PRs are structured, what conventional-commit prefixes to
  use, what the agent pipeline is, where decisions are recorded.
- Security reporters have a documented private channel (PVR primary, email
  fallback) — no more guessing where to send vulnerabilities.

**Negative / trade-offs**

- More files to keep in sync. `CONTRIBUTING.md` references `CLAUDE.md` and
  `decisions.md` by name; if either of those is renamed, this file must be
  updated. Mitigation: the agent pipeline already touches `decisions.md`
  on every architectural change, so drift is unlikely.
- The Tests badge becomes a public signal — flaky tests now visibly affect
  project perception. Mitigation: race-clean tests are already a hard
  rule (L-13 in the locked-decisions table).
- Scorecard score is now a public number. Initial run will likely show
  several un-fixed sub-checks (Pinned-Dependencies on the existing
  workflows, Branch-Protection visibility limits, Token-Permissions on
  `release.yml`, etc.). Each is a separate future issue.
- Contributor Covenant verbatim text is long and adds ~140 lines to the
  repo root. This is the cost of using the de-facto standard; it's worth
  it for the recognition it gets from the community-profile checker.

**Future work explicitly not in scope here** (each is its own future
issue):

- `dependabot.yml` for action and Go-module update PRs
- `codeql.yml` for code scanning
- `FUNDING.yml` once owner decides on sponsorships
- Signed releases / cosign / SBOM
- Pinning the existing `docker.yml`, `release.yml`, `docs.yml` actions to
  SHAs (Scorecard will flag this; addressing it is a separate cleanup PR)
- Multi-Go-version test matrix in `test.yml`
- Adding Discussions threads or `awesome-go` submission

---

### ADR-35: Server-Sent Events (SSE) record / replay / proxy passthrough

**Date**: 2026-04-16
**Issue**: #124
**Status**: Accepted

#### Context

LLM APIs (OpenAI, Anthropic, etc.) stream completions via Server-Sent Events
(SSE). Developers recording these interactions with httptape received a single
opaque `Body` blob containing concatenated SSE frames with no timing metadata.
Replay delivered the entire stream instantly, making it useless for testing
streaming UIs, back-pressure, or timing-dependent logic.

No existing Go HTTP mocking library (gock, httpmock) or standalone tool
(WireMock, json-server, Mockoon) provides SSE record + replay with per-event
timing and per-event sanitization. MSW offers mock-side SSE construction but
not recording of real upstream streams. mitmproxy captures but cannot replay.

This is httptape's strongest competitive differentiator.

#### Decision

##### Type design

**`SSEEvent`** (in `sse.go`): pure value type representing one parsed SSE event.

```go
type SSEEvent struct {
    OffsetMS int64  `json:"offset_ms"`        // ms since response headers
    Type     string `json:"type,omitempty"`    // "event:" field
    Data     string `json:"data"`             // "data:" field(s), joined with "\n"
    ID       string `json:"id,omitempty"`     // "id:" field
    Retry    int    `json:"retry,omitempty"`  // "retry:" field (ms)
}
```

**`SSETimingMode`** (in `sse.go`): sealed interface controlling replay timing.
Three constructors:

- `SSETimingRealtime()` -- replays with original inter-event gaps
- `SSETimingAccelerated(factor float64)` -- divides gaps by factor (must be > 0;
  panics otherwise as a constructor guard)
- `SSETimingInstant()` -- emits all events back-to-back with zero delay

The interface is sealed (unexported `sseTimingMode()` method) so the set of
implementations is closed. Each type computes its delay from `OffsetMS` deltas.

##### Schema migration

`RecordedResp` gains an `SSEEvents []SSEEvent` field with `json:"sse_events,omitempty"`.
Body and SSEEvents are mutually exclusive by construction:

- The Recorder populates one or the other, never both.
- During replay, `IsSSE()` (non-nil, non-empty SSEEvents) takes precedence.
- Old tapes (before SSE support) have `SSEEvents` nil/empty and continue to
  work unchanged -- the field is `omitempty`, so existing JSON fixtures round-trip
  without modification.

No schema version bump is needed. The field is purely additive.

##### Recording approach

`sseRecordingReader` (in `sse.go`) wraps the upstream response body:

1. `io.TeeReader` feeds bytes to both the caller and an `io.Pipe`.
2. A background goroutine reads from the pipe through `parseSSEStream`,
   calling `onEvent` for each dispatched event.
3. `parseSSEStream` follows the W3C SSE specification: blank-line dispatch,
   comment-line skip, field parsing with colon+space stripping, multi-line
   data joining, retry as digits-only.
4. When the caller closes the body (or upstream hits EOF), the pipe is closed,
   the parser exits, and `onDone` is called.

**Critical ordering**: `onDone` (which persists the tape) is called *before*
`close(r.done)`, so the tape is saved before `Close()` returns. This is
essential for synchronous recording mode where the caller expects the tape to
be in the store immediately after closing the body.

SSE detection is based on `Content-Type: text/event-stream` (case-insensitive,
parameter-tolerant via `mime.ParseMediaType`). Detection is enabled by default
(`sseRecording: true` on Recorder) and can be disabled with
`WithSSERecording(false)`, in which case SSE responses are buffered as regular
bodies.

##### Replay timing modes

The `Server` gains a `WithSSETiming(mode SSETimingMode)` option (default:
`SSETimingRealtime`). When serving an SSE tape, `serveSSE` checks for
`http.Flusher` support, sets SSE-required headers (`Content-Type`,
`Cache-Control`, `Connection`), writes the status code, then calls
`replaySSEEvents` which iterates over events, applying the timing mode's
`delay()` before each write. Each event is written with `writeSSEEvent`
and flushed individually. Context cancellation (client disconnect) is
respected between events.

The `Proxy` gains `WithProxySSETiming(mode SSETimingMode)` for L2 fallback
(default: `SSETimingInstant`). Proxy L2 fallback synthesizes an `*http.Response`
with a piped body and streams events through it.

##### Sanitization composition

Two new `SanitizeFunc` constructors (in `sanitizer.go`):

- `RedactSSEEventData(paths ...string)` -- applies body-path redaction to each
  event's Data field independently (treats each Data as a JSON body).
- `FakeSSEEventData(seed string, paths ...string)` -- applies deterministic
  faking to each event's Data field.

Both are no-ops for non-SSE tapes. They compose naturally with existing
pipeline functions -- no new interface or protocol needed. They copy the
SSEEvents slice before mutation to preserve immutability.

##### Shared `writeSSEEvent` helper

The `writeSSEEvent` function in `sse.go` writes a single SSE event in wire
format. It is used by:

- `replaySSEEvents` (server replay)
- `Proxy.sseResponseFromTape` (proxy L2 fallback)
- `health.go` SSE stream handler (refactored from inline formatting)

This eliminates duplicated SSE wire-format code across the codebase.

#### Consequences

**Positive**

- httptape can record, replay, and sanitize SSE streams from any upstream
  (LLM APIs, real-time feeds, notification streams) with per-event granularity.
- Replay timing modes let users choose between realistic simulation
  (Realtime), faster tests (Accelerated), and instant tests (Instant).
- Sanitization applies to individual event payloads, so PII in streamed
  LLM completions is redacted before it touches disk.
- Backward compatible: no schema version bump, old tapes work unchanged.
- No new dependencies (stdlib only).

**Negative / trade-offs**

- SSE events are fully parsed and stored individually, increasing fixture file
  size compared to a raw body blob for streams with many small events. This is
  acceptable because it enables per-event timing and sanitization.
- The `sseRecordingReader` adds a background goroutine per SSE response during
  recording. This is the same pattern used by the async recorder channel and
  has negligible overhead for the expected use case (a handful of concurrent
  SSE connections in test/dev).
- `parseSSEStream` buffers up to 1 MB per line (configurable scanner buffer).
  Pathological SSE streams with very long lines could hit this limit.

---

### ADR-36: Pluggable FileStore filename strategy

**Date**: 2026-04-16
**Issue**: #132
**Status**: Accepted

#### Context

FileStore previously hard-coded filenames as `<tape.ID>.json` (UUID-based).
While functional, these names are opaque when browsing fixture directories.
Developers frequently requested human-readable filenames such as
`get_api-users.json` that convey the HTTP method and URL at a glance.

At the same time, any filename strategy must preserve backward compatibility
(existing UUID-named fixtures must continue to work), support arbitrary custom
strategies, and guard against security issues when users provide their own
`FilenameStrategy` implementations.

#### Decision

##### FilenameStrategy type

A function type `FilenameStrategy func(tape Tape) string` computes the filename
stem (without `.json` extension) for a tape. Two built-in constructors are
provided:

- `UUIDFilenames()` -- returns `tape.ID` (default, backward-compatible).
- `ReadableFilenames()` -- format: `<method>_<url-slug>[_q-<query-hash>][_<body-hash>].json`.

Users inject a strategy via `WithFilenameStrategy(s FilenameStrategy)` option on
`NewFileStore`.

##### Lookup mechanism

Since filenames are no longer predictable from the tape ID alone, all lookup
methods (Load, Delete) use a two-phase approach:

1. **Fast path**: try `<id>.json` directly (works for UUID strategy and manual
   overrides).
2. **Scan fallback**: iterate all `.json` files in the directory, extract the
   `id` field from each, and match.

This ensures Load/Delete work regardless of which strategy was used when the
tape was saved.

##### URL slug generation

`slugifyURL` aggressively normalizes URL paths to `[a-z0-9-]`:
- Strip leading/trailing slashes
- Replace any non-alphanumeric character with a dash
- Collapse consecutive dashes
- Trim trailing dashes
- Return `"root"` for empty or `/` paths

##### Query string handling

Query strings are represented as a short hash with `q-` prefix: 4 hex characters
of SHA-256 of the raw query string. This is deterministic (same query produces
the same hash) and avoids exposing potentially sensitive query parameters in
filenames.

##### Body hash prefix

The first 4 characters of the tape's `BodyHash` field are appended when the body
hash is non-empty. This disambiguates requests to the same URL with different
bodies (e.g., different POST payloads).

##### Filename length cap

Filenames are capped at 200 characters before the `.json` extension. This keeps
total filename length under typical filesystem limits (255 bytes). Truncation
cleans up trailing dashes.

##### Collision resolution

When the strategy produces a stem that is already taken by a different tape,
counter suffixes `_2` through `_99` are tried. If all 99 slots are exhausted,
`ErrFilenameCollision` is returned. This is generous enough for practical use
while preventing runaway file creation.

##### Save overwrite cleanup

When a tape is re-saved (same ID, possibly different strategy), `removeOldFile`
scans for and removes the previous file to prevent duplicates. This is
best-effort: if removal fails (e.g., concurrent delete or permission issue), the
new file is still written and Save succeeds. The orphan file may cause ambiguity
but does not corrupt data.

##### Empty strategy fallback

If a strategy returns an empty string, `"."`, or `".."`, the stem falls back to
`tape.ID`. This ensures a valid filename is always produced.

##### Path-traversal sanitization

Strategy output is sanitized with `filepath.Base()` before use as a path
component. This strips any directory traversal (`../`, `/`, `\`) from custom
strategy results. After `filepath.Base`, empty / `"."` / `".."` results trigger
the fallback to `tape.ID`. The built-in strategies already produce safe values;
this guards against malicious or buggy custom strategies only.

##### Corrupt JSON handling

Corrupt `.json` files in the directory are silently skipped during scan
operations (List, Load scan fallback, Delete scan fallback). A single corrupt
file does not break the store.

##### Context cancellation in scan loops

All scan loops (scanForID, List, Delete scan fallback) check `ctx.Err()` between
iterations to respect context cancellation. A cancelled context stops the scan
and returns the cancellation error.

#### Consequences

**Positive**

- Human-readable fixture filenames improve developer experience when browsing
  directories.
- Full backward compatibility: existing UUID-named fixtures work without
  migration.
- Pluggable design allows custom strategies for specialized naming schemes.
- Path-traversal sanitization prevents security issues from custom strategies.

**Negative / trade-offs**

- Scan fallback is O(n) in the number of files when the fast path misses.
  Acceptable for typical fixture counts (tens to hundreds).
- Collision counter has a hard cap at 99. Exceeding this returns an error rather
  than silently generating longer names.
- `removeOldFile` is best-effort; a failed removal leaves an orphan file.

---

### ADR-37: MatchBodyFuzzy vacuous-true for body-less requests

**Date**: 2026-04-18
**Issue**: #178
**Status**: Accepted

#### Context

`MatchBodyFuzzy` (ADR-14, `matcher.go` lines 421-477) returns score 0 whenever
neither the incoming request nor the candidate tape has a JSON-parseable body.
This is correct for the "no evidence of a match" case when both sides _should_
have bodies but happens to eliminate body-less requests (e.g., `GET /users/1`
matched against a recorded `GET /users/1`).

The root cause is that `json.Unmarshal` on nil or empty bytes fails, causing the
criterion to return 0. Even if both bodies were valid but empty JSON (`{}`), no
paths would match, so `matched == 0` triggers the final `return 0`.

This makes `MatchBodyFuzzy` unsafe to compose globally in a `CompositeMatcher`
alongside REST-style routes that include body-less methods (GET, DELETE, HEAD).
Users who add `MatchBodyFuzzy("$.action")` to handle POST-heavy routes
inadvertently eliminate all their GET routes.

The semantically correct behavior: when both the request body and the tape body
are absent (or not parseable as JSON), the criterion should be vacuously true --
return a small positive score so the candidate stays alive without dominating
any real body-match score.

This follows the same pattern as `MatchQueryParams`, which returns its full
score (4) when the incoming request has no query parameters (vacuously true --
all zero params match). However, `MatchBodyFuzzy` should return a _reduced_
score (1 instead of 6) for the vacuous case, because "neither side has a body"
is weaker evidence than "both sides have bodies and the specified fields match."

#### Decision

##### Vacuous-true score: 1

When both bodies are absent, `MatchBodyFuzzy` returns 1 instead of 0.

Score 1 is the minimum positive value. It keeps the candidate alive in the
`CompositeMatcher` scoring loop (not eliminated) without inflating its total
score. A candidate that matches on actual body fields (score 6) will always
outscore one that passes only vacuously (score 1), preserving correct ranking.

Verification against the score-weight table (from ADR-4 / ADR-14):

| Criterion        | Score | Vacuous-true behavior |
|------------------|-------|-----------------------|
| MatchMethod      | 1     | N/A (always checks)   |
| MatchPath        | 2     | N/A (always checks)   |
| MatchRoute       | 1     | Returns 1 if route="" |
| MatchPathRegex   | 1     | N/A (always checks)   |
| MatchHeaders     | 3     | N/A (always checks)   |
| MatchQueryParams | 4     | Returns 4 if no query params |
| MatchBodyFuzzy   | 6     | **Returns 1 if both bodies absent (NEW)** |
| MatchBodyHash    | 8     | Returns 8 if both empty |

The dominance ordering is preserved:
- A real body-fuzzy match (6) always beats a vacuous body-fuzzy pass (1).
- `MatchBodyHash` both-empty (8) > `MatchBodyFuzzy` vacuous (1). No conflict.
- `MatchMethod(1) + MatchPath(2) + MatchBodyFuzzy-vacuous(1) = 4` does not
  accidentally outscore any higher-specificity criterion.

##### Definition of "body absent"

A body is considered **absent** if any of the following is true:
- The byte slice is `nil`
- The byte slice has length 0
- The byte slice fails `json.Unmarshal`

In Go, `len(nil) == 0`, so the nil and zero-length cases collapse into a single
`len(body) == 0` check. The unmarshal-failure case catches non-JSON content
(e.g., plain text, XML, binary) which cannot participate in field-level
comparison.

Bodies that ARE valid JSON -- including `{}`, `null`, `[]`, `"string"`, `0`,
`true` -- are NOT considered absent. They proceed through the existing
field-extraction logic. For `{}` vs `{}`, no paths will be found in either body,
so `matched == 0` and the criterion returns 0. This is correct: both sides
explicitly sent empty JSON objects, which is different from "no body at all."

Edge case summary:

| Request body | Tape body   | Result | Rationale |
|-------------|-------------|--------|-----------|
| nil         | nil         | 1      | Vacuous-true: both absent |
| nil         | `[]byte{}`  | 1      | Vacuous-true: both absent |
| `[]byte{}`  | nil         | 1      | Vacuous-true: both absent |
| `[]byte{}`  | `[]byte{}`  | 1      | Vacuous-true: both absent |
| nil         | `{"a":1}`   | 0      | Mismatch: one absent, one present |
| `{"a":1}`   | nil         | 0      | Mismatch: one absent, one present |
| `not json`  | `not json`  | 1      | Vacuous-true: both fail unmarshal |
| `not json`  | `{"a":1}`   | 0      | Mismatch: one absent, one present |
| `{"a":1}`   | `not json`  | 0      | Mismatch: one absent, one present |
| `{}`        | `{}`        | 0      | Both valid JSON, no paths match, matched=0 |
| `null`      | `null`      | 0      | Both valid JSON, paths fail extraction, matched=0 |
| `{"a":1}`   | `{"a":1}`   | 6      | Fields match (assuming path `$.a`) |
| `{"a":1}`   | `{"a":2}`   | 0      | Fields differ |

##### Sequence of checks inside the criterion

The modified `MatchBodyFuzzy` closure follows this sequence:

1. **No valid paths**: If `len(parsed) == 0`, return 0 (unchanged).
2. **Read and restore request body**: Read `req.Body` into `reqBody` bytes,
   replace `req.Body` with a new reader over the same bytes (unchanged).
3. **Determine absence**: Attempt `json.Unmarshal` on both `reqBody` and
   `candidate.Request.Body`. Track whether each side is "absent":
   - `reqAbsent = len(reqBody) == 0 || json.Unmarshal(reqBody, &reqData) != nil`
   - `tapeAbsent = len(candidate.Request.Body) == 0 || json.Unmarshal(candidate.Request.Body, &tapeData) != nil`
4. **Vacuous-true short-circuit**: If both absent, return 1.
5. **Asymmetric mismatch**: If one absent and the other not, return 0.
6. **Field comparison**: Proceed with existing path-extraction and
   `reflect.DeepEqual` logic (unchanged). If `matched == 0`, return 0.
   If all matched fields are equal, return 6.

This approach avoids duplicating unmarshal calls. The `json.Unmarshal` side
effects (populating `reqData` / `tapeData`) are preserved for step 6 when both
sides are present.

##### Godoc update

The `MatchBodyFuzzy` godoc comment must be updated to document the vacuous-true
behavior. Add the following to the "Matching semantics" list:

```
//   - If both the incoming request body and the tape body are absent
//     (nil, empty, or not valid JSON), the criterion returns 1 (vacuous
//     match — the body dimension is irrelevant for this request/tape
//     pair). If exactly one side is absent, the criterion returns 0.
```

#### File layout

Only two files are modified:

| File | Change |
|------|--------|
| `matcher.go` | Modify `MatchBodyFuzzy` closure: add vacuous-true check, update godoc |
| `matcher_test.go` | Update `TestMatchBodyFuzzy_BothBodiesEmpty` (expect 1 not 0), add new test cases |

No new files. No new types. No new exported functions. No new imports.

#### Error cases

No new error cases are introduced. The existing error handling is unchanged:
- `io.ReadAll` failure on request body: return 0 (unchanged).
- `json.Unmarshal` failure: now classified as "absent" rather than being
  an implicit early-return-0. The only behavioral change is when BOTH sides
  fail unmarshal (return 1 instead of 0).

#### Test strategy

Modify and add tests in `matcher_test.go` using the existing table-driven
pattern. All tests use the same style as the existing `TestMatchBodyFuzzy_*`
tests.

**Modified test:**

- `TestMatchBodyFuzzy_BothBodiesEmpty`: Change expected score from 0 to 1.
  This is the existing test at line 1171. The test name accurately describes
  the scenario; only the assertion changes.

**New tests (add as a single table-driven test `TestMatchBodyFuzzy_VacuousTrue`):**

| Sub-test name | Request body | Tape body | Want score | Notes |
|--------------|-------------|-----------|-----------|-------|
| both nil | nil | nil | 1 | Core fix case |
| both empty bytes | `[]byte{}` | `[]byte{}` | 1 | Empty but non-nil |
| req nil tape empty | nil | `[]byte{}` | 1 | Mixed nil/empty |
| req empty tape nil | `[]byte{}` | nil | 1 | Mixed nil/empty |
| both invalid JSON | `not json` | `also not json` | 1 | Both fail unmarshal |
| req nil tape has body | nil | `{"a":1}` | 0 | Asymmetric |
| req has body tape nil | `{"a":1}` | nil | 0 | Asymmetric |
| req invalid tape has body | `not json` | `{"a":1}` | 0 | Asymmetric |
| req has body tape invalid | `{"a":1}` | `not json` | 0 | Asymmetric |
| both empty JSON objects | `{}` | `{}` | 0 | Valid JSON, no paths match |
| both JSON null | `null` | `null` | 0 | Valid JSON, paths fail extraction |
| both bodied fields match | `{"action":"create"}` | `{"action":"create"}` | 6 | Unchanged behavior |
| both bodied fields differ | `{"action":"create"}` | `{"action":"delete"}` | 0 | Unchanged behavior |

All tests use `MatchBodyFuzzy("$.action")` as the criterion (at least one valid
parsed path) to avoid triggering the `len(parsed) == 0` early return.

**Existing tests that must continue to pass unchanged:**

- `TestMatchBodyFuzzy_SingleField`
- `TestMatchBodyFuzzy_MultipleFields`
- `TestMatchBodyFuzzy_NestedField`
- `TestMatchBodyFuzzy_ArrayWildcard`
- `TestMatchBodyFuzzy_FieldValueDiffers`
- `TestMatchBodyFuzzy_NonJSONRequestBody`
- `TestMatchBodyFuzzy_NonJSONTapeBody`
- `TestMatchBodyFuzzy_EmptyPaths`
- `TestMatchBodyFuzzy_PathInRequestNotInTape`
- `TestMatchBodyFuzzy_PathInTapeNotInRequest`
- `TestMatchBodyFuzzy_InvalidPaths`
- `TestMatchBodyFuzzy_AllPathsMissing`
- `TestMatchBodyFuzzy_BodyRestored`
- `TestMatchBodyFuzzy_DeepNestedObject`
- `TestMatchBodyFuzzy_NumericValue`
- `TestMatchBodyFuzzy_BooleanValue`
- `TestMatchBodyFuzzy_NullValue`
- `TestMatchBodyFuzzy_ObjectValue`
- `TestMatchBodyFuzzy_ArrayWildcard_DifferentValues`
- `TestMatchBodyFuzzy_ArrayWildcard_DifferentLengths`
- `TestMatchBodyFuzzy_Composability`
- `TestMatchBodyFuzzy_WildcardNotArray`
- `TestMatchBodyFuzzy_WildcardMissingFieldInElement`

Note: `TestMatchBodyFuzzy_NonJSONRequestBody` (line 1094) and
`TestMatchBodyFuzzy_NonJSONTapeBody` (line 1109) both expect 0 and remain
correct. In both cases, exactly one side has valid JSON and the other does not,
so the asymmetric mismatch rule applies (return 0).

**Composability integration test (new):**

Add `TestMatchBodyFuzzy_VacuousTrueComposability` that creates a
`CompositeMatcher` with `MatchMethod()`, `MatchPath()`, and
`MatchBodyFuzzy("$.action")`, then verifies that a `GET /users` request
(no body) correctly matches a recorded `GET /users` tape (no body) while a
`POST /users` tape with a body is not incorrectly selected. This directly
validates the user story from the issue.

#### Alternatives considered

1. **Return 6 (full score) for vacuous-true**: Rejected. A vacuous match is
   weaker evidence than an actual field match. Returning the full score would
   let body-less candidates compete equally with body-matched candidates, which
   is incorrect when both types of candidates exist for the same path.

2. **Return the full score only when both bodies are nil (not just absent)**:
   Rejected. This would not handle the case where a body is empty bytes
   (Content-Length: 0) or contains non-JSON content. The "absent" definition
   should be broad enough to cover all cases where field-level comparison is
   impossible.

3. **Skip the criterion entirely for body-less requests (return the full score
   like MatchQueryParams does)**: Rejected for the same reason as alternative 1.
   `MatchQueryParams` returns its full score (4) for the vacuous case because
   "no query params" is a common, well-defined state. For bodies, the distinction
   between "no body" and "empty body" is less clear-cut, and returning a reduced
   score (1) is more conservative and safer.

4. **Change MatchCriterion signature to support a "skip" sentinel**: Rejected.
   This would require changing the `MatchCriterion` type and `CompositeMatcher`
   scoring loop, which is out of scope for this bug fix. Issue #179 may
   address this as part of a broader `MatchCriterion` refactor.

#### Consequences

- **Bug fixed**: `MatchBodyFuzzy` is now safe to compose globally in a
  `CompositeMatcher` alongside body-less routes. GET, DELETE, HEAD, and OPTIONS
  requests no longer get eliminated.
- **Backward-compatible**: The only behavioral change is for the "both bodies
  absent" case, which previously returned 0 (eliminating the candidate) and now
  returns 1 (keeping it alive with minimal score). All other cases are unchanged.
- **One existing test modified**: `TestMatchBodyFuzzy_BothBodiesEmpty` changes
  its expected value from 0 to 1. This is intentional and reflects the bug fix.
- **Score 1 is conservative**: If a future ADR introduces a "skip" sentinel for
  criteria (issue #179), the vacuous-true logic could be revisited to use it
  instead of a positive score. The current approach is the minimal fix.
- **No new dependencies**: stdlib only.

---


### ADR-38: Promote MatchCriterion from function type to Criterion interface

**Date**: 2026-04-18
**Issue**: #179
**Status**: Accepted

> **Note**: Issue #178 is being architected in parallel and may also produce an ADR.
> If both land with the same number, the orchestrator will renumber during merge.

#### Context

`MatchCriterion` is currently a function type:

```go
type MatchCriterion func(req *http.Request, candidate Tape) int
```

This was chosen in ADR-4 for simplicity. However, the function type has limitations
that block upcoming work:

1. **Config-driven matcher composition** (issue #180) needs to dispatch on criterion
   identity — e.g., a JSON config `{"type": "body_fuzzy", "paths": ["$.action"]}`
   must resolve to the correct struct. A function type has no identity; an interface
   with a `Name()` method does.
2. **Debugging and logging**: when a candidate is eliminated, it is useful to report
   *which* criterion eliminated it. A function type provides no introspection.
3. **Consistency**: the top-level `Matcher` is already an interface. Having its
   sub-components be function types is inconsistent.

This ADR promotes criteria to an interface, converts each existing criterion from a
closure-returning function to a struct implementing the interface, and updates
`CompositeMatcher` to accept the new type.

The project is pre-1.0, so breaking changes to the public API are acceptable. This
refactor changes no matching behavior — it is a pure structural change.

#### Decision

##### Open decision 1: Back-compat shim policy

**Decision: Option B — Remove the old constructor functions; breaking change.**

Rationale:
- The project is pre-1.0. There are no stability guarantees.
- Keeping wrapper functions that return the old type is impossible once `CompositeMatcher`
  accepts `[]Criterion` instead of `[]MatchCriterion`. The wrappers would need to return
  `Criterion`, which changes their signature anyway — no back-compat is preserved.
- Option C (deprecate) doubles the API surface for a library with zero external users
  yet. The cost of maintaining deprecated wrappers outweighs the benefit.
- The `cmd/httptape` CLI does not use any criterion constructors directly (verified by
  grep). The only Go call sites are `matcher.go`, `matcher_test.go`, `proxy_test.go`,
  and the `DefaultMatcher()` function. All are in-repo.

The old `MatchCriterion` function type is removed entirely. `CriterionFunc` serves as
the adapter for ad-hoc functional criteria (same role `MatcherFunc` plays for `Matcher`).

##### Open decision 2: Name() string method on Criterion

**Decision: Include `Name()` in the `Criterion` interface in this issue.**

Rationale:
- Issue #180 (config-driven composition) directly depends on being able to identify a
  criterion by name. If we defer `Name()` to #180, that issue would need to change the
  interface (another breaking change to all implementations).
- The cost is low: each struct adds a one-line method returning a string constant.
- `CriterionFunc` returns `"custom"` as its name — ad-hoc criteria are not
  config-dispatchable, which is correct and expected.
- Names follow a consistent convention: lowercase, underscore-separated, matching the
  JSON config `type` field that #180 will use (e.g., `"method"`, `"path"`,
  `"path_regex"`, `"route"`, `"query_params"`, `"headers"`, `"body_hash"`,
  `"body_fuzzy"`).

##### Open decision 3: Field exposure / construction ergonomics

**Decision: Public fields for config-visible state + constructor for criteria that
need validation or pre-processing.**

Specific shapes:

- **Zero-config criteria** (`MethodCriterion`, `PathCriterion`, `QueryParamsCriterion`,
  `BodyHashCriterion`): zero-value structs, no fields, no constructor needed. Usage:
  `MethodCriterion{}`.

- **Simple-field criteria** (`RouteCriterion`, `HeadersCriterion`): public fields,
  no constructor needed. Usage: `HeadersCriterion{Key: "Accept", Value: "application/json"}`.
  `HeadersCriterion.Score()` canonicalizes the key at call time (cheap — `http.CanonicalHeaderKey`
  is a simple string manipulation). Alternatively, a `NewHeadersCriterion` constructor
  could pre-canonicalize, but the per-call cost is negligible and keeping struct literal
  construction maximizes ergonomics for both Go code and config-driven construction.

- **Validation-required criteria** (`PathRegexCriterion`): constructor
  `NewPathRegexCriterion(pattern string) (*PathRegexCriterion, error)` compiles the
  regex once. The struct has a public `Pattern string` field (for serialization/debugging)
  and a private `re *regexp.Regexp` field. Direct struct-literal construction is not
  useful because `re` would be nil; the constructor is required.

- **Pre-processing criteria** (`BodyFuzzyCriterion`): constructor
  `NewBodyFuzzyCriterion(paths ...string) *BodyFuzzyCriterion`. The struct has public
  `Paths []string` (for serialization/config round-tripping) and private
  `parsed []parsedPath` (populated by constructor). No error return — invalid paths are
  silently skipped, matching the current behavior of `MatchBodyFuzzy`. Config-driven
  construction (#180) calls `NewBodyFuzzyCriterion(config.Paths...)`.

##### Open decision 4: PathRegexCriterion constructor error handling

**Decision: `NewPathRegexCriterion` returns `(*PathRegexCriterion, error)` — no panic.**

Rationale:
- The CLAUDE.md panic exception is scoped to "nil required dependencies" — programming
  errors where a caller passes `nil` for a non-optional argument. An invalid regex
  pattern is a *value* error: the caller provided a non-nil string that happens to be
  syntactically invalid. This is a runtime validation failure, not a programming error.
- The current `MatchPathRegex` already returns `(MatchCriterion, error)`. Changing to
  panic would be a behavior regression and would surprise existing callers.
- Config-driven construction (#180) will deserialize regex patterns from JSON — panicking
  on user-provided config values would violate the "no panics in a library" principle.

##### Types

```go
// Criterion evaluates how well a candidate Tape matches an incoming request
// for a single dimension (method, path, body, etc.).
type Criterion interface {
    // Score returns a match score:
    //   - 0 means the candidate does not match on this dimension (eliminates it).
    //   - A positive value means the candidate matches, with higher values
    //     indicating a stronger/more specific match.
    //
    // Implementations must not modify the candidate tape. They may read and
    // restore the request body but must leave the request otherwise unchanged.
    Score(req *http.Request, candidate Tape) int

    // Name returns a stable identifier for this criterion type.
    // Used for debugging, logging, and config-driven dispatch.
    // Built-in criteria use lowercase underscore-separated names
    // (e.g., "method", "path", "body_hash").
    Name() string
}

// CriterionFunc is an adapter to allow the use of ordinary functions as
// Criterion implementations. Its Name() method returns "custom".
type CriterionFunc func(req *http.Request, candidate Tape) int

func (f CriterionFunc) Score(req *http.Request, candidate Tape) int {
    return f(req, candidate)
}

func (f CriterionFunc) Name() string {
    return "custom"
}
```

##### Struct implementations

```go
// MethodCriterion matches on HTTP method. Score: 1.
type MethodCriterion struct{}

func (MethodCriterion) Score(req *http.Request, candidate Tape) int { ... }
func (MethodCriterion) Name() string { return "method" }

// PathCriterion matches on URL path (exact). Score: 2.
type PathCriterion struct{}

func (PathCriterion) Score(req *http.Request, candidate Tape) int { ... }
func (PathCriterion) Name() string { return "path" }

// PathRegexCriterion matches the URL path against a compiled regex. Score: 1.
type PathRegexCriterion struct {
    // Pattern is the original regex pattern string.
    Pattern string
    re      *regexp.Regexp
}

// NewPathRegexCriterion compiles the pattern and returns a PathRegexCriterion.
// Returns an error if the pattern is not a valid regular expression.
func NewPathRegexCriterion(pattern string) (*PathRegexCriterion, error) { ... }

func (c *PathRegexCriterion) Score(req *http.Request, candidate Tape) int { ... }
func (c *PathRegexCriterion) Name() string { return "path_regex" }

// RouteCriterion matches on the tape's Route field. Score: 1.
// If Route is empty, the criterion always returns 1 (matches any tape).
type RouteCriterion struct {
    Route string
}

func (c RouteCriterion) Score(req *http.Request, candidate Tape) int { ... }
func (c RouteCriterion) Name() string { return "route" }

// QueryParamsCriterion matches on query parameters (subset match). Score: 4.
type QueryParamsCriterion struct{}

func (QueryParamsCriterion) Score(req *http.Request, candidate Tape) int { ... }
func (QueryParamsCriterion) Name() string { return "query_params" }

// HeadersCriterion matches a specific header key-value pair. Score: 3.
type HeadersCriterion struct {
    Key   string
    Value string
}

func (c HeadersCriterion) Score(req *http.Request, candidate Tape) int { ... }
func (c HeadersCriterion) Name() string { return "headers" }

// BodyHashCriterion matches on SHA-256 body hash. Score: 8.
type BodyHashCriterion struct{}

func (BodyHashCriterion) Score(req *http.Request, candidate Tape) int { ... }
func (BodyHashCriterion) Name() string { return "body_hash" }

// BodyFuzzyCriterion compares specific JSON fields in request bodies. Score: 6.
type BodyFuzzyCriterion struct {
    // Paths contains the JSONPath-like expressions to compare.
    Paths  []string
    parsed []parsedPath
}

// NewBodyFuzzyCriterion creates a BodyFuzzyCriterion with the given paths.
// Invalid paths are silently skipped (same behavior as the previous MatchBodyFuzzy).
func NewBodyFuzzyCriterion(paths ...string) *BodyFuzzyCriterion { ... }

func (c *BodyFuzzyCriterion) Score(req *http.Request, candidate Tape) int { ... }
func (c *BodyFuzzyCriterion) Name() string { return "body_fuzzy" }
```

##### Functions and methods

Exported functions and methods (new or changed):

| Function / Method | Signature | Notes |
|---|---|---|
| `NewPathRegexCriterion` | `func NewPathRegexCriterion(pattern string) (*PathRegexCriterion, error)` | Replaces `MatchPathRegex` |
| `NewBodyFuzzyCriterion` | `func NewBodyFuzzyCriterion(paths ...string) *BodyFuzzyCriterion` | Replaces `MatchBodyFuzzy` |
| `NewCompositeMatcher` | `func NewCompositeMatcher(criteria ...Criterion) *CompositeMatcher` | Signature changes from `...MatchCriterion` to `...Criterion` |
| `CriterionFunc.Score` | `func (f CriterionFunc) Score(req *http.Request, candidate Tape) int` | New |
| `CriterionFunc.Name` | `func (f CriterionFunc) Name() string` | New |
| `MethodCriterion.Score` | `func (MethodCriterion) Score(req *http.Request, candidate Tape) int` | New |
| `MethodCriterion.Name` | `func (MethodCriterion) Name() string` | New |
| `PathCriterion.Score` | `func (PathCriterion) Score(req *http.Request, candidate Tape) int` | New |
| `PathCriterion.Name` | `func (PathCriterion) Name() string` | New |
| `PathRegexCriterion.Score` | `func (c *PathRegexCriterion) Score(req *http.Request, candidate Tape) int` | New |
| `PathRegexCriterion.Name` | `func (c *PathRegexCriterion) Name() string` | New |
| `RouteCriterion.Score` | `func (c RouteCriterion) Score(req *http.Request, candidate Tape) int` | New |
| `RouteCriterion.Name` | `func (c RouteCriterion) Name() string` | New |
| `QueryParamsCriterion.Score` | `func (QueryParamsCriterion) Score(req *http.Request, candidate Tape) int` | New |
| `QueryParamsCriterion.Name` | `func (QueryParamsCriterion) Name() string` | New |
| `HeadersCriterion.Score` | `func (c HeadersCriterion) Score(req *http.Request, candidate Tape) int` | New |
| `HeadersCriterion.Name` | `func (c HeadersCriterion) Name() string` | New |
| `BodyHashCriterion.Score` | `func (BodyHashCriterion) Score(req *http.Request, candidate Tape) int` | New |
| `BodyHashCriterion.Name` | `func (BodyHashCriterion) Name() string` | New |
| `BodyFuzzyCriterion.Score` | `func (c *BodyFuzzyCriterion) Score(req *http.Request, candidate Tape) int` | New |
| `BodyFuzzyCriterion.Name` | `func (c *BodyFuzzyCriterion) Name() string` | New |

Removed:

| Function | Notes |
|---|---|
| `MatchMethod()` | Replaced by `MethodCriterion{}` |
| `MatchPath()` | Replaced by `PathCriterion{}` |
| `MatchPathRegex(pattern)` | Replaced by `NewPathRegexCriterion(pattern)` |
| `MatchRoute(route)` | Replaced by `RouteCriterion{Route: route}` |
| `MatchQueryParams()` | Replaced by `QueryParamsCriterion{}` |
| `MatchHeaders(key, value)` | Replaced by `HeadersCriterion{Key: key, Value: value}` |
| `MatchBodyHash()` | Replaced by `BodyHashCriterion{}` |
| `MatchBodyFuzzy(paths...)` | Replaced by `NewBodyFuzzyCriterion(paths...)` |

##### File layout

| File | Changes |
|---|---|
| `matcher.go` | Remove `MatchCriterion` type and all `Match*()` constructor functions. Add `Criterion` interface, `CriterionFunc` adapter, all eight criterion structs and their constructors. Update `CompositeMatcher` to use `[]Criterion`. Update `DefaultMatcher` to use struct literals. All criterion-related types stay in `matcher.go` per CLAUDE.md package structure convention. |
| `matcher_test.go` | Update all tests: replace `criterion(req, tape)` calls with `criterion.Score(req, tape)`. Replace `MatchMethod()` with `MethodCriterion{}`, etc. Replace `MatchCriterion(func(...) int { ... })` casts with `CriterionFunc(func(...) int { ... })`. Add `TestCriterionFunc_Name` and `TestCriterion_Names`. No behavioral changes to test assertions. |
| `proxy_test.go` | Line 429: update `NewCompositeMatcher(MatchMethod(), MatchPath(), MatchBodyHash())` to `NewCompositeMatcher(MethodCriterion{}, PathCriterion{}, BodyHashCriterion{})`. |
| `doc.go` | Update godoc references from `MatchCriterion` to `Criterion`, and from `MatchMethod`/`MatchPath` etc. to struct names. |
| `docs/api-reference.md` | Update the matcher criterion table to reflect struct types and `Criterion` interface. |
| `docs/matching.md` | Update examples and type references. |
| `CLAUDE.md` | Line 60: update comment from `MatchCriterion, CompositeMatcher, ExactMatcher` to `Criterion, CompositeMatcher, ExactMatcher`. |

No new files are created.

##### Sequence

This is a pure refactor with no behavioral changes. The request/response flow through
`CompositeMatcher.Match` is unchanged:

1. For each candidate tape, iterate over `m.criteria`.
2. Call `criterion.Score(req, tape)` (was `criterion(req, tape)`).
3. If score is 0, eliminate candidate (short-circuit).
4. Otherwise, accumulate score.
5. Return candidate with highest total score.

The only mechanical change is method dispatch (`criterion.Score(...)`) instead of
function call (`criterion(...)`).

##### Error cases

No new error cases are introduced. The only existing error case is regex compilation
in `NewPathRegexCriterion`, which preserves the `(*PathRegexCriterion, error)` return
signature from the current `MatchPathRegex`.

All other criteria remain infallible at construction time:
- `MethodCriterion{}`, `PathCriterion{}`, `QueryParamsCriterion{}`, `BodyHashCriterion{}` — zero-value structs, cannot fail.
- `RouteCriterion{Route: "..."}` — any string is valid.
- `HeadersCriterion{Key: "...", Value: "..."}` — any strings are valid; key is canonicalized at score time.
- `NewBodyFuzzyCriterion(paths...)` — invalid paths silently skipped (no error return), matching current behavior.

##### Test strategy

**Approach**: mechanical rename, no new test logic needed except for `Name()`.

1. **Individual criterion tests** (e.g., `TestMatchMethod`, `TestMatchPath`):
   - Replace `criterion := MatchMethod()` with `criterion := MethodCriterion{}`.
   - Replace `criterion(req, tape)` with `criterion.Score(req, tape)`.
   - For parameterized criteria: `MatchRoute(tt.route)` becomes `RouteCriterion{Route: tt.route}`.
   - For `MatchPathRegex`: `criterion, err := MatchPathRegex(...)` becomes `criterion, err := NewPathRegexCriterion(...)`.
   - For `MatchBodyFuzzy`: `criterion := MatchBodyFuzzy("$.action")` becomes `criterion := NewBodyFuzzyCriterion("$.action")`.
   - For `MatchHeaders`: `criterion := MatchHeaders("Accept", "application/json")` becomes `criterion := HeadersCriterion{Key: "Accept", Value: "application/json"}`.

2. **CompositeMatcher tests**: replace all `MatchMethod()`, `MatchPath()`, etc. with struct literals in `NewCompositeMatcher(...)` calls.

3. **Short-circuit test** (`TestCompositeMatcher_ShortCircuit`):
   - Replace `MatchCriterion(func(...) int { ... })` with `CriterionFunc(func(...) int { ... })`.

4. **Benchmark tests**: same mechanical rename pattern.

5. **New tests to add**:
   - `TestCriterionFunc_Name`: verify `CriterionFunc(...).Name()` returns `"custom"`.
   - `TestCriterion_Names`: table-driven test verifying each built-in criterion's `Name()` returns the expected string constant. This prevents regressions when #180 depends on these names for config dispatch.

6. **proxy_test.go**: one line change (line 429), mechanical rename.

7. All existing table-driven test structures and assertions remain identical. Score values are unchanged.

#### Consequences

**Benefits:**
- `Criterion` interface enables clean config-driven dispatch in #180 — a config
  `{"type": "body_fuzzy", "paths": [...]}` can resolve to `NewBodyFuzzyCriterion(paths...)`
  and verify via `Name()`.
- Struct types enable inspection (e.g., `PathRegexCriterion.Pattern` for debugging).
- Consistency with `Matcher` interface.
- `CriterionFunc` preserves the ability to create ad-hoc criteria from closures.

**Costs / trade-offs:**
- **Breaking change**: all external callers using `MatchMethod()`, `NewCompositeMatcher(MatchMethod(), ...)`, etc. must update. Mitigated by pre-1.0 status and zero known external consumers.
- **Slightly more verbose construction**: `MethodCriterion{}` vs `MatchMethod()`. The verbosity is minimal and the struct literal is actually more explicit about what is being constructed.
- **`Name()` method on every implementation**: small cost per struct (one-line method), justified by #180 requirements.
- **`HeadersCriterion` canonicalizes key at score time**: `http.CanonicalHeaderKey` is called per `Score()` invocation rather than once at construction. The function is cheap (simple ASCII case conversion) and the alternative (constructor with pre-canonicalization) would prevent struct-literal construction, which hurts config-driven ergonomics. If profiling shows this matters, a `NewHeadersCriterion` constructor can be added later without changing the interface.

**Documentation impact:**
- `doc.go`, `docs/api-reference.md`, `docs/matching.md`, `CLAUDE.md` reference the old function names and `MatchCriterion` type. These need updating as part of this PR.

**Future implications:**
- #180 (config-driven composition) can use `Name()` as a registry key and construct criteria from a `map[string]func(json.RawMessage) (Criterion, error)` factory.
- Additional criteria (future) implement the `Criterion` interface with their own `Name()` and `Score()`.

---


### ADR-39: Declarative matcher composition via JSON config

**Date**: 2026-04-18
**Issue**: #180
**Status**: Accepted

#### Context

Container consumers (Java/Kotlin/TS demos that pull the published Docker image) cannot
configure the httptape server's matcher beyond the hardcoded `DefaultMatcher()` (method +
path only). The upcoming Kotlin + Ktor + Koog agent demo requires body-aware matching to
distinguish multiple LLM calls to `POST /v1/chat/completions` that share the same method,
path, and headers, differing only in the `messages` JSON body array.

Per-criterion CLI flags (`--match-body-fuzzy`) were rejected by the PM because they do not
compose and lead to flag explosion as more criteria are added. The `Config` struct in
`config.go` already provides a versioned, JSON-Schema-validated declarative configuration
surface for sanitization rules. The `--config` flag already exists on `httptape serve`
(line 157 of `main.go`) but is currently unused by the serve command. Extending `Config`
with an optional `matcher` field reuses all existing infrastructure: parsing, validation,
schema, and loading.

This ADR builds on ADR-38 (issue #179), which promoted `MatchCriterion` from a function
type to a `Criterion` interface with `Score()` and `Name()` methods. The `Name()` method
on each criterion struct provides the registry key for config-driven dispatch.

#### Decision

##### Config struct extension

Add an optional top-level `Matcher` field to `Config`:

```go
// Config represents a declarative configuration for httptape.
// It can be loaded from JSON or constructed programmatically.
type Config struct {
    Version string         `json:"version"`
    Matcher *MatcherConfig `json:"matcher,omitempty"`
    Rules   []Rule         `json:"rules"`
}
```

The field is a pointer to `MatcherConfig` (not a value) so that `omitempty` works correctly
and `nil` vs zero-value is distinguishable. When `Matcher` is nil (field absent from JSON),
`BuildMatcher()` returns `DefaultMatcher()` -- no behavior change.

`Rules` remains required per the existing schema (`"required": ["version", "rules"]`).
However, `Validate()` must be updated to allow empty `Rules` when `Matcher` is present,
since a config file used purely for matcher composition (no sanitization) should be valid.
This is a relaxation: config files that only declare `matcher` and have `"rules": []` become
valid, while the existing behavior (rules-only configs) is unchanged.

**Correction on Rules requirement**: On further consideration, the simplest backward-
compatible approach is to keep `rules` required in the JSON schema (callers must pass
`"rules": []` at minimum) but relax the Go-side validation to allow an empty rules array
when a matcher is configured. This avoids changing the JSON schema's `required` array and
minimizes blast radius. Config files that only need matcher composition write `"rules": []`
explicitly -- a minor inconvenience that keeps the schema stable.

##### MatcherConfig type

```go
// MatcherConfig declares the composition of matching criteria for the replay server.
// It maps to a CompositeMatcher constructed via BuildMatcher.
type MatcherConfig struct {
    Criteria []CriterionConfig `json:"criteria"`
}
```

##### CriterionConfig type

A single struct with a `Type` discriminator and all possible type-specific fields.
Only the fields relevant to the given type are populated; irrelevant fields are
validated as absent.

```go
// CriterionConfig represents a single matching criterion in the declarative config.
// The Type field is the discriminator (matches Criterion.Name()).
// Type-specific fields are validated based on the Type value.
type CriterionConfig struct {
    Type  string   `json:"type"`
    Paths []string `json:"paths,omitempty"`
}
```

**Rationale for single-struct approach**: The three deserialization options were:

1. **Single struct with all possible fields, only relevant ones populated per type**
   (chosen). Cheap, simple, Go-idiomatic for a small number of type-specific fields.
   Validation enforces that irrelevant fields are not set. Adding a new criterion type
   that needs a new field means adding one field to `CriterionConfig` and one validation
   case -- a two-line change plus validation logic.

2. **Type-specific struct per criterion type with custom UnmarshalJSON**. More idiomatic
   in languages with sum types, but in Go this requires either a wrapper struct with
   `json.RawMessage` and a manual dispatch, or a custom `UnmarshalJSON` on the slice.
   The code volume is significantly higher for the same result, and it creates N types
   instead of 1.

3. **`map[string]any` for type-specific fields**. Flexible but completely untyped at
   the Go level. Every field access requires type assertions. Validation becomes manual
   and error-prone. This is what `Rule.Fields` uses for faker specs (because faker
   specs have high type diversity with 12+ types), but criterion configs have only 1
   type-specific field today (`paths`), making the cost/benefit of `map[string]any`
   unfavorable.

The single-struct approach is consistent with how `Rule` works in the existing config:
one struct with all possible fields, validated per `action` type. As the criterion
type count grows (future: `pattern` for `path_regex`, `key`/`value` for `headers`,
`route` for `route`), the struct gains one field each. If the field count ever becomes
unwieldy (unlikely -- criteria have at most 1-2 config fields each), a refactor to
option 2 can be done without changing the JSON schema.

##### Initial criterion types supported (scope of #180)

| `type` value | `Name()` match | Type-specific fields | Constructor call |
|---|---|---|---|
| `"method"` | `MethodCriterion.Name()` = `"method"` | none | `MethodCriterion{}` |
| `"path"` | `PathCriterion.Name()` = `"path"` | none | `PathCriterion{}` |
| `"body_fuzzy"` | `BodyFuzzyCriterion.Name()` = `"body_fuzzy"` | `paths` (required, non-empty) | `NewBodyFuzzyCriterion(paths...)` |

Out of scope for this issue: `path_regex`, `route`, `headers`, `query_params`,
`body_hash`. These can be added as follow-up issues by extending the dispatch
table and adding validation for their type-specific fields.

##### Factory function: BuildMatcher

```go
// BuildMatcher constructs a Matcher from the config's matcher declaration.
// If no matcher is configured (Matcher field is nil or has no criteria),
// it returns DefaultMatcher().
//
// BuildMatcher validates criterion types and their fields. It returns an error
// if any criterion type is unknown or required fields are missing/invalid.
//
// BuildMatcher assumes the config has been validated via Validate(). However,
// it performs its own materialization-time checks (e.g., for criteria that
// require runtime validation beyond shape checks).
func (c *Config) BuildMatcher() (Matcher, error)
```

The function constructs a `*CompositeMatcher` from the parsed criteria. It uses a
dispatch table keyed on the `Type` string:

```go
// criterionBuilders maps criterion type names to factory functions.
// Each factory validates type-specific fields and returns the constructed Criterion.
var criterionBuilders = map[string]func(CriterionConfig) (Criterion, error){
    "method":     buildMethodCriterion,
    "path":       buildPathCriterion,
    "body_fuzzy": buildBodyFuzzyCriterion,
}
```

Each builder function validates that only relevant fields are set and required fields
are present, then constructs the appropriate `Criterion` struct.

Error cases handled by `BuildMatcher`:
- Unknown criterion type: `fmt.Errorf("matcher: criteria[%d]: unknown type %q", i, cc.Type)`
- Missing required field: `fmt.Errorf("matcher: criteria[%d]: %q requires non-empty \"paths\"", i, cc.Type)`
- Irrelevant field set: `fmt.Errorf("matcher: criteria[%d]: %q does not use \"paths\"", i, cc.Type)`
- Empty criteria array: returns `DefaultMatcher(), nil` (no error, falls back to default)

##### Config.Validate extension

`Validate()` gains matcher validation alongside the existing rule validation:

1. If `c.Matcher` is nil, skip matcher validation (backward compatible).
2. If `c.Matcher` is non-nil, validate `c.Matcher.Criteria`:
   - If `Criteria` is empty, produce an error: `"matcher.criteria must be a non-empty array"`.
   - For each criterion config entry, validate:
     - `Type` is a recognized value (from `criterionBuilders` keys or a parallel `validCriterionTypes` set).
     - Type-specific field requirements (e.g., `body_fuzzy` requires non-empty `paths`).
     - Irrelevant fields are not set (e.g., `method` with `paths` produces an error).
     - All paths in `paths` are valid JSONPath-like syntax (reuse `parsePath`).
3. If `c.Matcher` is non-nil, relax the "rules must be non-empty" check: a config
   with a matcher but empty rules is valid (config used purely for matcher composition).
   The existing check `if len(c.Rules) == 0` is refined to
   `if len(c.Rules) == 0 && c.Matcher == nil`.

Validation errors from matcher config are accumulated into the same `errs` slice as
rule validation errors, following the existing pattern of collecting all errors before
returning.

The split between `Validate()` and `BuildMatcher()` is intentional:
- `Validate()` performs shape checks (type recognized, required fields present,
  irrelevant fields absent, path syntax valid).
- `BuildMatcher()` performs materialization checks (currently none beyond what
  `Validate()` covers, but future criteria like `path_regex` would compile the regex
  here and return a compile error).

##### JSON Schema extension (config.schema.json)

The schema gains a `matcher` property at the top level. The `additionalProperties: false`
constraint on the root object means we must add `matcher` to the properties map.

```json
{
  "matcher": {
    "type": "object",
    "description": "Optional matcher configuration for the replay server. Declares which criteria the CompositeMatcher uses to select recorded tapes.",
    "required": ["criteria"],
    "additionalProperties": false,
    "properties": {
      "criteria": {
        "type": "array",
        "minItems": 1,
        "description": "Ordered list of matching criteria. Each entry declares a criterion type and its type-specific fields.",
        "items": {
          "type": "object",
          "required": ["type"],
          "properties": {
            "type": {
              "type": "string",
              "enum": ["method", "path", "body_fuzzy"],
              "description": "Criterion type name. Must match a supported Criterion.Name() value."
            },
            "paths": {
              "type": "array",
              "items": {
                "type": "string",
                "pattern": "^\\$\\..+",
                "minLength": 3
              },
              "minItems": 1,
              "description": "JSONPath-like paths for body_fuzzy criterion. Required when type is body_fuzzy."
            }
          },
          "additionalProperties": false,
          "allOf": [
            {
              "if": {
                "properties": { "type": { "const": "body_fuzzy" } },
                "required": ["type"]
              },
              "then": {
                "required": ["type", "paths"]
              }
            },
            {
              "if": {
                "properties": { "type": { "enum": ["method", "path"] } },
                "required": ["type"]
              },
              "then": {
                "properties": {
                  "paths": false
                }
              }
            }
          ]
        }
      }
    }
  }
}
```

The `if/then` constructs enforce:
- `body_fuzzy` requires `paths`.
- `method` and `path` reject `paths`.

This approach uses `allOf` with `if/then` rather than `oneOf` because the criterion items
share most of their shape (only `paths` varies), making `oneOf` with three separate sub-schemas
unnecessarily repetitive. The `if/then` approach is cleaner and extends naturally as new
criterion types are added.

Additionally, the `rules` field's `minItems: 1` constraint must be removed from the
JSON schema, since rules can now be empty when a matcher is configured. This aligns
with the Go-side relaxation of the empty-rules validation.

##### CLI integration in cmd/httptape/main.go

`runServe` changes:

1. **Replace the dead `--config` flag** (currently `_ = fs.String("config", ...)`)
   with a live variable: `configPath := fs.String("config", "", "Path to httptape config JSON (matcher and sanitization rules)")`.

2. **After flag parsing**, if `*configPath != ""`:
   a. Load the config: `cfg, err := httptape.LoadConfigFile(*configPath)`.
   b. Build the matcher: `matcher, err := cfg.BuildMatcher()`.
   c. Append to server options: `serverOpts = append(serverOpts, httptape.WithMatcher(matcher))`.
   d. If `cfg.Rules` is non-empty, build the sanitization pipeline too (future-proofing
      for when serve mode supports sanitization, though currently serve mode does not
      sanitize). For now, the rules are simply ignored by serve mode -- the config is
      loaded and validated, but only the matcher portion is consumed.

3. **Remove the TODO comment** ("accepted but not used by serve").

4. **Update `--help` text**: The flag description changes from `"Path to sanitization
   config JSON (accepted but not used by serve)"` to `"Path to httptape config JSON
   (matcher and sanitization rules)"`.

5. **Error handling**: Config loading errors and `BuildMatcher` errors are returned
   as `fmt.Errorf("load config: %w", err)` (consistent with the `runRecord` pattern).

##### Backward compatibility

- **Existing config files** (sanitization-only, no `matcher` field) continue to work
  unchanged. The `matcher` field is optional (`omitempty` on the Go struct, not in
  `required` in the JSON schema). `BuildMatcher()` on a config with no matcher returns
  `DefaultMatcher()`.

- **Existing `serve` invocations** without `--config` are completely unchanged.
  `DefaultMatcher()` is still the default.

- **`LoadConfig` with `DisallowUnknownFields`**: The existing decoder uses
  `dec.DisallowUnknownFields()`. Adding the `Matcher` field to `Config` means JSON
  files containing `"matcher": {...}` will now decode successfully instead of failing
  with "unknown field". This is the desired behavior. Files without `"matcher"` continue
  to decode with `Matcher` as `nil`.

- **Record and proxy modes**: these modes already consume `--config` for sanitization
  only. Adding `Matcher` to `Config` does not break them. `BuildMatcher()` returns
  `DefaultMatcher()` when matcher config is absent. Record/proxy modes do not call
  `BuildMatcher()` -- they only call `BuildPipeline()`. No changes to `runRecord` or
  `runProxy` in this issue.

#### Types

| Type | File | Description |
|---|---|---|
| `MatcherConfig` | `config.go` | Declares matcher criteria composition. Contains `Criteria []CriterionConfig`. |
| `CriterionConfig` | `config.go` | Single criterion declaration with `Type` discriminator and type-specific fields (`Paths`). |

Modified type:

| Type | File | Change |
|---|---|---|
| `Config` | `config.go` | New optional field `Matcher *MatcherConfig` |

#### Functions and methods

| Function / Method | Signature | File | Notes |
|---|---|---|---|
| `Config.BuildMatcher` | `func (c *Config) BuildMatcher() (Matcher, error)` | `config.go` | New. Constructs `*CompositeMatcher` from config. Returns `DefaultMatcher()` when no matcher configured. |
| `Config.Validate` | `func (c *Config) Validate() error` | `config.go` | Modified. Adds matcher config validation. Relaxes empty-rules check when matcher is present. |

No new exported package-level functions. The `criterionBuilders` dispatch table and
individual builder functions (`buildMethodCriterion`, `buildPathCriterion`,
`buildBodyFuzzyCriterion`) are unexported.

#### File layout

| File | Changes |
|---|---|
| `config.go` | Add `MatcherConfig` and `CriterionConfig` types. Add `Matcher *MatcherConfig` field to `Config`. Add `BuildMatcher()` method. Extend `Validate()` with matcher validation. Add `criterionBuilders` dispatch table and builder functions. |
| `config_test.go` | Add tests for: config with no matcher (backward compat), config with valid matcher, config with unknown criterion type, config with `body_fuzzy` missing `paths`, config with `method` having spurious `paths`, config with empty criteria array, `BuildMatcher` returning `DefaultMatcher` for nil matcher, `BuildMatcher` constructing correct `CompositeMatcher`. |
| `config.schema.json` | Add `matcher` property with `criteria` array schema. Remove `minItems: 1` from `rules`. Add `if/then` constraints for per-type field validation. |
| `cmd/httptape/main.go` | Wire `--config` in `runServe`: load config, call `BuildMatcher()`, pass `WithMatcher(matcher)` to `NewServer`. Update flag description. Remove TODO comment. |
| `cmd/httptape/main_test.go` | Add test: serve with `--config` providing matcher config (write temp config file, verify exit code). Add test: serve with `--config` pointing to invalid config (verify exit code = `exitRuntime`). |

No new files. No changes to `matcher.go`, `server.go`, or any other existing files.

#### Sequence

Request/response flow when `--config` is provided to `httptape serve`:

1. CLI parses `--config <path>` and `--fixtures <dir>`.
2. `httptape.LoadConfigFile(path)` reads and parses the JSON. `DisallowUnknownFields` ensures no typos. `Validate()` checks version, rules (relaxed for empty when matcher present), and matcher criteria (type recognized, fields valid).
3. `cfg.BuildMatcher()` iterates `cfg.Matcher.Criteria`, dispatches each to its builder function via `criterionBuilders`, collects `[]Criterion`, returns `NewCompositeMatcher(criteria...)`. If `cfg.Matcher` is nil, returns `DefaultMatcher()`.
4. `serverOpts = append(serverOpts, httptape.WithMatcher(matcher))`.
5. `httptape.NewServer(store, serverOpts...)` creates the server with the custom matcher.
6. Server starts listening. Incoming requests are matched via the `CompositeMatcher` constructed from config.

Request matching at runtime (unchanged from existing `CompositeMatcher` behavior):

1. `Server.ServeHTTP` calls `s.matcher.Match(req, candidates)`.
2. `CompositeMatcher.Match` iterates candidates, scores each against all criteria.
3. If any criterion returns 0 for a candidate, that candidate is eliminated.
4. Highest-scoring surviving candidate is returned.

#### Error cases

| Error | Where | Handling |
|---|---|---|
| Config file not found / unreadable | `LoadConfigFile` | Returns `fmt.Errorf("config: open file: %w", err)`. CLI exits with `exitRuntime`. |
| Malformed JSON | `LoadConfig` | Returns `fmt.Errorf("config: invalid JSON: %w", err)`. |
| Unknown field in JSON | `LoadConfig` (via `DisallowUnknownFields`) | Returns `fmt.Errorf("config: invalid JSON: %w", err)`. |
| Unknown criterion type | `Validate` | Error: `"matcher.criteria[N]: unknown type \"foo\""`. |
| Missing required field (`body_fuzzy` without `paths`) | `Validate` | Error: `"matcher.criteria[N]: \"body_fuzzy\" requires non-empty \"paths\""`. |
| Irrelevant field set (`method` with `paths`) | `Validate` | Error: `"matcher.criteria[N]: \"method\" does not use \"paths\""`. |
| Invalid path syntax in `paths` | `Validate` | Error: `"matcher.criteria[N]: \"body_fuzzy\" invalid path syntax: \"bad\""`. |
| Empty criteria array | `Validate` | Error: `"matcher.criteria must be a non-empty array"`. |
| BuildMatcher on nil matcher | `BuildMatcher` | Returns `DefaultMatcher(), nil` (not an error). |

All errors from `Validate` are accumulated into a single error message (existing pattern).
`BuildMatcher` returns the first error encountered (since it performs materialization, not
shape validation). In practice, if `Validate` passes, `BuildMatcher` should not fail for
the three criterion types in scope (no materialization-time validation needed). Future
types like `path_regex` would have `BuildMatcher` return regex compilation errors.

#### Test strategy

All tests use stdlib `testing` only. Table-driven where appropriate.

**`config_test.go` -- new tests:**

1. **`TestLoadConfig_WithMatcher`**: Valid config with matcher + rules. Verify `Config.Matcher` is non-nil, `Criteria` has correct length and types.

2. **`TestLoadConfig_MatcherOnly`**: Config with matcher and empty rules (`"rules": []`). Verify validation passes. This confirms the relaxation of the empty-rules check.

3. **`TestLoadConfig_NoMatcher`**: Existing config without `matcher` field. Verify backward compatibility: `Config.Matcher` is nil, validation passes, `BuildMatcher()` returns a matcher equivalent to `DefaultMatcher()`.

4. **`TestLoadConfig_MatcherValidationErrors`**: Table-driven with cases:
   - Unknown criterion type -> error contains `"unknown type"`
   - `body_fuzzy` missing `paths` -> error contains `"requires non-empty \"paths\""`
   - `body_fuzzy` with empty `paths` array -> error contains `"requires non-empty \"paths\""`
   - `body_fuzzy` with invalid path syntax -> error contains `"invalid path syntax"`
   - `method` with spurious `paths` -> error contains `"does not use \"paths\""`
   - `path` with spurious `paths` -> error contains `"does not use \"paths\""`
   - Empty criteria array -> error contains `"must be a non-empty array"`
   - Missing `type` field -> error contains `"unknown type"` (empty string is unknown)

5. **`TestConfig_BuildMatcher_Default`**: `BuildMatcher()` on config with nil matcher returns a valid `Matcher` (not nil). Verify it matches on method + path (same as `DefaultMatcher`).

6. **`TestConfig_BuildMatcher_Composed`**: Config with `method` + `path` + `body_fuzzy` criteria. Build matcher, test against two tapes with same method/path but different bodies. Verify correct tape is selected.

7. **`TestConfig_BuildMatcher_UnknownType`**: `BuildMatcher()` on config with unknown criterion type (that somehow bypassed validation) returns an error. This tests `BuildMatcher`'s own error handling.

**`cmd/httptape/main_test.go` -- new tests:**

8. **`TestServeWithConfig`**: Write a temp config file with matcher config, invoke `run([]string{"serve", "--fixtures", tmpDir, "--config", configPath, "-h"})`, verify exit code is `exitOK`. This verifies the wiring works without actually starting a server.

9. **`TestServeWithInvalidConfig`**: Write a temp config file with invalid JSON, invoke `run(...)`, verify exit code is `exitRuntime`.

**Integration test (in `config_test.go` or `integration_test.go`):**

10. **`TestBuildMatcher_Integration`**: Create two tapes with the same method and path but different JSON bodies. Build a matcher from config with `method` + `path` + `body_fuzzy`. Construct HTTP requests matching each body. Verify the correct tape is returned for each request. This does not start an HTTP server -- it tests `BuildMatcher` output directly against `Match()`.

#### Alternatives considered

1. **Per-criterion CLI flags (`--match-body-fuzzy`)**: Rejected by PM. Does not compose.
   Each new criterion type would require a new flag with its own semantics. The existing
   `Config` JSON infrastructure already solves the composition problem.

2. **CriterionConfig with `json.RawMessage` and per-type structs**: More idiomatic for
   highly polymorphic types, but overkill when there is only one type-specific field
   (`paths`) across all three initial criterion types. Would require custom
   `UnmarshalJSON` on the criteria slice and 3+ additional types. The single-struct
   approach with per-type validation is simpler and equally correct.

3. **CriterionConfig with `map[string]any` for type-specific fields**: Maximum
   flexibility but zero type safety at the Go level. Every field access requires type
   assertions. This pattern is used for `Rule.Fields` (faker specs) because there are
   12+ faker types with diverse field shapes. For criteria (1 optional field today),
   the overhead of `map[string]any` is not justified.

4. **Named matcher presets (e.g., `"matcher": "body-aware"`)**: Rejected. Too opinionated.
   The criteria-array approach is composable and gives users full control. Presets could
   be added as sugar later without changing the underlying mechanism.

#### Consequences

**Benefits:**
- Container consumers can now configure matcher composition via a JSON config file
  mounted into the Docker container, without any Go code changes.
- The `--config` flag on `httptape serve` is no longer a dead flag. It controls both
  sanitization rules and matcher composition from a single file.
- Adding new criterion types to the config is a small, well-defined change: add one
  entry to the `criterionBuilders` dispatch table, one validation case in `Validate()`,
  and one `enum` value in the JSON schema. This is a two-minute change per criterion type.
- Criterion types not yet exposed via config (e.g., `path_regex`, `headers`) continue
  to work in Go code via direct struct construction. The config surface is additive.

**Costs / trade-offs:**
- The `CriterionConfig` struct will accumulate fields as more criterion types are
  exposed. This is manageable -- criteria have at most 1-2 config fields each.
- Config files that only need matcher composition must still include `"rules": []`
  because `rules` remains a required field in the JSON schema. This is a minor DX
  inconvenience that preserves backward compatibility.
- `Validate()` grows more complex with the matcher validation branch. The existing
  pattern of collecting all errors into `errs []string` scales well.

**Future implications:**
- Follow-up issues can expose `path_regex` (adds `pattern` field to `CriterionConfig`),
  `headers` (adds `key`/`value` fields), `route` (adds `route` field), `query_params`
  (no additional fields), and `body_hash` (no additional fields) by extending the
  dispatch table and validation logic.
- If `Config` eventually needs to support record-mode or proxy-mode specific settings,
  additional optional top-level fields can be added following the same `omitempty`
  pointer pattern used by `Matcher`.

---

### ADR-40: Kotlin + Ktor + Koog + Kotest demo (examples/kotlin-ktor-koog)

**Date**: 2026-04-18
**Issue**: #185
**Status**: Accepted

#### Context

The three matcher improvements -- #178 (vacuous-true fix), #179 (Criterion interface), and
#180 (declarative matcher composition via config) -- need an end-to-end proof artifact that
exercises all three together in a realistic multi-step agent scenario. Issue #185 specifies a
Kotlin + Ktor + Koog + Kotest demo where a single-tool AI agent makes two POST requests to the
same OpenAI URL (distinguished by JSON body content via `BodyFuzzyCriterion`) plus one body-less
GET request (coexisting via the vacuous-true fix), all served by one httptape container with a
declarative matcher config file.

This ADR resolves the 5 open questions from the PM spec and specifies the complete file layout,
dependency versions, build configuration, fixture format, test strategy, CI extension, and
dependabot coverage for `examples/kotlin-ktor-koog/`.

#### Open question resolutions

**Q1: Kotest BehaviorSpec vs FunSpec**

Decision: **BehaviorSpec**.

Rationale: The agent scenario has a natural Given/When/Then narrative ("Given a Koog agent
backed by httptape / When the user asks the weather-advice question / Then the response
streams the expected text"). BehaviorSpec's nested `Given`/`When`/`Then` blocks map directly
to this structure and produce readable test output. FunSpec is flatter and better for
independent test cases; the agent flow here is inherently sequential and narrative-driven.
The PM recommendation aligns with the use case.

**Q2: Koog tool HTTP client idiom**

Decision: **Ktor HttpClient (CIO engine)**.

Rationale: Koog's `ToolSet` tools return a `String` (or `TextObject`) -- the tool
implementation is ordinary Kotlin code. The tool's HTTP call to the weather API should use
Ktor's HttpClient (CIO engine) because: (1) the demo already depends on Ktor for the server,
so no new transitive dependency; (2) Ktor HttpClient is coroutine-native, matching Koog's
suspend-based tool execution; (3) the base URL can be injected via constructor parameter,
making test redirection straightforward. OkHttp would add an unnecessary dependency. Koog
does not provide its own HTTP client for tool implementations -- tools are user code.

**Q3: Multi-module vs single-module Gradle layout**

Decision: **Single module**.

Rationale: The demo has one application and one test class. Multi-module Gradle adds
`settings.gradle.kts` complexity (`include(":app")`, inter-module deps) for zero benefit.
The Java demo is also single-module. All source code lives under the standard
`src/main/kotlin/` and `src/test/kotlin/` trees within a single `build.gradle.kts`.

**Q4: Matcher config file location and container mount path**

Decision: `src/test/resources/httptape.config.json` in the source tree.
Mounted to `/config/httptape.config.json` inside the container via `withCopyFileToContainer`.
The `--config` CLI flag value is `/config/httptape.config.json`.

Rationale: Placing it under `src/test/resources/` makes it a classpath resource, accessible
via `Thread.currentThread().contextClassLoader.getResource("httptape.config.json")` for the
Testcontainers file-copy call. The `/config/` container path is distinct from `/fixtures/`
to avoid confusion. For the standalone `docker-compose.yml` path, the file is bind-mounted
from the project root-relative path `src/test/resources/httptape.config.json`.

**Q5: SSE fixture authoring strategy**

Decision: **Hand-craft following OpenAI's wire format, using the Java demo's
`chat-completion-headphones.json` as the structural template**.

Rationale: (a) Capturing from real OpenAI requires an API key, costs money, and produces
non-deterministic content that would need manual editing anyway. (b) Hand-crafting ensures
every field is intentional and documented. (c) The Java demo's existing SSE fixture already
demonstrates the correct `chat.completion.chunk` JSON structure with `offset_ms` timing and
the `[DONE]` sentinel. The two new fixtures (`chat-1.json` and `chat-2.json`) adapt this
template for tool-call deltas: `chat-1.json` uses `delta.tool_calls` (with function name and
arguments streamed across chunks), `chat-2.json` uses `delta.content` (same as the Java demo).
The OpenAI API reference for streaming tool calls documents the exact `tool_calls` delta
shape: `[{"index":0,"id":"call_xxx","type":"function","function":{"name":"getWeather","arguments":""}}]`
in the first chunk, then `[{"index":0,"function":{"arguments":"..."}}]` in subsequent chunks
streaming the JSON arguments.

#### Decision

##### Verified dependency versions

All versions verified against Maven Central / GitHub Releases / Gradle releases as of
2026-04-18. No assumptions.

| Dependency | Version | Source |
|---|---|---|
| Kotlin | 2.3.20 | GitHub Releases (v2.3.20, 2026-03-16, stable) |
| JDK | 25 | Matches Java demo |
| Ktor | 3.4.2 | GitHub Releases (2026-03-30, stable) |
| Koog | 0.8.0 | Maven Central + GitHub Releases (2026-04-11, stable). License: Apache 2.0. |
| Kotest | 6.1.11 | GitHub Releases + Maven Central (2026-04-04, stable) |
| Kotest Testcontainers Extension | 2.0.2 | Maven Central |
| Testcontainers | 2.0.4 | GitHub Releases + Maven Central (2026-03-19, stable) |
| Gradle | 9.4.1 | gradle.org/releases (current) |
| Logback | 1.5.13 | Maven Central (SLF4J runtime for Ktor) |
| kotlinx-serialization | 1.10.0 | Matches Koog 0.8.0 transitive |
| kotlinx-coroutines | 1.10.2 | Matches Koog 0.8.0 transitive |

**Ktor version note**: Koog 0.8.0 was built against Ktor 3.2.2. Using Ktor 3.4.2 is safe
because Ktor 3.x maintains binary compatibility across minor versions, and Gradle dependency
resolution will unify all Ktor artifacts to the highest requested version (3.4.2).

**Kotlin version note**: Koog 0.8.0 was built with Kotlin 2.3.10. Kotlin 2.3.20 is
backward-compatible within the same minor. No issues expected.

##### Directory layout

```
examples/kotlin-ktor-koog/
  .gitignore
  api.http
  build.gradle.kts
  docker-compose.yml
  Dockerfile
  gradle/
    wrapper/
      gradle-wrapper.jar
      gradle-wrapper.properties
  gradlew
  gradlew.bat
  README.md
  settings.gradle.kts
  src/
    main/
      kotlin/
        dev/
          httptape/
            demo/
              Application.kt
              WeatherAdviceRoute.kt
              WeatherAgent.kt
              WeatherTools.kt
              AppConfig.kt
      resources/
        application.yaml
        logback.xml
    test/
      kotlin/
        dev/
          httptape/
            demo/
              HttptapeContainer.kt
              WeatherAdviceTest.kt
      resources/
        fixtures/
          openai/
            chat-1.json
            chat-2.json
          weather/
            weather-berlin.json
        httptape.config.json
```

##### File descriptions

**`settings.gradle.kts`**

```kotlin
rootProject.name = "kotlin-ktor-koog-demo"
```

Single module. No `include()` statements.

**`build.gradle.kts`** — sketch of every dependency:

```kotlin
plugins {
    kotlin("jvm") version "2.3.20"
    kotlin("plugin.serialization") version "2.3.20"
    application
}

group = "dev.httptape"
version = "0.0.1-SNAPSHOT"

application {
    mainClass.set("dev.httptape.demo.ApplicationKt")
}

kotlin {
    jvmToolchain(25)
}

repositories {
    mavenCentral()
}

dependencies {
    // Ktor server
    implementation("io.ktor:ktor-server-netty:3.4.2")
    implementation("io.ktor:ktor-server-sse:3.4.2")
    implementation("io.ktor:ktor-server-content-negotiation:3.4.2")
    implementation("io.ktor:ktor-serialization-kotlinx-json:3.4.2")

    // Ktor client (for the weather tool's REST call)
    implementation("io.ktor:ktor-client-cio:3.4.2")
    implementation("io.ktor:ktor-client-content-negotiation:3.4.2")

    // Koog — AI agent framework (Ktor plugin + agents)
    implementation("ai.koog:koog-ktor:0.8.0")

    // kotlinx-serialization (Koog transitive, but explicit for clarity)
    implementation("org.jetbrains.kotlinx:kotlinx-serialization-json:1.10.0")

    // Logging
    implementation("ch.qos.logback:logback-classic:1.5.13")

    // Test — Kotest
    testImplementation("io.kotest:kotest-runner-junit5:6.1.11")
    testImplementation("io.kotest:kotest-assertions-core:6.1.11")

    // Test — Testcontainers
    testImplementation("org.testcontainers:testcontainers:2.0.4")
    testImplementation("io.kotest.extensions:kotest-extensions-testcontainers:2.0.2")

    // Test — Ktor client for issuing requests to the app under test
    testImplementation("io.ktor:ktor-client-cio:3.4.2")
    testImplementation("io.ktor:ktor-client-content-negotiation:3.4.2")
}

tasks.withType<Test>().configureEach {
    useJUnitPlatform()
}
```

**`Application.kt`** — Ktor entry point:

- Starts an embedded Netty server on port 8080 (configurable via env var `PORT`).
- Installs `ContentNegotiation` with `kotlinx.serialization.json`.
- Installs `SSE` plugin.
- Installs the `Koog` plugin with OpenAI configuration. The OpenAI `baseUrl` and `apiKey`
  are read from `application.yaml` (with env-var overrides):
  ```kotlin
  install(Koog) {
      llm {
          openAI(apiKey = environment.config.property("koog.openai.api-key").getString()) {
              baseUrl = environment.config.property("koog.openai.base-url").getString()
          }
      }
  }
  ```
- Registers the `/weather-advice` route.

**`application.yaml`**:

```yaml
ktor:
  deployment:
    port: 8080

koog:
  openai:
    base-url: ${OPENAI_BASE_URL:-https://api.openai.com}
    api-key: ${OPENAI_API_KEY:-sk-placeholder}

weather:
  base-url: ${WEATHER_BASE_URL:-https://wttr.in}
```

**`logback.xml`** — standard Logback configuration with INFO level for the demo, DEBUG
for `dev.httptape.demo`.

**`WeatherAdviceRoute.kt`** — defines the `GET /weather-advice` Ktor route:

```kotlin
fun Route.weatherAdviceRoute() {
    get("/weather-advice") {
        val city = call.parameters["city"] ?: return@get call.respondText(
            "Missing 'city' query parameter", status = HttpStatusCode.BadRequest
        )
        // Create agent with the streaming strategy and run it
        val weatherBaseUrl = application.environment.config.property("weather.base-url").getString()
        val weatherTools = WeatherTools(weatherBaseUrl)
        val toolRegistry = ToolRegistry {
            tools(weatherTools.asTools())
        }

        call.respondSse {
            val result = aiAgent(
                ToolCalls.SINGLE_RUN_SEQUENTIAL,
                model = OpenAIModels.CostOptimized.GPT4oMini,
                tools = toolRegistry
            ) { agent -> agent.run("What's the weather in $city? Should I bring an umbrella?") }

            send(ServerSentEvent(data = result))
        }
    }
}
```

Note: The exact SSE streaming approach depends on whether the dev uses Koog's streaming
strategy or the simpler `singleRunStrategy`. For the demo, `singleRunStrategy` with
`SINGLE_RUN_SEQUENTIAL` tool calls is sufficient -- the agent runs the full
request-tool-response loop internally and returns the final string. The route then sends
that string as an SSE event. This is simpler than wiring up Koog's graph-based streaming
strategy for a demo. The dev may adjust the streaming granularity (per-token vs. final
result) based on what works cleanly with Koog's API.

**`WeatherAgent.kt`** — this file may be merged into `WeatherAdviceRoute.kt` if the agent
construction is simple enough (as shown above using `aiAgent()` from the Koog Ktor plugin).
The architect leaves the dev discretion to split or merge. The key constraint is that the
agent must use `OpenAIModels.CostOptimized.GPT4oMini` (or similar) as the model and the
`WeatherTools` tool set.

**`WeatherTools.kt`** — defines the Koog tool set:

```kotlin
class WeatherTools(private val baseUrl: String) : ToolSet {

    private val client = HttpClient(CIO) {
        install(ContentNegotiation) {
            json(Json { ignoreUnknownKeys = true })
        }
    }

    @Tool
    @LLMDescription("Get the current weather forecast for a city")
    suspend fun getWeather(
        @LLMDescription("The city name to get weather for")
        city: String
    ): String {
        val response: JsonObject = client.get("$baseUrl/v1/forecast") {
            parameter("city", city)
        }.body()
        return response.toString()
    }
}
```

Key design points:
- The `baseUrl` is injected via constructor, allowing tests to redirect to httptape.
- The Ktor HttpClient with CIO engine is coroutine-native.
- The weather API path is `/v1/forecast?city=Berlin` (a fictional simplified endpoint rather
  than wttr.in's actual `/{city}?format=j1` path). This makes the matcher config cleaner:
  the path `/v1/forecast` is distinct from `/v1/chat/completions`, and query parameters
  are not part of path matching. Using wttr.in's actual path (`/Berlin?format=j1`) would
  require path-regex matching since the city is embedded in the path, which adds unnecessary
  complexity. The fixture response shape mimics wttr.in's JSON structure.

**`AppConfig.kt`** — optional. If configuration wiring beyond `application.yaml` is needed,
this file holds it. The dev may fold everything into `Application.kt` if it stays simple.

**`HttptapeContainer.kt`** — Testcontainers setup (Kotlin equivalent of the Java demo's
`TestcontainersConfig.java`):

```kotlin
object HttptapeContainer {
    val instance: GenericContainer<*> by lazy {
        GenericContainer("ghcr.io/vibewarden/httptape:0.10.1")
            .withCommand(
                "serve",
                "--fixtures", "/fixtures",
                "--config", "/config/httptape.config.json",
                "--sse-timing=realtime"
            )
            .withExposedPorts(8081)
            .waitingFor(Wait.forHttp("/").forStatusCode(404))
            .apply {
                // Mount fixtures — flatten subdirectories into /fixtures/
                copyFixture("fixtures/openai/chat-1.json")
                copyFixture("fixtures/openai/chat-2.json")
                copyFixture("fixtures/weather/weather-berlin.json")
                // Mount matcher config
                withCopyFileToContainer(
                    MountableFile.forClasspathResource("httptape.config.json"),
                    "/config/httptape.config.json"
                )
            }
            .also { it.start() }
    }

    val baseUrl: String
        get() = "http://${instance.host}:${instance.getMappedPort(8081)}"

    private fun GenericContainer<*>.copyFixture(classpathPath: String) {
        val filename = classpathPath.substringAfterLast("/")
        withCopyFileToContainer(
            MountableFile.forClasspathResource(classpathPath),
            "/fixtures/$filename"
        )
    }
}
```

Design rationale:
- Uses a Kotlin `object` with `lazy` initialization for JVM-singleton lifecycle (same
  container shared across all test classes, same pattern as the Java demo's
  `@TestConfiguration` with `@Bean` scope).
- Fixtures are enumerated explicitly rather than using classpath scanning (unlike the Java
  demo's `PathMatchingResourcePatternResolver`). Kotlin has no stdlib equivalent of Spring's
  classpath-glob scanner. For 3 fixtures, explicit enumeration is clearer than rolling a
  custom scanner. If the fixture count grows, the dev can add a simple utility that reads
  a manifest file or walks a resource directory.
- The `--config` flag points to `/config/httptape.config.json` inside the container.

**`WeatherAdviceTest.kt`** — Kotest BehaviorSpec:

```kotlin
class WeatherAdviceTest : BehaviorSpec({
    // Boot httptape once per JVM, then start a Ktor test server
    // with base URLs pointing at the httptape container.

    given("a Koog agent backed by httptape") {
        `when`("the user asks for weather advice for Berlin") {
            then("the response contains weather-based advice") {
                // 1. Start embedded Ktor server with:
                //    - koog.openai.base-url = HttptapeContainer.baseUrl
                //    - weather.base-url = HttptapeContainer.baseUrl
                //    - koog.openai.api-key = "sk-test-key"
                // 2. Issue GET /weather-advice?city=Berlin via Ktor test client
                // 3. Read SSE response
                // 4. Assert response contains "umbrella" (from chat-2.json fixture)
            }
        }
    }
})
```

The test uses Ktor's `testApplication { }` block (from `io.ktor.server.testing`) to start an
in-process server with overridden config properties. The Koog plugin inside the test server
connects to httptape (acting as both OpenAI and the weather API) via the dynamic base URL.

Alternative: instead of `testApplication`, the test could start a real Netty server on a
random port and hit it with an HTTP client. The dev should pick whichever approach integrates
more cleanly with Koog's Ktor plugin lifecycle. The `testApplication` approach is preferred
because it avoids port binding and is faster.

**Fixture structure:**

**`chat-1.json`** — POST /v1/chat/completions, response with tool_call:

```json
{
  "id": "chat-1-tool-call",
  "route": "",
  "recorded_at": "2026-04-18T10:00:00Z",
  "request": {
    "method": "POST",
    "url": "/v1/chat/completions",
    "headers": {
      "Content-Type": ["application/json"]
    },
    "body": "<JSON with messages=[{role:system,...},{role:user,...}]>",
    "body_hash": ""
  },
  "response": {
    "status_code": 200,
    "headers": {
      "Content-Type": ["text/event-stream"],
      "Cache-Control": ["no-cache"]
    },
    "body": null,
    "sse_events": [
      {
        "offset_ms": 50,
        "data": "{\"id\":\"chatcmpl-tc1\",\"object\":\"chat.completion.chunk\",\"created\":1713264000,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":null,\"tool_calls\":[{\"index\":0,\"id\":\"call_weather_1\",\"type\":\"function\",\"function\":{\"name\":\"getWeather\",\"arguments\":\"\"}}]},\"finish_reason\":null}]}"
      },
      {
        "offset_ms": 120,
        "data": "{\"id\":\"chatcmpl-tc1\",\"object\":\"chat.completion.chunk\",\"created\":1713264000,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"city\\\":\\\"Berlin\\\"}\"}}]},\"finish_reason\":null}]}"
      },
      {
        "offset_ms": 200,
        "data": "{\"id\":\"chatcmpl-tc1\",\"object\":\"chat.completion.chunk\",\"created\":1713264000,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}"
      },
      {
        "offset_ms": 250,
        "data": "[DONE]"
      }
    ]
  }
}
```

Key points:
- The request body contains `messages` with roles `["system", "user"]` only. This is what
  the matcher uses to distinguish this from `chat-2.json`.
- The SSE response streams tool_call deltas in OpenAI's exact wire format: first chunk has
  the full `tool_calls` array entry with `id`, `type`, `function.name`, and empty
  `arguments`; second chunk appends to `arguments`; third chunk has `finish_reason: "tool_calls"`.
- `finish_reason` is `"tool_calls"` (not `"stop"`) -- this signals Koog to execute the tool.
- The request body field is populated with a minimal but valid JSON that includes the
  `messages` array with `system` and `user` roles. The exact content does not matter for
  matching (only `$.messages[*].role` is checked), but it must be valid JSON.

**`weather-berlin.json`** — GET /v1/forecast?city=Berlin:

```json
{
  "id": "weather-berlin",
  "route": "",
  "recorded_at": "2026-04-18T10:00:01Z",
  "request": {
    "method": "GET",
    "url": "/v1/forecast?city=Berlin",
    "headers": {},
    "body": null,
    "body_hash": ""
  },
  "response": {
    "status_code": 200,
    "headers": {
      "Content-Type": ["application/json"]
    },
    "body": "eyJjaXR5IjoiQmVybGluIiwidGVtcCI6MTIsImNvbmRpdGlvbiI6InJhaW4iLCJodW1pZGl0eSI6ODIsIndpbmRfa3BoIjoxNSwiZmVlbHNfbGlrZSI6OX0=",
    "body_hash": ""
  }
}
```

Notes:
- `body` is base64-encoded JSON: `{"city":"Berlin","temp":12,"condition":"rain","humidity":82,"wind_kph":15,"feels_like":9}`.
  The dev should check whether httptape expects base64 or raw JSON in the `body` field and
  use the correct encoding. The Java demo's `get-user-1.json` fixture shows the convention.
- This is a body-less GET request. The `BodyFuzzyCriterion` returns vacuous-true (score 1)
  for this tape thanks to #178, keeping it alive in the composite matcher.

**`chat-2.json`** — POST /v1/chat/completions, follow-up with tool result:

```json
{
  "id": "chat-2-final-answer",
  "route": "",
  "recorded_at": "2026-04-18T10:00:02Z",
  "request": {
    "method": "POST",
    "url": "/v1/chat/completions",
    "headers": {
      "Content-Type": ["application/json"]
    },
    "body": "<JSON with messages=[{role:system},{role:user},{role:assistant,tool_calls:[...]},{role:tool,content:weather_json}]>",
    "body_hash": ""
  },
  "response": {
    "status_code": 200,
    "headers": {
      "Content-Type": ["text/event-stream"],
      "Cache-Control": ["no-cache"]
    },
    "body": null,
    "sse_events": [
      { "offset_ms": 50, "data": "...chunk with delta.role=assistant..." },
      { "offset_ms": 120, "data": "...chunk with delta.content='Based'..." },
      { "offset_ms": 200, "data": "...chunk with delta.content=' on'..." },
      { "offset_ms": 280, "data": "...chunk with delta.content=' the'..." },
      { "offset_ms": 360, "data": "...chunk with delta.content=' weather'..." },
      { "offset_ms": 440, "data": "...chunk with delta.content=' in'..." },
      { "offset_ms": 520, "data": "...chunk with delta.content=' Berlin'..." },
      { "offset_ms": 600, "data": "...chunk with delta.content=','..." },
      { "offset_ms": 680, "data": "...chunk with delta.content=' yes'..." },
      { "offset_ms": 760, "data": "...chunk with delta.content=','..." },
      { "offset_ms": 840, "data": "...chunk with delta.content=' bring'..." },
      { "offset_ms": 920, "data": "...chunk with delta.content=' an'..." },
      { "offset_ms": 1000, "data": "...chunk with delta.content=' umbrella'..." },
      { "offset_ms": 1080, "data": "...chunk with delta.content='.'..." },
      { "offset_ms": 1160, "data": "...chunk with finish_reason=stop..." },
      { "offset_ms": 1200, "data": "[DONE]" }
    ]
  }
}
```

Key points:
- The request body contains `messages` with roles `["system", "user", "assistant", "tool"]`.
  The `BodyFuzzyCriterion` on `$.messages[*].role` distinguishes this from `chat-1.json`
  (which has only `["system", "user"]`).
- The SSE response streams the final natural-language answer.
- The assembled content includes the word "umbrella" for test assertion.
- Each `data` field must be a full `chat.completion.chunk` JSON object following the Java
  demo's template. The `...` placeholders above are for illustration -- the dev must write
  out the full JSON for each event.

**`httptape.config.json`** — matcher configuration:

```json
{
  "version": "1",
  "matcher": {
    "criteria": [
      { "type": "method" },
      { "type": "path" },
      { "type": "body_fuzzy", "paths": ["$.messages[*].role"] }
    ]
  },
  "rules": []
}
```

This is the linchpin of the demo. It declares a `CompositeMatcher` with three criteria:
1. `method` — distinguishes GET (weather) from POST (OpenAI).
2. `path` — distinguishes `/v1/forecast` from `/v1/chat/completions`.
3. `body_fuzzy` on `$.messages[*].role` — distinguishes the two POST requests to
   `/v1/chat/completions` by their message roles.

For the GET `/v1/forecast` request, `body_fuzzy` returns vacuous-true (score 1) because
both the incoming request and the weather tape have no body. This is the #178 fix in action.

**`Dockerfile`** — multi-stage build:

```dockerfile
# Multi-stage build: Gradle build -> JRE 25 runtime
FROM eclipse-temurin:25-jdk AS build
WORKDIR /app
COPY gradle/ gradle/
COPY gradlew build.gradle.kts settings.gradle.kts ./
# Download dependencies first (layer caching)
RUN ./gradlew dependencies --no-daemon
COPY src/ src/
RUN ./gradlew build -x test --no-daemon

FROM eclipse-temurin:25-jre
WORKDIR /app
COPY --from=build /app/build/libs/*-all.jar app.jar
EXPOSE 8080
ENTRYPOINT ["java", "-jar", "app.jar"]
```

Note: the `application` plugin with `mainClass` set produces a runnable JAR. The dev may
need to configure the `shadowJar` or `jar` task depending on whether Ktor's fat-JAR or
distribution packaging is used. Alternatively, the dev can use the Ktor Gradle plugin for
packaging. The key requirement is that the Dockerfile produces a runnable image.

**`docker-compose.yml`**:

```yaml
services:
  httptape:
    image: ghcr.io/vibewarden/httptape:0.10.1
    command: ["serve", "--fixtures", "/fixtures", "--config", "/config/httptape.config.json", "--sse-timing=realtime"]
    volumes:
      - ./src/test/resources/fixtures/openai/chat-1.json:/fixtures/chat-1.json:ro
      - ./src/test/resources/fixtures/openai/chat-2.json:/fixtures/chat-2.json:ro
      - ./src/test/resources/fixtures/weather/weather-berlin.json:/fixtures/weather-berlin.json:ro
      - ./src/test/resources/httptape.config.json:/config/httptape.config.json:ro

  app:
    build: .
    ports:
      - "8080:8080"
    environment:
      - OPENAI_BASE_URL=http://httptape:8081
      - WEATHER_BASE_URL=http://httptape:8081
      - OPENAI_API_KEY=sk-placeholder
    depends_on:
      - httptape
```

Key difference from the Java demo: the `--config` flag is passed to httptape, mounting the
matcher config file into the container. The Java demo does not use `--config` because its
fixtures are method+path-unique (no two fixtures share method+path). This demo requires it.

**`api.http`**:

```
### Streaming weather advice (SSE)
GET http://localhost:8080/weather-advice?city=Berlin
Accept: text/event-stream
```

One request, mirroring the Java demo's pattern.

**`.gitignore`**:

```
# === Gradle ===
.gradle/
build/
!gradle/wrapper/gradle-wrapper.jar

# === httptape ===
.httptape-cache/

# === IntelliJ IDEA ===
.idea/
*.iml
*.ipr
*.iws
out/

# === Editor swap files ===
*.swp
*.swo
*~

# === OS files ===
.DS_Store
Thumbs.db
```

**`README.md`** — section headings (content to be written by dev):

1. **Title**: "Test your Koog AI agents deterministically"
2. **What this demo shows**: Single-tool Koog agent + mocked weather REST, tested E2E with httptape.
3. **Headline scenario**: The wire-level trace from the PM spec.
4. **Matcher composition callout**: Explicit callout that the demo exercises the matcher
   composition from #178/#179/#180 -- distinguishing two same-URL POST requests via
   `body_fuzzy` on `$.messages[*].role` plus a body-less GET via vacuous-true.
5. **Prerequisites**: Docker + JDK 25.
6. **Quick start**: `./gradlew test`
7. **Adding a new fixture**: Drop JSON in fixtures dir, update `HttptapeContainer.kt`.
8. **Dev workflow**: `./gradlew run` with httptape started via docker-compose or Testcontainers.
9. **Testcontainers reuse opt-in**: Same as Java demo.
10. **Try it standalone**: `docker compose up -d` + `curl`.
11. **Stack table**: Kotlin 2.3.20, JDK 25, Ktor 3.4.2, Koog 0.8.0, Kotest 6.1.11,
    Testcontainers 2.0.4, Gradle 9.4.1.
12. **Why not...?** table:

| Alternative | Limitation |
|---|---|
| **WireMock** | No native SSE record/replay. No sanitize-on-write. |
| **Koog's built-in mocks (`getMockExecutor`, `mockTool`)** | Mocks the LLM at executor level, skipping the HTTP layer entirely. You are not testing that your agent works with a real OpenAI-compatible endpoint. httptape tests the full HTTP integration -- serialization, headers, SSE parsing -- same as production. |
| **Real OpenAI calls in tests** | Slow, flaky, costs money, leaks PII into CI logs. |
| **Manual Ktor test stubs** | Skips the HTTP layer. Breaks when the API contract changes. |

##### Dependabot configuration

Append two blocks to `.github/dependabot.yml`:

```yaml
  # ----- Example: kotlin-ktor-koog (Gradle) -----
  - package-ecosystem: "gradle"
    directory: "/examples/kotlin-ktor-koog"
    schedule:
      interval: "weekly"
      day: "monday"
      time: "09:00"
      timezone: "Etc/UTC"
    labels:
      - "dependencies"
      - "java"
    commit-message:
      prefix: "chore"
      prefix-development: "chore"
      include: "scope"
    groups:
      minor-and-patch:
        applies-to: version-updates
        update-types:
          - "minor"
          - "patch"

  # ----- Example: kotlin-ktor-koog (Dockerfile, eclipse-temurin) -----
  - package-ecosystem: "docker"
    directory: "/examples/kotlin-ktor-koog"
    schedule:
      interval: "weekly"
      day: "monday"
      time: "09:00"
      timezone: "Etc/UTC"
    labels:
      - "dependencies"
      - "docker"
    groups:
      minor-and-patch:
        applies-to: version-updates
        update-types:
          - "minor"
          - "patch"
```

Uses `"java"` label (not `"kotlin"`) to match the existing Java demo pattern -- dependabot
labels are per-ecosystem, and `"java"` covers all JVM languages using Gradle/Maven.

##### CI workflow extension

Add one matrix entry to `.github/workflows/examples.yml`:

```yaml
          - name: kotlin-ktor-koog
            path: examples/kotlin-ktor-koog
            java-version: "25"
            build-command: ./gradlew test --no-daemon
```

The existing workflow already has `setup-java` conditional logic (`if: matrix.example.java-version`)
and uses `cache: maven` for Maven. For Gradle, the cache key needs to be different. The CI
step should be extended to support Gradle caching:

```yaml
      # --- Java toolchain (JVM examples) ---
      - name: Set up JDK
        if: matrix.example.java-version
        uses: actions/setup-java@v4
        with:
          distribution: temurin
          java-version: ${{ matrix.example.java-version }}
          cache: ${{ matrix.example.build-tool || 'maven' }}
```

The Kotlin matrix entry adds `build-tool: gradle` to enable Gradle caching via `setup-java`.
Existing Java entry gets `build-tool: maven` (or omits it, defaulting to `maven`). The dev
should verify that `actions/setup-java@v4`'s `cache: gradle` option works with the Gradle
wrapper committed in the example directory.

Alternative (simpler): use `actions/setup-java@v4` with `cache: gradle` and add
`cache-dependency-path: examples/kotlin-ktor-koog/build.gradle.kts` to scope the cache.

#### Types

No new Go types. This ADR specifies a Kotlin demo application -- all types are Kotlin
classes and objects within the `examples/kotlin-ktor-koog/` directory.

#### Functions and methods

No new Go functions. The demo exercises existing httptape functionality through the Docker
container and `--config` flag.

#### File layout

**New files (all under `examples/kotlin-ktor-koog/`):**

| File | Purpose |
|---|---|
| `.gitignore` | Gradle, IntelliJ, OS patterns |
| `api.http` | One SSE request for IDE HTTP client |
| `build.gradle.kts` | Gradle build with all dependencies |
| `docker-compose.yml` | Standalone demo path |
| `Dockerfile` | Multi-stage JDK 25 build |
| `README.md` | Demo documentation |
| `settings.gradle.kts` | Project name |
| `gradle/wrapper/*` | Gradle wrapper (committed) |
| `gradlew`, `gradlew.bat` | Gradle wrapper scripts |
| `src/main/kotlin/.../Application.kt` | Ktor entry point with Koog plugin |
| `src/main/kotlin/.../WeatherAdviceRoute.kt` | GET /weather-advice route |
| `src/main/kotlin/.../WeatherTools.kt` | Koog ToolSet with getWeather |
| `src/main/kotlin/.../AppConfig.kt` | Optional config wiring |
| `src/main/resources/application.yaml` | Ktor + Koog config |
| `src/main/resources/logback.xml` | Logging config |
| `src/test/kotlin/.../HttptapeContainer.kt` | Testcontainers singleton |
| `src/test/kotlin/.../WeatherAdviceTest.kt` | BehaviorSpec test |
| `src/test/resources/fixtures/openai/chat-1.json` | OpenAI tool-call SSE fixture |
| `src/test/resources/fixtures/openai/chat-2.json` | OpenAI final-answer SSE fixture |
| `src/test/resources/fixtures/weather/weather-berlin.json` | Weather REST JSON fixture |
| `src/test/resources/httptape.config.json` | Matcher composition config |

**Modified files (existing repo files):**

| File | Change |
|---|---|
| `.github/dependabot.yml` | Add `gradle` + `docker` blocks for `kotlin-ktor-koog` |
| `.github/workflows/examples.yml` | Add matrix entry + Gradle cache support |

#### Sequence

The end-to-end flow during `./gradlew test`:

1. Kotest discovers `WeatherAdviceTest` and starts executing the BehaviorSpec.
2. The test accesses `HttptapeContainer.instance`, triggering lazy initialization.
3. `HttptapeContainer` creates a `GenericContainer` with `ghcr.io/vibewarden/httptape:0.10.1`,
   copies the 3 fixture files into `/fixtures/` and the config into `/config/`, then starts
   the container with `serve --fixtures /fixtures --config /config/httptape.config.json --sse-timing=realtime`.
4. httptape loads the config, calls `BuildMatcher()` which constructs a `CompositeMatcher`
   with `MethodCriterion{}`, `PathCriterion{}`, and `NewBodyFuzzyCriterion("$.messages[*].role")`.
5. httptape loads the 3 fixtures from `/fixtures/` into the `FileStore`.
6. The test starts a Ktor `testApplication` with the Koog plugin configured to point at
   `HttptapeContainer.baseUrl` for both OpenAI and weather base URLs.
7. The test issues `GET /weather-advice?city=Berlin` to the test server.
8. The route handler creates a Koog agent with the `WeatherTools` tool set and runs it.
9. **HTTP #1**: Koog's OpenAI client sends `POST /v1/chat/completions` with
   `messages=[system, user]` to httptape.
   - httptape's `CompositeMatcher` scores all 3 tapes:
     - `chat-1.json`: method=POST(1) + path=/v1/chat/completions(2) + body_fuzzy roles=[system,user] match(6) = 9
     - `chat-2.json`: method=POST(1) + path=/v1/chat/completions(2) + body_fuzzy roles=[system,user,assistant,tool] NO match(0) = eliminated
     - `weather-berlin.json`: method=GET(0) = eliminated
   - Winner: `chat-1.json`. Returns SSE stream with tool_call delta for `getWeather("Berlin")`.
10. Koog parses the tool_call, invokes `WeatherTools.getWeather("Berlin")`.
11. **HTTP A**: `WeatherTools` sends `GET /v1/forecast?city=Berlin` to httptape.
    - `CompositeMatcher` scores:
      - `chat-1.json`: method=POST(0) = eliminated
      - `chat-2.json`: method=POST(0) = eliminated
      - `weather-berlin.json`: method=GET(1) + path=/v1/forecast(2) + body_fuzzy vacuous-true(1) = 4
    - Winner: `weather-berlin.json`. Returns weather JSON.
12. Koog receives the weather data and prepares the follow-up message.
13. **HTTP #2**: Koog sends `POST /v1/chat/completions` with
    `messages=[system, user, assistant(tool_call), tool(weather_json)]` to httptape.
    - `CompositeMatcher` scores:
      - `chat-1.json`: method=POST(1) + path=/v1/chat/completions(2) + body_fuzzy roles=[system,user] vs [system,user,assistant,tool] NO match(0) = eliminated
      - `chat-2.json`: method=POST(1) + path=/v1/chat/completions(2) + body_fuzzy roles=[system,user,assistant,tool] match(6) = 9
      - `weather-berlin.json`: method=GET(0) = eliminated
    - Winner: `chat-2.json`. Returns SSE stream with final answer containing "umbrella".
14. Koog assembles the final response string.
15. The route handler sends the result as an SSE event.
16. The test asserts the response contains "umbrella".

#### Error cases

| Error | Cause | Handling |
|---|---|---|
| httptape container fails to start | Docker not running, port conflict | Testcontainers throws `ContainerLaunchException`; test fails with clear error |
| Fixture file not found on classpath | Missing or misnamed file | `MountableFile.forClasspathResource` throws `IllegalArgumentException` at container setup |
| Invalid `httptape.config.json` | Malformed JSON, unknown criterion type | httptape exits with error; container health check fails; `ContainerLaunchException` |
| Koog fails to parse SSE tool_call delta | Incorrect `chat.completion.chunk` JSON format | Koog throws deserialization error; test fails with stack trace pointing to fixture |
| Matcher selects wrong fixture | Config mismatch, fixture body roles incorrect | Wrong response returned; assertion fails ("umbrella" not found) |
| Weather tool HTTP call fails | httptape not matching GET request | Ktor client throws; Koog reports tool failure; test fails |

#### Test strategy

One Kotest `BehaviorSpec` class (`WeatherAdviceTest`) with the following test cases:

1. **Happy path**: Given a Koog agent backed by httptape / When the user asks for weather
   advice for Berlin / Then the response contains "umbrella" (or similar weather-based
   advice from the `chat-2.json` fixture).

2. **SSE streaming verification** (optional, if the dev implements per-token streaming):
   Verify that multiple SSE events are received over time (not all at once), proving
   httptape's `--sse-timing=realtime` works. Mirrors the Java demo's `streamingPreservesCadence`
   test.

The test does NOT test:
- httptape's matcher logic (that is tested in `matcher_test.go` in the Go codebase).
- Koog's agent logic in isolation (that is Koog's own test suite).
- Real OpenAI calls (explicitly out of scope).

The test DOES prove:
- The three matcher improvements (#178, #179, #180) work end-to-end from a container
  consumer's perspective.
- Koog's OpenAI client correctly deserializes httptape's SSE fixtures (including tool_call
  deltas).
- A single httptape container can serve both SSE (OpenAI) and JSON (weather) responses,
  routing correctly via the declarative matcher config.

#### Alternatives considered

1. **Use Koog's Ktor plugin (`koog-ktor`) vs. standalone `AIAgent` with `simpleOpenAIExecutor`**:
   The Ktor plugin is chosen because: (a) it demonstrates the idiomatic Ktor integration
   path, which is Koog's primary JVM server framework; (b) it provides `aiAgent()` extension
   functions on route handlers, reducing boilerplate; (c) the `install(Koog) { llm { openAI { baseUrl = ... } } }`
   DSL makes base-URL override clean and visible. Using `simpleOpenAIExecutor` directly would
   require manual `OpenAILLMClient` construction with `OpenAIClientSettings(baseUrl = ...)`,
   which is more verbose and less representative of real Ktor+Koog usage.

2. **Use wttr.in's actual URL shape (`/{city}?format=j1`) vs. fictional `/v1/forecast?city={city}`**:
   The fictional path is chosen because wttr.in embeds the city name in the URL path, which
   would require `PathRegexCriterion` (not yet exposed via config per ADR-39's scope). The
   fictional `/v1/forecast?city=Berlin` keeps the city in query params (ignored by
   `PathCriterion`) and the path constant, making the matcher config simpler and focusing
   the demo on `body_fuzzy` -- the novel criterion being proven out.

3. **Kotest Testcontainers extension vs. manual `object` singleton**:
   The `kotest-extensions-testcontainers` library provides a `ContainerExtension` that can
   manage container lifecycle. However, the manual `object` with `lazy` is simpler,
   transparent, and mirrors the Java demo's pattern. The extension adds a dependency for
   syntactic sugar. The architect leaves this to the dev's discretion -- either approach is
   acceptable. The dependency is already listed in `build.gradle.kts` in case the dev
   prefers the extension approach.

4. **Gradle Shadow plugin for fat JAR vs. Ktor Gradle plugin**:
   Either approach produces a runnable JAR for the Dockerfile. The dev should pick whichever
   is simpler. The Ktor Gradle plugin (`id("io.ktor.plugin")`) has built-in fat-JAR support
   via `ktor { fatJar { ... } }`. Alternative: the `application` plugin's `installDist` task
   produces a distribution with launch scripts. The key requirement is that the Dockerfile
   can `COPY` and `java -jar` the result.

5. **Multiple Kotest test files vs. one BehaviorSpec**:
   One file is sufficient for the demo's single scenario. The Java demo has two test files
   (one per scenario: SSE and REST), but this demo has only one scenario (the agent flow
   exercises both SSE and REST internally). A single `WeatherAdviceTest.kt` keeps it simple.

#### Consequences

**Benefits:**
- Proves the #178/#179/#180 matcher stack works end-to-end from a container consumer's
  perspective, using a realistic multi-step AI agent scenario.
- Demonstrates httptape's value proposition for Kotlin/Ktor developers -- the second JVM
  demo after the Java/Spring Boot one, showing framework-agnostic compatibility.
- The matcher config file (`httptape.config.json`) serves as a reusable reference for users
  who need body-aware matching in their own projects.
- CI coverage for the Kotlin ecosystem via the `examples.yml` workflow.
- Dependabot coverage for Gradle dependencies and Docker base images.

**Costs / trade-offs:**
- **Maintenance burden**: three demos (Java, TypeScript, Kotlin) in three stacks. Each
  requires dependency updates (mitigated by dependabot), CI runner time (~3 min per run for
  Gradle + Testcontainers), and periodic version bumps when upstream frameworks release
  breaking changes.
- **Koog version churn**: Koog is pre-1.0 (currently 0.8.0) and its API may change. The
  demo will need updates when Koog releases breaking versions. The `kotest-extensions-testcontainers`
  library is also relatively new (2.0.2). Dependabot will flag these, but manual review is
  needed for breaking changes.
- **JDK 25 availability**: JDK 25 is cutting-edge. CI runners may not have it pre-installed,
  requiring `actions/setup-java` to download it. This adds ~30s to CI time.
- **Single httptape container**: serving both SSE and JSON responses from one container
  simplifies the demo but means all fixtures share one flat namespace. Filename collisions
  across fixture subdirectories would be caught at container setup time (explicit enumeration).

---

### ADR-41: Content-Type-driven body shape + ContentNegotiationCriterion (v0.12.0)

**Date**: 2026-04-18
**Issue**: #187 (absorbs #188, now closed)
**Status**: Accepted

#### Context

httptape stores HTTP bodies as `[]byte` in the Go struct and base64-encoded
strings in fixture JSON. This is correct but hostile to hand-authoring: a JSON
API response body like `{"id":1,"name":"Alice"}` becomes
`eyJpZCI6MSwibmFtZSI6IkFsaWNlIn0=` on disk. With the user about to
integrate httptape into the VibeWarden product (hand-authored fixtures for
deterministic AI agent testing), base64 bodies are a non-starter.

Separately, multiple fixtures at the same path but different response
Content-Types (e.g., JSON vs. XML) cannot be distinguished by the current
matcher stack. Real HTTP APIs use `Accept` / `Content-Type` content negotiation
for this (RFC 7231 section 5.3.2), and httptape needs a criterion that
implements it.

Both concerns share one root: **httptape must be Content-Type-aware** at
two layers:

1. **Storage layer** (body shape in fixture JSON): the fixture format should
   represent JSON bodies as native JSON, text bodies as strings, and binary
   bodies as base64.
2. **Matcher layer** (content negotiation): a new `ContentNegotiationCriterion`
   that compares the request `Accept` header against the candidate tape's
   response `Content-Type`, using RFC 7231 specificity and q-value scoring.

Both layers share a common MediaType parsing/classification utility, so they
are designed, implemented, and shipped as a single atomic v0.12.0 breaking
change.

#### Open question resolutions

**Q1: Vendor JSON types -- should `Accept: application/json` match `Content-Type: application/vnd.api+json`?**

Decision: **No, strict RFC 7231 semantics. `application/json` does NOT match
`application/vnd.api+json`.** However, `application/vnd.api+json` IS classified
as JSON-flavored for the *body shape* layer (Layer 1) because it uses the
`+json` structured syntax suffix (RFC 6838 section 4.2.8). These are two
separate concerns:

- Body shape classification: any media type with `+json` suffix or an
  explicit-list match (`application/json`, `application/ld+json`,
  `application/problem+json`) emits native JSON in fixtures. This is a
  format decision, not a matching decision.
- Content negotiation matching: `Accept: application/json` matches only
  `Content-Type: application/json`, not `application/vnd.api+json`. To match
  vendor types, the client must send `Accept: application/vnd.api+json` or
  `Accept: application/*` or `Accept: */*`. This follows RFC 7231 section
  5.3.2 literally.

Rationale: violating RFC 7231 matching semantics to be "convenient" would
create a subset-of-HTTP that surprises users who know the spec. The body shape
layer can safely be more liberal because it is a display/storage concern, not a
protocol compliance concern.

**Q2: Charset and parameter handling in matching**

Decision: **Parameters (except `q`) are ignored during content negotiation
matching.** `text/plain; charset=utf-8` matches `text/plain; charset=ascii`.

Rationale: RFC 7231 section 5.3.2 says parameters in the Accept header's
media-range narrow the match, but in practice almost no real-world API
distinguishes fixtures by charset. httptape is a mock server for testing,
not a conformant HTTP proxy -- pragmatism wins. The `q` parameter is always
consumed as the quality factor and never compared as a media type parameter.

Implementation note: `ParseMediaType` still *parses* parameters into the
`Parameters` map (for potential future use), but `Matches` and `MatchScore`
compare only type and subtype. This decision can be revisited if a real use
case arises.

**Q3: Body shape for `application/x-www-form-urlencoded`**

Decision: **String.** Form-encoded bodies are stored as JSON strings, e.g.,
`"body": "username=alice&password=secret"`. No key-value object parsing.

Rationale: parsing form bodies into a JSON object would create a semantic
transform (the body bytes would no longer round-trip identically through
encode/decode). httptape replays bodies verbatim -- the Go struct's `Body
[]byte` is the source of truth. The fixture format is a human-friendly
*presentation* of those bytes, not a parsed representation. Parsing is a
separate concern that could be layered on top in the future.

**Q4: Scoring weight for ContentNegotiationCriterion**

Decision: **Score 5.** The weight table becomes:

| Criterion                     | Score |
|-------------------------------|-------|
| MethodCriterion               | 1     |
| PathCriterion                 | 2     |
| RouteCriterion                | 1     |
| PathRegexCriterion            | 1     |
| HeadersCriterion              | 3     |
| QueryParamsCriterion          | 4     |
| **ContentNegotiationCriterion** | **5** |
| BodyFuzzyCriterion            | 6     |
| BodyHashCriterion             | 8     |

Rationale: Content negotiation is more specific than a generic header match
(3) or query params (4), but less specific than body-level matching (6, 8).
Score 5 slots it between query params and body fuzzy, which matches the
intuitive specificity ordering: discriminating by response format is more
meaningful than matching on query parameters but less meaningful than matching
on request body content.

Within the `ContentNegotiationCriterion.Score()` method, the returned score
is always 5 on match (not variable). The *ranking* among multiple matching
candidates is handled by the criterion itself returning 0 for non-matching
candidates, so the CompositeMatcher's scoring loop eliminates them. When
multiple candidates survive, they have equal content-negotiation scores;
other criteria (path, body, etc.) break the tie. This is intentionally simple
for v0.12.0. A future enhancement could return a variable score based on
specificity/q-value (e.g., exact type match scores higher than wildcard
match), but this adds complexity without a proven need.

**Refinement -- variable scoring within ContentNegotiationCriterion:**

On further reflection, variable scoring *within* the criterion is valuable
when the *only* differentiating dimension is Accept/Content-Type (e.g., three
fixtures for `GET /users` with JSON, XML, CSV responses). In that scenario,
all three survive method+path scoring equally. The content-negotiation criterion
must break the tie. The scoring approach:

- Exact type+subtype match: score 5
- Subtype wildcard match (e.g., `application/*` matching `application/json`): score 4
- Full wildcard match (`*/*`): score 3
- No match: score 0

This preserves the general position of content negotiation (3-5 range) in the
global weight table while allowing intra-criterion ranking. If q-values differ
among matching media ranges, the highest-q matching range determines the score
tier; ties within a tier are broken by specificity.

**Q5: Coexistence with `HeadersCriterion("Accept", ...)`**

Decision: **Allowed, not an error, but documented as redundant.** If a user
configures both `content_negotiation` and `HeadersCriterion{Key: "Accept",
Value: "..."}`, both contribute independently to the composite score. The
`HeadersCriterion` performs exact string matching on the Accept *request*
header (both sides must have it), while `ContentNegotiationCriterion` performs
RFC 7231 media-range matching against the *response* Content-Type. They test
different things and their scores add. However, using both is almost certainly
a mistake -- documenting this in `matcher.go` godoc and `docs/matching.md` is
sufficient.

`Validate()` does NOT produce an error for this combination -- it is not
wrong, just unusual. No special interaction logic.

**Q6: Migration tool**

Decision: **In scope.** Add `httptape migrate-fixtures <dir>` as a subcommand
in `cmd/httptape/main.go`. The tool reads each `.json` file in `<dir>`
(non-recursively), detects old-format fixtures (body is a base64 string where
Content-Type indicates it should be a native JSON object or text string),
converts to new format, writes back with proper indentation + trailing newline.

Behavior details:
- Files that are already in new format are skipped (idempotent).
- Files that are not valid Tape JSON are skipped with a warning to stderr.
- SSE tapes (`sse_events` present and non-empty) are skipped (body is null).
- Recursive mode via `--recursive` flag (default: non-recursive).
- Summary printed to stdout: `migrated N files, skipped M files, errors E files`.
- Exit code 0 on success (even if some files were skipped), non-zero if any
  write fails.

As part of this PR, run the migration tool against all fixture directories in
the repo.

**Q7: Empty body representation**

Decision: **`null`.** Empty bodies (nil `Body []byte`) are represented as
`"body": null` in fixture JSON. This is consistent with the current behavior
(Go's `encoding/json` marshals `[]byte(nil)` as `null`), maintains backward
compatibility for existing fixtures that already use `null`, and is
semantically correct (`null` = "no body", not "empty string body").

An explicitly empty body (`Body = []byte{}`, length 0) is also marshaled as
`null`. The distinction between nil and empty-but-allocated is not meaningful
at the HTTP level and should not leak into fixtures.

Omitting the `body` field entirely (via `omitempty`) was considered but
rejected: the field should always be present for structural consistency and
to make fixtures self-documenting. A reader should never have to wonder
whether a missing `body` field means "null body" or "field accidentally
deleted."

**Q8: File naming**

Decision: **`media_type.go`** (with underscore). Go convention for multi-word
filenames is underscore-separated (`store_file.go`, `store_memory.go`). The
test file is `media_type_test.go`.

#### Decision

##### Shared utility -- `media_type.go`

New file at the package root containing media type parsing, classification,
and matching utilities. Consumed by `tape.go` (body shape dispatch) and
`matcher.go` (ContentNegotiationCriterion).

**Types:**

```go
// MediaType represents a parsed media type (e.g., "application/json; charset=utf-8").
// It is a value type with no I/O.
type MediaType struct {
    // Type is the top-level type (e.g., "application", "text", "image").
    Type string
    // Subtype is the subtype (e.g., "json", "plain", "png").
    // For structured syntax suffixes, this includes the full subtype
    // (e.g., "vnd.api+json").
    Subtype string
    // Suffix is the structured syntax suffix without the '+' prefix
    // (e.g., "json" for "application/vnd.api+json"). Empty if no suffix.
    Suffix string
    // Params holds media type parameters (e.g., charset=utf-8).
    // The "q" parameter is extracted into QValue and not included here.
    Params map[string]string
    // QValue is the quality factor from the Accept header (0.0-1.0).
    // Defaults to 1.0 if not specified.
    QValue float64
}
```

**Functions:**

```go
// ParseMediaType parses a single media type string (e.g., "application/json;
// charset=utf-8") into a MediaType. Parameters including q-value are parsed.
// Returns an error if the string is fundamentally malformed (no "/" separator).
// Uses mime.ParseMediaType from stdlib internally.
func ParseMediaType(s string) (MediaType, error)

// ParseAccept parses an Accept header value into a slice of MediaType entries,
// sorted by precedence (highest q-value first, then specificity).
// Malformed individual media ranges are silently skipped.
// An empty or missing Accept header returns a single entry for "*/*" with q=1.0.
func ParseAccept(accept string) []MediaType

// IsJSON reports whether the media type represents JSON content.
// True for: application/json, any type with +json suffix,
// application/ld+json, application/problem+json, etc.
func IsJSON(mt MediaType) bool

// IsText reports whether the media type represents human-readable text content.
// True for: text/*, application/xml, application/javascript,
// application/x-www-form-urlencoded, any type with +xml suffix.
// False for types that IsJSON returns true for (JSON is handled separately).
func IsText(mt MediaType) bool

// IsBinary reports whether the media type represents binary content.
// Returns true when neither IsJSON nor IsText returns true, or when
// the media type is empty/unknown. This is the fallback classification.
func IsBinary(mt MediaType) bool

// MatchesMediaRange reports whether a response Content-Type satisfies an
// Accept media range. Type and subtype are compared; parameters (except q)
// are ignored per Q2 resolution. Supports wildcards: */* matches anything,
// type/* matches any subtype of type.
func MatchesMediaRange(accept, contentType MediaType) bool

// Specificity returns a specificity score for a media range:
//   3 for exact type/subtype (e.g., application/json)
//   2 for subtype wildcard (e.g., application/*)
//   1 for full wildcard (*/* )
// Used to rank among multiple matching media ranges in an Accept header.
func Specificity(mt MediaType) int
```

Implementation notes:
- `ParseMediaType` wraps `mime.ParseMediaType` from stdlib, extracting the
  `q` parameter as `QValue` (defaulting to 1.0) and splitting the media type
  string into `Type`/`Subtype`/`Suffix`.
- `ParseAccept` splits on comma, calls `ParseMediaType` for each entry,
  sorts by (QValue desc, Specificity desc), silently skips malformed entries.
- `IsJSON` checks: `Type == "application" && (Subtype == "json" || Suffix == "json")`.
- `IsText` checks: `Type == "text"`, or
  `Type == "application" && Subtype in {"xml", "javascript", "x-www-form-urlencoded"}`,
  or `Suffix == "xml"`. Returns false if `IsJSON` would return true (JSON
  takes priority).
- `IsBinary` returns `!IsJSON(mt) && !IsText(mt)`.

##### Layer 1 -- Content-Type-driven body shape in `tape.go`

**Approach:** custom `MarshalJSON`/`UnmarshalJSON` on `RecordedReq` and
`RecordedResp`. The Go struct retains `Body []byte` internally. Only the
on-disk JSON representation changes.

**Removal of `BodyEncoding` field:** The `BodyEncoding` field and
`BodyEncoding` type are removed from both `RecordedReq` and `RecordedResp`.
The `body_encoding` JSON field is no longer emitted. The `detectBodyEncoding`
function is replaced by the `IsJSON`/`IsText`/`IsBinary` classifiers in
`media_type.go`. This is a breaking change to the fixture JSON schema (the
`body_encoding` field disappears), but it was an informational field with no
behavioral effect on replay -- replay always writes `Body` bytes verbatim.

The Recorder and Proxy still need to know the Content-Type for populating
the body hash and (now) for nothing else in the tape construction. The
`BodyEncoding` field was set but never read by the library itself -- it was
purely informational for fixture authors. The new body shape makes it
redundant because the shape itself conveys the encoding.

**MarshalJSON for RecordedReq and RecordedResp:**

1. Build a map representing all JSON fields except `body`.
2. Look up the Content-Type from `Headers`.
3. Classify using `ParseMediaType` + `IsJSON`/`IsText`/`IsBinary`.
4. If `Body` is nil or empty: emit `"body": null`.
5. If JSON-flavored: marshal `Body` bytes as `json.RawMessage` into the
   `"body"` field. This emits the JSON natively (object/array/primitive)
   without escaping. If the bytes are not valid JSON despite the Content-Type
   claiming JSON, fall back to base64 string.
6. If text-y: emit `"body": "<string>"` -- the body bytes interpreted as
   UTF-8, emitted as a JSON string.
7. If binary: emit `"body": "<base64>"` -- standard base64 encoding,
   emitted as a JSON string. This is the same behavior as Go's default
   `[]byte` JSON encoding.
8. `body_encoding` is NOT emitted (field removed).

**UnmarshalJSON for RecordedReq and RecordedResp:**

1. Unmarshal all fields except `body` normally.
2. Extract the raw `body` value using `json.RawMessage`.
3. If the raw value is `null`: set `Body = nil`.
4. Determine the JSON token type of the raw value:
   - If JSON object (`{`) or array (`[`): the body is native JSON.
     Marshal the raw value back to compact JSON bytes and store as `Body`.
   - If JSON string (`"`): need to distinguish text from base64.
     a. Unmarshal the string value.
     b. Look up Content-Type from the already-parsed `Headers`.
     c. If Content-Type is JSON-flavored: this is a legacy fixture or a
        scalar JSON value stored as a string. Try `json.Valid()` on the
        string bytes; if valid JSON, store as `Body`. Otherwise treat as
        text.
     d. If Content-Type is text-y: store the string as UTF-8 bytes directly.
     e. If Content-Type is binary or unknown: base64-decode the string and
        store the decoded bytes as `Body`. If base64 decoding fails, store
        the string as UTF-8 bytes (graceful degradation).
5. If `body_encoding` field is present in the JSON (legacy fixture), it is
   silently ignored during unmarshal. The custom unmarshal does not read it.

**Round-trip property:** `Marshal(Unmarshal(json))` produces identical JSON
for all three body shapes. This is guaranteed by:
- Native JSON bodies: stored as `json.RawMessage` on marshal, reconstructed
  from `json.RawMessage` on unmarshal. The raw bytes are preserved exactly
  (including whitespace from the original fixture, since `json.RawMessage` is
  round-trip-safe for valid JSON).

  **Correction:** `json.RawMessage` preserves the raw bytes during a single
  unmarshal, but when we re-marshal the tape, we convert `Body []byte` to
  `json.RawMessage`. If the original fixture had pretty-printed JSON in the
  body field, and the body bytes are stored as-is, then re-marshaling will
  produce the same pretty-printed JSON in the body field. However, if the body
  bytes were obtained from a real HTTP response (compact JSON), the fixture
  will show compact JSON in the body field. This is correct behavior -- the
  body field reflects the actual bytes.

- Text bodies: string <-> UTF-8 bytes is lossless for valid UTF-8.
- Base64 bodies: base64-encode(bytes) is deterministic.

**Legacy fixture compatibility:** Existing base64-only fixtures are NOT
auto-migrated by the unmarshaler. When loading an old fixture with
`"body": "eyJpZCI6MSwi..."` and `Content-Type: application/json`, the
unmarshaler sees a JSON string token and Content-Type is JSON-flavored.
It runs `json.Valid()` on the string value. Base64-encoded JSON is NOT valid
JSON, so it falls through to binary/base64 decode. The base64-decoded bytes
are stored as `Body`. On re-marshal, the Content-Type is JSON, so the body
is emitted as a native JSON object. This means loading + saving an old fixture
implicitly converts it. However, the spec says existing fixtures are migrated
explicitly via the migration tool -- the implicit conversion on load-then-save
is an acceptable fallback but not the primary migration path.

**Interaction with sanitization:** The sanitization pipeline operates on
`Tape` values with `Body []byte`. The sanitizer receives and returns bytes.
The MarshalJSON/UnmarshalJSON only affects the JSON representation. No changes
to sanitizer code are needed.

**Interaction with matching:** `BodyFuzzyCriterion` and `BodyHashCriterion`
operate on `Body []byte` (the in-memory representation). The JSON encoding
is irrelevant to matching. No changes to matcher scoring logic are needed
(only the new `ContentNegotiationCriterion` is added).

**Interaction with replay:** `Server.ServeHTTP` writes `tape.Response.Body`
directly. No changes needed -- the bytes are the same regardless of how they
were encoded in JSON.

##### Layer 2 -- ContentNegotiationCriterion in `matcher.go`

**New type:**

```go
// ContentNegotiationCriterion selects tapes whose response Content-Type
// satisfies the incoming request's Accept header, following RFC 7231
// section 5.3.2 media range matching with specificity-based scoring.
//
// Scoring:
//   - Exact type/subtype match:     5
//   - Subtype wildcard match:       4
//   - Full wildcard (*/*) match:    3
//   - No match:                     0
//
// When the request has no Accept header, it is treated as Accept: */*
// (per RFC 7231 section 5.3.2). When a candidate tape has no response
// Content-Type header, it is treated as application/octet-stream.
//
// If the Accept header contains multiple media ranges, the criterion
// uses the highest-specificity range that matches the candidate's
// Content-Type, weighted by q-value. Media ranges with q=0 explicitly
// exclude the Content-Type.
//
// Malformed Accept headers: individual malformed media ranges are silently
// skipped. If all ranges are malformed, the Accept header is treated as */*.
// The criterion never panics on malformed input.
type ContentNegotiationCriterion struct{}

func (ContentNegotiationCriterion) Score(req *http.Request, candidate Tape) int
func (ContentNegotiationCriterion) Name() string // returns "content_negotiation"
```

**Score algorithm:**

1. Parse the request's `Accept` header via `ParseAccept`. If absent or empty,
   use `[MediaType{Type: "*", Subtype: "*", QValue: 1.0}]`.
2. Parse the candidate tape's response `Content-Type` via `ParseMediaType`.
   If absent or empty, use `MediaType{Type: "application", Subtype: "octet-stream"}`.
3. For each media range in the parsed Accept (already sorted by q-value desc,
   specificity desc):
   a. If `q == 0`: if this range matches the Content-Type, immediately return
      0 (explicitly excluded).
   b. If `MatchesMediaRange(range, contentType)`: return the specificity score
      (5 for exact, 4 for subtype wildcard, 3 for full wildcard).
4. If no range matched: return 0.

The iteration short-circuits on the first match because `ParseAccept` sorts
by precedence (highest q-value first, then specificity). The first match is
by definition the best match.

##### Config integration in `config.go`

Add `"content_negotiation"` to the `criterionBuilders` dispatch table:

```go
"content_negotiation": {validate: validateContentNegotiationCriterion, build: buildContentNegotiationCriterion},
```

Validation: `content_negotiation` takes no type-specific fields. If `Paths`
is set, produce an error (same pattern as `method` and `path`).

Builder: returns `ContentNegotiationCriterion{}`.

The `CriterionConfig` struct does not need new fields -- `content_negotiation`
uses no type-specific configuration.

##### JSON Schema extension (`config.schema.json`)

Add `"content_negotiation"` to the criterion type enum:

```json
"enum": ["method", "path", "body_fuzzy", "content_negotiation"]
```

Add an `if/then` constraint to reject `paths` when type is
`content_negotiation` (same as `method` and `path`):

```json
{
  "if": {
    "properties": { "type": { "enum": ["method", "path", "content_negotiation"] } },
    "required": ["type"]
  },
  "then": {
    "properties": {
      "paths": false
    }
  }
}
```

This replaces the existing `if/then` for `["method", "path"]` -- just add
`"content_negotiation"` to the enum array.

##### CLI migration subcommand in `cmd/httptape/main.go`

New subcommand `migrate-fixtures`:

```
httptape migrate-fixtures <dir> [--recursive]
```

Implementation:

```go
func runMigrateFixtures(args []string) error
```

1. Parse flags: `--recursive` (bool, default false).
2. Read `.json` files from `<dir>` (optionally recursive).
3. For each file:
   a. Read file contents.
   b. Try `json.Unmarshal` into a `Tape`. If it fails, skip with warning.
   c. Check if migration is needed: look at the `body` field in the raw JSON.
      If it's a string and the Content-Type indicates JSON or text, migration
      is needed.
   d. If migration needed: marshal the tape back to JSON (which uses the new
      custom MarshalJSON). Write back to the same file.
   e. Track stats: migrated, skipped, errored.
4. Print summary.

The migration tool uses the library's own `json.Unmarshal` (which now has
custom unmarshal logic) followed by `json.MarshalIndent` (which now has
custom marshal logic). The unmarshal reads the old format correctly (base64
string bodies are decoded to `Body []byte`), and the marshal writes the new
format (Content-Type-aware body shape).

##### Removal of `BodyEncoding` type and fields

The following are removed from `tape.go`:
- `BodyEncoding` type alias
- `BodyEncodingIdentity` constant
- `BodyEncodingBase64` constant
- `RecordedReq.BodyEncoding` field
- `RecordedResp.BodyEncoding` field
- `detectBodyEncoding` function

The following are updated:
- `recorder.go`: remove all `detectBodyEncoding` calls and `BodyEncoding`
  field assignments in tape construction.
- `proxy.go`: same removals.
- `recorder_test.go`: remove `TestDetectBodyEncoding`,
  `TestRecorder_BinaryBody` BodyEncoding assertion,
  `TestRecorder_TextBody` BodyEncoding assertion.
- `server_test.go`: remove `BodyEncoding` field from test tape construction.

#### Types

| Type | File | Description |
|---|---|---|
| `MediaType` | `media_type.go` | Parsed media type with type, subtype, suffix, params, q-value |
| `ContentNegotiationCriterion` | `matcher.go` | Criterion implementing RFC 7231 Accept/Content-Type matching |

Modified types:

| Type | File | Change |
|---|---|---|
| `RecordedReq` | `tape.go` | Remove `BodyEncoding` field. Add custom `MarshalJSON`/`UnmarshalJSON`. |
| `RecordedResp` | `tape.go` | Remove `BodyEncoding` field. Add custom `MarshalJSON`/`UnmarshalJSON`. |

Removed types:

| Type | File | Notes |
|---|---|---|
| `BodyEncoding` | `tape.go` | Type alias and both constants removed |

#### Functions and methods

New exported functions:

| Function | Signature | File |
|---|---|---|
| `ParseMediaType` | `func ParseMediaType(s string) (MediaType, error)` | `media_type.go` |
| `ParseAccept` | `func ParseAccept(accept string) []MediaType` | `media_type.go` |
| `IsJSON` | `func IsJSON(mt MediaType) bool` | `media_type.go` |
| `IsText` | `func IsText(mt MediaType) bool` | `media_type.go` |
| `IsBinary` | `func IsBinary(mt MediaType) bool` | `media_type.go` |
| `MatchesMediaRange` | `func MatchesMediaRange(accept, contentType MediaType) bool` | `media_type.go` |
| `Specificity` | `func Specificity(mt MediaType) int` | `media_type.go` |
| `ContentNegotiationCriterion.Score` | `func (ContentNegotiationCriterion) Score(req *http.Request, candidate Tape) int` | `matcher.go` |
| `ContentNegotiationCriterion.Name` | `func (ContentNegotiationCriterion) Name() string` | `matcher.go` |
| `RecordedReq.MarshalJSON` | `func (r RecordedReq) MarshalJSON() ([]byte, error)` | `tape.go` |
| `RecordedReq.UnmarshalJSON` | `func (r *RecordedReq) UnmarshalJSON(data []byte) error` | `tape.go` |
| `RecordedResp.MarshalJSON` | `func (r RecordedResp) MarshalJSON() ([]byte, error)` | `tape.go` |
| `RecordedResp.UnmarshalJSON` | `func (r *RecordedResp) UnmarshalJSON(data []byte) error` | `tape.go` |

Removed exported functions/types:

| Symbol | File |
|---|---|
| `BodyEncoding` type | `tape.go` |
| `BodyEncodingIdentity` const | `tape.go` |
| `BodyEncodingBase64` const | `tape.go` |

Removed unexported functions:

| Symbol | File |
|---|---|
| `detectBodyEncoding` | `tape.go` |

#### File layout

**New files:**

| File | Purpose |
|---|---|
| `media_type.go` | MediaType struct, ParseMediaType, ParseAccept, IsJSON, IsText, IsBinary, MatchesMediaRange, Specificity |
| `media_type_test.go` | Comprehensive tests for all media type utilities |
| `CHANGELOG.md` | Changelog with v0.12.0 entry (created if not existing) |

**Modified files:**

| File | Changes |
|---|---|
| `tape.go` | Remove `BodyEncoding` type, constants, `detectBodyEncoding`. Remove `BodyEncoding` fields from `RecordedReq` and `RecordedResp`. Add `MarshalJSON`/`UnmarshalJSON` methods on both types. |
| `tape_test.go` | Add body marshal/unmarshal tests for all three shapes. Add round-trip tests. Update existing tests that reference `BodyEncoding`. |
| `matcher.go` | Add `ContentNegotiationCriterion` struct with `Score` and `Name` methods. Add to `CompositeMatcher` godoc score table. |
| `matcher_test.go` | Add `ContentNegotiationCriterion` tests: exact match, wildcard, q-value, multi-candidate, missing headers, malformed input. |
| `config.go` | Add `"content_negotiation"` entry to `criterionBuilders`. Add `validateContentNegotiationCriterion` and `buildContentNegotiationCriterion`. Update `CriterionConfig` godoc to list `content_negotiation`. |
| `config_test.go` | Add tests for `content_negotiation` criterion config loading and BuildMatcher. |
| `config.schema.json` | Add `"content_negotiation"` to criterion type enum. Update `if/then` constraint. |
| `recorder.go` | Remove `detectBodyEncoding` calls and `BodyEncoding` field assignments. |
| `proxy.go` | Remove `detectBodyEncoding` calls and `BodyEncoding` field assignments. |
| `recorder_test.go` | Remove/update `TestDetectBodyEncoding` and BodyEncoding assertions. |
| `server_test.go` | Remove `BodyEncoding` fields from test tape construction. |
| `integration_test.go` | Add end-to-end tests: (a) record+replay with JSON/text/binary bodies, (b) multi-Content-Type fixture serving with config-driven content negotiation matcher. |
| `cmd/httptape/main.go` | Add `runMigrateFixtures` function and `"migrate-fixtures"` case in command dispatch. |
| `cmd/httptape/main_test.go` | Add tests for `migrate-fixtures` subcommand. |
| `docs/fixtures-authoring.md` | New body shape examples (JSON native, text string, binary base64), migration instructions. |
| `docs/matching.md` | ContentNegotiationCriterion documentation with multi-Content-Type example. |
| `docs/api-reference.md` | Add MediaType, ParseMediaType, ParseAccept, IsJSON, IsText, IsBinary, ContentNegotiationCriterion entries. Remove BodyEncoding entries. |
| `docs/cli.md` | Add `migrate-fixtures` subcommand documentation. |
| `doc.go` | Update package-level doc to mention Content-Type-aware body shape. |
| `CLAUDE.md` | No structural changes needed. Package structure comment already says `tape.go` has `RecordedReq`/`RecordedResp`. The `media_type.go` file should be added to the package structure comment. |

**Fixture files to migrate:**

| Directory | Files | Action |
|---|---|---|
| `examples/java-spring-boot/src/test/resources/fixtures/users/` | `get-user-1.json`, `get-user-999.json`, `get-users.json` | Migrate body from base64 to native JSON |
| `examples/java-spring-boot/src/test/resources/fixtures/openai/` | `chat-completion-headphones.json` | SSE tape -- body is null, skip |
| `examples/kotlin-ktor-koog/src/test/resources/fixtures/weather/` | `weather-berlin.json` | Migrate body from base64 to native JSON |
| `examples/kotlin-ktor-koog/src/test/resources/fixtures/openai/` | `chat-1.json`, `chat-2.json` | Migrate request body from base64 to native JSON; response body is null (SSE) or JSON, migrate accordingly |
| `testcontainers/testdata/` | `config.json` | Check if it has body fields that need migration |

The `examples/ts-frontend-first/.httptape-cache/fixtures/` directory contains
machine-generated cache files. These should be migrated too (via the
migration tool with `--recursive`), or regenerated. The `.httptape-cache`
is gitignored in most setups, but if committed, migrate.

#### Sequence

**Recording flow (unchanged functionally, just removes BodyEncoding):**

1. `Recorder.RoundTrip` captures request and response body bytes.
2. Constructs `RecordedReq` and `RecordedResp` with `Body []byte`.
3. `BodyEncoding` field no longer set (removed).
4. Tape is passed to sanitizer, then saved to store.
5. `FileStore.Save` calls `json.MarshalIndent(tape, ...)`.
6. `RecordedReq.MarshalJSON` and `RecordedResp.MarshalJSON` dispatch on
   Content-Type to produce the appropriate body shape.
7. Fixture written to disk with native JSON / string / base64 body.

**Loading flow (enhanced with custom unmarshal):**

1. `FileStore.Load` reads `.json` file and calls `json.Unmarshal`.
2. `RecordedReq.UnmarshalJSON` and `RecordedResp.UnmarshalJSON` detect the
   JSON token type of the `body` field and decode accordingly:
   - Object/array -> re-marshal to bytes -> `Body`
   - String + text CT -> UTF-8 bytes -> `Body`
   - String + binary CT -> base64 decode -> `Body`
   - String + JSON CT -> try json.Valid, if yes store as bytes, else base64 decode -> `Body`
   - null -> `Body = nil`
3. The rest of the Tape fields are unmarshaled normally.

**Content negotiation matching flow:**

1. Request arrives at `Server.ServeHTTP` with `Accept: application/json`.
2. Server calls `matcher.Match(req, candidates)`.
3. `CompositeMatcher` iterates candidates. For each:
   a. Other criteria score as usual.
   b. `ContentNegotiationCriterion.Score` parses `Accept` header, parses
      candidate's response `Content-Type`, computes specificity score.
4. Candidates whose Content-Type does not satisfy Accept get score 0 and
   are eliminated.
5. Highest-scoring surviving candidate wins.

**Migration flow:**

1. User runs `httptape migrate-fixtures ./fixtures`.
2. Tool reads each `.json` file.
3. For each: unmarshal (custom UnmarshalJSON handles old format correctly),
   then marshal (custom MarshalJSON produces new format).
4. Write back to file. Print summary.

#### Error cases

| Error | Where | Handling |
|---|---|---|
| Malformed Content-Type in headers | `MarshalJSON` | Fall back to binary (base64) encoding. Never fail marshal due to bad CT. |
| Body claims JSON but is not valid JSON | `MarshalJSON` | Fall back to base64 string. Log nothing (library). |
| Body is base64 string but base64 decode fails | `UnmarshalJSON` | Store the string as UTF-8 bytes (graceful degradation). |
| Missing `body` field in fixture JSON | `UnmarshalJSON` | `Body = nil`. Not an error. |
| Unknown JSON token type for body | `UnmarshalJSON` | Return `fmt.Errorf("unexpected body type")`. |
| Malformed Accept header | `ContentNegotiationCriterion.Score` | Skip malformed media ranges. If all malformed, treat as `*/*`. Never return error. |
| Missing Accept header | `ContentNegotiationCriterion.Score` | Treat as `*/*` (RFC 7231). |
| Missing Content-Type on candidate | `ContentNegotiationCriterion.Score` | Treat as `application/octet-stream`. |
| Malformed Content-Type on candidate | `ContentNegotiationCriterion.Score` | Return 0 (cannot match). |
| Migration tool: file is not valid Tape JSON | `runMigrateFixtures` | Skip with warning to stderr. |
| Migration tool: write permission denied | `runMigrateFixtures` | Report error, continue with next file, exit non-zero. |

#### Test strategy

All tests use stdlib `testing` only. Table-driven where appropriate.

**`media_type_test.go`:**

1. `TestParseMediaType`: table-driven with cases:
   - Standard types: `application/json`, `text/plain`, `image/png`
   - With parameters: `text/plain; charset=utf-8`, `application/json; charset=utf-8`
   - With q-value: `text/html;q=0.9`, `application/xml;q=0`
   - Vendor types: `application/vnd.api+json`, `application/vnd.github.v3+json`
   - Suffix extraction: `application/vnd.api+json` -> Suffix = "json"
   - Malformed: empty string, no slash, whitespace-only, `///invalid`
   - Edge cases: `*/*`, `application/*`, `text/*`

2. `TestParseAccept`: table-driven:
   - Single type: `application/json`
   - Multiple types: `application/json, text/html;q=0.9, */*;q=0.1`
   - Q-value ordering: verify sorted by q desc then specificity desc
   - Empty/missing: returns `[*/*]`
   - All malformed: returns `[*/*]`
   - Mixed valid/malformed: skips malformed, keeps valid

3. `TestIsJSON`: `application/json` (true), `application/vnd.api+json` (true),
   `application/ld+json` (true), `text/plain` (false), `image/png` (false),
   `application/xml` (false)

4. `TestIsText`: `text/plain` (true), `text/html` (true), `application/xml`
   (true), `application/javascript` (true), `application/x-www-form-urlencoded`
   (true), `application/json` (false -- IsJSON has priority), `image/png`
   (false)

5. `TestIsBinary`: `image/png` (true), `application/octet-stream` (true),
   `application/protobuf` (true), empty/unknown (true), `text/plain` (false),
   `application/json` (false)

6. `TestMatchesMediaRange`: table-driven:
   - `application/json` matches `application/json` (true)
   - `application/*` matches `application/json` (true)
   - `*/*` matches anything (true)
   - `application/json` does NOT match `application/vnd.api+json` (false)
   - `text/plain` does NOT match `text/html` (false)
   - `text/*` matches `text/html` (true)

7. `TestSpecificity`: `application/json` (3), `application/*` (2), `*/*` (1)

**`tape_test.go` -- body marshal/unmarshal tests:**

8. `TestRecordedReq_MarshalJSON_JSONBody`: request with `Content-Type:
   application/json` and JSON body bytes. Verify the marshaled JSON has
   `"body": {"key": "value"}` (native object, not string).

9. `TestRecordedReq_MarshalJSON_TextBody`: request with `Content-Type:
   text/plain` and text body. Verify `"body": "hello world"`.

10. `TestRecordedReq_MarshalJSON_BinaryBody`: request with `Content-Type:
    image/png` and binary body. Verify `"body": "<base64>"`.

11. `TestRecordedReq_MarshalJSON_NilBody`: body is nil. Verify `"body": null`.

12. `TestRecordedReq_MarshalJSON_EmptyBody`: body is `[]byte{}`. Verify
    `"body": null`.

13. `TestRecordedReq_MarshalJSON_InvalidJSONWithJSONCT`: Content-Type claims
    JSON but body is not valid JSON. Verify fallback to base64.

14. `TestRecordedReq_MarshalJSON_VendorJSON`: Content-Type is
    `application/vnd.api+json`. Verify native JSON output.

15. `TestRecordedResp_MarshalJSON_*`: parallel tests for response (same
    cases as above, plus SSE tape where body is null and sse_events is set).

16. `TestRecordedReq_UnmarshalJSON_NativeJSON`: fixture with `"body": {"k": "v"}`.
    Verify `Body` is `[]byte('{"k":"v"}')` (compact JSON).

17. `TestRecordedReq_UnmarshalJSON_StringText`: fixture with `"body": "hello"`.
    Verify `Body` is `[]byte("hello")`.

18. `TestRecordedReq_UnmarshalJSON_Base64Binary`: fixture with `"body": "AQID"`
    and binary Content-Type. Verify `Body` is `[]byte{1, 2, 3}`.

19. `TestRecordedReq_UnmarshalJSON_Null`: fixture with `"body": null`. Verify
    `Body` is nil.

20. `TestRecordedReq_UnmarshalJSON_LegacyBase64JSON`: fixture with
    `"body": "eyJrIjoidiJ9"` and `Content-Type: application/json`. Verify
    `Body` is `[]byte('{"k":"v"}')` (base64-decoded).

21. `TestRoundTrip_JSONBody`: marshal -> unmarshal -> marshal, verify output
    is identical.

22. `TestRoundTrip_TextBody`: same round-trip test for text.

23. `TestRoundTrip_BinaryBody`: same round-trip test for binary.

24. `TestRoundTrip_NilBody`: same round-trip test for nil body.

**`matcher_test.go` -- ContentNegotiationCriterion tests:**

25. `TestContentNegotiationCriterion_ExactMatch`: Accept `application/json`,
    candidate CT `application/json`. Expect score 5.

26. `TestContentNegotiationCriterion_SubtypeWildcard`: Accept `application/*`,
    candidate CT `application/json`. Expect score 4.

27. `TestContentNegotiationCriterion_FullWildcard`: Accept `*/*`, candidate CT
    `application/json`. Expect score 3.

28. `TestContentNegotiationCriterion_NoMatch`: Accept `text/html`, candidate
    CT `application/json`. Expect score 0.

29. `TestContentNegotiationCriterion_QValueZeroExcludes`: Accept
    `application/json;q=0, text/html`. Candidate CT `application/json`.
    Expect score 0.

30. `TestContentNegotiationCriterion_MissingAccept`: no Accept header.
    Candidate CT `application/json`. Expect score 3 (treated as `*/*`).

31. `TestContentNegotiationCriterion_MissingContentType`: Accept
    `application/json`. Candidate has no Content-Type. Expect score 0
    (treated as `application/octet-stream`, which doesn't match
    `application/json`).

32. `TestContentNegotiationCriterion_MalformedAccept`: Accept header is
    garbage. Expect score 3 (treated as `*/*` since all ranges malformed).

33. `TestContentNegotiationCriterion_MultipleRanges`: Accept
    `text/html;q=0.9, application/json, */*;q=0.1`. Candidate CT
    `application/json`. Expect score 5 (exact match with q=1.0).

34. `TestContentNegotiationCriterion_MultiCandidate`: 3 candidates with
    different Content-Types. Verify the matcher selects the correct one based
    on Accept header.

35. `TestContentNegotiationCriterion_Name`: verify `Name()` returns
    `"content_negotiation"`.

**`config_test.go`:**

36. `TestLoadConfig_ContentNegotiationCriterion`: valid config with
    `content_negotiation` criterion type. Verify loads correctly.

37. `TestConfig_BuildMatcher_ContentNegotiation`: build matcher from config
    with `content_negotiation`, verify it returns a working matcher.

38. `TestLoadConfig_ContentNegotiationWithPaths`: config with
    `content_negotiation` + paths. Verify validation error.

**`integration_test.go`:**

39. `TestIntegration_ContentTypeBodyShape`: record a JSON response, verify
    the saved fixture file has native JSON body. Record a text response,
    verify string body. Record a binary response, verify base64 body. Load
    all three, verify replay produces correct bytes.

40. `TestIntegration_ContentNegotiation`: load 3 fixtures for the same path
    with different response Content-Types (JSON, XML, CSV). Configure matcher
    with method + path + content_negotiation. Send requests with varying
    Accept headers. Assert correct fixture selected each time.

**`cmd/httptape/main_test.go`:**

41. `TestMigrateFixtures`: create temp dir with old-format fixture, run
    `migrate-fixtures`, verify file is updated to new format.

42. `TestMigrateFixtures_AlreadyMigrated`: create temp dir with new-format
    fixture, run `migrate-fixtures`, verify file is unchanged.

43. `TestMigrateFixtures_InvalidJSON`: create temp dir with non-JSON file,
    run `migrate-fixtures`, verify it's skipped with no error exit.

#### Alternatives considered

1. **Auto-detect body encoding from content (the original #187 spec):**
   Inspect the body bytes to determine encoding: try JSON parse, check UTF-8
   validity, fall back to base64. Rejected because: (a) auto-detection is
   ambiguous -- a valid UTF-8 string could be text or base64-encoded binary
   that happens to be valid UTF-8; (b) requires heuristics that can produce
   surprising results; (c) Content-Type is the authoritative signal for body
   interpretation in HTTP -- ignoring it in favor of content sniffing is
   an anti-pattern.

2. **Dual encoding with `body` and `bodyText` fields (WireMock style):**
   Two separate fields where `body` holds base64 and `bodyText` holds string.
   Rejected because: (a) two fields for the same concept is confusing; (b)
   requires choosing which field to populate, which is effectively the same
   decision as the single-field approach but with more surface area; (c)
   WireMock's approach was designed for a different context (configuration
   files, not recorded fixtures).

3. **`body_encoding` field as the dispatch signal (the existing approach,
   enhanced):** Keep `body_encoding` field, add `"json"` as a third value.
   When `body_encoding: "json"`, the body field contains native JSON.
   Rejected because: (a) requires fixture authors to manually set
   `body_encoding` correctly, which is error-prone; (b) the Content-Type
   header already contains this information -- duplicating it in a separate
   field violates DRY; (c) the `body_encoding` field has no behavioral effect
   on replay (it's informational only), so it's already dead weight.

4. **Separate cycles for body shape and content negotiation:** Ship body
   shape change as v0.12.0, content negotiation as v0.13.0. Rejected because:
   (a) both features share the MediaType utility -- building it once is
   cleaner; (b) both touch fixtures and docs; (c) two sequential breaking
   changes are more disruptive than one atomic change; (d) the user
   explicitly requested a combined cycle.

5. **External `mime` package for media type parsing:** Go's stdlib
   `mime.ParseMediaType` handles parameter parsing but not Accept header
   splitting or q-value extraction. A third-party package could provide a
   complete RFC 7231 implementation. Rejected because: (a) stdlib-only
   constraint in v1; (b) the subset of RFC 7231 needed here is small enough
   to implement correctly in ~100 lines; (c) `mime.ParseMediaType` handles
   the hard part (parameter parsing with quoted-strings, escaping, etc.) --
   we only need to add comma-splitting and q-value extraction.

#### Consequences

**Benefits:**

- **Fixture DX transformation:** JSON API responses are now human-readable
  in fixture files. Hand-authoring, code review, and git diffs become
  dramatically easier. This was the blocking issue for integrating httptape
  into VibeWarden.
- **Content negotiation support:** multi-format APIs (JSON/XML/CSV) can be
  mocked with separate fixtures at the same path, selected by Accept header.
  This is a natural extension of httptape's matching system and enables
  real-world API mocking patterns.
- **Shared MediaType utility:** the parsing/classification functions are
  useful beyond these two features -- future work (e.g., response
  transformation, conditional body processing) can reuse them.
- **Migration tool:** existing users can upgrade fixtures mechanically
  rather than manually editing each file.
- **Cleaner fixture format:** removing the `body_encoding` field eliminates
  a redundant, informational-only field that added no value.

**Costs / trade-offs:**

- **Breaking change:** fixture JSON format changes. The `body` field shape
  is now polymorphic (object, string, or base64 string depending on
  Content-Type). The `body_encoding` field is removed. All existing fixtures
  must be migrated. Pre-1.0 status makes this acceptable.
- **Custom MarshalJSON/UnmarshalJSON complexity:** custom JSON encoding adds
  code to `tape.go` and requires careful testing. The round-trip property
  must be verified. Edge cases (invalid JSON despite JSON Content-Type,
  base64 strings that happen to look like valid JSON) require careful
  handling.
- **Migration burden:** every fixture in the repo (and every external user's
  fixtures) must be migrated. The migration tool mitigates this but is still
  work. External users pinned to v0.11 who upgrade will need to run the tool.
- **Variable scoring in ContentNegotiationCriterion:** the 3/4/5 scoring
  scheme (wildcard/subtype-wildcard/exact) works for the common case but
  does not handle complex q-value scenarios perfectly. For example, two
  Accept ranges with different q-values matching the same Content-Type at
  different specificities could theoretically produce counter-intuitive
  results. This is acceptable for v0.12.0 -- the scoring is deterministic
  and correct for all practical use cases.

**Future implications:**

- The multi-Content-Type fixture pattern (JSON + XML + CSV at the same path)
  becomes idiomatic and can be documented as a first-class feature.
- `docs/fixtures-authoring.md` needs to be updated every time a new body
  shape is added (unlikely -- the three shapes cover all practical cases).
- The `body_encoding` removal is permanent. If a future need arises for
  explicit encoding hints, a new field with a different design should be
  considered rather than resurrecting the old one.
- The `media_type.go` utility surface is public API. Adding new classifiers
  (e.g., `IsMultipart`) is additive and backward-compatible.

---

### ADR-42: Template helpers, `PathPatternCriterion`, and `{{pathParam.*}}` accessors

**Date**: 2026-04-18
**Issue**: #196
**Status**: Accepted

> **Note**: Issue #199 is being architected in parallel by another architect and may
> produce ADR-43. If both land with the same number, the orchestrator will renumber
> during merge reconciliation.

#### Context

httptape has `{{request.*}}` templating in response bodies and headers (shipped
in v0.11). The existing template system resolves `request.method`, `request.path`,
`request.url`, `request.headers.<N>`, `request.query.<N>`, and
`request.body.<path>` -- all accessor-style, no arguments, dispatched via simple
string prefix matching in `resolveExpr()` in `templating.go`.

Realistic test fixtures need more: timestamps, UUIDs, counters, random strings, and
deterministic fake data that stays consistent across related requests. Additionally,
fixtures serving parameterized routes (e.g., `/users/:id`) need to extract path
parameters and use them in response templates.

Issue #196 bundles three concerns:

1. **Template helpers** -- new `{{...}}` expressions with keyword arguments (`now`,
   `uuid`, `randomHex`, `randomInt`, `counter`, `faker.*`).
2. **`PathPatternCriterion`** -- a new `Criterion` that matches Express-style URL
   patterns (e.g., `/users/:id`) and exposes captured segments.
3. **`{{pathParam.*}}` accessor** -- resolves captured path segments from
   `PathPatternCriterion` in template expressions.

Issue #199 (synthetic responses via exemplar tapes) has a hard dependency on #196.
The design must ensure that `PathPatternCriterion`, `{{pathParam.*}}`, `{{faker.*}}`,
and the helper argument parsing are stable enough for #199 to build on without
rework.

#### Decision

##### Open question resolutions

**Q1: Pattern syntax -- Express-style `:id` vs OpenAPI-style `{id}`**

Decision: **Express-style `:id`**.

Rationale:
- Express `:id` is the dominant idiom in Go HTTP routers (`chi`, `gin`, `gorilla/mux`
  v1, `httprouter`). Go developers will recognize it immediately.
- OpenAPI `{id}` uses curly braces, which collide with httptape's `{{...}}` template
  delimiters. A pattern like `/users/{id}` in a JSON string creates visual noise
  next to `"body": "{{faker.name}}"`. Express style avoids this.
- The colon prefix is unambiguous: a path segment starting with `:` is always a
  named capture; literal colons in URLs are percent-encoded (`%3A`).

**Q2: Match precedence weight for `PathPatternCriterion`**

Decision: **Score 3** (same as `HeadersCriterion`).

Rationale:
- `PathPatternCriterion` is more specific than `PathRegexCriterion` (score 1) because
  it carries structural semantics (named segments, fixed literal segments), but less
  specific than `QueryParamsCriterion` (score 4) which requires field-level equality.
- It must score *higher* than `PathCriterion` (score 2) when both are present in a
  `CompositeMatcher`, because a pattern match `/users/:id` matching the request
  `/users/42` is a positive signal -- but `PathCriterion` would return 0 for this
  pair (no exact match), eliminating the candidate. In practice, `PathPatternCriterion`
  and `PathCriterion` should NOT coexist in the same matcher (documented). If they do,
  `PathCriterion` dominates (returns 0 for non-exact paths, eliminating candidates
  that `PathPatternCriterion` would accept).
- Score 3 ties with `HeadersCriterion`. This is acceptable: both represent a
  "moderately specific" match dimension. Composite scoring still differentiates
  them when combined with other criteria.

Updated weight table:

| Criterion                     | Score |
|-------------------------------|-------|
| MethodCriterion               | 1     |
| PathCriterion                 | 2     |
| RouteCriterion                | 1     |
| PathRegexCriterion            | 1     |
| **PathPatternCriterion**      | **3** |
| HeadersCriterion              | 3     |
| QueryParamsCriterion          | 4     |
| ContentNegotiationCriterion   | 3-5   |
| BodyFuzzyCriterion            | 6     |
| BodyHashCriterion             | 8     |

**Q3: Query-param-in-pattern support**

Decision: **Out of scope.** Query params are accessed via `{{request.query.*}}`.
`PathPatternCriterion` matches the path component only. This is consistent with
how Go HTTP routers work (path routing is separate from query parsing).

**Q4: Parser strategy -- extend ad-hoc or write a proper tokenizer**

Decision: **Replace with a small tokenizer/mini-parser in `templating.go`.**

Rationale:
- The current `resolveExpr()` is prefix-matching on raw strings. Adding keyword
  arguments (`seed=val`, `format=unix`, `min=0 max=100`) to the same approach would
  require increasingly fragile string splitting.
- A proper tokenizer is ~150 lines of Go (scan for function name, then iterate
  key=value pairs with quoted-string support). The complexity budget is bounded
  because we explicitly exclude conditionals, loops, and partials (#196 scope).
- The tokenizer enables clean extension for #199 (pipe operators `| int`).
- The scan step (`scanTemplateExprs`) is unchanged -- it still finds `{{...}}`
  boundaries. Only the *evaluation* of the inner expression changes: instead of
  raw string prefix matching, the inner text is parsed into a `parsedExpr` struct.

**Q5: Faker seed fallback -- `{{faker.email}}` without explicit `seed=`**

Decision: **Hash `tape.ID + request.URL.Path` as automatic seed.**

When `{{faker.email}}` is used without a `seed=` argument:
1. Concatenate `tape.ID + ":" + r.URL.Path`.
2. SHA-256 hash the concatenation, hex-encode, take the first 16 chars.
3. Use the result as the seed argument to the Faker.

This produces a deterministic seed that varies per tape and per request path, so
`/users/1` and `/users/2` get different fakes, and different tapes for the same
path also get different fakes. The seed is stable across server restarts because
it depends only on the tape ID (stored in the fixture) and the request path
(determined by the client).

Rationale for this approach over alternatives:
- Using only `request.path` would cause different tapes for the same path to produce
  identical fakes (collision).
- Using the full request (body hash, headers) would make fakes vary by request
  details that shouldn't affect response generation.
- Using `tape.ID` alone would make all requests to the same tape produce the same
  fakes (no per-path variation).

**Q6: Counter overflow**

Decision: **Wrap at math.MaxInt64 back to 0.** Silent wrap, no error, no log.

Rationale:
- A counter that wraps at 2^63 will never wrap in practice (at 1M requests/sec,
  it would take ~292,000 years).
- Erroring would be worse than wrapping because it would break tests that happen
  to be running near the limit.
- Wrapping is consistent with Go's integer overflow behavior (not a panic).
- Testing: unit test explicitly sets the counter to `math.MaxInt64 - 1` and verifies
  the next call returns `math.MaxInt64`, and the one after returns `0`.

**Q7: Type coercion helpers (`| int`, `| float`, `| bool`)**

Decision: **Deferred to #199.** The issue body explicitly says "deferred to #199
where they are needed for exemplar JSON body rendering." This ADR does not design
pipe operators. However, the parser design accommodates them: the `parsedExpr`
struct has room for a `coerce` field, and the tokenizer recognizes `|` as a
delimiter. #199's architect can extend without reworking the parser.

##### Template parser design

###### `parsedExpr` type (new, in `templating.go`)

```go
// parsedExpr is a parsed template expression. It represents either an accessor
// (e.g., "request.path") or a helper call (e.g., "faker.email seed=user-42").
type parsedExpr struct {
    // name is the function/accessor name (e.g., "request.path", "now",
    // "faker.email", "counter", "pathParam.id").
    name string

    // args holds keyword arguments as key-value pairs.
    // Nil for accessor-style expressions (request.*, pathParam.*).
    // Example: {"seed": "user-42", "format": "unix"}.
    args map[string]string
}
```

###### `parseExpr` function (new, in `templating.go`)

```go
// parseExpr parses the inner text of a {{...}} expression into a parsedExpr.
// It splits the text into a function name and optional keyword arguments.
//
// Syntax:
//   - Accessors: "request.path", "pathParam.id"
//   - Helpers without args: "now", "uuid"
//   - Helpers with args: "now format=unix", "faker.email seed=user-42"
//   - Quoted arg values: "randomHex length=16"
//
// Keyword argument values may contain nested {{...}} expressions which are
// resolved before the helper is invoked (see resolveArgs).
//
// Returns the parsed expression. If the input is empty, returns a parsedExpr
// with an empty name.
func parseExpr(raw string) parsedExpr
```

The parser:
1. Trims whitespace.
2. Splits on the first whitespace to get `name` and the rest.
3. If there is a rest, scans for `key=value` pairs. Values are
   unquoted strings (no quotes needed because `}}` terminates the expression
   and `=` is only used as key-value separator). A value runs until the next
   whitespace or end-of-string.
4. Returns `parsedExpr{name, args}`.

Note: quoted values are not needed in v1. The only characters that could be
ambiguous are spaces, but helper argument values (seeds, format strings, numbers)
do not contain spaces in any planned use case. If a future helper needs spaces in
values, quoted-string support can be added to `parseExpr` without changing the
`parsedExpr` type.

###### Nested template evaluation in arguments

When a helper argument contains `{{...}}` (e.g., `seed=user-{{pathParam.id}}`),
the nested expression must be resolved before the helper runs. This is a
single-pass approach:

1. `parseExpr` stores the raw argument value (e.g., `"user-{{pathParam.id}}"`).
2. Before invoking the helper, each argument value is scanned for `{{...}}`.
   If found, the inner expressions are resolved recursively using the same
   evaluation context. The resolved value replaces the nested `{{...}}`.
3. The helper receives the fully-resolved argument value.

Recursion depth is bounded: nested expressions inside argument values are always
accessors (`pathParam.*`, `request.*`) which do not themselves have arguments.
A nested helper-in-an-argument (e.g., `seed={{uuid}}`) is technically possible
but the single-pass resolver handles it correctly because `uuid` is a zero-arg
helper. Deeper nesting (helper inside helper inside argument) is not supported
and would be left as literal text -- this is documented as a limitation.

The resolution function:

```go
// resolveArgs resolves nested {{...}} expressions in helper argument values.
// It uses the same evaluation context (request, pathParams, tape) as the
// top-level resolver. Returns the args map with all nested expressions resolved.
func resolveArgs(args map[string]string, ctx *templateCtx) map[string]string
```

###### Evaluation context

The `resolveExpr` function currently takes `(expr string, r *http.Request, reqBody []byte)`.
This must be extended to carry additional context: path parameters, tape metadata
(for auto-seed), and server-level state (counters). Rather than adding more
parameters, introduce a context struct:

```go
// templateCtx holds the evaluation context for template resolution.
// It is constructed per-request by the Server before template resolution
// and passed through to all resolvers.
type templateCtx struct {
    // req is the incoming HTTP request.
    req *http.Request
    // reqBody is the cached request body bytes.
    reqBody []byte
    // pathParams holds captured path segments from PathPatternCriterion.
    // Nil if no path pattern was used for matching.
    pathParams map[string]string
    // tapeID is the matched tape's ID (used for auto-seed generation).
    tapeID string
    // counters is the server's counter state (shared, mutex-protected).
    counters *counterState
    // randSource provides randomness for non-deterministic helpers
    // (uuid, randomHex, randomInt). Injectable for testing.
    randSource io.Reader
}
```

The existing `resolveExpr` function signature changes to:

```go
func resolveExpr(expr string, ctx *templateCtx) (string, bool)
```

And internally dispatches on the `parsedExpr.name` prefix:
- `request.*` -- existing accessor logic (unchanged behavior)
- `pathParam.*` -- new accessor, reads from `ctx.pathParams`
- `now` -- new helper
- `uuid` -- new helper
- `randomHex` -- new helper
- `randomInt` -- new helper
- `counter` -- new helper
- `faker.*` -- new helper bridge

`ResolveTemplateBody` and `ResolveTemplateHeaders` gain an additional `ctx *templateCtx`
parameter. Their public signatures change:

```go
func ResolveTemplateBody(body []byte, ctx *templateCtx, strict bool) ([]byte, error)
func ResolveTemplateHeaders(h http.Header, ctx *templateCtx, strict bool) (http.Header, error)
```

This is a breaking change to two exported functions. Pre-1.0, acceptable. The
only call sites are in `server.go` `ServeHTTP` and the test file.

##### Helper implementations

Each helper is implemented as a function in `templating.go`. Helpers are not
an interface or registry -- they are a fixed set dispatched by name in a
switch statement inside `resolveExpr`. This keeps the code simple and avoids
over-engineering. If user-defined helpers are ever needed (out of scope per
#196), a registry pattern can be introduced later.

###### `now`

```go
// resolveNow returns the current UTC time formatted per the "format" argument.
// Supported formats: "rfc3339" (default), "iso" (alias for rfc3339),
// "unix" (seconds since epoch), "unixMillis" (ms since epoch).
// Custom Go time format strings are also accepted (e.g., "2006-01-02").
func resolveNow(args map[string]string) string
```

Arguments:
- `format` (optional, default `"rfc3339"`): output format.

Implementation: `time.Now().UTC()`, then format based on the `format` arg.
For `"unix"`: `strconv.FormatInt(t.Unix(), 10)`. For `"unixMillis"`:
`strconv.FormatInt(t.UnixMilli(), 10)`. For others: `t.Format(formatStr)`.

Testability: the `templateCtx` does NOT carry an injectable clock. `now` is
inherently non-deterministic (returns the actual current time). Tests that use
`now` verify the output is a valid RFC3339 string and was produced within the
last second, rather than comparing exact values. This is the standard Go
approach for time-dependent functions.

###### `uuid`

```go
// resolveUUID generates a random UUID v4 string.
func resolveUUID(ctx *templateCtx) string
```

Uses `ctx.randSource` (defaults to `crypto/rand.Reader`). Generates 16 random
bytes, sets version and variant bits per RFC 4122 section 4.4, formats as
`xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx`.

This is non-deterministic by default. For deterministic UUIDs, use
`{{faker.uuid seed=...}}` instead.

###### `randomHex`

```go
// resolveRandomHex generates a random hex string of the specified length.
func resolveRandomHex(args map[string]string, ctx *templateCtx) (string, error)
```

Arguments:
- `length` (required): number of hex characters to produce.

Implementation: read `length/2` (rounded up) random bytes from `ctx.randSource`,
hex-encode, truncate to `length`.

Error if `length` is missing, non-numeric, or <= 0.

###### `randomInt`

```go
// resolveRandomInt generates a random integer in [min, max].
func resolveRandomInt(args map[string]string, ctx *templateCtx) (string, error)
```

Arguments:
- `min` (optional, default `0`): inclusive minimum.
- `max` (optional, default `100`): inclusive maximum.

Implementation: read bytes from `ctx.randSource`, convert to int, mod into
range. Error if `min` or `max` is non-numeric or if `min > max`.

###### `counter`

```go
// resolveCounter increments and returns a named counter.
func resolveCounter(args map[string]string, ctx *templateCtx) string
```

Arguments:
- `name` (optional, default `"default"`): counter name.

Returns the post-increment value as a decimal string.

Counter state is held on the `Server` in a `counterState` struct:

```go
// counterState manages named counters for the {{counter}} template helper.
// Thread-safe via a sync.Mutex.
type counterState struct {
    mu       sync.Mutex
    counters map[string]int64
}

// Next increments the named counter and returns the new value.
// The counter starts at 1 on first call. Wraps to 0 at math.MaxInt64.
func (cs *counterState) Next(name string) int64
```

The `counterState` lives on the `Server` struct as a private field, initialized
in `NewServer`. It is passed into the `templateCtx` on each request.

Reset API:

```go
// ResetCounter resets the named counter to 0. If name is empty, all counters
// are reset.
func (s *Server) ResetCounter(name string)
```

This is the Go API for counter reset. An admin HTTP endpoint for reset is out
of scope for #196 (tied to #194, the admin API feature).

###### `faker.*`

The `faker.*` helpers bridge to the existing `Faker` interface in `faker.go`.
The suffix after `faker.` maps to a faker type:

| Template expression        | Faker type        |
|----------------------------|-------------------|
| `{{faker.email ...}}`      | `EmailFaker{}`    |
| `{{faker.name ...}}`       | `NameFaker{}`     |
| `{{faker.phone ...}}`      | `PhoneFaker{}`    |
| `{{faker.address ...}}`    | `AddressFaker{}`  |
| `{{faker.creditCard ...}}` | `CreditCardFaker{}` |
| `{{faker.hmac ...}}`       | `HMACFaker{}`     |
| `{{faker.redacted ...}}`   | `RedactedFaker{}` |
| `{{faker.uuid ...}}`       | `PrefixFaker{Prefix: ""}` + UUID formatting |

For `faker.uuid`: since there is no dedicated UUID faker in the existing
infrastructure, implement as `PrefixFaker{Prefix: ""}` applied to a UUID-format
seed, then format the hex output as `xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx`
with version/variant bits set. Alternatively, a new unexported `uuidFaker`
function that uses `computeHMAC` directly to produce 16 bytes, sets UUID v4
bits, and formats. This is an implementation detail.

The `faker.*` helpers do NOT expose `NumericFaker`, `DateFaker`, `PatternFaker`,
or `PrefixFaker` via template syntax because those require type-specific
parameters (`length`, `format`, `pattern`, `prefix`) that would complicate the
argument parsing. They remain accessible via the Go API (`FakeFieldsWith`) and
JSON config.

Arguments:
- `seed` (optional): the HMAC seed. If omitted, uses the auto-seed derived from
  `tape.ID + ":" + request.URL.Path` (per Q5 resolution).

Resolution function:

```go
// resolveFaker dispatches to the appropriate Faker and returns its output.
// The fakerType is the suffix after "faker." (e.g., "email", "name").
// The seed is resolved from args or auto-generated.
func resolveFaker(fakerType string, args map[string]string, ctx *templateCtx) (string, bool)
```

The Faker's `Fake(seed, original)` method is called with:
- `seed` = the resolved seed value.
- `original` = the seed value (acting as both seed and original input, since
  there is no "original" value in the template context -- the Faker is generating
  from scratch, not transforming an existing value).

The output of `Fake()` is converted to string via `fmt.Sprintf("%v", result)`.

##### `PathPatternCriterion` design

###### Type

```go
// PathPatternCriterion matches the incoming request's URL path against an
// Express-style pattern with named segments (e.g., "/users/:id").
//
// Named segments start with a colon and match any single non-empty path
// segment (i.e., one or more characters excluding '/'). The captured values
// are exposed to the template evaluation context via {{pathParam.NAME}}.
//
// Examples:
//   - "/users/:id" matches "/users/42" (id="42"), not "/users/", "/users"
//   - "/users/:id/orders/:oid" matches "/users/1/orders/7" (id="1", oid="7")
//   - "/api/v1/items" matches only "/api/v1/items" (no captures, acts like PathCriterion)
//
// PathPatternCriterion should NOT be used alongside PathCriterion in the same
// CompositeMatcher. PathCriterion returns 0 for paths that don't exactly match,
// which eliminates candidates that PathPatternCriterion would accept.
//
// Returns score 3 on match, 0 on mismatch.
//
// The pattern is compiled into a regex at construction time (via
// NewPathPatternCriterion). The compiled regex and capture-name list are stored
// as private fields for efficient per-request matching.
type PathPatternCriterion struct {
    // Pattern is the original Express-style pattern string.
    Pattern string

    re         *regexp.Regexp
    paramNames []string
}
```

###### Constructor

```go
// NewPathPatternCriterion compiles an Express-style path pattern into a
// PathPatternCriterion. Named segments (e.g., ":id") are converted to named
// regex capture groups. Returns an error if the pattern is malformed.
//
// Pattern syntax:
//   - Literal segments: "/users", "/api/v1"
//   - Named segments: "/:id", "/:userId" (matches one or more non-'/' chars)
//   - Mixed: "/users/:id/orders/:orderId"
//
// The compiled regex anchors to the full path (^ ... $). Trailing slashes are
// significant: "/users/:id" does NOT match "/users/42/".
func NewPathPatternCriterion(pattern string) (*PathPatternCriterion, error)
```

Pattern compilation:
1. Split pattern on `/`.
2. For each segment:
   - If it starts with `:`, extract the param name (rest of segment), emit
     `(?P<name>[^/]+)` regex group.
   - Otherwise, emit the literal segment (regex-escaped via `regexp.QuoteMeta`).
3. Join with `/`, anchor with `^` and `$`.
4. Compile with `regexp.Compile`.
5. Store `paramNames` in order of appearance (for efficient extraction).

Validation:
- Empty param names (`:` alone) produce an error.
- Duplicate param names produce an error.
- Pattern must start with `/`.

###### Score method

```go
func (c *PathPatternCriterion) Score(req *http.Request, candidate Tape) int
```

1. Match `req.URL.Path` against `c.re`. If no match, return 0.
2. Parse `candidate.Request.URL` to get its path. Match against `c.re`. If no
   match, return 0. (Both request and tape must match the pattern, same logic
   as `PathRegexCriterion`.)
3. Return 3.

Note: the captured path params are NOT extracted in `Score`. They are extracted
separately by the `Server` after matching, when constructing the `templateCtx`.
This keeps the `Criterion` interface clean (no side effects, no output beyond
the score).

###### Param extraction method

```go
// ExtractParams extracts the named path parameters from a URL path that
// matches this criterion's pattern. Returns nil if the path does not match.
// This method is called by the Server after matching to populate the
// template evaluation context.
func (c *PathPatternCriterion) ExtractParams(path string) map[string]string
```

###### Name method

```go
func (c *PathPatternCriterion) Name() string { return "path_pattern" }
```

##### `{{pathParam.*}}` accessor

In `resolveExpr`, add a case for the `pathParam.` prefix:

```go
case strings.HasPrefix(pe.name, "pathParam."):
    paramName := pe.name[len("pathParam."):]
    if ctx.pathParams == nil {
        return "", false
    }
    val, ok := ctx.pathParams[paramName]
    if !ok {
        return "", false
    }
    return val, true
```

This follows the same pattern as `request.headers.<N>` and `request.query.<N>`.

##### Server integration

In `Server.ServeHTTP`, after matching a tape (step 4), before template resolution
(step 9):

1. Check if the `Server.matcher` is a `*CompositeMatcher`. If so, iterate its
   criteria to find a `*PathPatternCriterion`. If found, call
   `ExtractParams(req.URL.Path)` to get path params.
2. Construct `templateCtx` with the request, request body, path params, tape ID,
   counters, and rand source.
3. Pass `templateCtx` to `ResolveTemplateBody` and `ResolveTemplateHeaders`.

If no `PathPatternCriterion` is in the matcher, `pathParams` is nil and
`{{pathParam.*}}` expressions resolve to unresolvable (empty in lenient mode,
error in strict mode).

The `Server` struct gains two new private fields:

```go
type Server struct {
    // ... existing fields ...
    counters   *counterState
    randSource io.Reader
}
```

`NewServer` initializes `counters` and `randSource`:

```go
s.counters = &counterState{counters: make(map[string]int64)}
if s.randSource == nil {
    s.randSource = rand.Reader // crypto/rand
}
```

A new unexported `ServerOption` for testing:

```go
func withRandSource(r io.Reader) ServerOption {
    return func(s *Server) { s.randSource = r }
}
```

##### Recursive template resolution in native JSON body objects

ADR-41 introduced Content-Type-driven body shapes where JSON bodies are stored
as native JSON objects in fixtures. When templating resolves, it must handle
these native JSON objects:

The current `ResolveTemplateBody` scans `body []byte` for `{{...}}` and performs
string-level substitution. For native JSON body objects, this still works because
the body is stored as `[]byte` in the `Tape` struct (the native JSON is
marshaled back to bytes before template resolution). Template expressions
embedded in JSON string values (e.g., `"name": "{{faker.name}}"`) are correctly
found and resolved by the byte-level scanner.

However, there is a subtlety: when the resolved value contains characters that
would break JSON syntax (e.g., a faker produces `"John "The Rock" Johnson"`),
the result would be malformed JSON. The solution is:

1. If the body `Content-Type` is JSON-flavored, and the body contains `{{`:
   a. Unmarshal the body as `any` (JSON tree).
   b. Walk the tree recursively. For each string leaf that contains `{{`,
      resolve templates in that string.
   c. Re-marshal the tree to JSON.
   This ensures all resolved values are properly JSON-escaped.

2. If the body is not JSON, use the existing byte-level string substitution
   (unchanged).

New function:

```go
// resolveTemplateJSON resolves template expressions in a JSON body by
// walking the deserialized JSON tree. String values containing {{...}} are
// resolved; non-string values are left unchanged. Returns the re-serialized
// JSON bytes.
//
// This function ensures that resolved values are properly JSON-escaped,
// which byte-level string substitution cannot guarantee.
func resolveTemplateJSON(body []byte, ctx *templateCtx, strict bool) ([]byte, error)
```

The function:
1. `json.Unmarshal(body, &data)`.
2. Recursively walk `data`:
   - `map[string]any`: recurse into each value.
   - `[]any`: recurse into each element.
   - `string`: if contains `{{`, resolve via `resolveTemplateString`.
   - Other types (`float64`, `bool`, `nil`): leave as-is.
3. `json.Marshal(data)`.

`ResolveTemplateBody` checks the tape's response `Content-Type` (passed via
`templateCtx` or inferred from the body) to decide which resolution path to take.
For simplicity, the heuristic is: if the body starts with `{` or `[` (after
trimming whitespace), use `resolveTemplateJSON`; otherwise, use byte-level
substitution. This avoids needing the Content-Type in the template context.

##### Config integration

Add `"path_pattern"` to the `criterionBuilders` dispatch table in `config.go`:

```go
"path_pattern": {validate: validatePathPatternCriterion, build: buildPathPatternCriterion},
```

The `CriterionConfig` struct gains a new field:

```go
type CriterionConfig struct {
    Type    string   `json:"type"`
    Paths   []string `json:"paths,omitempty"`
    Pattern string   `json:"pattern,omitempty"` // for path_pattern
}
```

Validation:
- `path_pattern` requires non-empty `Pattern`.
- `path_pattern` rejects `Paths`.
- All other criterion types reject `Pattern`.

Builder:
```go
func buildPathPatternCriterion(cc CriterionConfig) (Criterion, error) {
    return NewPathPatternCriterion(cc.Pattern)
}
```

##### JSON Schema extension (`config.schema.json`)

Add `"path_pattern"` to the criterion type enum:
```json
"enum": ["method", "path", "body_fuzzy", "content_negotiation", "path_pattern"]
```

Add `pattern` property to the criterion item:
```json
"pattern": {
    "type": "string",
    "minLength": 1,
    "description": "Express-style path pattern for path_pattern criterion (e.g., /users/:id)."
}
```

Add `if/then` constraints:
- `path_pattern` requires `pattern`, rejects `paths`.
- All existing types reject `pattern`.

#### Types

| Type | File | Description |
|---|---|---|
| `parsedExpr` | `templating.go` | Parsed template expression with name and keyword arguments |
| `templateCtx` | `templating.go` | Per-request evaluation context for template resolution |
| `counterState` | `templating.go` | Thread-safe named counter state, lives on Server |
| `PathPatternCriterion` | `matcher.go` | Criterion matching Express-style URL patterns with named captures |

Modified types:

| Type | File | Change |
|---|---|---|
| `Server` | `server.go` | Add `counters *counterState` and `randSource io.Reader` fields |
| `CriterionConfig` | `config.go` | Add `Pattern string` field |

#### Functions and methods

New exported:

| Function / Method | Signature | File |
|---|---|---|
| `NewPathPatternCriterion` | `func NewPathPatternCriterion(pattern string) (*PathPatternCriterion, error)` | `matcher.go` |
| `PathPatternCriterion.Score` | `func (c *PathPatternCriterion) Score(req *http.Request, candidate Tape) int` | `matcher.go` |
| `PathPatternCriterion.Name` | `func (c *PathPatternCriterion) Name() string` | `matcher.go` |
| `PathPatternCriterion.ExtractParams` | `func (c *PathPatternCriterion) ExtractParams(path string) map[string]string` | `matcher.go` |
| `Server.ResetCounter` | `func (s *Server) ResetCounter(name string)` | `server.go` |
| `ResolveTemplateBody` | `func ResolveTemplateBody(body []byte, ctx *templateCtx, strict bool) ([]byte, error)` | `templating.go` (signature change) |
| `ResolveTemplateHeaders` | `func ResolveTemplateHeaders(h http.Header, ctx *templateCtx, strict bool) (http.Header, error)` | `templating.go` (signature change) |

New unexported:

| Function / Method | Signature | File |
|---|---|---|
| `parseExpr` | `func parseExpr(raw string) parsedExpr` | `templating.go` |
| `resolveArgs` | `func resolveArgs(args map[string]string, ctx *templateCtx) map[string]string` | `templating.go` |
| `resolveNow` | `func resolveNow(args map[string]string) string` | `templating.go` |
| `resolveUUID` | `func resolveUUID(ctx *templateCtx) string` | `templating.go` |
| `resolveRandomHex` | `func resolveRandomHex(args map[string]string, ctx *templateCtx) (string, error)` | `templating.go` |
| `resolveRandomInt` | `func resolveRandomInt(args map[string]string, ctx *templateCtx) (string, error)` | `templating.go` |
| `resolveCounter` | `func resolveCounter(args map[string]string, ctx *templateCtx) string` | `templating.go` |
| `resolveFaker` | `func resolveFaker(fakerType string, args map[string]string, ctx *templateCtx) (string, bool)` | `templating.go` |
| `resolveTemplateJSON` | `func resolveTemplateJSON(body []byte, ctx *templateCtx, strict bool) ([]byte, error)` | `templating.go` |
| `counterState.Next` | `func (cs *counterState) Next(name string) int64` | `templating.go` |
| `counterState.Reset` | `func (cs *counterState) Reset(name string)` | `templating.go` |
| `validatePathPatternCriterion` | `func validatePathPatternCriterion(cc CriterionConfig) error` | `config.go` |
| `buildPathPatternCriterion` | `func buildPathPatternCriterion(cc CriterionConfig) (Criterion, error)` | `config.go` |
| `withRandSource` | `func withRandSource(r io.Reader) ServerOption` | `server.go` |
| `autoSeed` | `func autoSeed(tapeID, path string) string` | `templating.go` |

Note on `templateCtx` exported/unexported status: `templateCtx` is **unexported**.
The `ResolveTemplateBody` and `ResolveTemplateHeaders` functions use it as a parameter,
but since `templateCtx` is unexported, these functions cannot be called by external
packages with a custom context. This is intentional: the `templateCtx` is an
internal implementation detail of the `Server`. External callers who used the
old `ResolveTemplateBody(body, req, strict)` API lose the ability to call it
directly. If this is a concern, a convenience wrapper can be added:

```go
// ResolveTemplateBodySimple is a backward-compatible convenience wrapper that
// resolves templates using only request data (no path params, no counters,
// no faker). Equivalent to the pre-#196 ResolveTemplateBody behavior.
func ResolveTemplateBodySimple(body []byte, r *http.Request, strict bool) ([]byte, error)
```

This wrapper constructs a minimal `templateCtx` internally.

#### File layout

| File | Changes |
|---|---|
| `templating.go` | Major rework. Add `parsedExpr`, `templateCtx`, `counterState` types. Add `parseExpr`, `resolveArgs`, `autoSeed` functions. Add helper functions: `resolveNow`, `resolveUUID`, `resolveRandomHex`, `resolveRandomInt`, `resolveCounter`, `resolveFaker`. Add `resolveTemplateJSON` for JSON-aware resolution. Refactor `resolveExpr` to use `parsedExpr` and `templateCtx`. Change `ResolveTemplateBody` and `ResolveTemplateHeaders` signatures to accept `*templateCtx`. Add `ResolveTemplateBodySimple` backward-compat wrapper. |
| `templating_test.go` | Major expansion. Add tests for `parseExpr`, all helpers, `pathParam.*` accessor, nested template evaluation, counter state, JSON-aware resolution, backward-compat wrapper. |
| `matcher.go` | Add `PathPatternCriterion` struct, `NewPathPatternCriterion` constructor, `Score`, `Name`, `ExtractParams` methods. Update `CompositeMatcher` godoc score table. |
| `matcher_test.go` | Add `PathPatternCriterion` tests: pattern matching, param extraction, edge cases, malformed patterns, coexistence warnings. |
| `server.go` | Add `counters *counterState` and `randSource io.Reader` to `Server`. Initialize in `NewServer`. Add `ResetCounter` method. Add `withRandSource` test option. Update `ServeHTTP` to construct `templateCtx` and pass it to resolve functions. Extract path params from matcher if `PathPatternCriterion` is present. |
| `server_test.go` | Add tests for counter reset, path-param extraction in ServeHTTP, end-to-end templating with helpers. |
| `config.go` | Add `Pattern string` field to `CriterionConfig`. Add `"path_pattern"` to `criterionBuilders`. Add `validatePathPatternCriterion` and `buildPathPatternCriterion`. Update validation for existing types to reject `Pattern`. |
| `config_test.go` | Add tests for `path_pattern` config loading, validation, `BuildMatcher`. |
| `config.schema.json` | Add `"path_pattern"` to criterion type enum. Add `pattern` property. Add `if/then` constraints. |
| `docs/template-helpers.md` | New file. Comprehensive documentation of all template helpers, `pathParam.*` accessor, keyword arguments, nested evaluation, faker bridge, counter behavior. |
| `docs/matching.md` | Add `PathPatternCriterion` section with pattern syntax, score, and usage notes. |
| `docs/replay.md` | Update templating section with cross-link to `docs/template-helpers.md`. |
| `docs/api-reference.md` | Add entries for `PathPatternCriterion`, `NewPathPatternCriterion`, `Server.ResetCounter`, `ResolveTemplateBodySimple`, updated `ResolveTemplateBody` signature. |
| `CLAUDE.md` | Add `template_helpers.md` to docs list. No package structure changes (all code stays in existing files). |

No new Go source files are created. All template logic stays in `templating.go`
(consistent with the existing layout where template code already lives there,
not in `server.go`).

#### Sequence

**Request/response flow with path-param templating and helpers:**

1. Request arrives at `Server.ServeHTTP` (e.g., `GET /users/42`).
2. CORS, error simulation checks (unchanged).
3. Server loads tapes from store.
4. `matcher.Match(req, tapes)` runs. If `PathPatternCriterion` is among the
   criteria, its `Score` method matches `/users/42` against `/users/:id` pattern.
   Returns score 3. Other criteria score as usual. Best-scoring tape wins.
5. No-match fallback (unchanged).
6. Per-fixture error override, delay (unchanged).
7. SSE check (unchanged -- SSE tapes skip templating).
8. **Construct `templateCtx`:**
   a. Read request body (once).
   b. If matcher is `*CompositeMatcher`, search its criteria for `*PathPatternCriterion`.
      If found, call `ExtractParams(req.URL.Path)` -> `{"id": "42"}`.
   c. Build `templateCtx{req, reqBody, pathParams, tape.ID, s.counters, s.randSource}`.
9. **Resolve templates:**
   a. `ResolveTemplateBody(tape.Response.Body, ctx, strict)`:
      - Fast path: no `{{` -> return body as-is.
      - Detect JSON body (starts with `{` or `[`): use `resolveTemplateJSON`.
      - Otherwise: use byte-level `scanTemplateExprs` + `resolveExpr` loop.
   b. `ResolveTemplateHeaders(tape.Response.Headers, ctx, strict)`:
      - Same per-value resolution as before, using new `resolveExpr` with `templateCtx`.
10. For each `{{...}}` expression:
    a. `parseExpr(raw)` -> `parsedExpr{name, args}`.
    b. `resolveArgs(args, ctx)` -> resolve any nested `{{...}}` in arg values.
    c. Dispatch on `name`:
       - `request.*` -> existing accessor logic.
       - `pathParam.*` -> lookup in `ctx.pathParams`.
       - `now` -> `resolveNow(args)`.
       - `uuid` -> `resolveUUID(ctx)`.
       - `randomHex` -> `resolveRandomHex(args, ctx)`.
       - `randomInt` -> `resolveRandomInt(args, ctx)`.
       - `counter` -> `resolveCounter(args, ctx)`.
       - `faker.*` -> `resolveFaker(suffix, args, ctx)`.
       - Other -> unresolvable (lenient: empty, strict: error).
    d. Replace `{{...}}` with resolved value.
11. Write response (unchanged).

#### Error cases

| Error | Where | Handling |
|---|---|---|
| Malformed path pattern (no leading `/`) | `NewPathPatternCriterion` | Return `nil, fmt.Errorf(...)` |
| Empty param name in pattern (`:` alone) | `NewPathPatternCriterion` | Return `nil, fmt.Errorf(...)` |
| Duplicate param names in pattern | `NewPathPatternCriterion` | Return `nil, fmt.Errorf(...)` |
| Invalid regex from pattern compilation | `NewPathPatternCriterion` | Return `nil, fmt.Errorf(...)` |
| Missing required arg (`randomHex` without `length`) | `resolveRandomHex` | Return `("", error)` -> strict: 500, lenient: empty |
| Non-numeric arg value (`length=abc`) | `resolveRandomHex`, `resolveRandomInt` | Return `("", error)` -> strict: 500, lenient: empty |
| `min > max` in `randomInt` | `resolveRandomInt` | Return `("", error)` -> strict: 500, lenient: empty |
| Unknown faker type (`faker.unknown`) | `resolveFaker` | Return `("", false)` -> strict: 500, lenient: empty |
| `pathParam.*` without PathPatternCriterion | `resolveExpr` | Return `("", false)` -> strict: 500, lenient: empty |
| `pathParam.x` where `x` not in captures | `resolveExpr` | Return `("", false)` -> strict: 500, lenient: empty |
| Config: `path_pattern` without `pattern` | `validatePathPatternCriterion` | Validation error |
| Config: `path_pattern` with `paths` | `validatePathPatternCriterion` | Validation error |
| Config: other types with `pattern` | `validateMethodCriterion` etc. | Validation error |
| Counter wrap at `math.MaxInt64` | `counterState.Next` | Silent wrap to 0 |

#### Test strategy

All tests use stdlib `testing` only. Table-driven where appropriate.

**`templating_test.go` -- new/modified tests:**

1. `TestParseExpr`: table-driven for accessor (`request.path`), zero-arg helper
   (`now`), single-arg helper (`now format=unix`), multi-arg helper
   (`randomInt min=1 max=50`), `faker.*` with seed (`faker.email seed=user-42`),
   pathParam accessor (`pathParam.id`), empty input.

2. `TestResolveNow`: verify RFC3339 output (default), unix format, iso format,
   custom Go format. Time assertions: verify within 2-second window.

3. `TestResolveUUID`: inject deterministic `randSource` via `bytes.NewReader`.
   Verify output format matches UUID v4 pattern. Verify version and variant bits.

4. `TestResolveRandomHex`: inject deterministic `randSource`. Verify length,
   hex validity. Error cases: missing length, non-numeric length, zero length.

5. `TestResolveRandomInt`: inject deterministic `randSource`. Verify within
   range. Error cases: non-numeric min/max, min > max.

6. `TestResolveCounter`: verify sequential increment (1, 2, 3...), named
   counters are independent, default name, wrap at `math.MaxInt64`.

7. `TestResolveCounter_Concurrent`: launch N goroutines incrementing the same
   counter. Verify final value equals N. Run with `-race`.

8. `TestResolveFaker_Email`: verify deterministic output with explicit seed.
   Same seed -> same output. Different seed -> different output.

9. `TestResolveFaker_Name`: same as email but for NameFaker.

10. `TestResolveFaker_Phone`: same pattern.

11. `TestResolveFaker_Address`: same pattern.

12. `TestResolveFaker_AutoSeed`: no `seed=` arg. Verify deterministic per
    (tapeID, path) pair. Different paths -> different output.

13. `TestResolveFaker_Unknown`: `faker.doesnotexist` -> unresolvable.

14. `TestResolvePathParam`: verify resolution against `templateCtx` with
    populated `pathParams`. Missing param -> unresolvable. Nil pathParams map
    -> unresolvable.

15. `TestResolveArgs_Nested`: `seed=user-{{pathParam.id}}` with pathParams
    `{"id": "42"}` -> resolved to `seed=user-42`.

16. `TestResolveTemplateBody_Helpers`: end-to-end body resolution with
    multiple helpers and accessors in the same body.

17. `TestResolveTemplateJSON`: JSON body with template expressions in string
    values. Verify JSON-escaping of resolved values. Verify non-string values
    untouched.

18. `TestResolveTemplateJSON_NestedObject`: deeply nested JSON with templates
    at various depths.

19. `TestResolveTemplateBodySimple`: backward-compat wrapper. Verify existing
    `request.*` expressions still work.

20. `TestResolveExpr_BackwardCompat`: all existing `request.*` expressions
    continue to work with the new `templateCtx`-based signature.

**`matcher_test.go` -- new tests:**

21. `TestPathPatternCriterion_BasicMatch`: `/users/:id` matches `/users/42`.
    Score = 3.

22. `TestPathPatternCriterion_MultiSegment`: `/users/:id/orders/:oid` matches
    `/users/1/orders/7`. Score = 3.

23. `TestPathPatternCriterion_NoMatch`: `/users/:id` does not match `/posts/1`.
    Score = 0.

24. `TestPathPatternCriterion_ExactLiteral`: `/api/v1/health` matches only
    `/api/v1/health`. Score = 3. No captures.

25. `TestPathPatternCriterion_TrailingSlash`: `/users/:id` does not match
    `/users/42/`. Score = 0.

26. `TestPathPatternCriterion_EmptySegment`: `/users/:id` does not match
    `/users/`. Score = 0.

27. `TestPathPatternCriterion_ExtractParams`: verify `ExtractParams` returns
    correct map for matching paths, nil for non-matching paths.

28. `TestNewPathPatternCriterion_Errors`: empty param name, duplicate param
    names, missing leading `/`, empty pattern.

29. `TestPathPatternCriterion_Name`: verify returns `"path_pattern"`.

30. `TestPathPatternCriterion_TapeMustMatch`: both request path and tape URL
    path must match the pattern (same as PathRegexCriterion logic).

**`server_test.go` -- new tests:**

31. `TestServer_ResetCounter`: set counter, reset by name, verify 0. Reset
    all (empty name), verify all 0.

32. `TestServer_PathParamTemplating`: create tape with `PathPatternCriterion`
    and template body `"id": "{{pathParam.id}}"`. Send request to `/users/42`.
    Verify response body contains `"id": "42"`.

33. `TestServer_HelperTemplating`: tape with `{{now}}`, `{{counter name=c1}}`,
    `{{faker.email seed=test}}`. Verify each resolves correctly.

34. `TestServer_CounterAcrossRequests`: send 3 requests, verify counter
    increments across them.

**`config_test.go` -- new tests:**

35. `TestLoadConfig_PathPatternCriterion`: valid config with `path_pattern`
    type and `pattern` field.

36. `TestConfig_BuildMatcher_PathPattern`: build matcher, verify it matches
    patterned paths.

37. `TestLoadConfig_PathPatternValidationErrors`: missing pattern, spurious
    paths, pattern on wrong type.

**`race_test.go` -- new test:**

38. `TestCounterState_Race`: dedicated race test for concurrent counter
    increment. Run with `-race`.

**`integration_test.go` -- new test:**

39. `TestIntegration_PathParamFaker`: end-to-end test with `PathPatternCriterion`
    and `{{faker.name seed=user-{{pathParam.id}}}}` in response body. Send
    requests for `/users/1` and `/users/2`. Verify different but deterministic
    names. Re-send `/users/1`, verify same name as first call.

#### Rationale

**Why Express-style `:id` over OpenAPI `{id}`:**
Express-style is the dominant idiom in the Go HTTP router ecosystem. OpenAPI's
curly braces conflict with httptape's `{{...}}` template delimiters. Developers
who know Express, chi, or gin will immediately understand `:id`. OpenAPI-style
can be supported as a future alternative syntax without breaking the Express default.

**Why tokenizer over regex for expression parsing:**
The current `resolveExpr` uses string prefix matching (`expr[:8] == "request."`).
Adding keyword arguments to this approach would require `strings.Fields` splitting
followed by `strings.SplitN` on `=`, with special cases for quoted values and
nested `{{...}}`. A structured `parseExpr` function that returns `parsedExpr{name, args}`
is cleaner, testable in isolation, and extensible for #199 (pipe operators).
The tokenizer is ~50 lines of code -- not a complex parser.

**Why `templateCtx` struct over more function parameters:**
`resolveExpr` currently takes 3 parameters. Adding pathParams, tapeID, counters,
and randSource would bring it to 7+. A context struct bundles related data,
reduces parameter sprawl, and makes it easy to add future context fields
(e.g., scenario state from #195) without signature changes.

**Why counters live on Server, not globally:**
Per-instance counters respect the "no global state" principle. Each `NewServer`
gets its own counter space. Tests don't interfere with each other. The `counterState`
is `sync.Mutex`-protected for concurrent access.

#### Alternatives considered

1. **text/template from stdlib:** Go's `text/template` provides a full
   template engine with custom functions. Rejected because: (a) its syntax
   (`{{.Request.Path}}`) differs from httptape's existing `{{request.path}}`
   convention, breaking backward compatibility; (b) it includes conditionals
   and loops which are explicitly out of scope; (c) error messages from
   `text/template` expose Go-internal details that are confusing in an HTTP
   mocking context; (d) the security model of `text/template` (arbitrary method
   calls via FuncMap) is broader than needed.

2. **Helper registry with `TemplateHelper` interface:** Define a
   `TemplateHelper` interface and register helpers by name. Rejected because
   the set of helpers is fixed and curated (no user-defined helpers per #196
   scope). An interface adds indirection without benefit. A switch statement
   is simpler and keeps all dispatch in one place. If user-defined helpers
   are added in the future, the switch can be replaced with a registry.

3. **Separate `template_helpers.go` file:** Move helper implementations to a
   new file. Rejected because the helpers are tightly coupled to `resolveExpr`
   and `templateCtx` -- splitting them would create circular dependencies
   between the files (both would need `templateCtx`). Keeping everything in
   `templating.go` is consistent with the existing layout and avoids artificial
   file boundaries.

4. **PathPatternCriterion stores captures in request context (`context.WithValue`):**
   Instead of `ExtractParams`, the `Score` method could store captures in
   `req.Context()`. Rejected because: (a) the `Criterion` interface contract says
   "must not modify the request"; (b) `context.WithValue` requires a context key
   type, adding ceremony; (c) the captures are only needed for templating, not
   matching -- separating extraction from scoring is cleaner.

5. **Inlined auto-seed (hash inside resolveExpr):** Instead of the separate
   `autoSeed` function, compute the seed inline. Rejected for testability:
   `autoSeed` as a separate function can be unit-tested in isolation.

#### Consequences

**Benefits:**

- **Realistic fixtures without hand-coding:** Template helpers eliminate the need
  to hard-code timestamps, UUIDs, and fake data in every fixture. Fixtures become
  templates that produce dynamic, realistic responses.
- **Deterministic faking via `{{faker.*}}`:** Same seed produces the same fake
  across runs and across related requests. This is the key differentiator -- most
  mocking tools offer random-only or hardcoded values.
- **Path-parameterized fixtures:** A single tape for `/users/:id` can serve
  infinite user IDs with per-ID deterministic fakes. This directly enables
  #199 (exemplar tapes).
- **Nested evaluation (`seed=user-{{pathParam.id}}`):** Enables derived seeds
  that vary per path segment, creating per-entity consistency without manual
  fixture wiring.
- **Backward compatible:** All existing `{{request.*}}` expressions continue
  to work. `WithTemplating(false)` fast path is preserved. `WithStrictTemplating`
  semantics are preserved.

**Costs / trade-offs:**

- **Breaking change to `ResolveTemplateBody` and `ResolveTemplateHeaders`
  signatures:** These were exported functions that external callers might use
  directly. Mitigated by the `ResolveTemplateBodySimple` backward-compat wrapper
  and pre-1.0 status.
- **Increased complexity in `templating.go`:** The file grows from ~310 lines to
  ~600-700 lines. This is acceptable: the file is cohesive (all template-related
  logic) and the additional code is straightforward (helper functions, parser,
  context struct).
- **Non-deterministic helpers (`now`, `uuid`, `randomHex`, `randomInt`):**
  These produce different output on every call, which makes fixtures non-reproducible
  by default. Documented: use `faker.*` with explicit seeds for reproducibility.
  The `randSource` injection point enables deterministic testing.
- **`PathPatternCriterion` and `PathCriterion` conflict:** Using both in the same
  `CompositeMatcher` causes `PathCriterion` to eliminate pattern-matched candidates.
  Documented with a prominent warning. Config validation does NOT reject this
  combination (it is not wrong, just unhelpful).

**Future implications:**

- **#199 (exemplar tapes):** This ADR provides the foundation. #199 adds the
  `exemplar` flag, `url_pattern` field, synthesis mode, and pipe operators (`| int`).
  The `parsedExpr` struct can be extended with a `coerce` field for pipe operators
  without reworking the parser.
- **#195 (scenarios):** Scenario state can be exposed to templates via a new
  `templateCtx` field (e.g., `ctx.scenarioState`). The `templateCtx` struct is
  designed for extensibility.
- **#194 (admin API):** Counter reset can be exposed via the admin HTTP endpoint
  by calling `s.ResetCounter(name)`.
- **User-defined helpers (out of scope):** If ever needed, the `resolveExpr`
  switch statement can be replaced with a registry (`map[string]HelperFunc`)
  populated by a `WithTemplateHelper(name, fn)` `ServerOption`.

---


### ADR-43: Synthetic responses via exemplar tapes

**Date**: 2026-04-18
**Issue**: #199
**Status**: Accepted

> **Dependency note**: This ADR assumes #196 (ADR-42) delivers `PathPatternCriterion`
> (with colon-prefixed named segments, e.g., `/users/:id`), the `{{pathParam.NAME}}`
> template accessor, nested template evaluation (expressions inside helper arguments
> are resolved before the helper runs), and the `{{faker.*}}` / `{{now}}` / `{{uuid}}`
> template helpers. This ADR does NOT redesign those primitives. If #196's design
> changes, the integration points noted here must be revisited.
>
> **Parallel architect note**: ADR-42 (#196) is being designed concurrently in a
> separate worktree. The orchestrator will reconcile both ADRs on merge.

#### Context

httptape today is strict replay-only. A request that does not match any tape
falls through to the fallback handler (default 404). This is correct for
integration tests, where unexpected requests should fail fast. But in
frontend-first development workflows -- where the backend is partially
specified and the UI is being built against a mock -- this rigidity is a
blocker. A developer records one tape for `/users/1` and wants the server to
synthesize responses for `/users/2`, `/users/3`, etc., with realistic,
deterministic fake data.

Issue #199 introduces "exemplar tapes" -- hand-authored fixtures with URL
patterns and templated response bodies. When the server is running in
synthesis mode and no exact-URL tape matches, exemplars are consulted. The
first matching exemplar's response body is rendered with captured path params,
deterministic faker helpers, and other template expressions.

The design is gated by a two-level opt-in:
1. **Per-tape**: the `exemplar: true` flag marks a tape as a pattern-based
   template, not a recorded interaction.
2. **Per-server**: the `WithSynthesis()` option (or `--synthesize` CLI flag)
   enables the exemplar fallback path. Without this, exemplar tapes are loaded
   but never consulted -- integration tests are safe.

#### Open question resolutions

**Q1: `url_pattern` as separate field vs overloading `url`?**

Decision: **Separate field.** `URLPattern string` on `RecordedReq` is a new
optional field (`json:"url_pattern,omitempty"`), distinct from `URL string`.
This gives unambiguous typing: `URL` is always a concrete URL (recorded or
hand-authored for exact match); `URLPattern` is always a colon-prefixed
pattern (e.g., `/users/:id`). Validation enforces mutual exclusivity (see Q2).

Rationale: overloading `url` to sometimes be a pattern and sometimes be a
literal URL would require content-inspection heuristics. A separate field is
self-documenting and validatable at load time.

**Q2: Can a single tape be both exact-url and exemplar?**

Decision: **No. Mutually exclusive.** Validation rules:
- `Exemplar == true` requires `URLPattern != ""` and `URL == ""`.
- `Exemplar == false` (or unset) requires `URLPattern == ""`.
- If `URL != ""` and `URLPattern != ""`, validation error.
- If `Exemplar == true` and `URLPattern == ""`, validation error.

A tape library can have both an exact tape for `/users/1` and an exemplar for
`/users/:id`. The exact tape takes precedence when synthesis is enabled.

**Q3: Synthesis + SSE?**

Decision: **Out of scope for this issue.** SSE exemplar tapes require template
resolution on individual `SSEEvent.Data` fields, which is a distinct problem.
If a tape has `Exemplar: true` and `SSEEvents` is non-empty, validation
produces an error: "SSE exemplar tapes are not supported."

This is documented as a known limitation. A follow-up issue can add SSE
exemplar support.

**Q4: Synthesis + request body matching (`body_fuzzy`)?**

Decision: **Allowed but unusual.** An exemplar with POST + `body_fuzzy`
criterion is permitted. The exemplar fallback only triggers when no exact match
is found. If the server's `CompositeMatcher` includes `BodyFuzzyCriterion`,
the exemplar's request body (if any) is scored against the incoming request's
body using the same criteria. This means an exemplar can narrow its match
domain beyond just the URL pattern.

This is explicitly supported -- not deferred. The interaction is natural
because the exemplar fallback reuses the existing matcher infrastructure
(see Sequence below). No special handling is needed.

**Q5: Persistence -- same directory or subdirectory?**

Decision: **Same directory, flag-based discrimination.** Exemplar tapes live
alongside normal tapes in the same fixtures directory. The `"exemplar": true`
field in the JSON distinguishes them. No subdirectory convention.

Rationale: subdirectories would require changes to `FileStore` scanning logic.
Flag-based discrimination is simpler and consistent with how other tape
metadata (e.g., `route`) works.

**Q6: Faker seed stability across exemplar redeclarations?**

Decision: **Documented as a feature.** Seeds like `user-{{pathParam.id}}`
are content-derived (the evaluated template expression), not tape-identity-
derived (no dependency on tape ID or filename). Renaming the tape file,
changing the tape ID, or rearranging exemplars has zero effect on generated
fake data, as long as the seed expression evaluates to the same string.
This is deterministic by design (HMAC-SHA256 with fixed project key).

**Q7: Type coercion (`| int`, `| float`, `| bool`) -- which issue?**

Decision: **Type coercion ships with #199, not #196.** Coercion is only
meaningful when template expressions are embedded in native JSON object bodies
(the exemplar use case). In string bodies, everything is already a string.
#196's template helpers produce string values, which is correct for the
string-body use case. #199 adds the recursive JSON walk and needs coercion at
leaf positions to emit proper JSON types (integer `42` instead of string
`"42"`).

If #196's architect decides to include coercion as a template-engine
primitive, #199 reuses it. If #196 defers, #199 implements it. Either way,
this ADR defines the coercion semantics.

Coercion syntax: `| int`, `| float`, `| bool` appended to the expression
inside `{{...}}`. Examples:
- `{{pathParam.id | int}}` -- resolve `pathParam.id`, parse as integer, emit
  as JSON number.
- `{{request.query.page | int}}` -- query param coerced to integer.
- `{{faker.randomInt seed=x min=1 max=100 | int}}` -- faker output coerced.

If coercion fails (e.g., `"abc" | int`), the behavior depends on strict mode:
- Strict: return HTTP 500 with `X-Httptape-Error: template`.
- Lenient: emit the uncoerced string value.

**Q8: Interaction with `body_fuzzy` exemplars?**

Decision: **Explicitly supported.** See Q4.

#### Decision

##### Tape struct additions

Two new fields, both optional, backward-compatible (`omitempty`):

```go
// Tape struct gains:
type Tape struct {
    // ... existing fields ...

    // Exemplar marks this tape as a pattern-based template for synthesis mode.
    // When true, the tape's request uses URLPattern instead of URL, and the
    // response body may contain template expressions that are resolved at
    // serve time using captured path parameters and other template helpers.
    //
    // Exemplar tapes are only consulted when the server has synthesis enabled
    // (WithSynthesis option). When synthesis is disabled, exemplar tapes are
    // loaded but ignored.
    Exemplar bool `json:"exemplar,omitempty"`
}
```

```go
// RecordedReq struct gains:
type RecordedReq struct {
    // ... existing fields ...

    // URLPattern is a colon-prefixed path pattern (e.g., "/users/:id") used
    // by exemplar tapes for pattern-based matching. Mutually exclusive with
    // URL: a tape must have either URL (exact match) or URLPattern (pattern
    // match via PathPatternCriterion), never both.
    //
    // Only meaningful when the parent Tape has Exemplar set to true.
    // Validation enforces: Exemplar==true requires URLPattern!="",
    // and URLPattern!="" requires Exemplar==true.
    URLPattern string `json:"url_pattern,omitempty"`
}
```

JSON marshal/unmarshal semantics:
- Both fields use `omitempty`. Existing fixtures without these fields
  unmarshal with zero values (`false`, `""`), producing no behavioral change.
- Marshal of existing non-exemplar tapes omits both fields.
- `Exemplar: true` must be explicitly set in hand-authored fixture JSON.
- Round-trip: `Marshal(Unmarshal(json))` preserves both fields identically.

##### Server struct additions

```go
type Server struct {
    // ... existing fields ...

    // synthesis enables exemplar tape fallback. When true, requests that
    // don't match any exact tape are checked against exemplar tapes.
    // When false (default), exemplar tapes are loaded but never consulted.
    synthesis bool
}
```

##### New ServerOption

```go
// WithSynthesis enables synthesis mode on the Server. When enabled, requests
// that don't match any exact-URL tape fall back to exemplar tapes -- tapes
// with Exemplar: true and a URLPattern. The exemplar's response body is
// rendered using template helpers (path params, fakers, etc.) to produce a
// unique, deterministic response.
//
// Synthesis is disabled by default. When disabled, exemplar tapes are loaded
// but never consulted, ensuring integration tests are not affected by
// exemplar tapes in the fixture directory.
//
// Requires that tapes are loaded from a store that includes exemplar fixtures.
// Exemplar tapes must pass validation (see ValidateExemplar).
func WithSynthesis() ServerOption {
    return func(s *Server) { s.synthesis = true }
}
```

##### Tape validation function

```go
// ValidateExemplar checks that a tape marked as an exemplar is structurally
// valid. Returns nil if the tape is valid, or an error describing the issue.
//
// Validation rules:
//   - Exemplar==true requires URLPattern to be non-empty.
//   - Exemplar==true requires URL to be empty.
//   - URLPattern is only valid when Exemplar==true.
//   - SSE exemplars (Exemplar==true with non-empty SSEEvents) are not
//     supported and produce an error.
//   - Non-exemplar tapes with URLPattern set produce an error.
//   - URL and URLPattern set simultaneously produce an error.
//
// ValidateExemplar is called by ValidateTape. It does not validate the
// URLPattern syntax -- that is the responsibility of PathPatternCriterion
// from #196.
func ValidateExemplar(t Tape) error
```

Additionally, add a general `ValidateTape` function that calls
`ValidateExemplar` plus any other tape-level validation:

```go
// ValidateTape checks a tape for structural validity. Currently checks
// exemplar-specific constraints. Returns nil if valid.
func ValidateTape(t Tape) error
```

##### Match flow (Server.ServeHTTP)

The `ServeHTTP` method gains a two-phase match flow when synthesis is enabled.
The change is localized to `Server.ServeHTTP` -- the `Matcher` interface is
NOT modified. The flow:

1. **Load all tapes** from store (existing: `s.store.List(r.Context(), Filter{})`).

2. **Partition tapes** into two slices:
   - `exactTapes`: tapes where `Exemplar == false` (normal tapes).
   - `exemplarTapes`: tapes where `Exemplar == true`.
   When synthesis is disabled (`s.synthesis == false`), skip the partition --
   pass all tapes to the matcher as today (exemplar tapes will fail matching
   because `PathCriterion` compares `URL` path, and exemplar tapes have no
   `URL` -- they will score 0 and be eliminated).

   **Correction on fail-safe**: Relying on `PathCriterion` returning 0 for
   exemplar tapes is fragile -- it depends on which criteria the user
   configured. Instead, when synthesis is disabled, explicitly filter out
   exemplar tapes before passing candidates to the matcher. This guarantees
   exemplar tapes are truly inert regardless of matcher configuration.

3. **Phase 1 -- exact match**: call `s.matcher.Match(r, exactTapes)`. If a
   match is found, proceed to response rendering (existing logic). Done.

4. **Phase 2 -- exemplar fallback** (only if synthesis enabled AND phase 1
   found no match):
   a. Build a temporary `PathPatternCriterion` for each exemplar tape using
      its `URLPattern`. **Correction**: building a criterion per exemplar per
      request is wasteful. Instead, on tape load (once per `ServeHTTP` call),
      build a `pathPatternMatcher` -- a small struct that wraps a list of
      `(pattern, exemplarTape)` pairs, pre-compiled. Since `ServeHTTP` loads
      tapes on every request (the O(n) scan acknowledged in ADR-4), the
      per-request compilation cost is bounded by the number of exemplars.
   b. For each exemplar tape (in declaration order), test the incoming
      request path against the exemplar's `URLPattern` using
      `PathPatternCriterion` from #196. If the pattern matches:
      - Record the match with its specificity score (from
        `PathPatternCriterion.Score`).
      - Also run any other criteria in the server's matcher against the
        exemplar (method, headers, body_fuzzy, etc.) -- the exemplar must
        pass ALL criteria, not just the path pattern. This ensures a POST
        exemplar is only consulted for POST requests.
   c. Among all matching exemplars, select the one with the highest total
      score. Ties are broken by declaration order (first in slice wins,
      matching `CompositeMatcher` behavior).
   d. If a matching exemplar is found, capture the path parameters from the
      `PathPatternCriterion` match and proceed to template rendering.

5. **No match at all**: invoke `s.onNoMatch` callback (if set) and write
   fallback response (existing logic).

**Specificity note**: `PathPatternCriterion.Score` from #196 is expected to
return a higher score for more specific patterns (fewer wildcard segments).
This ADR assumes the following contract from #196:
- `/users/:id/orders` (1 param) scores higher than `/users/:id` (1 param,
  shorter) -- wait, these have the same param count. The specificity is
  actually about the total number of literal segments vs param segments.
- This ADR relies on #196 defining a score that encodes specificity. If #196
  uses a flat score (e.g., always returns 1), the "most specific exemplar
  wins" requirement is met by extending the criterion or implementing a
  secondary sort here.

**Dependency note for #196 architect**: This ADR requires that
`PathPatternCriterion.Score` either encodes specificity in its score value
OR that `PathPatternCriterion` exposes the captured path parameters
(e.g., `map[string]string`) so the server can feed them into the template
context. The exact API is:

```go
// Required from #196 (assumed contract):
type PathPatternCriterion struct {
    Pattern string
    // ... private fields ...
}

// MatchAndCapture matches the request path against the pattern and returns
// captured path parameters. Returns nil, false if the pattern does not match.
// This is used by the exemplar fallback path in Server.ServeHTTP.
func (c *PathPatternCriterion) MatchAndCapture(path string) (map[string]string, bool)
```

If #196 does not expose `MatchAndCapture`, this ADR will need a package-level
helper or will need to parse the pattern and match manually. The preferred
approach is for #196 to expose this method, since the pattern parsing logic
already exists in `PathPatternCriterion.Score`.

##### Template resolution for exemplar responses

Exemplar response rendering extends the existing `ResolveTemplateBody` flow
with two additions:
1. **Path parameter context**: captured path params (from
   `PathPatternCriterion.MatchAndCapture`) are injected into the template
   resolution context, accessible via `{{pathParam.NAME}}`. This is a #196
   primitive -- this ADR just plumbs the captured values.
2. **Recursive JSON body resolution**: when the response body is a native
   JSON object (per ADR-41's polymorphic body model), template resolution
   must walk the JSON tree and resolve expressions at leaf string positions.

**Recursive JSON body template resolution algorithm:**

```go
// ResolveTemplateBodyJSON recursively resolves template expressions in a
// JSON body. This is used for exemplar tapes whose response bodies are
// native JSON objects (not string or base64).
//
// The algorithm walks the JSON tree depth-first:
//   - Object: recurse into each value. Keys are not template-resolved.
//   - Array: recurse into each element.
//   - String leaf: resolve template expressions (same as ResolveTemplateBody
//     on string bodies). Apply type coercion if present (| int, | float,
//     | bool). If the entire string is a single template expression with
//     coercion, the leaf value type changes (string -> number/bool).
//   - Number/bool/null leaf: no template resolution. Return as-is.
//
// The function operates on the unmarshaled `any` tree (from json.Unmarshal)
// and returns a new tree with resolved values. The caller marshals the
// result back to JSON bytes.
func resolveTemplateJSON(data any, ctx *TemplateContext, strict bool) (any, error)
```

Walk algorithm in detail:

1. **Input**: `data any` (the unmarshaled JSON body, which is
   `map[string]any`, `[]any`, `string`, `float64`, `bool`, or `nil`).

2. **Object** (`map[string]any`): create a new map. For each key-value pair,
   recurse on the value. Keys are NOT resolved (template expressions in JSON
   keys would produce invalid JSON structure). Assign the resolved value to
   the same key in the new map.

3. **Array** (`[]any`): create a new slice. For each element, recurse. Append
   the resolved element.

4. **String** (`string`): this is where template resolution happens.
   a. If the string contains no `{{`, return as-is (fast path).
   b. Scan for template expressions using `scanTemplateExprs`.
   c. Check if the entire string is a single template expression with type
      coercion (e.g., `"{{pathParam.id | int}}"`). If so:
      - Resolve the expression (without the coercion pipe).
      - Parse the coercion: `| int`, `| float`, `| bool`.
      - Convert the resolved string to the target type.
      - Return the typed value (`float64` for int/float, `bool` for bool).
      - On conversion failure: strict mode returns error; lenient mode
        returns the uncoerced string.
   d. If the string contains mixed literal text and expressions (e.g.,
      `"Hello, {{pathParam.name}}!"`), or multiple expressions without
      coercion, resolve all expressions and concatenate. Return as string.
      Coercion is only supported when the entire string is a single
      expression -- mixed coercion is ambiguous and an error.

5. **Number** (`float64`), **bool**, **nil**: return as-is.

The result `any` tree is marshaled back to `[]byte` via `json.Marshal` for
the response body.

**Interaction with existing `ResolveTemplateBody`**: the existing function
operates on raw `[]byte`. For exemplar tapes with native JSON bodies, the
new flow is:
1. Unmarshal `tape.Response.Body` to `any` (since the body is stored as
   compact JSON bytes per ADR-41).
2. Call `resolveTemplateJSON(data, ctx, strict)`.
3. Marshal the resolved tree back to `[]byte`.

For exemplar tapes with string bodies (text Content-Type), the existing
`ResolveTemplateBody` is used unchanged.

For exemplar tapes with base64 bodies (binary Content-Type), no template
resolution occurs.

The dispatch is based on the response `Content-Type` header using the
existing `ParseMediaType` / `IsJSON` / `IsText` / `IsBinary` classifiers
from `media_type.go`.

##### TemplateContext type

A new type to carry the template resolution context, replacing the current
approach of passing `*http.Request` and `reqBody []byte` separately:

```go
// TemplateContext holds the data available to template expressions during
// resolution. It carries the incoming request, cached request body, and
// additional context like captured path parameters.
type TemplateContext struct {
    // Request is the incoming HTTP request.
    Request *http.Request
    // RequestBody is the cached request body bytes (read once, reusable).
    RequestBody []byte
    // PathParams holds captured path parameter values from PathPatternCriterion.
    // Empty for non-exemplar tapes.
    PathParams map[string]string
}
```

The existing `resolveExpr` function is updated to accept `*TemplateContext`
instead of `(*http.Request, []byte)`. The `{{pathParam.NAME}}` expression
resolution looks up `ctx.PathParams[NAME]`.

This change is backward-compatible for non-exemplar tapes: `PathParams` is
nil/empty, and `{{pathParam.*}}` expressions resolve to empty string (lenient)
or error (strict).

##### Server.ServeHTTP updated flow

Step-by-step from request arrival to response:

1. CORS handling (unchanged).
2. Error simulation (unchanged).
3. Load all tapes: `tapes, err := s.store.List(r.Context(), Filter{})`.
4. **Partition**: split tapes into `exactTapes` (Exemplar==false) and
   `exemplarTapes` (Exemplar==true). When synthesis is disabled, set
   `exemplarTapes = nil` (or skip the partition entirely -- just filter out
   exemplar tapes from the candidates).
5. **Phase 1**: `tape, ok := s.matcher.Match(r, exactTapes)`.
6. If ok: proceed to step 9 (response rendering) with `pathParams = nil`.
7. **Phase 2** (only if `!ok && s.synthesis && len(exemplarTapes) > 0`):
   a. For each exemplar tape, compile `PathPatternCriterion` from
      `tape.Request.URLPattern`.
   b. Check if the request path matches the pattern. If yes, capture path
      params.
   c. Score the exemplar against all other configured criteria (method,
      headers, etc.). An exemplar must pass all criteria to be eligible.
   d. Track the highest-scoring match.
   e. If a match is found: `tape = bestExemplar`, `pathParams = capturedParams`.
      Proceed to step 9.
   f. If no match: proceed to step 8.
8. **No match**: invoke `s.onNoMatch` (if set), write fallback response. Done.
9. **Per-fixture error override** (unchanged).
10. **Delay** (unchanged).
11. **SSE check**: if SSE tape, call `s.serveSSE` (unchanged). Exemplar SSE
    tapes are rejected at validation time, so this branch is never hit for
    exemplars.
12. **Template resolution** (enhanced for exemplars):
    a. Build `TemplateContext` with request, cached body, and `pathParams`.
    b. Determine body type from response Content-Type:
       - JSON: unmarshal body to `any`, call `resolveTemplateJSON`, marshal
         back.
       - Text: call `ResolveTemplateBody` (existing, with `TemplateContext`).
       - Binary: no resolution.
    c. Resolve template expressions in response headers (existing
       `ResolveTemplateHeaders`, updated to use `TemplateContext`).
13. **Write response** (unchanged).

##### CLI flag

Add `--synthesize` flag to `httptape serve` in `cmd/httptape/main.go`:

```go
synthesize := fs.Bool("synthesize", false, "Enable synthesis mode (exemplar tapes generate responses for unmatched URLs)")
```

If `*synthesize` is true, append `httptape.WithSynthesis()` to `serverOpts`.

After server creation, log the synthesis state:

```go
if *synthesize {
    // Count exemplar tapes.
    allTapes, _ := store.List(context.Background(), httptape.Filter{})
    exemplarCount := 0
    for _, t := range allTapes {
        if t.Exemplar {
            exemplarCount++
        }
    }
    logger.Printf("synthesis mode ENABLED -- %d exemplar tape(s) loaded", exemplarCount)
} else {
    // Check if exemplar tapes exist but synthesis is off.
    allTapes, _ := store.List(context.Background(), httptape.Filter{})
    for _, t := range allTapes {
        if t.Exemplar {
            logger.Printf("WARNING: %d exemplar tape(s) found but synthesis is disabled (use --synthesize to enable)", countExemplars(allTapes))
            break
        }
    }
}
```

##### Startup validation

At server startup (in `cmd/httptape/main.go`, after store creation, before
server start), validate all loaded tapes:

```go
allTapes, err := store.List(context.Background(), httptape.Filter{})
if err != nil {
    return fmt.Errorf("load tapes: %w", err)
}
for _, t := range allTapes {
    if err := httptape.ValidateTape(t); err != nil {
        return fmt.Errorf("invalid tape %s: %w", t.ID, err)
    }
}
```

This catches structural errors (exemplar without URLPattern, URL + URLPattern
on same tape, SSE exemplar) at startup, not at request time.

##### Admin endpoint integration (#194)

If #194 (admin API) ships, synthesis state should be queryable via
`GET /__httptape/config` or similar. This is a note for the #194 architect,
not a design commitment in this ADR. The `Server.synthesis` field is already
accessible within the package.

##### Type coercion implementation

Type coercion is a template-expression-level feature. It is parsed from the
raw expression string:

```go
// parseCoercion splits a template expression into the expression body and
// an optional type coercion. Returns the expression without the coercion
// pipe, the coercion type (or ""), and whether a coercion was found.
//
// Examples:
//   "pathParam.id | int"    -> ("pathParam.id", "int", true)
//   "pathParam.id"          -> ("pathParam.id", "", false)
//   "faker.name seed=foo"   -> ("faker.name seed=foo", "", false)
//   "request.query.page | float" -> ("request.query.page", "float", true)
func parseCoercion(raw string) (expr string, coercion string, ok bool)
```

Supported coercions:
- `int`: `strconv.ParseFloat` then truncate to `int64`, emit as JSON number.
- `float`: `strconv.ParseFloat`, emit as JSON number.
- `bool`: `strconv.ParseBool`, emit as JSON boolean.

Coercion is only effective in `resolveTemplateJSON` (native JSON body
context). In `ResolveTemplateBody` (string body), coercion is ignored --
everything is a string already.

#### Types

| Type | File | Description |
|---|---|---|
| `TemplateContext` | `templating.go` | Carries request, cached body, path params for template resolution |

Modified types:

| Type | File | Change |
|---|---|---|
| `Tape` | `tape.go` | New field `Exemplar bool` (`json:"exemplar,omitempty"`) |
| `RecordedReq` | `tape.go` | New field `URLPattern string` (`json:"url_pattern,omitempty"`) |
| `Server` | `server.go` | New field `synthesis bool` |

#### Functions and methods

New exported:

| Function / Method | Signature | File |
|---|---|---|
| `WithSynthesis` | `func WithSynthesis() ServerOption` | `server.go` |
| `ValidateTape` | `func ValidateTape(t Tape) error` | `tape.go` |
| `ValidateExemplar` | `func ValidateExemplar(t Tape) error` | `tape.go` |

New unexported:

| Function | Signature | File |
|---|---|---|
| `resolveTemplateJSON` | `func resolveTemplateJSON(data any, ctx *TemplateContext, strict bool) (any, error)` | `templating.go` |
| `parseCoercion` | `func parseCoercion(raw string) (expr string, coercion string, ok bool)` | `templating.go` |
| `coerceValue` | `func coerceValue(s string, coercion string) (any, error)` | `templating.go` |

Modified:

| Function / Method | File | Change |
|---|---|---|
| `resolveExpr` | `templating.go` | Signature changes from `(expr string, r *http.Request, reqBody []byte) (string, bool)` to `(expr string, ctx *TemplateContext) (string, bool)`. Adds `pathParam.*` lookup from `ctx.PathParams`. |
| `ResolveTemplateBody` | `templating.go` | Signature adds `*TemplateContext` parameter (or internal refactor to build context from existing params). |
| `ResolveTemplateHeaders` | `templating.go` | Same context refactor. |
| `Server.ServeHTTP` | `server.go` | Adds two-phase match (exact then exemplar fallback), tape partitioning, exemplar-specific template resolution with JSON walk. |

#### File layout

**Modified files:**

| File | Changes |
|---|---|
| `tape.go` | Add `Exemplar bool` to `Tape`. Add `URLPattern string` to `RecordedReq`. Update `RecordedReq.MarshalJSON` and `RecordedReq.UnmarshalJSON` to include `url_pattern`. Add `ValidateTape` and `ValidateExemplar` functions. |
| `tape_test.go` | Add tests for `ValidateTape`, `ValidateExemplar`, JSON marshal/unmarshal round-trip with exemplar fields. |
| `server.go` | Add `synthesis bool` to `Server`. Add `WithSynthesis() ServerOption`. Modify `ServeHTTP` with two-phase match flow, tape partitioning, exemplar fallback with path param capture, and JSON body template resolution dispatch. |
| `server_test.go` | Add tests for synthesis mode: exact wins over exemplar, exemplar fallback, synthesis disabled ignores exemplars, most-specific exemplar wins, method filtering on exemplars, startup validation. |
| `templating.go` | Add `TemplateContext` type. Add `resolveTemplateJSON`, `parseCoercion`, `coerceValue`. Refactor `resolveExpr` to use `TemplateContext`. Add `pathParam.*` resolution. Update `ResolveTemplateBody` and `ResolveTemplateHeaders` to use `TemplateContext` internally. |
| `templating_test.go` | Add tests for `resolveTemplateJSON` (recursive walk on objects, arrays, nested objects, mixed types), `parseCoercion`, `coerceValue`, `pathParam.*` resolution. |
| `cmd/httptape/main.go` | Add `--synthesize` flag to `runServe`. Add startup logging and exemplar tape validation. |
| `cmd/httptape/main_test.go` | Add test for `--synthesize` flag parsing. |
| `integration_test.go` | Add end-to-end test: exemplar tape serving `/users/1`, `/users/2`, `/users/7` with deterministic fake data per ID. Add negative test: synthesis disabled, exemplar ignored, fallback 404. |
| `config.schema.json` | Add `exemplar` and `url_pattern` to the tape schema (if tape schema is defined in config -- architect note: tapes are not validated via `config.schema.json`, they are standalone fixture files. No schema change needed for config. Fixture schema is implicit.) |
| `CLAUDE.md` | No structural changes needed. The package structure is unchanged. |

**New files:**

| File | Purpose |
|---|---|
| `docs/synthesis.md` | User documentation: when to use synthesis, how it differs from replay, opt-in gating, exemplar authoring guide, seed stability. |

**Modified docs:**

| File | Changes |
|---|---|
| `docs/fixtures-authoring.md` | Add section on exemplar tape authoring, `url_pattern` field, `exemplar: true` flag, template body examples. |
| `docs/cli.md` | Add `--synthesize` flag documentation for `serve` command. |
| `docs/api-reference.md` | Add `WithSynthesis`, `ValidateTape`, `ValidateExemplar`, `TemplateContext` entries. Update `Tape` and `RecordedReq` field tables. |

**CHANGELOG:**

Add entry for new synthesis mode feature.

#### Sequence

**Request arrives at Server with synthesis enabled, no exact match:**

1. Request: `GET /users/42`.
2. `ServeHTTP` loads all tapes from store.
3. Partition: `exactTapes` = [tape for `/users/1`], `exemplarTapes` = [exemplar for `/users/:id`].
4. Phase 1: `matcher.Match(req, exactTapes)` -- `/users/42` does not match `/users/1`. No match.
5. Phase 2 (synthesis enabled):
   a. For exemplar tape with pattern `/users/:id`:
      - `PathPatternCriterion.MatchAndCapture("/users/42")` returns `{"id": "42"}`, true.
      - Run other criteria: `MethodCriterion.Score(req, exemplar)` = 1 (GET matches GET). Pass.
   b. Best match: the exemplar with params `{"id": "42"}`.
6. Build `TemplateContext{Request: req, RequestBody: nil, PathParams: {"id": "42"}}`.
7. Response body is native JSON: `{"id": "{{pathParam.id | int}}", "name": "{{faker.name seed=user-{{pathParam.id}}}}"}`.
8. Unmarshal body to `any`.
9. `resolveTemplateJSON` walks the tree:
   - Key `"id"`: value `"{{pathParam.id | int}}"` -- single expression with coercion.
     Resolve `pathParam.id` -> `"42"`. Coerce `| int` -> `float64(42)`.
   - Key `"name"`: value `"{{faker.name seed=user-{{pathParam.id}}}}"` -- single expression.
     Nested eval: `user-{{pathParam.id}}` -> `"user-42"`. Then `faker.name seed=user-42`
     -> `"Alice Johnson"` (deterministic from HMAC). Return as string.
10. Marshal resolved tree: `{"id":42,"name":"Alice Johnson"}`.
11. Write response: 200, Content-Type: application/json, body: `{"id":42,"name":"Alice Johnson"}`.

**Request arrives at Server with synthesis disabled, exemplar exists:**

1. Request: `GET /users/42`.
2. `ServeHTTP` loads all tapes from store.
3. Synthesis disabled: filter out exemplar tapes. `candidates` = [tape for `/users/1`].
4. `matcher.Match(req, candidates)` -- no match.
5. No match: write fallback (404, "httptape: no matching tape found").

**Request arrives at Server with synthesis enabled, exact match exists:**

1. Request: `GET /users/1`.
2. `ServeHTTP` loads all tapes, partitions.
3. Phase 1: `matcher.Match(req, exactTapes)` -- `/users/1` matches tape for `/users/1`. Match found.
4. Proceed to response rendering with existing exact tape. Exemplar NOT consulted.
5. Write recorded response (exact replay).

#### Error cases

| Error | Where | Handling |
|---|---|---|
| Tape with `Exemplar==true` but empty `URLPattern` | `ValidateExemplar` | Returns `fmt.Errorf("httptape: exemplar tape %s: url_pattern is required", t.ID)` |
| Tape with `Exemplar==true` and non-empty `URL` | `ValidateExemplar` | Returns `fmt.Errorf("httptape: exemplar tape %s: url and url_pattern are mutually exclusive", t.ID)` |
| Tape with `Exemplar==false` and non-empty `URLPattern` | `ValidateExemplar` | Returns `fmt.Errorf("httptape: tape %s: url_pattern requires exemplar to be true", t.ID)` |
| SSE exemplar tape | `ValidateExemplar` | Returns `fmt.Errorf("httptape: exemplar tape %s: SSE exemplars are not supported", t.ID)` |
| Tape with both `URL` and `URLPattern` | `ValidateExemplar` | Returns `fmt.Errorf("httptape: tape %s: url and url_pattern are mutually exclusive", t.ID)` |
| Invalid `URLPattern` syntax | `PathPatternCriterion` (from #196) | Error at criterion construction time. Server logs and skips the exemplar. |
| Type coercion failure (e.g., `"abc" \| int`) | `resolveTemplateJSON` | Strict: returns error -> HTTP 500 with `X-Httptape-Error: template`. Lenient: emits uncoerced string. |
| Unresolvable template expression in exemplar body | `resolveTemplateJSON` / `ResolveTemplateBody` | Strict: error -> HTTP 500. Lenient: empty string. Same as existing behavior. |
| Store error during tape load | `ServeHTTP` | Returns HTTP 500 "httptape: store error" (existing behavior). |
| All tapes invalid at startup | CLI startup | `runServe` returns error, process exits. |

#### Test strategy

All tests use stdlib `testing` only. Table-driven where appropriate.

**`tape_test.go` -- validation tests:**

1. `TestValidateExemplar_Valid`: exemplar with `Exemplar: true`, `URLPattern: "/users/:id"`, `URL: ""`. Expect nil error.

2. `TestValidateExemplar_MissingURLPattern`: `Exemplar: true`, `URLPattern: ""`. Expect error containing "url_pattern is required".

3. `TestValidateExemplar_MutuallyExclusive`: `Exemplar: true`, `URL: "/users/1"`, `URLPattern: "/users/:id"`. Expect error.

4. `TestValidateExemplar_URLPatternWithoutExemplar`: `Exemplar: false`, `URLPattern: "/users/:id"`. Expect error.

5. `TestValidateExemplar_SSEExemplar`: `Exemplar: true`, `URLPattern: "/events/:id"`, `SSEEvents: [...]`. Expect error.

6. `TestValidateExemplar_NonExemplarNoURLPattern`: normal tape, `Exemplar: false`, `URL: "/users/1"`, `URLPattern: ""`. Expect nil (valid).

7. `TestTape_MarshalJSON_Exemplar`: marshal exemplar tape, verify JSON contains `"exemplar": true` and `"url_pattern": "/users/:id"`.

8. `TestTape_UnmarshalJSON_Exemplar`: unmarshal JSON with `"exemplar": true` and `"url_pattern"`, verify fields set correctly.

9. `TestTape_UnmarshalJSON_NoExemplar`: unmarshal JSON without `"exemplar"` field. Verify `Exemplar == false`, `URLPattern == ""`.

10. `TestTape_MarshalJSON_RoundTrip_Exemplar`: marshal -> unmarshal -> marshal, verify identical output.

**`server_test.go` -- synthesis mode tests:**

11. `TestServer_SynthesisDisabled_ExemplarIgnored`: store with one exemplar tape. Server without `WithSynthesis`. Request matching exemplar pattern. Expect fallback 404.

12. `TestServer_SynthesisEnabled_ExactWins`: store with exact tape for `/users/1` AND exemplar for `/users/:id`. Server with `WithSynthesis`. Request `/users/1`. Expect exact tape response, NOT synthesized.

13. `TestServer_SynthesisEnabled_ExemplarFallback`: store with exact tape for `/users/1` and exemplar for `/users/:id`. Request `/users/2`. Expect synthesized response from exemplar with path params resolved.

14. `TestServer_SynthesisEnabled_MostSpecificExemplar`: two exemplars: `/users/:id` and `/users/:id/orders`. Request `/users/42/orders`. Expect the more specific exemplar wins.

15. `TestServer_SynthesisEnabled_MethodFilter`: exemplar for GET `/users/:id`. Request POST `/users/42`. Expect fallback 404 (method mismatch eliminates exemplar).

16. `TestServer_SynthesisEnabled_NoExemplars`: store with no exemplar tapes. Synthesis enabled. Request for unmatched URL. Expect fallback 404.

17. `TestServer_SynthesisEnabled_DeterministicResponse`: same request `/users/42` twice. Verify identical response bodies (deterministic faker).

**`templating_test.go` -- JSON template resolution tests:**

18. `TestResolveTemplateJSON_Object`: input `{"name": "{{pathParam.name}}"}` with pathParams `{"name": "alice"}`. Expect `{"name": "alice"}`.

19. `TestResolveTemplateJSON_NestedObject`: input `{"user": {"id": "{{pathParam.id | int}}"}}` with pathParams `{"id": "42"}`. Expect `{"user": {"id": 42}}` (number, not string).

20. `TestResolveTemplateJSON_Array`: input `[{"id": "{{pathParam.id | int}}"}]` with pathParams `{"id": "1"}`. Expect `[{"id": 1}]`.

21. `TestResolveTemplateJSON_NonStringLeaf`: input `{"count": 5, "active": true}`. Expect unchanged (numbers and bools are not template-resolved).

22. `TestResolveTemplateJSON_MixedContent`: input `{"greeting": "Hello, {{pathParam.name}}!"}`. Expect `{"greeting": "Hello, alice!"}` (string, no coercion).

23. `TestResolveTemplateJSON_CoercionInt`: `"{{pathParam.id | int}}"` with `id=42`. Expect `float64(42)`.

24. `TestResolveTemplateJSON_CoercionFloat`: `"{{request.query.price | float}}"` with query `price=19.99`. Expect `float64(19.99)`.

25. `TestResolveTemplateJSON_CoercionBool`: `"{{request.query.active | bool}}"` with query `active=true`. Expect `true` (bool).

26. `TestResolveTemplateJSON_CoercionFailure_Strict`: `"{{pathParam.id | int}}"` with `id=abc`, strict mode. Expect error.

27. `TestResolveTemplateJSON_CoercionFailure_Lenient`: `"{{pathParam.id | int}}"` with `id=abc`, lenient mode. Expect string `"abc"`.

28. `TestResolveTemplateJSON_NoTemplates`: input `{"id": 1, "name": "Alice"}`. Expect unchanged (fast path, no allocations).

29. `TestParseCoercion`: table-driven: `"pathParam.id | int"` -> `("pathParam.id", "int", true)`, `"pathParam.id"` -> `("pathParam.id", "", false)`, `"faker.name seed=foo"` -> no coercion.

**`integration_test.go`:**

30. `TestIntegration_ExemplarSynthesis`: end-to-end test. Create fixture directory with one exact tape (`/users/1`) and one exemplar (`/users/:id`). Start server with `WithSynthesis()`. Request `/users/1` -> exact replay. Request `/users/2` -> synthesized response with `id=2`. Request `/users/7` -> synthesized response with `id=7`. Verify `/users/2` and `/users/7` produce different fake names but each is deterministic across re-requests.

31. `TestIntegration_SynthesisDisabled_ExemplarIgnored`: same fixtures as above but server without `WithSynthesis()`. Request `/users/2` -> fallback 404.

**`cmd/httptape/main_test.go`:**

32. `TestServeWithSynthesize`: invoke with `--synthesize` flag, verify no error.

#### Alternatives considered

1. **All tapes can synthesize (no `exemplar` flag)**: Any tape with template
   expressions in its body becomes a synthesizer. Rejected because: (a) this
   conflates recorded tapes with hand-authored templates -- a recorded tape
   that happens to contain `{{` in its response body would be incorrectly
   treated as a template; (b) no explicit opt-in makes it impossible to
   distinguish intentional exemplars from incidental string matches; (c) URL
   patterns need to be explicitly declared -- they cannot be inferred from
   exact URLs.

2. **Per-path-pattern config (in `config.json`) instead of per-tape flag**:
   Define URL patterns and response templates in the config file, separate
   from fixture JSON. Rejected because: (a) splits the definition of a mock
   response across two files (config + fixture), making authoring harder;
   (b) the fixture JSON is already the natural place for response templates
   (body, headers, status code); (c) the `exemplar: true` flag is
   self-documenting and requires no config changes.

3. **Separate `exemplars/` subdirectory**: Exemplar tapes live in a dedicated
   subdirectory, eliminating the need for the `exemplar` flag. Rejected
   because: (a) requires `FileStore` changes to scan subdirectories; (b)
   breaks the single-directory convention; (c) less portable (directory
   structure is environment-specific, flag-based discrimination is not).

4. **Server-level flag per-path-pattern (not per-tape)**: `WithSynthesis()`
   accepts a list of path patterns. Only those patterns are synthesized.
   Rejected because: (a) duplicates information already in the exemplar tapes'
   `URLPattern` field; (b) requires keeping the server config and fixture
   files in sync; (c) the per-tape flag is simpler and self-contained.

5. **No server-level flag (always synthesize if exemplar tapes exist)**:
   Rejected because: (a) silently changes behavior for integration test users
   who happen to have exemplar tapes in their fixture directory; (b) violates
   fail-safe principle -- exemplars should be inert unless explicitly
   activated; (c) the PM spec explicitly requires a two-level opt-in.

#### Consequences

**Benefits:**

- **Frontend-first workflow unlocked**: developers record one tape and get
  realistic, deterministic responses for any parameter value. The mock server
  becomes a lightweight, contract-driven stub -- no separate stub framework
  needed.
- **Fail-safe defaults**: integration test users are completely unaffected.
  Exemplar tapes are inert unless `WithSynthesis()` is explicitly set. Even
  if exemplar fixtures are accidentally included in a test fixture directory,
  they are ignored.
- **Deterministic reproducibility**: faker seeds are content-derived
  (`user-{{pathParam.id}}`), not tape-identity-derived. The same request
  always produces the same fake data, regardless of tape file name or
  declaration order. This makes UI snapshots and visual regression tests
  reliable.
- **Composable with existing matcher**: exemplars pass through the same
  `CompositeMatcher` criteria as exact tapes (method, headers, body_fuzzy,
  etc.). No special-case matcher logic.
- **Backward compatible**: new fields are `omitempty` and default to zero
  values. Existing fixtures load unchanged. Existing server configurations
  behave identically.

**Costs / trade-offs:**

- **Two-phase match adds complexity to `ServeHTTP`**: the method gains a
  second matching pass. This is acceptable because (a) the second pass is
  conditional (synthesis enabled + no exact match), (b) exemplar count is
  typically small (single digits), and (c) the O(n) scan is already the
  bottleneck, not the match logic.
- **Type coercion is limited**: only `| int`, `| float`, `| bool`. No
  `| string` (identity), no `| array`, no custom coercions. This covers
  the common cases (ID fields, boolean flags, numeric values). Extensions
  can be added later.
- **SSE exemplars deferred**: a user who wants to synthesize SSE streams
  (e.g., LLM streaming responses with varying content) cannot do so with
  this feature. A follow-up issue is needed.
- **`TemplateContext` refactor touches existing signatures**: `resolveExpr`
  and related functions change their parameter lists. All existing callers
  (internal to the package) must be updated. No public API break since these
  functions are unexported.
- **Dependency on #196**: this feature cannot ship until #196 delivers
  `PathPatternCriterion` and `{{pathParam.*}}`. If #196 is delayed, #199
  is blocked.

**Future implications:**

- **SSE exemplars** can be added as a follow-up by extending
  `resolveTemplateJSON` to walk `SSEEvent.Data` strings.
- **Stateful exemplars** (#195 scenarios) could combine with synthesis to
  produce request-sequence-dependent responses from exemplar templates.
- **Schema-based synthesis** (generating responses from OpenAPI schemas)
  could layer on top of the exemplar infrastructure by auto-generating
  exemplar tapes from schema definitions.
- **Admin API** (#194) can expose synthesis state (enabled/disabled, exemplar
  count) via a query endpoint.

---


## PM Log

### 2026-04-16

- **Created #121** — `feat: technical health endpoint surface for proxy mode (snapshot + SSE + active probe)`.
  - Labels: `priority:high`. No milestone (per request).
  - Adds `/__httptape/health` (JSON snapshot) and `/__httptape/health/stream` (SSE) under explicit opt-in CLI flags `--health-endpoint` and `--upstream-probe-interval`. Active background probe feeds the same state machine the request path does.
  - Settled the "what is state?" question in favor of definition B (most-recently-served source: `live` / `l1-cache` / `l2-cache`) — rationale: aligns with what the consuming UI is communicating to the user and gives a single source of truth fed by both real and synthetic (probe) traffic.
  - Status comment posted: `READY_FOR_ARCH`.
  - Open questions flagged for the architect: final flag names, default probe cadence, probe method/path, SSE back-pressure strategy, JSON field names, SSE event-name convention.
  - Downstream consumer noted: `ts-frontend-first` will switch its badge to drive off the SSE stream once this lands. The hosted-demo / per-visitor outage simulation feature in `~/workspace/httptape-demos/ts-frontend-first/BACKLOG.md` is explicitly out of scope here and depends on this issue landing first.

- **Created #122** — `chore: enrich Docker Hub & GHCR registry pages (OCI labels, README sync, README parity)`.
  - Labels: `priority:medium`, `documentation`. No milestone (cosmetic/docs scope).
  - Three concrete deliverables in one chore-issue: (1) add `org.opencontainers.image.*` labels to the root `Dockerfile` (with `version` wired from a build-arg in `.github/workflows/docker.yml`), (2) extend release CI with a `peter-evans/dockerhub-description` step that pushes the GitHub README to Docker Hub on tag pushes, (3) rebalance the README's `## Docker` section so GHCR is given equal billing with Docker Hub instead of being a one-line footnote.
  - Explicit non-goals: image signing/cosign, SBOM/provenance attestations, vulnerability scanning, renaming the Docker Hub repo, multi-version registry docs. Each is a separate future issue.
  - Called out that the Docker Hub README sync only takes effect on the *next* push to the registry — merging the PR is not enough on its own to make the hub.docker.com page change. The GHCR auto-link verification is also a post-release manual step (the only acceptance criterion that cannot be confirmed pre-merge).
  - Open questions flagged for the architect: single consolidated `LABEL` vs. one `LABEL` per key; which workflow file (`docker.yml` vs. `release.yml`) hosts the README-sync step; whether the existing `DOCKERHUB_TOKEN` is scoped wide enough to write the repo description (vs. push-only).
  - Status comment posted: `READY_FOR_ARCH`.

- **Created #127** — `docs: document the typed Faker surface (interface, 12 built-ins, JSON config syntax)`.
  - Labels: `priority:high`, `documentation`. No milestone (could grab into v0.9.0).
  - Pure docs change driven by an audit against `main` @ `bf456dc` that found a major undocumented public surface: the `Faker` interface, `FakeFieldsWith`, all 12 built-in fakers (`RedactedFaker`, `FixedFaker`, `HMACFaker`, `EmailFaker`, `PhoneFaker`, `CreditCardFaker`, `NumericFaker`, `DateFaker`, `PatternFaker`, `PrefixFaker`, `NameFaker`, `AddressFaker`), the `Rule.Fields` config field, and the JSON-config typed-faker syntax (both shorthand and object form) are absent from `docs/sanitization.md`, `docs/api-reference.md`, and `docs/config.md`. Today, JSON-config users have no in-docs path to discover typed fakers at all.
  - Three deliverables in one issue: (1) new \"Typed fakers\" section in `docs/sanitization.md` with the interface contract, per-built-in prose + Go example, decision guide vs auto-detect, custom-`Faker` example; (2) `docs/api-reference.md` additions covering the interface, `FakeFieldsWith`, all 12 typed types, and the `Rule.Fields` field; (3) `docs/config.md` typed-fake-fields section covering both syntaxes, validation/error behavior, and three end-to-end examples (email shorthand, numeric object form, credit-card shorthand). Cross-links between the three files required.
  - Reference material called out for the architect/dev: `faker.go` (canonical), `sanitizer.go` (`FakeFieldsWith` wiring), `config.go` (`parseFakerSpec` and friends), `faker_test.go` and `config_test.go` (snippet sources).
  - Explicit non-goals: no new fakers, no renames, no behavior change to `parseFakerSpec`, no `config.schema.json` edits unless an example surfaces an actual schema gap (in which case open a sibling issue), no README / `llms-full.txt` / `llms.txt` / `PROJECT.md` / `docs/cli.md` edits.
  - Open questions flagged for the architect: per-built-in H3 vs grouped sections in `docs/sanitization.md`; whether to show `Rule.Fields` as a Go literal or JSON in the api-reference example; how to handle inconsistent constructor-vs-struct-literal idioms across the 12 fakers (document per-type and flag back if it's a real inconsistency); whether `parseFakerSpec`'s current error messages are user-friendly enough to document verbatim or warrant a separate code issue.
  - Audit gap was specifically that `docs/sanitization.md` only covered the auto-detect `FakeFields(seed, paths...)` path — nothing in the user-facing docs surfaces the typed entrypoint or the built-ins, despite all 12 implementations being exported in `faker.go` and reachable from JSON config.
  - Status comment posted: `READY_FOR_ARCH`.

- **Created #126** — `docs: retire stale PROJECT.md and BACKLOG.md (delete or replace with redirect stubs)`.
  - Labels: `priority:medium`, `documentation`. No milestone.
  - Pure docs chore. `PROJECT.md` and `BACKLOG.md` at the repo root are 6-12 months stale and now actively misleading: `PROJECT.md` lines 62-95 contain a fabricated API surface (non-existent constructors, wrong type fields, wrong constructor signatures) and lines 144-149 still claim "no CLI" even though `cmd/httptape` ships with Docker images and testcontainers integration; `BACKLOG.md` shows every milestone 1-4 item as an unchecked TODO when in fact all are merged and shipped.
  - Spec deliberately offers two viable paths (delete vs. redirect stub) and asks the architect to choose, with rationale captured in the PR description. Pre-flight repo-wide grep already confirmed no in-repo files reference either filename, so the audit step should be a quick spot-check.
  - Out of scope: any change to `decisions.md`, any change to unrelated `BACKLOG.md` files in sibling repos (e.g. `httptape-demos/ts-frontend-first/BACKLOG.md`), any code or test changes, any new top-level docs (CONTRIBUTING.md / ROADMAP.md).
  - Open question for the architect: pick Option A (delete) vs. Option B (3-line redirect stub). Trade-off framed in the issue around external-bookmark and AI-scraper-index breakage vs. ongoing maintenance.
  - Status comment posted: `READY_FOR_ARCH`.

- **Created #128** — `chore: documentation drift cleanup (Go version, image size, missing flags/options/fields, example fixes)`.
  - Labels: `priority:medium`, `documentation`. No milestone.
  - Bundles eight independently-small docs drift items into one chore: (1) Go version `1.22 -> 1.26` in `docs/getting-started.md` and `docs/index.md`; (2) Docker image size unified across eight locations (`README.md` x3, `docs/docker.md`, `docs/ui-first-dev.md`, `llms.txt`, `llms-full.txt`) using a freshly-measured number — measurement is itself an acceptance criterion; (3) missing CLI flags in `docs/cli.md` (`--tls-cert/key/ca/insecure` for `record` and `proxy`, `--config` for `serve` with the "accepted but not used" caveat); (4) missing replay options in `docs/replay.md` (`WithCORS`, `WithDelay`, `WithErrorRate`, `WithReplayHeaders`); (5) missing entries in `docs/api-reference.md` (`WithRecorderTLSConfig`, `WithProxyTLSConfig`, `BuildTLSConfig`, `Tape.Metadata`); (6) panic-conditions documentation for `NewRecorder`/`NewServer`/`NewProxy`; (7) example-correctness fixes in `docs/getting-started.md` (the async-flush misclaim and the empty-body-with-redacted-PII inconsistency); (8) `metadata` key added to the sample fixture in `docs/storage.md`.
  - Explicit scope coordination: registry-naming inconsistency (`tibtof/httptape` vs. `ghcr.io/vibewarden/httptape`) is owned by #122 and explicitly out-of-scope here. `Rule.Fields` is owned by the parallel typed-Faker docs issue (#127) and explicitly deferred — the `docs/api-reference.md` fix here covers TLS-related missing entries and `Tape.Metadata` only.
  - Pure docs change: no `.go` files, no tests, no `go.mod`/`go.sum`, no CI, no `Dockerfile`. Image-size measurement requires a local Docker build, which is called out as expected work.
  - No open questions for the architect. The image-size measurement is correctly deferred to the implementer (and is itself an acceptance criterion); everything else is concretely specified including which lines, which files, and the resolution for each ambiguity.
  - Status comment posted: `READY_FOR_ARCH`.

- **Created #129** — `chore: repository polish — community-health files, CI badge, OpenSSF Scorecard`.
  - Labels: `priority:medium`, `documentation`, `chore` (created the `chore` label — did not exist; color `#FEF2C0`, description "Maintenance, polish, or housekeeping work"). No milestone (could grab into v0.9.0).
  - Bundles 8 deliverables that together fix the "looks early-stage" GitHub-profile signal: `SECURITY.md`, `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md` (Contributor Covenant v2.1), YAML form-style issue templates (`bug.yml` + `feature.yml` + `config.yml` with blank issues disabled and a Discussions link), `PULL_REQUEST_TEMPLATE.md`, a CI status badge in the README, and an OpenSSF Scorecard workflow + badge.
  - Pre-flight reality check folded into the spec: there is **no existing `test.yml` workflow** — `release.yml` only runs tests on tag push, `docker.yml` runs build without tests, `docs.yml` is docs-only. Therefore the issue requires the architect to add a new minimal `test.yml` running `go test ./... -race -count=1` on push to `main` and PRs targeting `main`. A CI badge for a non-existent workflow would be worse than no badge.
  - All content must be honest about current project state — `SECURITY.md` says supported version is `main` only (no v0.9.0 yet), `CONTRIBUTING.md` describes the actual PM → Architect → Dev → Reviewer agent pipeline rather than aspirational OSS process, conventional-commit prefixes listed match what's in `git log` (`feat`, `fix`, `chore`, `docs`, `test`, `ci`).
  - Explicit non-goals: `FUNDING.yml` (owner still deciding on sponsorships), repo-metadata changes already done out-of-band (topics, homepage, Discussions toggle), social-preview image (UI-only upload), `awesome-go` submission (Tier 3, deferred to v0.9.0), Show HN / launch posts (owner-driven, post-v1.0), and any of the next-tier security automation (`dependabot.yml`, `codeql.yml`, cosign, SBOM) which are each their own future issue.
  - Hard constraint reaffirmed: zero new Go-module dependencies (`go.mod` / `go.sum` unchanged); CI Action versions are not Go deps and are fine.
  - Acceptance includes a self-check the dev runs in the PR description: `gh api repos/VibeWarden/httptape/community/profile` should show all eight community-profile checklist items satisfied after merge.
  - Open questions flagged for the architect: (1) single consolidated PR vs. 2–3 split PRs (PM recommendation: single, for cohesion); (2) whether `gh repo edit --enable-private-vulnerability-reporting` is available in the installed `gh` (SECURITY.md content branches on the answer — PVR vs. private email); (3) whether to publish Scorecard results to the OpenSSF dashboard (requires `id-token: write`, makes the badge meaningfully clickable) vs. private (Actions-log only); (4) CI badge label ("CI" vs. "Tests").
  - File-level coordination noted with #122 (Docker hub registry polish): #122 touches `docker.yml` and `release.yml`; #129 adds new `test.yml` and `scorecard.yml`. No conflict, but reviewers of either PR should know the other is in flight.
  - Status comment posted: `READY_FOR_ARCH`.

### 2026-04-18 — Matcher improvements for Koog agent demo

Context: the next planned demo is Kotlin + Ktor + Koog + Kotest + Testcontainers. The headline scenario is a multi-step Koog agent that makes two `POST /v1/chat/completions` calls in one test, identical URL/method/headers, distinguished only by the `messages` array in the JSON body. This requires body-aware matching, which exposed one bug and two gaps.

- **Created #178** — `fix: MatchBodyFuzzy eliminates body-less requests (both-empty should be vacuously true)`.
  - Labels: `priority:medium`, `type:bug`. No milestone.
  - Bug: when both incoming request and candidate tape have no body, `MatchBodyFuzzy` returns 0 and eliminates the candidate. Fix: return a small positive score (1) when both sides are empty (vacuously true). Field-mismatch and one-sided-empty cases unchanged.
  - 7 acceptance criteria, all testable. Table-driven tests covering both-empty, one-empty, both-match, both-differ.
  - No dependencies on other issues.
  - Status comment posted: `READY_FOR_ARCH`.

- **Created #179** — `refactor: promote MatchCriterion from function type to Criterion interface`.
  - Labels: `priority:high`, `type:refactor`. No milestone.
  - Promotes `MatchCriterion` (bare function type) to a `Criterion` interface with `Score()` method. All 8 existing criteria become exported structs. `CriterionFunc` adapter retained for ad-hoc use. Old `MatchCriterion` type removed (pre-1.0, breaking is acceptable).
  - 10 acceptance criteria. Pure refactor, no behavioral change.
  - 3 open questions flagged for the architect: (1) back-compat shims for constructor functions; (2) optional `Name()` method; (3) exported vs. unexported fields on criterion structs.
  - #180 depends on this issue.
  - Status comment posted: `READY_FOR_ARCH`.

- **Created #180** — `feat: CLI --match-body-fuzzy flag for httptape serve`.
  - Labels: `priority:high`, `type:feature`. No milestone.
  - Adds a repeatable `--match-body-fuzzy <path>` flag to `httptape serve`. When provided, server matcher becomes `NewCompositeMatcher(MethodCriterion{}, PathCriterion{}, BodyFuzzyCriterion{Paths: ...})`. When absent, default behavior unchanged.
  - 6 acceptance criteria including CLI tests, integration test with two same-URL different-body fixtures, and help text update.
  - Depends on #179 (Criterion interface). Architect should NOT start until #179 is approved and merged.
  - Independent of #178 (the demo's requests all have bodies, but the fix improves robustness for mixed route sets).
  - Out of scope: `--match-headers`, `--match-query-params`, `--match-body-hash`, declarative config-file form.
  - Status comment posted: `READY_FOR_ARCH`.

Dispatch plan: #178 and #179 can be architected in parallel. #180 waits for #179 to be merged.

### 2026-04-18 — #180 revision: CLI flag -> declarative config

- **Revised #180** — title changed from `feat: CLI --match-body-fuzzy flag for httptape serve` to `feat: declarative matcher composition via config file (and wire --config into serve)`.
  - **Rationale for the pivot:** The original `--match-body-fuzzy` repeatable CLI flag design was rejected because (1) it leads to flag explosion as more criteria are exposed, (2) it bypasses the existing JSON config infrastructure (`Config` in `config.go`, `config.schema.json`), and (3) the `--config` flag already exists on `serve` in `cmd/httptape/main.go` line 157 but is currently unused ("accepted but not used by serve"). Extending `Config` with an optional `matcher` field reuses all existing validation, schema, and loading infrastructure.
  - **New design:** `Config` gains an optional top-level `matcher` field with a `criteria` array. Each entry has a `type` discriminator (`method`, `path`, `body_fuzzy`) and type-specific fields (`paths` for `body_fuzzy`). A factory function (`BuildMatcher`) constructs the `CompositeMatcher`. `runServe` in `main.go` now loads the config and passes `WithMatcher(...)` to `NewServer` when `--config` is provided. The TODO comment is removed.
  - **Scope tightened:** only `method`, `path`, and `body_fuzzy` criterion types in this issue. Other types (`path_regex`, `route`, `headers`, `query_params`, `body_hash`) are follow-up issues. Per-criterion CLI flags are explicitly out of scope (rejected design).
  - **Dependencies unchanged:** still depends on #179 (Criterion interface). Independent of #178 (vacuous-true fix).
  - 11 acceptance criteria covering: Config struct changes, BuildMatcher factory, LoadConfig validation, schema update, CLI wiring, help text, config_test.go, main_test.go, integration test, backward compatibility.
  - Body and labels updated. Design-pivot rationale comment posted. Status re-set to `READY_FOR_ARCH`.
  - #178 and #179 were NOT modified.

### 2026-04-18 — Kotlin + Ktor + Koog + Kotest demo

- **Created #185** — `feat(examples): Kotlin + Ktor + Koog + Kotest demo with mocked OpenAI agent and weather REST`.
  - Labels: `type:feature`, `priority:high`, `examples` (created the `examples` label — did not exist; color `#6f42c1`, description "Example applications and demos").
  - This is the proof artifact that #178 (vacuous-true fix), #179 (Criterion interface), and #180 (declarative matcher composition) all work correctly together end-to-end.
  - Headline scenario: single-tool Koog agent that makes two `POST /v1/chat/completions` calls (distinguished by `$.messages[*].role` via `BodyFuzzyCriterion`) plus one body-less `GET` for weather data (coexists via #178 vacuous-true fix), all served by a single httptape Testcontainers instance with a declarative matcher config file (#180).
  - Stack: Kotlin 2.x, JDK 25, Ktor 3.x (Netty), Koog (latest), Kotest (BehaviorSpec recommended), Testcontainers, Gradle Kotlin DSL (latest wrapper). All versions to be verified against Maven Central / Gradle Plugin Portal at implementation time (hard constraint after the Spring Boot version incident).
  - Weather API: wttr.in (`https://wttr.in/Berlin?format=j1`) — free, no API key.
  - 18 acceptance criteria covering: directory structure, Gradle build, Ktor endpoint, Koog agent + tool, configurable base URLs, 3 fixtures, matcher config, Kotest test, single shared container, api.http, Dockerfile + docker-compose.yml, README (parallel to Java demo), matcher-callout in README, dependabot config, CI matrix entry, SSE fixture accuracy, `./gradlew test` green, PR held for user review.
  - PR explicitly NOT to be merged — held for user's personal review.
  - 5 open questions flagged for the architect: (1) Kotest BehaviorSpec vs FunSpec, (2) Koog tool HTTP client idiom, (3) multi-module vs single-module Gradle, (4) matcher config file location and container mount path, (5) SSE fixture authoring strategy for tool-call deltas.
  - Parallel structure to #167 (Java demo). References #178, #179, #180 for the matcher stack.
  - Status comment posted: `READY_FOR_ARCH`.

### 2026-04-18 — Combined Content-Type awareness cycle (v0.12.0)

**User decision:** combine #187 (Content-Type-driven body field) and #188 (ContentNegotiationCriterion) into a single cycle.

**Rationale:**
- Both features share a common **MediaType parsing/classification utility** (parse media types, classify as JSON/text/binary, parse Accept headers with q-values). Building this utility once and consuming it from both layers avoids duplication and ensures consistent behavior.
- Both touch **fixtures** — #187 migrates the body format; #188 adds new multi-Content-Type fixtures for content negotiation tests. One fixture migration pass is cleaner than two.
- Both touch the same **docs** (`docs/matching.md`, `docs/fixtures-authoring.md`).
- Single coherent **v0.12.0 release narrative**: "better fixture authoring + smart content negotiation as the same Content-Type-aware idea applied at two layers."
- Both are **pre-1.0 breaking changes** — one atomic breaking change is less disruptive than two sequential ones.
- One ADR, one PR, one shared `mediaType.go` helper.

**Actions taken:**
- **Closed #188** with a redirect comment pointing to the revised #187.
- **Revised #187** — new title: `feat(tape): Content-Type-driven body field + ContentNegotiationCriterion (v0.12.0)`. Full revised spec covering both layers (body shape + content negotiation) plus shared MediaType utility. 24 acceptance criteria, 8 open questions for the architect.
- **Created `breaking-change` label** (color `#B60205`, description "Pre-1.0 breaking change to public API or fixture format"). Applied to #187.
- **Added `milestone:4-advanced-matching` label** to #187 (content negotiation is an advanced matching feature).
- **Status comment posted** on #187: `READY_FOR_ARCH`. Queued for after PR #186 merges.

**Key design pivot from original #187:** the original spec used escaped JSON strings for JSON bodies (e.g., `"body": "{\"model\":\"gpt-4o-mini\",...}"`). The revised spec uses **native JSON objects** (e.g., `"body": {"model": "gpt-4o-mini", ...}`), making the `body` field truly polymorphic: its JSON type (object, string, or base64 string) is determined by the Content-Type header. This is a more ambitious but significantly better DX outcome.

**No open questions from the PM.** All 8 open questions are flagged for the architect.

### 2026-04-18 — Feature-parity audit: four post-launch gaps filed to Future milestone

**Trigger:** Feature-parity audit against general-purpose HTTP mocking tools identified four gaps that would meaningfully improve httptape's test-utility value. All four are post-launch work — not blocking any current milestone. Filed to the tracker so they don't get lost.

**Shared framing:** Each issue is framed as "here's the gap + here's our design" — no competing products are referenced by name. Where relevant, the LLM agent multi-turn testing angle is called out as the primary motivation, since that is httptape's current marketing wedge.

**Issues created:**

1. **#194** — `feat: request journal + admin HTTP endpoint for post-hoc verification`
   - Labels: `type:feature`, `priority:high`, `milestone:future`. Milestone: Future.
   - In-memory bounded ring buffer on `Server` recording every inbound request. Go API (`Journal()`, `ClearJournal()`, `JournalLen()`) plus opt-in HTTP admin endpoints under `/__httptape/` prefix with filtering (method, path, body-contains, since, limit). Gated by `WithAdminAPI() ServerOption` / `--admin-api` CLI flag.
   - Key open questions for architect: full body vs hash in journal entries, concurrency model, unmatched request inclusion.
   - No dependencies.

2. **#195** — `feat(matcher): scenarios for stateful multi-step interactions (sequence + state machine)`
   - Labels: `type:feature`, `priority:high`, `milestone:future`. Milestone: Future.
   - Two tiers: declarative linear sequences (80% case) and full state machine transitions (power case). New `ScenarioCriterion` implementing `Criterion`. Per-scenario state tracked on `Server`, thread-safe. Config validation ensures tape IDs exist and transitions form valid graphs.
   - Key open questions for architect: terminal behavior (sticky vs loop), cross-scenario reset semantics.
   - Soft dependency on #194 (admin endpoints for scenario reset/inspect), but can ship independently.

3. **#196** — `feat(templating): response template helpers with deterministic faker integration`
   - Labels: `type:feature`, `priority:medium`, `milestone:future`. Milestone: Future.
   - Extends `{{...}}` parser with `now`, `uuid`, `randomHex`, `randomInt`, `counter`, and `faker.*` helpers. The `faker.*` helpers bridge to the existing HMAC-based Faker infrastructure — same seed produces the same output across runs. This is the differentiator: reproducible fakes baked into templating.
   - Key open questions for architect: parser strategy (ad-hoc extension vs proper tokenizer), seedless faker fallback policy, counter overflow behavior.
   - No hard dependencies; integrates nicely with #194 for counter reset.

4. **#197** — `feat(server): inbound TLS listener (self-signed cert or user-provided)`
   - Labels: `type:feature`, `priority:medium`, `milestone:future`. Milestone: Future.
   - User-provided cert/key or auto-generated self-signed cert at startup (stdlib crypto only). Logs fingerprint and PEM for programmatic trust/pinning. Relevant for LLM SDK clients that hardcode HTTPS.
   - Key open questions for architect: cert expiry, port behavior (switch to 8443 vs dual-listen), key algorithm.
   - No dependencies.

**Priority rationale:** #194 and #195 are high priority because they directly unlock the multi-turn agent testing narrative (verify what the agent sent; serve stateful response sequences). #196 and #197 are medium priority — useful but not blocking the core value proposition.

**All four issues:** `READY_FOR_ARCH` status comments posted. Architects not dispatched — returned to user for triage.

### 2026-04-18 — Path-parameter templating (#196 scope expansion) + exemplar tapes (#199)

**Context:** Frontend-first development workflows need the mock server to synthesize responses for parameterized routes (e.g., `/users/1`, `/users/2`, `/users/42`) from a single exemplar tape, using deterministic fake data derived from captured path parameters. This requires two coordinated changes: (1) path-parameter matching and template accessors in the templating engine, and (2) a new "exemplar" tape mode with server-level opt-in synthesis.

**Actions taken:**

1. **Revised #196** — `feat(templating): response template helpers with deterministic faker integration`
   - Scope expanded to include path-parameter templating (matcher + template accessor).
   - **New matcher:** `PathPatternCriterion` with colon-prefixed named segments (`:id` syntax), config dispatch as `"type": "path_pattern"`, JSON Schema enum update.
   - **New template accessor:** `{{pathParam.NAME}}` resolves captured URL segments. `{{request.query.NAME}}` verified and documented as first-class.
   - **Nested template evaluation:** `seed=user-{{pathParam.id}}` evaluated before the helper runs — key mechanism for per-ID deterministic faking.
   - 3 new open questions added for the architect: pattern syntax choice (Express `:id` vs OpenAPI `{id}`), match precedence weight, query-param-in-pattern exclusion.
   - Type coercion helpers (`| int`, `| float`, `| bool`) deferred to #199 where they are needed for exemplar JSON body rendering.
   - Body rewritten cleanly (not appended) to integrate both scopes into a coherent spec.
   - Status reconfirmed: `READY_FOR_ARCH`. Scope expansion comment posted.

2. **Created #199** — `feat: synthetic responses via exemplar tapes (opt-in, frontend-first workflows)`
   - Labels: `type:feature`, `priority:medium`, `milestone:future`. Milestone: Future.
   - New optional `exemplar: bool` field on `Tape` and `url_pattern: string` on `RecordedReq` (mutually exclusive with exact `url`). Purely additive fields — not a breaking change.
   - **Critical opt-in gate:** synthesis is disabled by default. `WithSynthesis()` ServerOption / `--synthesize` CLI flag is the master switch. Per-tape `exemplar: true` is necessary but not sufficient. This protects integration tests from silently masking missing fixtures.
   - Matching precedence: exact URL wins over exemplar; among exemplars, most-specific pattern wins (fewest wildcard segments).
   - Template resolution recurses into native JSON objects, resolves strings, skips base64.
   - Type coercion helpers (`| int`, `| float`, `| bool`) for JSON type correctness in rendered bodies.
   - 16 acceptance criteria. 6 open questions for the architect (separate field vs overload, SSE scope, POST exemplar + body_fuzzy interaction, persistence layout, seed stability).
   - **Hard dependency on #196** — cannot ship before #196 (needs `PathPatternCriterion`, `{{pathParam.*}}`, `{{faker.*}}`).
   - Independent of #194 (journal), #195 (scenarios), #197 (inbound TLS).
   - Status: `READY_FOR_ARCH`.

**Rationale for the two-issue split:** #196 delivers the template engine and matcher primitives (reusable infrastructure). #199 builds the exemplar-tape mode on top of those primitives (product feature). This keeps #196 shippable and testable independently, and lets the architect design the template engine without being coupled to the exemplar concept.

**Dependency chain:** #196 (template helpers + path params) -> #199 (exemplar tapes + synthesis mode).

**Both issues:** `READY_FOR_ARCH` status comments posted. Architects not dispatched — returned to user.
