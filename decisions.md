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

`RedactBodyPaths` uses a minimal JSONPath-like subset (`$.field`, `$.nested.field`,
`$.array[*].field`) rather than full JSONPath or jq because the stdlib-only constraint
(L-04) prohibits external JSONPath libraries, and the subset covers the vast majority
of real-world body redaction needs. Full JSONPath features (recursive descent, filter
expressions, bracket notation) are out of scope for v1 but the internal `segment`-based
path representation can be extended to support them without changing the public API.

Redaction is type-aware: strings become `"[REDACTED]"`, numbers become `0`, booleans
become `false`, while null/object/array values are left unchanged. This preserves the
JSON schema of the redacted body so replay consumers do not break on unexpected types.
Non-JSON bodies are silently passed through unchanged.

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

`ExportBundle` is a standalone package-level function (not a `Store` method) that
produces a tar.gz archive with a `manifest.json` entry followed by `fixtures/<id>.json`
entries. tar.gz was chosen over zip because the stdlib `archive/tar` + `compress/gzip`
pair supports streaming via `io.Pipe` (no need to buffer the full archive in memory),
and tar.gz is the standard format for Go tooling.

The manifest file (`Manifest` type) accompanies the tapes so that importers can validate
the bundle (fixture count, route list, export timestamp) without inspecting every fixture.
This makes the bundle self-describing and enables the two-phase validate-then-persist
import strategy in ADR-10.

`ExportBundle` composes on `Store.List` rather than adding a method to the `Store`
interface, preserving interface segregation and avoiding breaking third-party adapters.

---

### ADR-10: Bundle import (tar.gz)

**Date**: 2026-03-30
**Issue**: #36
**Status**: Accepted

`ImportBundle` uses a two-phase validate-then-persist strategy: phase 1 reads the
entire tar.gz bundle into memory and validates the manifest, fixture count, and
structural integrity of each tape without touching the store; phase 2 persists all
validated tapes via `Store.Save`. This ensures a corrupt or truncated bundle never
partially modifies the store. The trade-off is that all fixtures must fit in memory,
which is acceptable for v1 volumes.

On ID collision, the merge strategy is upsert: bundle fixtures overwrite existing
fixtures with the same ID, while fixtures already in the store whose IDs are absent
from the bundle are left untouched. This leverages `Store.Save`'s existing upsert
semantics and makes re-import idempotent. If `Save` fails mid-persist, the partial
import is safe to retry because upsert converges to the correct state.

Unknown tar entries are silently skipped for forward compatibility.

### ADR-11: Selective export (filter by route, method, time)

**Date**: 2026-03-30
**Issue**: #37
**Status**: Accepted

Selective export filtering (`WithRoutes`, `WithMethods`, `WithSince`) is applied as
a post-filter on the tape slice returned by `Store.List`, rather than extending the
`Store.List` interface with richer query parameters. The existing `Filter` type only
supports single `Route`/`Method` strings; adding multi-route slices and time-range
fields would change the contract for every `Store` adapter -- a breaking change to
avoid in v1.

Post-filtering keeps export as a bundle-assembly concern that composes on top of the
hexagonal port without modifying it. The trade-off is that `Store.List` loads all tapes
before discarding non-matching ones, which is acceptable for v1 volumes. If stores grow
large, a future ADR can push filters down to `Store.List`. All active filters are AND-ed;
zero filters preserves the "export all" default.

---

### ADR-12: MatchPathRegex — Regex path matching criterion

Removed in #246 cleanup — did not meet the tightened ADR bar (see issue for criteria).

---

### ADR-13: MatchHeaders — Header-based matching criterion

Removed in #246 cleanup — did not meet the tightened ADR bar (see issue for criteria).

---

### ADR-14: MatchBodyFuzzy — Field-level body matching criterion

Removed in #246 cleanup — did not meet the tightened ADR bar (see issue for criteria).

---

### ADR-15: Concurrent safety audit

Removed in #246 cleanup — did not meet the tightened ADR bar (see issue for criteria).

---

### ADR-16: Performance benchmark suite

Removed in #246 cleanup — did not meet the tightened ADR bar (see issue for criteria).

---

### ADR-18: Declarative sanitization config (JSON)

**Date**: 2026-03-30
**Issue**: #76
**Status**: Accepted

The JSON config format maps 1:1 to the Go Pipeline API: each `rules` entry has an
`action` discriminator (`redact_headers`, `redact_body`, `fake`) and action-specific
fields that correspond directly to `RedactHeaders`, `RedactBodyPaths`, and `FakeFields`
constructor arguments. `BuildPipeline` converts the config back to a `Pipeline` by
mapping each rule to its `SanitizeFunc` in order. This round-trip equivalence means
any pipeline expressible in Go code is expressible in JSON and vice versa, with no
lossy translation.

Config validation is intentionally stricter than the Go API: invalid paths cause a
load-time error (instead of being silently ignored) because config files are written
by humans and should fail loudly on typos. The `"version": "1"` field gates future
schema evolution. `DisallowUnknownFields` catches typos in field names at parse time.

### ADR-19: Standalone CLI

Removed in #246 cleanup — did not meet the tightened ADR bar (see issue for criteria).

---

### ADR-20: Docker Image

Removed in #246 cleanup — did not meet the tightened ADR bar (see issue for criteria).

---

### ADR-21: Testcontainers Module

Removed in #246 cleanup — did not meet the tightened ADR bar (see issue for criteria).

---

### ADR-22: LoadFixtures convenience functions

Removed in #246 cleanup — did not meet the tightened ADR bar (see issue for criteria).

---

### ADR-23: Go DSL for mock definitions

Removed in #246 cleanup — did not meet the tightened ADR bar (see issue for criteria).

---

### ADR-24: CORS support, latency simulation, and error simulation

**Date**: 2026-03-30
**Issues**: #94, #95, #96
**Status**: Accepted

Three `ServerOption`-based features (`WithCORS`, `WithDelay`, `WithErrorRate`) are
implemented as inline checks in `ServeHTTP` rather than as composable `http.Handler`
middleware. The architectural decision is the execution order:

1. **CORS** (first) -- headers must be present on all responses, including errors,
   so browsers can read them.
2. **Error simulation** (second) -- random 500s short-circuit before any work;
   skipping the delay is intentional (a simulated outage returns fast).
3. **Delay** (third) -- applied only to successful matches, after per-fixture
   metadata override resolution.
4. **Normal replay** (last) -- existing match-and-respond logic.

This order is locked because per-fixture error/delay overrides require access to the
matched tape (available only after step 4's matching phase), which rules out
pre-matching middleware. CORS before error simulation ensures browsers can read the
simulated 500 response body.

---

### ADR-25: Replay header injection (WithReplayHeaders)

Removed in #246 cleanup — did not meet the tightened ADR bar (see issue for criteria).

---

### ADR-26: Fallback-to-cache proxy mode with L1/L2 caching

**Date**: 2026-03-30
**Issue**: #108
**Status**: Accepted

The `Proxy` type introduced an L1 (MemoryStore, raw/ephemeral) + L2 (FileStore,
sanitized/persistent) two-tier caching architecture for fallback-to-cache proxy mode.
This ADR established the original design; the caching layer has since been refactored
into `CachingTransport` (see ADR-44), which composes the L2 layer as a reusable
`http.RoundTripper` primitive with stale-fallback semantics.

The core L1/L2 principle remains: L1 preserves raw unsanitized data within a session
for best-fidelity fallback; L2 holds sanitized data safe for disk persistence,
honoring the "sensitive data never touches disk" promise. On fallback, L1 is checked
first (session consistency), then L2 (cross-session persistence).

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

The health surface uses a "most-recently-served source" state machine with three
values (`live`, `l1-cache`, `l2-cache`) mirroring the existing `X-Httptape-Source`
header semantics. The state machine has a single mutator (`observe`) fed by both
real client traffic and a synthetic active probe, ensuring the snapshot endpoint
(`GET /__httptape/health`) and the SSE stream (`/__httptape/health/stream`) cannot
drift -- they read the same field under the same mutex.

SSE uses a bounded per-subscriber buffer (size 8) with drop-and-disconnect on overflow.
A subscriber whose buffer fills is removed and its connection closed; the client's
`EventSource` auto-reconnects, and the initial-on-connect event re-seeds the correct
state, so no application-visible information is lost. This keeps the broadcast loop
O(N) without holding the mutex during slow flushes.

The entire surface is opt-in (`WithProxyHealthEndpoint`). With the option absent, the
Proxy holds a nil `*HealthMonitor` and every call site is a nil-receiver no-op,
preserving byte-for-byte default behavior.

---

### ADR-29: Repository polish — community-health files, CI badge, OpenSSF Scorecard

Removed in #246 cleanup — did not meet the tightened ADR bar (see issue for criteria).

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

Removed in #246 cleanup — did not meet the tightened ADR bar (see issue for criteria).

---

### ADR-37: MatchBodyFuzzy vacuous-true for body-less requests

**Date**: 2026-04-18
**Issue**: #178
**Status**: Accepted

When both the incoming request body and the candidate tape body are absent (nil,
empty, or non-JSON), `MatchBodyFuzzy` returns score 1 instead of 0. This is the
vacuous-true principle: body-less requests (GET, DELETE, HEAD) score a minimum
positive value so they stay alive in `CompositeMatcher` scoring rather than being
eliminated. Without this, adding `MatchBodyFuzzy("$.action")` to handle POST routes
inadvertently kills all body-less routes.

Score 1 (not the full score of 6) is intentional: "neither side has a body" is weaker
evidence than "both sides have bodies and the specified fields match." A real body
match (6) always outscores a vacuous pass (1), preserving correct ranking. If exactly
one side is absent and the other is not, the criterion returns 0 (asymmetric mismatch).

---


### ADR-38: Promote MatchCriterion from function type to Criterion interface

Removed in #246 cleanup — did not meet the tightened ADR bar (see issue for criteria).

---

### ADR-39: Declarative matcher composition via JSON config

**Date**: 2026-04-18
**Issue**: #180
**Status**: Accepted

Each `CriterionConfig` entry in the JSON config has a `type` discriminator field that maps
directly to the `Criterion.Name()` string returned by each criterion struct (e.g.,
`"method"`, `"path"`, `"body_fuzzy"`). `BuildMatcher` dispatches via a `criterionBuilders`
registry keyed on the type name, constructing the corresponding `Criterion` struct and
assembling them into a `*CompositeMatcher`.

This registry pattern (rather than per-criterion CLI flags or preset names) was chosen
because it composes: adding a new criterion type to the config is a one-entry addition to
the dispatch table + one validation case + one `enum` value in the JSON schema. Per-criterion
CLI flags do not compose and lead to flag explosion. Named presets are too opinionated and
cannot express arbitrary combinations.

The `CriterionConfig` struct uses a single flat struct with all possible type-specific fields
(currently just `Paths`) rather than per-type structs with custom `UnmarshalJSON`, because
criteria have at most 1-2 config fields each and the single-struct approach keeps code
volume low.

---

### ADR-40: Kotlin + Ktor + Koog + Kotest demo (examples/kotlin-ktor-koog)

Removed in #246 cleanup — did not meet the tightened ADR bar (see issue for criteria).

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

### ADR-44: CachingTransport library API with stale-fallback policy

**Date**: 2026-04-18
**Issues**: #202, #164
**Status**: Accepted

#### Context

httptape exposes `Recorder` (forwards + records, no replay) and `Server`
(replays only, no forward) as library primitives. The `proxy` CLI subcommand
combines both -- cache by match, fall through on miss -- but that logic
lives inside the CLI binary, not as a reusable `http.RoundTripper`. Consumers
embedding httptape as a Go library (e.g., VibeWarden's Caddy-based egress
proxy, single-binary deployment) need the combined logic exposed as a
composable standard interface.

Issue #202 defines the CachingTransport API surface. Issue #164 (sibling,
same v0.13 milestone) defines the edge-case policy for "cache miss + upstream
down." These are resolved together as a single architectural unit because
#164's fallback semantics are a CachingTransport implementation concern.

