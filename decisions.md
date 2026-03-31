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
  `ExactMatcher()` if not provided via options.
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
//   - matcher is ExactMatcher() (matches by method + URL path)
//   - fallback status is 404 Not Found
//   - fallback body is "httptape: no matching tape found"
//   - no onNoMatch callback
//
// The store must not be nil. If it is, NewServer returns a Server that
// always responds with 500 Internal Server Error and a descriptive body.
func NewServer(store Store, opts ...ServerOption) *Server
```

`Store` is the first required argument (same pattern as `NewRecorder` in ADR-2).
The matcher is an option because a sensible default exists.

##### Functional options

```go
// WithMatcher sets the Matcher used to find tapes for incoming requests.
// If not set, ExactMatcher() is used.
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
