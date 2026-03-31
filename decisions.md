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