Two primary use cases drive priority:

1. **Zero-cost demo hosting**: record ~50 real LLM responses once, replay
   infinitely for every demo visitor. Sanitized fixtures committed to repo.
2. **Egress proxy integration**: record in dev/staging, replay in tests.
   VibeWarden's single-binary Caddy egress proxy wraps CachingTransport.

#### Decision

##### Resolved open questions

**#202 Q1 -- Naming**: `CachingTransport`. Confirmed. Reads naturally for the
LLM-cost use case; "replay" is misleading since it also records.

**#202 Q2 -- Store failure during record**: return-and-log. Cache failures
do not break the user's application. The `onError` callback reports it; the
response is returned to the caller.

**#202 Q3 -- CLI proxy refactor**: refactor `runProxy` in
`cmd/httptape/main.go` to instantiate `CachingTransport` internally. Single
source of truth for cache-through-upstream logic. The Proxy type is preserved
but its `RoundTrip` delegates to CachingTransport (see CLI Refactor Plan).

**#202 Q4 -- Cache-Control respect**: NO. This is not an RFC 7234 cache; it
is a tape-match layer. LLM APIs return `no-store` and that is exactly what
we override. Document prominently in godoc and in `docs/caching-transport.md`.

**#202 Q5 -- Body size limits**: configurable `WithCacheMaxBodySize(n int)`
(default 10 MiB = 10 * 1024 * 1024). Requests whose body exceeds the limit
bypass cache entirely (pass through to upstream, response is not recorded).
This prevents unbounded memory usage from large file uploads.

**#202 Q6 -- TTL / eviction**: NO for v1. Fixtures are conceptually
immutable. Manual pruning or `migrate-fixtures` if needed.

**#202 Q7 -- Interaction with Recorder**: keep Recorder as the record-only
primitive (audit use cases where you always want to capture). CachingTransport
is the replay+record-on-miss sibling. Do not deprecate Recorder.

**#164 Q1 -- Non-SSE cache miss + upstream down**: default behavior is
transparent error propagation. A connection-refused from upstream returns the
transport error to the caller. A 5xx from upstream returns the 5xx response.
Optional stale-fallback behavior (opt-in via `WithCacheUpstreamDownFallback`)
serves the most-recently-cached response with `X-Httptape-Stale: true` header.

**#164 Q2 -- SSE cache miss + upstream down**: same as non-SSE. If no SSE
tape is cached, propagate the error. If stale-fallback is enabled and a
cached SSE tape exists, replay it with `X-Httptape-Stale: true` header. No
partial-stream synthesis -- either a full cached tape exists or it does not.

