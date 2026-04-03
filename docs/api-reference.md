# API Reference

Quick reference of all exported types, functions, and options in the `httptape` package.

## Core types

### Tape

```go
type Tape struct {
    ID         string       `json:"id"`
    Route      string       `json:"route"`
    RecordedAt time.Time    `json:"recorded_at"`
    Request    RecordedReq  `json:"request"`
    Response   RecordedResp `json:"response"`
}

func NewTape(route string, req RecordedReq, resp RecordedResp) Tape
```

### RecordedReq

```go
type RecordedReq struct {
    Method           string       `json:"method"`
    URL              string       `json:"url"`
    Headers          http.Header  `json:"headers"`
    Body             []byte       `json:"body"`
    BodyHash         string       `json:"body_hash"`
    BodyEncoding     BodyEncoding `json:"body_encoding,omitempty"`
    Truncated        bool         `json:"truncated,omitempty"`
    OriginalBodySize int64        `json:"original_body_size,omitempty"`
}
```

### RecordedResp

```go
type RecordedResp struct {
    StatusCode       int          `json:"status_code"`
    Headers          http.Header  `json:"headers"`
    Body             []byte       `json:"body"`
    BodyEncoding     BodyEncoding `json:"body_encoding,omitempty"`
    Truncated        bool         `json:"truncated,omitempty"`
    OriginalBodySize int64        `json:"original_body_size,omitempty"`
}
```

### BodyEncoding

```go
type BodyEncoding string

const (
    BodyEncodingIdentity BodyEncoding = "identity" // UTF-8 text
    BodyEncodingBase64   BodyEncoding = "base64"   // binary content
)
```

### Utility

```go
func BodyHashFromBytes(b []byte) string // SHA-256 hex hash, empty for nil/empty input
```

---

## Recorder

```go
type Recorder struct { /* unexported */ }

func NewRecorder(store Store, opts ...RecorderOption) *Recorder
func (r *Recorder) RoundTrip(req *http.Request) (*http.Response, error) // implements http.RoundTripper
func (r *Recorder) Close() error
```

### RecorderOption

| Option | Signature | Default |
|--------|-----------|---------|
| WithTransport | `WithTransport(rt http.RoundTripper)` | `http.DefaultTransport` |
| WithRoute | `WithRoute(route string)` | `""` |
| WithSanitizer | `WithSanitizer(s Sanitizer)` | no-op Pipeline |
| WithAsync | `WithAsync(enabled bool)` | `true` |
| WithBufferSize | `WithBufferSize(size int)` | `1024` |
| WithSampling | `WithSampling(rate float64)` | `1.0` |
| WithMaxBodySize | `WithMaxBodySize(n int)` | `0` (no limit) |
| WithSkipRedirects | `WithSkipRedirects(skip bool)` | `false` |
| WithOnError | `WithOnError(fn func(error))` | no-op |

**Details:** [Recording](recording.md)

---

## Server

```go
type Server struct { /* unexported */ }

func NewServer(store Store, opts ...ServerOption) *Server
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) // implements http.Handler
```

### ServerOption

| Option | Signature | Default |
|--------|-----------|---------|
| WithMatcher | `WithMatcher(m Matcher)` | `DefaultMatcher()` |
| WithFallbackStatus | `WithFallbackStatus(code int)` | `404` |
| WithFallbackBody | `WithFallbackBody(body []byte)` | `"httptape: no matching tape found"` |
| WithOnNoMatch | `WithOnNoMatch(fn func(*http.Request))` | nil |

**Details:** [Replay](replay.md)

---

## Sanitization

### Interfaces

```go
type Sanitizer interface {
    Sanitize(Tape) Tape
}

type SanitizeFunc func(Tape) Tape
```

### Pipeline

```go
type Pipeline struct { /* unexported */ }

func NewPipeline(funcs ...SanitizeFunc) *Pipeline
func (p *Pipeline) Sanitize(t Tape) Tape // implements Sanitizer
```

### Built-in sanitize functions

| Function | Signature |
|----------|-----------|
| RedactHeaders | `RedactHeaders(names ...string) SanitizeFunc` |
| RedactBodyPaths | `RedactBodyPaths(paths ...string) SanitizeFunc` |
| FakeFields | `FakeFields(seed string, paths ...string) SanitizeFunc` |