**#164 Q3 -- Timeout**: `WithCacheUpstreamTimeout(time.Duration)` defaulting
to 0 (no timeout -- let the user's transport timeout dominate). When set,
CachingTransport wraps the request context with a deadline before forwarding
to upstream. On timeout, the fallback path is entered (same as transport
error).

##### Type: CachingTransport

```go
// CachingTransport is an http.RoundTripper that consults a tape Store on
// each request. On cache hit, it returns the recorded response without
// contacting upstream. On cache miss, it forwards to upstream, records the
// response (after sanitization), and returns it.
//
// CachingTransport is NOT an RFC 7234 HTTP cache. It does not honor
// Cache-Control, Vary, or any other HTTP caching headers. It is a
// tape-match layer: identical requests (as defined by the Matcher) get
// identical recorded responses.
//
// CachingTransport is safe for concurrent use by multiple goroutines.
type CachingTransport struct {
    upstream      http.RoundTripper
    store         Store
    matcher       Matcher
    sanitizer     Sanitizer
    route         string
    onError       func(error)
    cacheFilter   func(*http.Response) bool
    singleFlight  bool
    maxBodySize   int
    sseRecording  bool

    // stale-fallback (#164)
    staleFallback bool
    upstreamTimeout time.Duration

    // single-flight coordination (stdlib-only)
    sfMu    sync.Mutex
    sfCalls map[string]*sfCall
}
```

Where `sfCall` is an unexported type for single-flight coordination:

```go
// sfCall tracks an in-flight upstream request for single-flight dedup.
type sfCall struct {
    wg   sync.WaitGroup
    resp *http.Response  // set before wg.Done
    err  error           // set before wg.Done
    body []byte          // buffered response body for sharing
}
```

##### Single-flight implementation (stdlib-only)

Manual single-flight using `sync.Mutex` + `map[string]*sfCall`:

```go
func (ct *CachingTransport) doSingleFlight(key string, fn func() (*http.Response, []byte, error)) (*http.Response, []byte, error) {
    ct.sfMu.Lock()
    if call, ok := ct.sfCalls[key]; ok {
        ct.sfMu.Unlock()
        call.wg.Wait()
        // Clone the response for each waiter
        return cloneResponse(call.resp, call.body), copyBytes(call.body), call.err
    }
    call := &sfCall{}
    call.wg.Add(1)
    ct.sfCalls[key] = call
    ct.sfMu.Unlock()

    resp, body, err := fn()
    call.resp = resp
    call.body = body
    call.err = err
    call.wg.Done()

    ct.sfMu.Lock()
    delete(ct.sfCalls, key)
    ct.sfMu.Unlock()

    return resp, body, err
}
```

The key is computed from the match dimensions: `method + "|" + path + "|" + bodyHash`.
This is a ~30-line stdlib-only implementation that avoids the `x/sync/singleflight`
dependency while providing the same dedup semantics.

Waiters receive a cloned response (fresh `io.ReadCloser` over the same body
bytes) so each caller gets an independent, consumable response.

##### CachingOption type and constructors

```go
// CachingOption configures a CachingTransport.
type CachingOption func(*CachingTransport)

// WithCacheMatcher sets the Matcher used to identify equivalent requests.
// Default: NewCompositeMatcher(MethodCriterion{}, PathCriterion{}, BodyHashCriterion{}).
func WithCacheMatcher(m Matcher) CachingOption

// WithCacheSanitizer sets the sanitization pipeline applied to recorded
// tapes before store persistence. Default: NewPipeline() (no-op).
func WithCacheSanitizer(s Sanitizer) CachingOption

// WithCacheFilter sets a predicate controlling which upstream responses
// are cached. Only responses for which fn returns true are persisted.
// Default: cache 2xx responses only.
func WithCacheFilter(fn func(*http.Response) bool) CachingOption

// WithCacheSingleFlight controls single-flight deduplication of concurrent
// cache misses. When true (default), concurrent requests with the same match
// key share a single upstream call.
func WithCacheSingleFlight(enabled bool) CachingOption

// WithCacheMaxBodySize sets the maximum request body size in bytes for
// cache participation. Requests whose body exceeds this limit bypass the
// cache entirely (forwarded to upstream, response not recorded).
// Default: 10 MiB (10 * 1024 * 1024).
// A value of 0 means no limit.
func WithCacheMaxBodySize(n int) CachingOption

// WithCacheRoute sets the route label applied to all tapes created by
// this transport.
func WithCacheRoute(route string) CachingOption

// WithCacheOnError sets a callback invoked when a non-fatal error occurs
// (store failure, body read failure on the record path). Defaults to no-op.
func WithCacheOnError(fn func(error)) CachingOption

// WithCacheSSERecording controls whether SSE stream recording is enabled.
// When true (default), SSE responses on the miss path are tee'd to the
// caller and accumulated for tape persistence.
func WithCacheSSERecording(enabled bool) CachingOption

// WithCacheUpstreamDownFallback enables stale-response fallback when
// upstream is unreachable or returns a transport error on a cache miss.
// When enabled, CachingTransport searches the store for the best-matching
// tape (using the configured Matcher) and returns it with an
// X-Httptape-Stale: true header. When disabled (default), transport errors
// are propagated to the caller.
//
// This is useful for demo hosting (upstream flakiness should not break the
// demo) but wrong for integration tests (which should see the real failure).
func WithCacheUpstreamDownFallback(enabled bool) CachingOption

// WithCacheUpstreamTimeout sets a timeout for upstream requests on cache
// miss. When set, the request context is wrapped with a deadline before
// forwarding. Default: 0 (no timeout; the caller's http.Client timeout
// dominates).
func WithCacheUpstreamTimeout(d time.Duration) CachingOption
```

##### Constructor

```go
// NewCachingTransport creates a new CachingTransport wrapping the given
// upstream transport with store-backed caching.
//
// On RoundTrip, it consults the store for a matching tape. On hit, it
// returns the recorded response. On miss, it forwards to upstream, records
// the response (after sanitization), and returns it.
//
// upstream and store must not be nil. Panics on nil (constructor guard per
// CLAUDE.md).
//
// Default configuration:
//   - matcher: method + path + body_hash
//   - sanitizer: no-op Pipeline
//   - cache filter: 2xx only
//   - single-flight: enabled
//   - max body size: 10 MiB
//   - SSE recording: enabled
//   - stale fallback: disabled
//   - upstream timeout: 0 (no timeout)
//   - route: ""
//   - onError: no-op
func NewCachingTransport(upstream http.RoundTripper, store Store, opts ...CachingOption) *CachingTransport
```

##### RoundTrip flow (non-SSE)

```
1. Read request body into buffer (respecting maxBodySize).
   - If body exceeds maxBodySize: restore body on req, pass through to
     upstream directly (no cache lookup, no recording). Return.

2. Compute body hash from buffer.

3. Replace req.Body with fresh reader over buffer (upstream can consume it).

4. Compute single-flight key: method + "|" + URL.Path + "|" + bodyHash.

5. Cache lookup: store.List(ctx, Filter{Route: route}) -> matcher.Match(req, tapes).

6. HIT: synthesize *http.Response from tape.
   - For non-SSE tapes: set headers, wrap body in io.NopCloser(bytes.NewReader(body)).
   - For SSE tapes: use piped body with event replay (same as Proxy.sseResponseFromTape).
   - Return response with no X-Httptape-Source header (cache hits are transparent).

7. MISS: forward to upstream (with optional single-flight dedup).
   a. If upstreamTimeout > 0, wrap context with timeout.
   b. If singleFlight, use doSingleFlight(key, fn). Otherwise call fn directly.
   c. fn: upstream.RoundTrip(req).
   d. On transport error:
      - If staleFallback: search store for best match, return with
        X-Httptape-Stale: true. If no match, propagate error.
      - If !staleFallback: propagate error.
   e. On success:
      - Read response body into buffer.
      - Restore response body with fresh reader.
      - Check cacheFilter(resp). If false, return response without recording.
      - Build Tape from request + response.
      - Apply sanitizer.
      - Store.Save (synchronous). On failure, call onError; still return response.
      - Return response.
```

##### SSE tee flow on miss

When the upstream response has `Content-Type: text/event-stream` and SSE
recording is enabled:

```
1. Request arrives, cache miss (no matching tape).

2. Upstream returns SSE response (200 with text/event-stream).

3. CachingTransport wraps the upstream response body in an
   sseRecordingReader (same as Recorder.roundTripSSE). The wrapper:

   a. Tees upstream bytes to the caller via Read().
   b. Background goroutine parses the tee'd stream into SSEEvents.
   c. Each parsed event is appended to an in-memory slice.

4. Caller reads response.Body, receiving events as they arrive from
   upstream (streaming, not buffered).

5. When upstream closes the stream (EOF) or caller closes body:
   a. The sseRecordingReader's onDone callback fires.
   b. If the stream was cleanly completed (no error):
      - Build Tape with SSEEvents.
      - Apply sanitizer (uses RedactSSEEventData/FakeSSEEventData if
        configured in the pipeline).
      - Store.Save.
   c. If the stream was truncated (client disconnect mid-stream):
      - Discard the partial tape. Do NOT persist incomplete SSE tapes.
      - Call onError with a diagnostic message.

6. On subsequent requests with the same match key:
   a. Cache hit returns the stored SSE tape.
   b. Response body is a piped stream replaying events with instant
      timing (SSETimingInstant, same as Proxy fallback default).
```

Single-flight coordination for SSE misses: the first caller gets the
live tee'd stream. Concurrent callers with the same key WAIT for the
first caller's stream to complete, then get the cached tape (replay).
This is different from non-SSE single-flight (where all waiters get a
cloned response). Rationale: SSE streams are long-lived; cloning a
live stream to multiple waiters would require a fan-out implementation
that is disproportionately complex for v1. The wait-then-replay approach
is simpler and correct.

##### SSE replay on hit

On cache hit for an SSE tape, CachingTransport synthesizes a response
using the same `sseResponseFromTape` pattern as Proxy:

```go
func (ct *CachingTransport) sseResponseFromTape(tape Tape) *http.Response {
    header := tape.Response.Headers.Clone()
    header.Set("Content-Type", "text/event-stream")
    header.Del("Content-Length")

    pr, pw := io.Pipe()
    go func() {
        for _, ev := range tape.Response.SSEEvents {
            if err := writeSSEEvent(pw, ev); err != nil {
                pw.CloseWithError(err)
                return
            }
        }
        pw.Close()
    }()

    return &http.Response{
        StatusCode: tape.Response.StatusCode,
        Header:     header,
        Body:       pr,
    }
}
```

No timing delay on replay -- events are emitted back-to-back (instant).
This matches the RoundTripper contract (callers expect responses quickly).
If callers need timing fidelity, they should use the Server (http.Handler)
which supports SSETimingMode.

##### Stale-fallback implementation (#164)

When `staleFallback` is enabled and an upstream call fails (transport error
or timeout):

```go
func (ct *CachingTransport) serveStaleFallback(req *http.Request) (*http.Response, error) {
    tapes, err := ct.store.List(req.Context(), Filter{Route: ct.route})
    if err != nil {
        return nil, fmt.Errorf("httptape: stale fallback store list: %w", err)
    }

    tape, ok := ct.matcher.Match(req, tapes)
    if !ok {
        return nil, nil // no stale match available
    }

    resp := ct.tapeToResponse(tape)
    resp.Header.Set("X-Httptape-Stale", "true")
    return resp, nil
}
```

The matcher is the SAME matcher used for the cache lookup. The "most recently
cached response" is naturally the result of the matcher since the matcher
picks the best match from the store's current contents. If multiple tapes
match, the matcher's scoring and ordering rules apply (same as normal cache
hit).

The `X-Httptape-Stale: true` header signals to callers that the response
is from a stale cache, not from upstream. This is critical for observability
(VibeWarden dashboard, demo health indicators).

##### Error-handling matrix

| Failure mode | Caller sees | Notes |
|---|---|---|
| Request body exceeds maxBodySize | Upstream response (bypass cache) | No recording |
| Request body read fails | `error` (wrapped) | Cannot proceed |
| Store.List fails on cache lookup | Upstream response (miss path) | onError called |
| Matcher finds no match (cache miss) | Upstream response | Normal miss flow |
| Upstream transport error (miss) | `error` (propagated) OR stale response if staleFallback | See #164 |
| Upstream returns non-2xx (filtered) | Upstream response (not cached) | Response returned, not recorded |
| Upstream response body read fails | Upstream response (partial) | onError called, not cached |
| Sanitizer fails | Upstream response | onError called, tape not saved |
| Store.Save fails (record path) | Upstream response | onError called |
| Single-flight coordination | Cloned response for waiters | Waiters get independent body readers |
| SSE stream truncated (client disconnect) | Partial stream (no tape persisted) | onError called |
| SSE upstream error mid-stream | Error propagated on Read() | Partial tape discarded |
| Store.List fails on stale fallback | `error` (original transport error) | onError called |

##### CachingTransport vs Proxy relationship

CachingTransport is a simpler, single-store primitive:
- One store (not L1+L2)
- Single-flight dedup (Proxy has none)
- No health endpoint
- Pure RoundTripper (no CLI, no reverse proxy)

Proxy (existing) is a richer, CLI-oriented primitive:
- L1 (raw/ephemeral) + L2 (sanitized/persistent) two-tier cache
- Health endpoint surface
- CLI subcommand integration
- No single-flight (acceptable for CLI proxy where request volume is lower)

The CLI proxy subcommand can be refactored to compose CachingTransport
internally. However, the two-tier L1/L2 architecture of Proxy does not
map directly to CachingTransport's single-store model. Two options:

**Option A -- Proxy wraps CachingTransport for the miss path**: Proxy
keeps its L1/L2 split but delegates cache-miss-then-upstream to
CachingTransport (configured with L2 as its store). L1 remains managed
by Proxy directly. This preserves Proxy's L1/L2 semantics while
sharing the upstream+record logic.

**Option B -- Refactor Proxy to use CachingTransport as transport**:
Proxy sets CachingTransport as its `transport` field. CachingTransport
handles L2 cache lookup + upstream + recording. Proxy adds L1 as a
fast pre-check. On Proxy.RoundTrip:
1. Check L1. Hit -> return from L1.
2. Forward to CachingTransport (which checks L2, then upstream).
3. On CachingTransport miss-then-upstream-success, Proxy intercepts
   the response to also save raw to L1.

**Decision**: Option B. CachingTransport is the upstream's transport.
Proxy wraps it, adding L1 as a pre-check layer and raw-save on success.
This cleanly reuses CachingTransport's single-flight, body-buffering,
SSE-tee, and sanitization logic without duplication.

Refactoring steps:
1. In `NewProxy`, construct a CachingTransport internally:
   ```go
   ct := NewCachingTransport(p.transport, p.l2,
       WithCacheMatcher(p.matcher),
       WithCacheSanitizer(p.sanitizer),
       WithCacheRoute(p.route),
       WithCacheOnError(p.onError),
   )
   ```
2. Proxy.RoundTrip becomes:
   a. Capture request body (for L1 matching).
   b. Check L1 cache. Hit -> return from L1 with `X-Httptape-Source: l1-cache`.
   c. Forward to CachingTransport.RoundTrip.
   d. On success, save raw tape to L1 (CachingTransport already saved
      sanitized to L2).
   e. Return response.

This preserves all existing Proxy behavior:
- L1 pre-check (raw, fast)
- L2 handled by CachingTransport
- Sanitization on L2 only
- Health endpoint unchanged
- Fallback: L1 first, L2 (via CachingTransport stale-fallback) second
- All existing CLI flags, stdout/stderr, exit codes unchanged

##### File layout

**New files:**
- `caching_transport.go` -- `CachingTransport` struct, `CachingOption` type,
  all `WithCache*` option functions, `NewCachingTransport`, `RoundTrip`,
  single-flight implementation, SSE tee/replay helpers, stale-fallback logic.
- `caching_transport_test.go` -- unit and integration tests.
- `docs/caching-transport.md` -- user-facing documentation.

**Modified files:**
- `proxy.go` -- refactor to compose CachingTransport internally. Proxy keeps
  its public API unchanged. Internal `RoundTrip` delegates to CachingTransport
  for the L2+upstream path. L1 pre-check remains Proxy-owned.
- `proxy_test.go` -- existing tests must continue to pass. Add tests
  verifying CachingTransport composition.
- `cmd/httptape/main.go` -- no change to `runProxy` flags or behavior. The
  refactor is internal to the `Proxy` type.
- `CLAUDE.md` -- add `caching_transport.go` and `caching_transport_test.go`
  to the package structure table.
- `CHANGELOG.md` -- add v0.13.0 entry for CachingTransport.
- `docs/proxy.md` -- cross-reference CachingTransport as the library primitive.
- `docs/api-reference.md` -- add CachingTransport section.
- `docs/recording.md` -- mention CachingTransport as the replay+record sibling.
- `docs/cli.md` -- note that `proxy` subcommand uses CachingTransport internally.
- `llms.txt`, `llms-full.txt` -- refresh with new surface area.

**Not modified:**
- `recorder.go` -- not deprecated; remains the record-only primitive.
- `server.go` -- `serveSSE` stays as-is; CachingTransport uses the
  `writeSSEEvent` and `sseRecordingReader` helpers from `sse.go` which are
  already shared.
- `store.go`, `store_file.go`, `store_memory.go` -- unchanged.
- `matcher.go` -- unchanged.
- `sanitizer.go` -- unchanged; `Pipeline.Sanitize` is called as-is.
- `tape.go` -- unchanged.

#### Types

```go
// CachingTransport struct (see above for full field list)
type CachingTransport struct { ... }

// CachingOption functional option type
type CachingOption func(*CachingTransport)

// sfCall -- unexported single-flight call tracker
type sfCall struct {
    wg   sync.WaitGroup
    resp *http.Response
    err  error
    body []byte
}
```

#### Functions and methods

```go
// Constructor
func NewCachingTransport(upstream http.RoundTripper, store Store, opts ...CachingOption) *CachingTransport

// http.RoundTripper implementation
func (ct *CachingTransport) RoundTrip(req *http.Request) (*http.Response, error)

// Option functions
func WithCacheMatcher(m Matcher) CachingOption
func WithCacheSanitizer(s Sanitizer) CachingOption
func WithCacheFilter(fn func(*http.Response) bool) CachingOption
func WithCacheSingleFlight(enabled bool) CachingOption
func WithCacheMaxBodySize(n int) CachingOption
func WithCacheRoute(route string) CachingOption
func WithCacheOnError(fn func(error)) CachingOption
func WithCacheSSERecording(enabled bool) CachingOption
func WithCacheUpstreamDownFallback(enabled bool) CachingOption
func WithCacheUpstreamTimeout(d time.Duration) CachingOption

// Internal methods (unexported)
func (ct *CachingTransport) doSingleFlight(key string, fn func() (*http.Response, []byte, error)) (*http.Response, []byte, error)
func (ct *CachingTransport) serveStaleFallback(req *http.Request) (*http.Response, error)
func (ct *CachingTransport) tapeToResponse(tape Tape) *http.Response
func (ct *CachingTransport) sseResponseFromTape(tape Tape) *http.Response
func (ct *CachingTransport) roundTripSSE(req *http.Request, resp *http.Response, reqBody []byte) (*http.Response, error)
func (ct *CachingTransport) onErrorSafe(err error)
```

#### Sequence: cache hit (non-SSE)

```
Client -> CachingTransport.RoundTrip(req)
  1. Read req.Body into buffer, compute bodyHash
  2. Restore req.Body with fresh reader
  3. store.List(ctx, Filter{Route: route})
  4. matcher.Match(req, tapes) -> tape, true
  5. Build *http.Response from tape (headers, body, status)
  6. Return response
Client <- *http.Response
```

#### Sequence: cache miss (non-SSE)

```
Client -> CachingTransport.RoundTrip(req)
  1. Read req.Body into buffer, compute bodyHash
  2. Restore req.Body with fresh reader
  3. store.List(ctx, Filter{Route: route})
  4. matcher.Match(req, tapes) -> _, false
  5. Compute singleFlight key: method|path|bodyHash
  6. [If singleFlight] Enter doSingleFlight(key, fn)
  7. [If upstreamTimeout > 0] Wrap ctx with deadline
  8. upstream.RoundTrip(req) -> resp, nil
  9. Read resp.Body into buffer
  10. Restore resp.Body with fresh reader
  11. cacheFilter(resp) -> true
  12. Build Tape{Request: ..., Response: ...}
  13. sanitizer.Sanitize(tape) -> sanitizedTape
  14. store.Save(ctx, sanitizedTape) -> nil [onError if err]
  15. Return response
Client <- *http.Response
```

#### Sequence: cache miss, SSE tee

```
Client -> CachingTransport.RoundTrip(req)
  1. Read req.Body into buffer, compute bodyHash
  2. Restore req.Body, cache lookup -> miss
  3. upstream.RoundTrip(req) -> resp (Content-Type: text/event-stream)
  4. Wrap resp.Body in sseRecordingReader(resp.Body, startTime, onEvent, onDone)
     - onEvent: append SSEEvent to in-memory list
     - onDone(nil): build Tape with SSEEvents, sanitize, store.Save
     - onDone(err): discard partial tape, call onError
  5. Return response with wrapped body
Client <- *http.Response (streaming)
  Client reads events as they arrive from upstream
  When stream completes -> onDone fires -> tape persisted
```

#### Sequence: cache miss, upstream down, stale fallback

```
Client -> CachingTransport.RoundTrip(req)
  1. Read req.Body, compute bodyHash, cache lookup -> miss
  2. upstream.RoundTrip(req) -> nil, error (connection refused)
  3. staleFallback == true
  4. store.List(ctx, Filter{Route: route})
  5. matcher.Match(req, tapes) -> tape, true
  6. Build *http.Response from tape
  7. Set X-Httptape-Stale: true header
  8. Return response
Client <- *http.Response (stale, with diagnostic header)
```

#### Error cases

See error-handling matrix above.

#### Test strategy

**Unit tests (caching_transport_test.go):**

1. **Cache hit -- non-SSE**: pre-populated MemoryStore, request matches ->
   returns cached response, upstream NOT called.
2. **Cache hit -- SSE**: pre-populated store with SSE tape, request matches ->
   returns piped SSE stream, upstream NOT called.
3. **Cache miss -- non-SSE**: empty store, upstream called, response cached,
   second identical request returns cached response.
4. **Cache miss -- SSE tee**: empty store, upstream returns SSE, events tee'd,
   tape persisted after stream completes.
5. **Cache miss -- SSE partial disconnect**: client closes body mid-stream,
   partial tape NOT persisted.
6. **Upstream error propagation**: upstream returns error, no stale fallback ->
   error returned to caller.
7. **Stale fallback -- hit**: upstream error + staleFallback=true +
   pre-populated store -> stale response with X-Httptape-Stale: true.
8. **Stale fallback -- miss**: upstream error + staleFallback=true + empty
   store -> error propagated.
9. **Cache filter rejects**: upstream returns 500, default filter (2xx only)
   -> response returned but not cached.
10. **Sanitization applies**: configure pipeline with RedactHeaders, verify
    stored tape has redacted headers, original response is unmodified.
11. **Single-flight dedup**: two goroutines send identical request
    concurrently on cache miss -> upstream called exactly once.
12. **MaxBodySize bypass**: request with body > limit -> forwarded without
    cache interaction.
13. **Store.Save failure**: mock store fails on Save -> response still
    returned, onError called.
14. **Upstream timeout**: WithCacheUpstreamTimeout set, upstream delays ->
    context deadline exceeded, stale fallback attempted.
15. **Route filter**: WithCacheRoute("api") set -> only tapes with matching
    route are considered.
16. **Concurrent safety**: `go test -race` with multiple goroutines calling
    RoundTrip with mixed hits/misses.

**Test patterns:**
- MemoryStore for all store interactions.
- `httptest.NewServer` for upstream simulation.
- `RoundTripFunc` adapter for transport injection.
- Table-driven tests where practical.
- `testing.T.Parallel()` for independent tests.

**Integration tests (in caching_transport_test.go or integration_test.go):**
- End-to-end with `http.Client{Transport: ct}` -> httptest upstream ->
  MemoryStore. Verify: first request hits upstream, second request serves
  from cache, third request after upstream goes down serves stale (if
  configured).

#### Alternatives considered

1. **Custom Caddy module for VibeWarden**: rejected because it duplicates
   httptape's core logic inside a specific framework. CachingTransport is
   framework-agnostic -- VibeWarden wraps it in Caddy, others wrap it in
   stdlib, Gin, Echo, etc.

2. **RFC 7234 compliant cache**: rejected because POST requests are
   uncacheable in RFC 7234. LLM APIs use POST for completions -- the
   primary use case. httptape is a tape-match layer, not a standards-compliant
   HTTP cache.

3. **Passthrough Recorder with retroactive match check**: rejected because it
   always forwards to upstream (defeating the cache-hit zero-cost property).
   The whole point is to NOT call upstream when a matching tape exists.

4. **Extend existing Proxy with single-flight and single-store mode**: rejected
   because Proxy's L1/L2 architecture, health endpoint, and CLI integration
   add complexity that library embedders don't need. CachingTransport is the
   minimal primitive; Proxy composes it.