### Constants and helpers

```go
const Redacted = "[REDACTED]"

func DefaultSensitiveHeaders() []string
```

**Details:** [Sanitization](sanitization.md)

---

## Matching

### Interfaces

```go
type Matcher interface {
    Match(req *http.Request, candidates []Tape) (Tape, bool)
}

type MatcherFunc func(req *http.Request, candidates []Tape) (Tape, bool)
func (f MatcherFunc) Match(req *http.Request, candidates []Tape) (Tape, bool)

type MatchCriterion func(req *http.Request, candidate Tape) int
```

### Matchers

```go
func DefaultMatcher() *CompositeMatcher          // MatchMethod + MatchPath
func ExactMatcher() Matcher                       // first method+path match
func NewCompositeMatcher(criteria ...MatchCriterion) *CompositeMatcher
```

### Built-in criteria

| Function | Signature | Score |
|----------|-----------|-------|
| MatchMethod | `MatchMethod() MatchCriterion` | 1 |
| MatchPath | `MatchPath() MatchCriterion` | 2 |
| MatchPathRegex | `MatchPathRegex(pattern string) (MatchCriterion, error)` | 1 |
| MatchRoute | `MatchRoute(route string) MatchCriterion` | 1 |
| MatchHeaders | `MatchHeaders(key, value string) MatchCriterion` | 3 |
| MatchQueryParams | `MatchQueryParams() MatchCriterion` | 4 |
| MatchBodyFuzzy | `MatchBodyFuzzy(paths ...string) MatchCriterion` | 6 |
| MatchBodyHash | `MatchBodyHash() MatchCriterion` | 8 |

**Details:** [Matching](matching.md)

---

## Storage

### Interface

```go
type Store interface {
    Save(ctx context.Context, tape Tape) error
    Load(ctx context.Context, id string) (Tape, error)
    List(ctx context.Context, filter Filter) ([]Tape, error)
    Delete(ctx context.Context, id string) error
}

type Filter struct {
    Route  string
    Method string
}

var ErrNotFound = errors.New("httptape: tape not found")
var ErrInvalidID = errors.New("httptape: invalid tape ID")
```

### MemoryStore

```go
func NewMemoryStore(opts ...MemoryStoreOption) *MemoryStore
```

### FileStore

```go
func NewFileStore(opts ...FileStoreOption) (*FileStore, error)
func WithDirectory(dir string) FileStoreOption  // default: "fixtures"
```

**Details:** [Storage](storage.md)

---

## Import/Export

```go
func ExportBundle(ctx context.Context, s Store, opts ...ExportOption) (io.Reader, error)
func ImportBundle(ctx context.Context, s Store, r io.Reader) error
```

### ExportOption

| Option | Signature |
|--------|-----------|
| WithRoutes | `WithRoutes(routes ...string)` |
| WithMethods | `WithMethods(methods ...string)` |
| WithSince | `WithSince(t time.Time)` |
| WithSanitizerConfig | `WithSanitizerConfig(summary string)` |

### Manifest

```go
type Manifest struct {
    ExportedAt      time.Time `json:"exported_at"`
    FixtureCount    int       `json:"fixture_count"`
    Routes          []string  `json:"routes"`
    SanitizerConfig string    `json:"sanitizer_config,omitempty"`
}
```

**Details:** [Import/Export](import-export.md)

---

## Configuration

```go
type Config struct {
    Version string `json:"version"`
    Rules   []Rule `json:"rules"`
}

type Rule struct {
    Action  string   `json:"action"`
    Headers []string `json:"headers,omitempty"`
    Paths   []string `json:"paths,omitempty"`
    Seed    string   `json:"seed,omitempty"`
}

func LoadConfig(r io.Reader) (*Config, error)
func LoadConfigFile(path string) (*Config, error)
func (c *Config) Validate() error
func (c *Config) BuildPipeline() *Pipeline
```

### Action constants

```go
const (
    ActionRedactHeaders = "redact_headers"
    ActionRedactBody    = "redact_body"
    ActionFake          = "fake"
)
```

**Details:** [Config](config.md)