5. **External `golang.org/x/sync/singleflight`**: rejected per stdlib-only
   constraint. The manual implementation is ~30 lines and well-understood.

#### Consequences

**What becomes possible:**
- VibeWarden single-binary egress proxy with httptape embedded. No sidecar.
- Zero-cost LLM demo hosting: record 50 responses, replay infinitely.
- Any Go developer can drop CachingTransport into `http.Client{Transport: ct}`
  for transparent caching with sanitization.
- Proxy CLI subcommand shares the same cache-through logic via composition.

**What changes:**
- Proxy's internal RoundTrip is refactored to delegate to CachingTransport for
  the L2+upstream path. External behavior is unchanged (same flags, same
  stdout/stderr, same exit codes).
- New file `caching_transport.go` added to the package.

**What breaks:**
- Nothing. Purely additive. All existing types, constructors, options, and CLI
  commands remain unchanged. Proxy's internal refactor preserves all observable
  behavior.

**Future implications:**
- Admin endpoints (#194) can be added to CachingTransport later via a
  `WithCacheAdminHandler()` option that exposes cache stats and purge
  endpoints. The design leaves room for this.
- TTL/eviction can be added via a `WithCacheTTL(time.Duration)` option that
  filters out expired tapes during cache lookup. The store interface remains
  unchanged -- TTL is implemented as a client-side filter on `List` results.
- The Proxy refactor demonstrates that CachingTransport is composable as a
  building block, not a monolith.

---

### ADR-45: Proxy composes CachingTransport internally (Option B completion)

Removed in #246 cleanup — did not meet the tightened ADR bar (see issue for criteria).

---

### ADR-46: Panic discipline in option and factory functions

Removed in #246 cleanup — did not meet the tightened ADR bar (see issue for criteria).

---

## ADR-47: Single-flight key fully identifies a matchable request
**Date**: 2026-06-05
**Issue**: #285
**Status**: Accepted

### Context

`CachingTransport.RoundTrip` builds its single-flight deduplication key
(`caching_transport.go:339`) as:

```go
sfKey := req.Method + "|" + req.URL.Path + "|" + bodyHash
```

This key omits `req.URL.RawQuery` and all request-header dimensions. Two concurrent
cache-miss requests sharing method, path, and body but differing in query string or
headers collapse into a single upstream call; the waiter receives the leader's
response — the wrong one. The reproduced bug is `/data?page=1` vs `/data?page=2`
(query string); a parallel exposure exists for any matcher that inspects headers
(`HeadersCriterion`, `ContentNegotiationCriterion`).

The locked correctness principle: **the single-flight key must fully identify a
matchable request.** A waiter must never receive a response that the configured
`Matcher` would not have matched to its own request. Correctness (never serve the
wrong response) beats collapse-efficiency.

The transport does not know which dimensions the configured `Matcher` actually
inspects — the `Matcher` interface (`Match(req, candidates) (Tape, bool)`) exposes
no introspection, and the PM spec explicitly forbids changing it. We must therefore
choose a key that is safe for *any* matcher the embedder could plug in.

### Decision

Construct the single-flight key from **every request dimension a `Matcher` could
possibly distinguish**, derived only from `*http.Request` fields and stdlib
primitives. Concretely the key folds in: method, full path, raw query string, body
hash (already computed), and a canonical hash of all request headers except a fixed
hop-by-hop / volatile denylist.

**Why hash-all-headers (option a) over an optional matcher-key interface (option b)
or a "provably-safe" conservative key (option c):**

- Option (b) requires either a new interface method or a type assertion against a
  concrete matcher, plus a per-`Criterion` "which dimensions do I read" contract.
  Built-in criteria read arbitrary fields (`ContentNegotiationCriterion` reads
  `Accept`; `HeadersCriterion` reads one configurable key; `CriterionFunc` reads
  *anything*). There is no sound, complete way to enumerate the inspected dimensions
  without either changing the `Criterion`/`Matcher` contract (out of scope) or
  guessing (unsafe for custom criteria). Rejected.
- Option (c) "only collapse on a provably-safe key" degenerates to exactly option
  (a): the only key provably safe against an unknown matcher is one that captures
  every dimension the matcher *could* read.
- Option (a) is correct against any matcher — including `CriterionFunc` and future
  criteria — with zero interface change and stdlib only. Its sole cost is reduced
  collapsing when two genuinely-equivalent requests differ in an *incidental* header
  (e.g. a per-request trace ID). That cost is exactly the trade the locked principle
  prescribes: never serve a waiter the wrong response, even at the price of an extra
  upstream call. The duplicate call is harmless — both responses match the request,
  and the cache filter / store de-dupes the persisted tape on the next lookup.

The header denylist excludes headers that are hop-by-hop or per-connection volatile
and that no built-in matcher inspects, so that incidental transport noise does not
needlessly defeat collapsing. Everything else is included (conservative-by-default:
unknown headers are kept in the key).

#### Types

No new exported types. No change to `Matcher`, `Criterion`, `CompositeMatcher`, or
any option type. No change to `CachingTransport`'s struct fields or public API.

#### Functions and methods

One new unexported helper in `caching_transport.go`:

```go
// singleFlightKey derives a single-flight deduplication key that fully
// identifies a matchable request. It folds in method, path, raw query, the
// request body hash, and a canonical hash of all request headers except a
// fixed hop-by-hop / volatile denylist. The key is safe for any configured
// Matcher: two requests sharing a key are indistinguishable to every
// dimension a Matcher could read, so a single-flight waiter never receives a
// response that would not match its own request.
func singleFlightKey(req *http.Request, bodyHash string) string
```

Header canonicalization inside `singleFlightKey`:

1. Collect header names from `req.Header`, skipping any in `sfHeaderDenylist`
   (compared via `http.CanonicalHeaderKey`).
2. Sort the surviving canonical names lexicographically (`sort.Strings`) for a
   stable, order-independent serialization.
3. For each name, sort its value slice (a copy — do not mutate `req.Header`) so
   multi-valued headers hash deterministically regardless of send order.
4. Serialize as `name "\x00" v1 "\x01" v2 ... "\n"` per header into a
   `strings.Builder` / `bytes.Buffer`, write the bytes to a `sha256` hash, and
   hex-encode the digest. (`\x00`/`\x01`/`\n` are control bytes that cannot appear
   in header names or values, so the serialization is unambiguous.)
5. Final key: `req.Method + "|" + req.URL.Path + "|" + req.URL.RawQuery + "|" +
   bodyHash + "|" + headerDigestHex`.

Supporting package-level value:

```go
// sfHeaderDenylist names request headers excluded from the single-flight key.
// These are hop-by-hop / per-connection / volatile and are not inspected by any
// built-in Matcher; including them would needlessly defeat single-flight
// collapsing for genuinely-equivalent requests. Keys are stored canonicalized.
var sfHeaderDenylist = map[string]struct{}{ ... }
```

Denylist contents (canonical form): `Connection`, `Proxy-Connection`,
`Keep-Alive`, `Transfer-Encoding`, `Te`, `Trailer`, `Upgrade`, `Content-Length`,
`Host` (carried separately on `req.Host`, not a matcher dimension), and the
single-flight-coordination header set httptape itself adds is not present on the
request path so needs no entry. Note: `Host` is intentionally excluded because no
built-in criterion matches on it and it is redundant with path/route; if a future
criterion matches on Host, revisit. Authorization/Cookie/Accept and all other
content-negotiation or app headers are **kept** (they are matcher-relevant).

#### File layout

Modified files only — no new files:

- `caching_transport.go`
  - Replace the inline `sfKey` construction at line 339 with
    `sfKey := singleFlightKey(req, bodyHash)`.
  - Add the `singleFlightKey` helper and `sfHeaderDenylist` var (near the
    single-flight machinery, e.g. just below `roundTripWithSingleFlight` or above
    `RoundTrip`).
  - Add imports `crypto/sha256`, `encoding/hex`, `sort` (all stdlib). `strings` or
    `bytes` already available via existing imports — reuse `bytes.Buffer`.
  - Update the `WithCacheSingleFlight` godoc (and/or a comment at the construction
    site) to state the key is derived from method, path, raw query, body hash, and a
    canonical hash of all non-denylisted request headers (AC-7).
- `caching_transport_test.go`
  - Add the two regression tests below (AC-4, AC-5).

`roundTripWithSingleFlight` itself is **unchanged**: it already accepts `sfKey` as a
parameter and its inflight-map logic is key-agnostic. The only change is *what key*
`RoundTrip` passes in.

#### Sequence

1. `RoundTrip` buffers the request body, computes `bodyHash`, restores `req.Body`
   (unchanged from today).
2. Cache lookup runs as today (single-flight is a miss-path concern only).
3. On miss, `RoundTrip` calls `sfKey := singleFlightKey(req, bodyHash)`.
4. `singleFlightKey` reads `req.Method`, `req.URL.Path`, `req.URL.RawQuery`, the
   already-computed `bodyHash`, and `req.Header` (read-only). It produces the
   composite key. It does **not** read `req.Body` (body is fully represented by
   `bodyHash`).
5. `RoundTrip` passes `sfKey` to `roundTripWithSingleFlight` (or
   `roundTripUpstream` when single-flight is disabled — that path ignores the key).
6. Inflight coordination, leader/waiter handoff, SSE re-query, and stale fallback
   are all unchanged.

#### Error cases

- `singleFlightKey` cannot fail — it performs no I/O and returns a string. No new
  error path.
- `req.Header == nil`: the header digest is the hash of the empty serialization
  (stable, non-empty hex string). Safe.
- `req.URL.RawQuery == ""`: contributes an empty segment between two `|`
  delimiters; the delimiter framing keeps `Path="/a", Query="b=1"` distinct from
  `Path="/a", Query=""` with a tape that happens to have `b` in path — the `|`
  separators are unambiguous because `RawQuery` is a distinct URL field.
- Header value containing a `|`: irrelevant — header values are hashed into a single
  hex segment, never concatenated raw into the `|`-delimited key, so no delimiter
  injection is possible.

#### Test strategy

Table-light, goroutine-based, mirroring the existing
`TestCachingTransport_SingleFlightDedup` pattern (barrier via `sync.WaitGroup`,
`atomic.Int64` upstream call counter, `roundTripperFunc` stub, `NewMemoryStore`).

- **AC-4 — `TestCachingTransport_SingleFlightDistinguishesQueryString`**: configure
  `WithCacheMatcher(NewCompositeMatcher(MethodCriterion{}, PathCriterion{},
  QueryParamsCriterion{}))`. Stub upstream echoes the request's `page` query param
  into the response body and increments the counter (with a small `time.Sleep` so
  the two goroutines overlap in the inflight window). Two goroutines fire
  `/data?page=1` and `/data?page=2` released from a barrier. Assert upstream called
  **exactly 2**, and the caller for `page=1` got the `page=1` body and likewise for
  `page=2`. To force the inflight overlap deterministically, gate the upstream stub
  on a channel/`WaitGroup` so the second request provably enters
  `roundTripWithSingleFlight` while the first is still in flight (otherwise the
  second could legitimately become a cache *hit* — to keep it a concurrent *miss*,
  the upstream stub should block until both goroutines have arrived). Simplest
  deterministic shape: an upstream that blocks on a shared `sync.WaitGroup` until
  `callCount == 2`, then unblocks both.

- **AC-5 — `TestCachingTransport_SingleFlightCollapsesIdenticalHeaders`**: same
  matcher; both goroutines send the identical URL `/data?page=1` and identical
  headers. Stub upstream sleeps ~50ms to widen the inflight window (reuse the
  existing `SingleFlightDedup` timing approach). Assert upstream called **exactly
  1**, both callers received the same body. This proves the richer key does not
  break collapsing for genuinely-identical requests (AC-3 + AC-5).

- **AC-2 header coverage**: add a third assertion (or a sibling test
  `TestCachingTransport_SingleFlightDistinguishesHeaders`) using
  `HeadersCriterion{Key: "Accept", Value: ...}` or `ContentNegotiationCriterion{}`,
  with two goroutines differing only in `Accept` (`application/json` vs `text/csv`),
  asserting 2 upstream calls and per-Accept bodies. Use the same barrier shape as
  AC-4.

- **AC-8 (no regressions)**: existing `TestCachingTransport_SingleFlightDedup`,
  `SingleFlightDisabled`, `DefaultMatcherDedups`, `ConcurrentSafety`,
  `SingleFlightSSEWaiters`, and `SingleFlightStaleFallbackForWaiters` must pass
  unchanged. The default matcher (method+path+body) test sends identical requests, so
  the richer key still collapses them — verify `DefaultMatcherDedups` still asserts 1
  upstream call.

- A small unit test `TestSingleFlightKey` (table-driven) asserting: distinct keys for
  differing query / Accept / body; identical keys for header-order and value-order
  permutations; identical keys when only a denylisted header (e.g. `Connection`)
  differs.

### Consequences

- **Correctness is guaranteed for any matcher**, including custom `CriterionFunc`
  and `MatcherFunc` implementations, without touching the `Matcher`/`Criterion`
  interfaces. This is the property the locked principle demands.
- **Collapse efficiency drops only for incidental header differences.** Two requests
  that are equivalent to the matcher but differ in a non-denylisted header (e.g. a
  per-request `X-Request-Id`) will each make an upstream call. This is the accepted
  trade. Embedders who want maximal collapsing should strip volatile headers before
  the transport, or we extend `sfHeaderDenylist` in a follow-up if a concrete header
  proves common. The denylist is the single tuning knob if this becomes a problem.
- **No public API change, no new dependency** (stdlib `crypto/sha256`,
  `encoding/hex`, `sort`, `bytes`). Satisfies AC-6.
- **Future option, not taken now**: if collapse efficiency for header-heavy clients
  becomes a real complaint, a follow-up could add an *optional* interface (e.g.
  `SingleFlightKeyer`) that a matcher may implement to narrow the key — defaulting to
  this safe key. Deferred; not needed for correctness.

## PM Log

### 2026-06-05

**Issue created/updated:** #285 — fix(caching): single-flight key omits query/headers; concurrent waiters get wrong response

**Action:** Wrote implementation-ready spec and posted as a comment on issue #285. Applied label `status:ready-for-arch`. Created the `status:ready-for-arch` label (did not previously exist in repo).

**Spec summary:**
- Root cause pinned to `caching_transport.go:339` — key excludes `req.URL.RawQuery` and header dimensions.
- 8 acceptance criteria covering: correctness for query-string and header differences, single-flight collapsing preserved for genuinely-identical requests, regression tests for both cases, stdlib-only constraint, godoc update, and no regressions in existing tests.
- Scope is tightly bounded to `caching_transport.go` and `caching_transport_test.go`.
- Key design question delegated to architect: whether to extend the key with raw headers or adopt a smarter mechanism — must satisfy AC-2 (header differences produce independent upstream calls) while remaining stdlib-only.

**Open questions:** None.

### 2026-06-05 (3)

**Issue created/updated:** #280 — security(cli): record/proxy fail open without --config (no raw recording mode is violated)

**Action:** Wrote implementation-ready spec and posted as a comment on issue #280. Applied labels `status:ready-for-arch` and `milestone:5-production-readiness`.

**Spec summary:**
- Recommends Option A: auto-apply safe default pipeline (`RedactHeaders()` + `RedactQueryParams()`) when no `--config` is supplied, plus a prominent stderr warning naming every redacted header and query param, plus an explicit opt-out flag `--unsafe-raw` that uses the no-op pipeline and prints a louder `UNSAFE` warning.
- `--config` still replaces the default pipeline exactly as today; no stacking.
- `--unsafe-raw` + `--config` together is a usage error (exit code 1).
- Both `record` and `proxy` subcommands treated symmetrically.
- No changes to the library's own `NewRecorder`/`NewProxy` defaults; fix is CLI-only.
- 3 open questions delegated to architect: warning output mechanism for testability (logger vs separate writer), flag-interaction check ordering, and `--config` flag description text update.
- No ADR required (CLI behavior correction, not a library architecture decision).

**Open questions:** See spec comment on #280 — 3 architect-facing questions about logger testability, flag-check ordering, and flag-description text.

### 2026-06-05 (2)

**Issue created/updated:** #279 — security(sanitizer): query-string & userinfo secrets are never sanitized

**Action:** Wrote implementation-ready spec and posted as a comment on issue #279. Applied label `status:ready-for-arch` and posted `Status: READY_FOR_ARCH` comment.

**Spec summary:**
- 14 acceptance criteria covering: `RedactQueryParams`, `FakeQueryParams`, `DefaultSensitiveQueryParams`, userinfo unconditional redaction, config-file wiring (`redact_query` / `fake_query` actions), determinism guarantee, silent-skip on malformed URLs, param-name case-sensitivity (case-sensitive, with rationale), key-order caveat after `url.Values.Encode`, godoc requirements, and test coverage floor.
- Key decisions made in spec: case-sensitive matching (safer than silent case-folding); userinfo always redacted by both functions (no opt-out); `fake_query` requires explicit `params` (empty params = error, unlike `redact_query` which has a useful default set); `FakeQueryParamsWith` deferred (no identified use case).
- Scope bounded to `sanitizer.go` and `config.go` only. No new dependencies.

**Open questions:** None.

## ADR-48: Query-string and userinfo sanitization in the sanitize-on-write pipeline
**Date**: 2026-06-05
**Issue**: #279
**Status**: Accepted

### Context

`RecordedReq.URL` is a raw string marshaled verbatim to disk (`tape.go:63`). No
existing `SanitizeFunc` inspects it. Query-parameter secrets (`?api_key=`,
`?access_token=`, presigned-URL `?sig=`/`?X-Amz-Signature=`) and userinfo
credentials (`https://user:pass@host`) therefore land in fixture JSON in cleartext.
This breaches httptape's core security guarantee — fixtures are safe to commit —
which CLAUDE.md names the project's "most original idea" and instructs us to treat
as a security boundary. The header and body sanitizers (`RedactHeaders`,
`RedactBodyPaths`, `FakeFields`, `FakeFieldsWith`) already close the header and
body surfaces; the URL surface is the remaining gap.

This ADR is warranted (it extends the sanitization security boundary) per the
tightened ADR bar; it adds two new public `SanitizeFunc` constructors, a default
list helper, unconditional userinfo redaction, and two config actions.

### Decision

Add URL sanitization to `sanitizer.go` mirroring the exact shape of the existing
header/body sanitizers, and wire two new config actions in `config.go`. All work is
stdlib-only: `net/url` (already imported by `matcher.go`) is the only new import in
`sanitizer.go`. The faker determinism path reuses the existing unexported
`computeHMAC` and `fakeString` helpers in `faker.go`/`sanitizer.go` — confirmed
present (`sanitizer.go:512` `computeHMAC`, `sanitizer.go:579` `fakeString`) and
in-package reachable.

#### Approach decision — re-encode only when a value actually changed (option b)

The PM spec's key trade-off: `url.Values.Encode()` sorts keys alphabetically, so
round-tripping any URL through parse→Encode→assign mutates param order even when no
sensitive param was touched. We choose **option (b): only rewrite `t.Request.URL`
when a redaction/fake actually occurred, or when userinfo was present**. Rationale:

1. **Minimize mutation of data that doesn't need changing.** A `SanitizeFunc` that
   gratuitously reorders every URL it sees produces noisier fixture diffs and
   surprises users who inspect committed fixtures. Byte-stability on the no-match
   path is a desirable property.
2. **It is safe for matching.** `QueryParamsCriterion.Score` (`matcher.go:224`)
   parses the tape URL with `url.Parse(...).Query()` and compares by key/value map —
   fully order-independent. `PathCriterion`/`PathPatternCriterion` only inspect the
   path, also unaffected. So re-encoding (and its key sort) never changes matching;
   the choice between (a) and (b) is purely about whether we mutate untouched URLs.
3. **Implementation is cheap.** We track a `changed` boolean across query mutation
   and userinfo stripping. If `changed` is false, return the tape with `URL`
   untouched (byte-identical). If true, set `u.RawQuery = values.Encode()` and
   `t.Request.URL = u.String()`.

Note the asymmetry: userinfo presence forces `changed = true` even when no query
param matched (AC-7 requires unconditional userinfo redaction). So a URL like
`https://user:pass@host/path?keep=1` with `RedactQueryParams("secret")` IS rewritten
(userinfo stripped), and as a side effect its query may be re-sorted — acceptable
and matching-safe. A URL with no userinfo and no matching param is left byte-identical.

#### Types

No new exported types. Internal:

- A package-level `var defaultSensitiveQueryParams []string` in `sanitizer.go`,
  alongside the existing `defaultSensitiveHeaders`, containing:
  `api_key`, `access_token`, `token`, `secret`, `password`, `sig`, `signature`,
  `X-Amz-Signature`, `X-Goog-Signature`.

#### Functions and methods

New exported symbols in `sanitizer.go`:

```go
// DefaultSensitiveQueryParams returns a fresh copy of the default sensitive
// query-parameter name list on each call (mirrors DefaultSensitiveHeaders).
func DefaultSensitiveQueryParams() []string

// RedactQueryParams returns a SanitizeFunc that replaces matching query-param
// values in t.Request.URL with Redacted ("[REDACTED]") and unconditionally
// strips userinfo. Case-sensitive name matching. No names => defaults.
func RedactQueryParams(names ...string) SanitizeFunc

// FakeQueryParams returns a SanitizeFunc that replaces matching query-param
// values with fakeString(computeHMAC(seed, value)) and unconditionally strips
// userinfo. Case-sensitive name matching.
func FakeQueryParams(seed string, names ...string) SanitizeFunc
```

Internal helper in `sanitizer.go` (shared by both constructors to avoid
duplication):

```go
// sanitizeRequestURL parses rawURL, applies replace() to each value of each
// param whose name is in names, strips userinfo, and re-encodes only if a
// change occurred. On url.Parse error it returns rawURL unchanged.
// replace receives (name, value) and returns the replacement value.
func sanitizeURL(rawURL string, names map[string]struct{}, replace func(value string) string) string
```

`RedactQueryParams` builds `names` set (case-sensitive, raw strings — NOT
canonicalized, unlike headers) and passes `replace = func(string) string { return Redacted }`.
`FakeQueryParams` passes `replace = func(v string) string { return fakeString(computeHMAC(seed, v)) }`.

Each returned `SanitizeFunc` assigns `t.Request.URL = sanitizeURL(...)` and returns
`t` by value (the existing copy-on-modify convention; `Tape` is a value, and we
only reassign a scalar string field, so no deep copy of headers/body is needed —
they are left as-is, satisfying AC-11's "byte-identical" requirement for
headers/body/response).

#### File layout

- `sanitizer.go` — modified: add `defaultSensitiveQueryParams` var,
  `DefaultSensitiveQueryParams`, `RedactQueryParams`, `FakeQueryParams`,
  `sanitizeURL` helper; add `"net/url"` to imports.
- `config.go` — modified: add `ActionRedactQuery` / `ActionFakeQuery` constants,
  register them in `validActions`, add a `Params []string` field to `Rule`,
  add validation cases, add `BuildPipeline` cases.
- `sanitizer_test.go` — modified/new: query-param + userinfo test matrix.
- `config_test.go` — modified/new: config round-trip tests for both actions.

No new files; no new packages; single flat package preserved.

#### Config wiring

- Constants: `ActionRedactQuery = "redact_query"`, `ActionFakeQuery = "fake_query"`.
  Add both to `validActions`.
- `Rule` gains `Params []string `json:"params,omitempty"``. New field name `Params`
  avoids collision with existing `Headers` (header action) and `Paths` (body
  JSONPath action); semantically distinct (URL query param names). `DisallowUnknownFields`
  on the decoder now accepts `params` because the struct field exists.
- `Validate()` new cases:
  - `ActionRedactQuery`: `Params` optional (empty => defaults, matching
    `RedactQueryParams()`); reject `paths`, `seed`, `fields`, `headers`.
  - `ActionFakeQuery`: require non-empty `seed` AND non-empty `params` (empty params
    is a no-op misconfiguration — error per AC-9); reject `paths`, `fields`, `headers`.
  Follow the existing error-accumulation style (`errs = append(...)`, "X does not use Y").
- `BuildPipeline()` new cases:
  - `ActionRedactQuery` => `RedactQueryParams(rule.Params...)`.
  - `ActionFakeQuery` => `FakeQueryParams(rule.Seed, rule.Params...)`.

#### Sequence (per SanitizeFunc invocation, applied by Pipeline.Sanitize on write)

1. `Pipeline.Sanitize(t)` calls the URL `SanitizeFunc` (already fires before
   `Store.Save` — AC-10, no pipeline change).
2. `sanitizeURL(t.Request.URL, names, replace)`:
   a. `u, err := url.Parse(rawURL)`; if `err != nil` return `rawURL` unchanged
      (silent skip, AC-2/AC-5).
   b. `changed := false`.
   c. If `u.User != nil`: set `u.User = url.User(Redacted)` (username `[REDACTED]`,
      no password => password cleared, AC-7); `changed = true`.
   d. `values := u.Query()`; for each `name` in `values` that is in the `names` set
      (raw, case-sensitive string equality), replace every element of
      `values[name]` via `replace(v)`; if any replacement happened, `changed = true`.
   e. If `changed`: `u.RawQuery = values.Encode()`; return `u.String()`.
      Else: return `rawURL` (byte-identical, AC-12).
3. The `SanitizeFunc` assigns the result to `t.Request.URL` and returns `t`.

#### Error cases

- Malformed URL (`url.Parse` error): leave URL unchanged, return no error
  (silent skip — consistent with non-JSON body handling, ADR-17). AC-2/AC-5.
- No matching params and no userinfo: URL returned byte-identical. AC-12.
- Empty `names` for `RedactQueryParams`: substitute `DefaultSensitiveQueryParams()`.
- `FakeQueryParams` with empty `names`: at the library level this is a no-op (no
  param matches) — not an error; the *config* layer rejects empty `params` for
  `fake_query` (AC-9) because that is the misconfiguration-prone surface.
- Userinfo with username only (`https://user@host`): still stripped to
  `https://[REDACTED]@host`; `changed = true`.

#### Test strategy (table-driven, stdlib `testing` only, behavior-named)

In `sanitizer_test.go`:
- Redaction replaces matching param values, preserves non-matching ones (AC-2, AC-11).
- Case-sensitive matching: `api_key` does not redact `API_KEY` (AC-3).
- Default param set used when called with no names; covers each default name (AC-4).
- `DefaultSensitiveQueryParams` returns a fresh copy (mutating result doesn't
  affect a second call).
- Multiple values for one param name each redacted (AC-2).
- `FakeQueryParams` determinism: same seed+value => byte-identical URL across two
  calls; different seed => different value (AC-6).
- `FakeQueryParams` value equals `fakeString(computeHMAC(seed, original))`.
- Userinfo unconditional redaction: `https://user:pass@host/path?other=value` =>
  `https://[REDACTED]@host/path?other=value`, non-matching param untouched (AC-7).
- Userinfo stripped even on no-query-match path; username-only userinfo handled.
- Byte-stability: no-match, no-userinfo URL is byte-identical; assert query
  key/value equivalence via `url.ParseQuery`, NOT byte-identical param order (AC-12).
- Headers/body/response byte-identical after URL sanitization (AC-11).
- Malformed URL left unchanged (silent skip).
- On-write boundary: record secret query param, run through `Pipeline` containing
  `RedactQueryParams`, assert stored `Request.URL` lacks the secret (AC-10).

In `config_test.go`:
- `redact_query` JSON with explicit `params` builds a pipeline that redacts them (AC-8).
- `redact_query` with empty params validates and uses defaults.
- `redact_query` rejects `paths`/`seed`/`fields`.
- `fake_query` JSON round-trip => pipeline => redacted fixture (AC-9).
- `fake_query` missing `seed` => validation error; empty `params` => validation error.
- `LoadConfig` accepts `params` field (DisallowUnknownFields path).

### Consequences

- The URL surface of the sanitize-on-write boundary is now closed for query
  params and userinfo; combined with header/body sanitizers, the documented
  "fixtures safe to commit" guarantee holds for the common credential-in-URL cases.
- Option (b) keeps untouched URLs byte-stable, minimizing fixture diff noise; the
  only re-encoding (and possible key re-sort) happens when a value was actually
  changed or userinfo was present — all matching-safe.
- Case-sensitive param matching (vs case-insensitive headers) is a deliberate
  divergence: query keys are case-sensitive per RFC 3986, and silently redacting
  the wrong key via case-folding is worse than missing one. Documented in godoc.
- Userinfo has no opt-out by design: both URL sanitizers strip it unconditionally,
  so a caller cannot accidentally leak `user:pass@`. No standalone `RedactUserinfo`
  is introduced (avoids an opt-in footgun).
- `FakeQueryParamsWith` (per-param Faker map) is deferred — no identified use case;
  can be added later without breaking this design.
- New `Rule.Params` field is additive and backward-compatible; existing configs
  without it are unaffected.

---

## ADR-49: CLI `record`/`proxy` fail closed by default (safe sanitization + `--unsafe-raw` opt-out)

**Date**: 2026-06-05
**Issue**: #280
**Status**: Accepted

#### Context

The library's `NewRecorder` / `NewProxy` default to a no-op `NewPipeline()` (no
sanitizers). The CLI only attached a sanitizer when `--config` was supplied, so
`record` and `proxy` invoked without `--config` wrote Authorization headers,
cookies, query-param secrets, and URL userinfo to disk verbatim. This contradicts
the project's headline guarantee ("sanitize-on-write; there is no raw recording
mode") for the audience most likely to omit a config: non-Go users running the
Docker image or standalone binary.

This is a durable change to the CLI's default security posture (raw → sanitized),
so it is recorded as an ADR even though the underlying library defaults are
unchanged.

#### Decision

The **CLI** (not the library) fails closed. The library's no-op default is
deliberately left intact (out of scope per the spec; `NewRecorder`/`NewProxy`
remain unopinionated). The CLI selects one of three sanitizer modes for `record`
and `proxy`:

1. **`--config <path>`** — load the config-derived pipeline exactly as today. No
   default prepended/appended. No safe-default warning.
2. **`--unsafe-raw`** — apply no sanitizer (library no-op default). Print a loud
   `UNSAFE` warning.
3. **Neither flag (default)** — apply a built-in safe pipeline
   `NewPipeline(RedactHeaders(), RedactQueryParams())` and print a safe-default
   warning naming the redacted categories.

`--config` together with `--unsafe-raw` is a usage error (exit 1), detected
**after flag parsing but before any config file read** (the error path must not
touch disk).

Pipeline composition order is irrelevant here: `RedactHeaders` only touches
`Tape.Request/Response.Headers`; `RedactQueryParams` only touches
`Tape.Request.URL`. Disjoint fields, so the two `SanitizeFunc`s commute.

##### Proxy L1/L2 placement

Proxy sanitizes on the L2 (persisted) write path only; L1 is the in-memory hot
cache. `WithProxySanitizer` already wires the sanitizer to the L2 write path
(the `--config` branch uses it today). The safe default is passed through the
**same** `WithProxySanitizer` option, so persisted fixtures are sanitized and no
L1/L2-specific handling is required.

#### Types

None. No new exported types.

#### Functions and methods

One new unexported helper in `cmd/httptape/main.go`:

```go
// defaultCLISanitizer returns the safe default sanitizer pipeline applied by
// record and proxy when neither --config nor --unsafe-raw is supplied. It
// redacts DefaultSensitiveHeaders, DefaultSensitiveQueryParams, and URL userinfo.
func defaultCLISanitizer() *httptape.Pipeline {
    return httptape.NewPipeline(
        httptape.RedactHeaders(),
        httptape.RedactQueryParams(),
    )
}
```

Two new string constants (or `const`-block strings) in `cmd/httptape/main.go`
for the warning bodies, so tests can reference a stable substring:

```go
const (
    safeDefaultWarning = "WARNING: no --config supplied; applying safe default sanitization. " +
        "Redacted headers: Authorization, Cookie, Set-Cookie, X-Api-Key, Proxy-Authorization, X-Forwarded-For. " +
        "Redacted query params: api_key, access_token, token, secret, password, sig, signature, X-Amz-Signature, X-Goog-Signature. " +
        "URL userinfo (user:password@) always stripped. " +
        "To customize, use --config. To disable all sanitization (not recommended), use --unsafe-raw."

    unsafeRawWarning = "UNSAFE: --unsafe-raw set; recording raw traffic with NO sanitization. " +
        "Sensitive headers, query params, and URL userinfo WILL be written to disk verbatim. " +
        "Do not commit these fixtures."
)
```

(The header/query-param lists above are hard-coded to mirror
`DefaultSensitiveHeaders()` / `DefaultSensitiveQueryParams()`. The dev should add
a small `_test.go` assertion that these constant strings contain every element of
those two default slices, so the warning text cannot silently drift from the
actual defaults.)

No new library functions. `RedactHeaders`, `RedactQueryParams`, `NewPipeline`,
`WithSanitizer`, `WithProxySanitizer` already exist.

#### File layout

Modified:
- `cmd/httptape/main.go`
  - Add `--unsafe-raw` bool to the `record` flag set (`runRecord`, ~line 399).
  - Add `--unsafe-raw` bool to the `proxy` flag set (`runProxy`, ~line 542).
  - Update `--config` flag description on both to note it replaces the default,
    e.g. record: `"Path to sanitization config JSON (replaces the safe default sanitizer)"`,
    proxy: `"Path to sanitization config JSON applied to L2 writes (replaces the safe default sanitizer)"`.
  - Add `defaultCLISanitizer()` helper and the two warning constants.
  - Replace the `if *configPath != "" { ... }` sanitizer block in `runRecord`
    (~453-460) and `runProxy` (~603-610) with the three-way selection logic below.
- `cmd/httptape/main_test.go`
  - New tests (see Test strategy).

No library files change.

#### Sequence (per command, after `fs.Parse` and the existing
`--upstream`/`--fixtures` required-flag checks)

1. Validate mutual exclusion: `if *configPath != "" && *unsafeRaw { return &usageError{fmt.Errorf("--config and --unsafe-raw are mutually exclusive")} }`. This runs **before** `LoadConfigFile`, so the guaranteed-error path performs no file read.
2. Select sanitizer mode:
   - `*configPath != ""` → `LoadConfigFile` → `cfg.BuildPipeline()` → append `WithSanitizer`/`WithProxySanitizer`. (unchanged today's behavior)
   - `*unsafeRaw` → append no sanitizer option; `logger.Println(unsafeRawWarning)`.
   - else (default) → append `WithSanitizer(defaultCLISanitizer())` (record) / `WithProxySanitizer(defaultCLISanitizer())` (proxy); `logger.Println(safeDefaultWarning)`.
3. The warning `logger.Println(...)` call sits in the same flow as the existing
   pre-listen log lines, i.e. it is emitted **before** `httpServer.ListenAndServe`.
   Place it right where the old config block was (well before the `go func()`
   shutdown watcher and `ListenAndServe`), satisfying "print before the listener
   starts."
4. Remaining flow (recorder/proxy construction, reverse proxy, listen) is unchanged.

#### Error cases

- `--config` + `--unsafe-raw` → `*usageError` → exit 1, message on stderr via
  `logger.Println` (the `run` dispatcher already maps `*usageError` to `exitUsage`
  and logs it). No file read occurs.
- `--config` with an unreadable/invalid file → existing `"load config: %w"`
  runtime error (exit 2), unchanged.
- Default and `--unsafe-raw` paths cannot fail (no I/O in
  `defaultCLISanitizer`); `NewPipeline`/`RedactHeaders`/`RedactQueryParams` do not
  return errors.

#### Test strategy (CLI test harness, `cmd/httptape/main_test.go`)

Reuse the **existing** `captureStderr(t, fn)` helper — it already swaps the
package-level `logger` to an `os.Pipe`, which is the minimal seam the PM asked
for. No new seam, no production wiring change is required: warnings go through
`logger`, and `captureStderr` already redirects `logger`. This is the smallest
seam consistent with the current code shape.

Reuse the existing live-server pattern from `TestProxyHealthEndpointMounted`:
bind `127.0.0.1:0` to grab a free port, start an `httptest.NewServer` upstream,
run `run(args)` in a goroutine, poll until the listener accepts, make the probe
request(s), then stop. To stop the blocking `run`, the dev should send the
process `SIGINT` (the command installs `signal.NotifyContext` on SIGINT/SIGTERM):
`syscall.Kill(syscall.Getpid(), syscall.SIGINT)` after the request, then drain
`done`. (Async recorder: after SIGINT the shutdown goroutine calls
`recorder.Close()` which flushes; wait on `done` before reading fixtures so the
flush has completed.) For proxy, L2 writes are synchronous to the store on the
fallthrough/record path; still wait on `done` for clean shutdown before reading.

Test matrix (each row implemented for **both** `record` and `proxy`):

1. **Default redacts** — no `--config`, no `--unsafe-raw`. Make one upstream
   request carrying `Authorization: Bearer secret` and URL `?token=mytoken`. After
   shutdown, load the written fixture JSON from the fixtures dir and assert the
   stored request's `Authorization` header == `[REDACTED]` and the `token` query
   param value == `[REDACTED]`. (Closes the on-write boundary at the CLI level.)
2. **Default warning to stderr** — wrap the run in `captureStderr`; assert the
   returned string contains `safeDefaultWarning` (or a stable substring naming
   Authorization and api_key). Assert it is on stderr, not stdout (the harness
   only captures the `logger` stderr stream, so presence there proves the channel).
3. **`--config` suppresses default warning** — point `--config` at a minimal
   valid config file in `t.TempDir()`; assert the captured stderr does **not**
   contain the `safeDefaultWarning` substring.
4. **`--unsafe-raw` records raw** — assert the stored `Authorization` header
   value is the literal `Bearer secret` (un-redacted) and the captured stderr
   contains `UNSAFE`.
5. **`--unsafe-raw` + `--config` → usage error** — `run(...)` returns `exitUsage`
   (1); follow the `TestTLSListenerFlags_MutualExclusion*` pattern. No server is
   started, so no goroutine/port juggling needed.
6. **`-h` lists `--unsafe-raw`** — extend the existing help-flag tests to assert
   `--unsafe-raw` appears in `record -h` and `proxy -h` output.
7. **Drift guard** — table test asserting `safeDefaultWarning` contains every
   string in `httptape.DefaultSensitiveHeaders()` and
   `httptape.DefaultSensitiveQueryParams()`.

Prefer table-driven tests where the record/proxy rows share structure; a small
helper that runs a command against a live upstream and returns the written
fixtures + captured stderr keeps the two subcommands' tests DRY.

### Consequences

- The CLI now honors the project's "no raw recording mode" guarantee by default;
  raw recording is reachable only via an explicit, loudly-warned `--unsafe-raw`.
- Behavior change for existing zero-config CLI users: fixtures recorded without
  `--config` will now have sensitive headers/query params/userinfo redacted.
  This is the intended correction. Users who relied on raw output must add
  `--unsafe-raw`. README/docs describing config-less `record`/`proxy` as raw must
  be updated (spec acceptance criterion).
- The shipped examples are unaffected: `ts-frontend-first` passes
  `--config /config/sanitize.json` (explicit config branch); the Spring Boot and
  Ktor demos run `serve` only.
- Library defaults are unchanged, preserving the unopinionated embeddable API and
  honoring the spec's out-of-scope boundary.
- The warning text duplicates the default header/param lists as literal strings;
  the drift-guard test prevents silent divergence from the canonical default
  slices without coupling the user-facing copy to runtime list ordering.
