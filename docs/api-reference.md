# API Reference

Quick reference of all exported types, functions, and options in the `httptape` package. httptape follows the 3 Rs: **Record, Redact, Replay**.

## Core types

### Tape

```go
type Tape struct {
    ID         string         `json:"id"`
    Route      string         `json:"route"`
    RecordedAt time.Time      `json:"recorded_at"`
    Request    RecordedReq    `json:"request"`
    Response   RecordedResp   `json:"response"`
    Metadata   map[string]any `json:"metadata,omitempty"`
    Exemplar   bool           `json:"exemplar,omitempty"`
}

func NewTape(route string, req RecordedReq, resp RecordedResp) Tape
func ValidateTape(t Tape) error
func ValidateExemplar(t Tape) error
```

### RecordedReq

```go
type RecordedReq struct {
    Method           string      `json:"method"`
    URL              string      `json:"url"`
    URLPattern       string      `json:"url_pattern,omitempty"`
    Headers          http.Header `json:"headers"`
    Body             []byte      `json:"-"`
    BodyHash         string      `json:"body_hash"`
    Truncated        bool        `json:"truncated,omitempty"`
    OriginalBodySize int64       `json:"original_body_size,omitempty"`
}
```

Implements `json.Marshaler` and `json.Unmarshaler`. The `Body` field is serialized
based on the Content-Type header: native JSON for `application/json`, string for `text/*`,
base64 for binary, and `null` for nil/empty.

### RecordedResp

```go
type RecordedResp struct {
    StatusCode       int         `json:"status_code"`
    Headers          http.Header `json:"headers"`
    Body             []byte      `json:"-"`
    Truncated        bool        `json:"truncated,omitempty"`
    OriginalBodySize int64       `json:"original_body_size,omitempty"`
    SSEEvents        []SSEEvent  `json:"sse_events,omitempty"`
}
```

Implements `json.Marshaler` and `json.Unmarshaler` with the same Content-Type-driven
body shape as `RecordedReq`.

### MediaType

```go
type MediaType struct {
    Type    string            // e.g., "application"
    Subtype string            // e.g., "json"
    Suffix  string            // e.g., "json" (from "+json" structured syntax suffix)
    Params  map[string]string // e.g., {"charset": "utf-8"}
    QValue  float64           // quality factor from Accept header (0.0-1.0, default 1.0)
}

func ParseMediaType(s string) (MediaType, error)
func ParseAccept(accept string) []MediaType
func IsJSON(mt MediaType) bool
func IsText(mt MediaType) bool
func IsBinary(mt MediaType) bool
func MatchesMediaRange(accept, contentType MediaType) bool
func Specificity(mt MediaType) int
```

Utilities for Content-Type-driven body encoding and content negotiation matching.

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

Panics if `store` is nil.

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
| WithRecorderTLSConfig | `WithRecorderTLSConfig(cfg *tls.Config)` | nil |

**Details:** [Recording](recording.md)

---

## CachingTransport

```go
type CachingTransport struct { /* unexported */ }

func NewCachingTransport(upstream http.RoundTripper, store Store, opts ...CachingOption) *CachingTransport
func (ct *CachingTransport) RoundTrip(req *http.Request) (*http.Response, error) // implements http.RoundTripper
```

Panics if `upstream` or `store` is nil.

### CachingOption

| Option | Signature | Default |
|--------|-----------|---------|
| WithCacheMatcher | `WithCacheMatcher(m Matcher)` | method + path + body_hash |
| WithCacheSanitizer | `WithCacheSanitizer(s Sanitizer)` | no-op Pipeline |
| WithCacheFilter | `WithCacheFilter(fn func(*http.Response) bool)` | 2xx only |
| WithCacheSingleFlight | `WithCacheSingleFlight(enabled bool)` | `true` |
| WithCacheMaxBodySize | `WithCacheMaxBodySize(n int)` | 10 MiB |
| WithCacheRoute | `WithCacheRoute(route string)` | `""` |
| WithCacheOnError | `WithCacheOnError(fn func(error))` | no-op |
| WithCacheSSERecording | `WithCacheSSERecording(enabled bool)` | `true` |
| WithCacheLookupDisabled | `WithCacheLookupDisabled()` | `false` |
| WithCacheUpstreamDownFallback | `WithCacheUpstreamDownFallback(enabled bool)` | `false` |
| WithCacheUpstreamTimeout | `WithCacheUpstreamTimeout(d time.Duration)` | `0` (no timeout) |

**Details:** [CachingTransport](caching-transport.md)

---

## Proxy

```go
type Proxy struct { /* unexported */ }

func NewProxy(l1, l2 Store, opts ...ProxyOption) *Proxy
func (p *Proxy) RoundTrip(req *http.Request) (*http.Response, error) // implements http.RoundTripper
```

Panics if `l1` or `l2` is nil.

### ProxyOption

| Option | Signature | Default |
|--------|-----------|---------|
| WithProxyTransport | `WithProxyTransport(rt http.RoundTripper)` | `http.DefaultTransport` |
| WithProxySanitizer | `WithProxySanitizer(s Sanitizer)` | no-op Pipeline |
| WithProxyMatcher | `WithProxyMatcher(m Matcher)` | `DefaultMatcher()` |
| WithProxyRoute | `WithProxyRoute(route string)` | `""` |
| WithProxyOnError | `WithProxyOnError(fn func(error))` | nil |
| WithProxyFallbackOn | `WithProxyFallbackOn(fn func(error, *http.Response) bool)` | transport errors only |
| WithProxyTLSConfig | `WithProxyTLSConfig(cfg *tls.Config)` | nil |

**Details:** [Proxy Mode](proxy.md)

---

## Server

```go
type Server struct { /* unexported */ }

func NewServer(store Store, opts ...ServerOption) (*Server, error)
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) // implements http.Handler
func (s *Server) ResetCounter(name string)                         // reset named or all template counters
```

Returns an error if option values are invalid. Panics if `store` is nil.

### ServerOption

| Option | Signature | Default |
|--------|-----------|---------|
| WithMatcher | `WithMatcher(m Matcher)` | `DefaultMatcher()` |
| WithFallbackStatus | `WithFallbackStatus(code int)` | `404` |
| WithFallbackBody | `WithFallbackBody(body []byte)` | `"httptape: no matching tape found"` |
| WithOnNoMatch | `WithOnNoMatch(fn func(*http.Request))` | nil |
| WithCORS | `WithCORS()` | disabled |
| WithDelay | `WithDelay(d time.Duration)` | `0` |
| WithErrorRate | `WithErrorRate(rate float64)` | `0.0` |
| WithReplayHeaders | `WithReplayHeaders(key, value string)` | none |
| WithSynthesis | `WithSynthesis()` | disabled |

**Details:** [Replay](replay.md), [Synthesis](synthesis.md)

---

## TLS helpers

```go
func BuildTLSConfig(certFile, keyFile, caFile string, insecure bool) (*tls.Config, error)
```

Constructs a `*tls.Config` from optional PEM file paths and an insecure flag. Returns `(nil, nil)` when all parameters are zero-valued (use Go defaults). `certFile` and `keyFile` must be supplied together for mTLS; `caFile` overrides the system root CAs; `insecure` sets `InsecureSkipVerify`. Used internally by the CLI's `--tls-*` flags and exposed for embedders that build their own `*tls.Config` from the same inputs.

**Details:** [TLS](tls.md)

---

## Redaction (Sanitization)

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
| FakeFieldsWith | `FakeFieldsWith(seed string, fields map[string]Faker) SanitizeFunc` |

### Faker interface

```go
type Faker interface {
    Fake(seed string, original any) any
}
```

`FakeFieldsWith` wires explicit `Faker` implementations to JSONPath-like paths, in contrast to `FakeFields` which auto-detects the strategy from each value's runtime type.

### Built-in fakers

All twelve are exported struct types and are constructed as struct literals (no `NewXFaker` constructors).

| Type | Construction | Output |
|---|---|---|
| `RedactedFaker` | `RedactedFaker{}` | strings -> `"[REDACTED]"`, numbers -> `0`, bools -> `false` |
| `FixedFaker` | `FixedFaker{Value: any}` | always returns `Value` |
| `HMACFaker` | `HMACFaker{}` | strings -> `"fake_<hex>"`, numbers -> positive int |
| `EmailFaker` | `EmailFaker{}` | `"user_<hex>@example.com"` |
| `PhoneFaker` | `PhoneFaker{}` | digits replaced, format preserved |
| `CreditCardFaker` | `CreditCardFaker{}` | `XXXX-XXXX-XXXX-XXXX`, prefix preserved, valid Luhn |
| `NumericFaker` | `NumericFaker{Length: int}` | string of N HMAC-derived digits |
| `DateFaker` | `DateFaker{Format: string}` | date in Go layout (default `"2006-01-02"`) |
| `PatternFaker` | `PatternFaker{Pattern: string}` | `#` -> digit, `?` -> letter, others literal |
| `PrefixFaker` | `PrefixFaker{Prefix: string}` | `"<Prefix><16-hex>"` |
| `NameFaker` | `NameFaker{}` | `"<First> <Last>"` from internal lists |
| `AddressFaker` | `AddressFaker{}` | `"<num> <street> <suffix>, <city>, <ST> <zip>"` |

PII-shaped and generic fakers leave non-string inputs unchanged. `RedactedFaker` and `HMACFaker` additionally handle numbers (and `RedactedFaker` handles booleans).

### Constants and helpers

```go
const Redacted = "[REDACTED]"

func DefaultSensitiveHeaders() []string
```

**Details:** [Redaction](sanitization.md#typed-fakers)

---

## Matching

### Interfaces

```go
type Matcher interface {
    Match(req *http.Request, candidates []Tape) (Tape, bool)
}

type MatcherFunc func(req *http.Request, candidates []Tape) (Tape, bool)
func (f MatcherFunc) Match(req *http.Request, candidates []Tape) (Tape, bool)

type Criterion interface {
    Score(req *http.Request, candidate Tape) int
    Name() string
}

type CriterionFunc func(req *http.Request, candidate Tape) int
func (f CriterionFunc) Score(req *http.Request, candidate Tape) int
func (f CriterionFunc) Name() string  // returns "custom"
```

### Matchers

```go
func DefaultMatcher() *CompositeMatcher          // MethodCriterion + PathCriterion
func ExactMatcher() Matcher                       // first method+path match
func NewCompositeMatcher(criteria ...Criterion) *CompositeMatcher
```

### Built-in criteria

| Criterion | Construction | Score |
|-----------|-------------|-------|
| MethodCriterion | `MethodCriterion{}` | 1 |
| PathCriterion | `PathCriterion{}` | 2 |
| PathRegexCriterion | `NewPathRegexCriterion(pattern string) (*PathRegexCriterion, error)` | 1 |
| PathPatternCriterion | `NewPathPatternCriterion(pattern string) (*PathPatternCriterion, error)` | 3 |
| RouteCriterion | `RouteCriterion{Route: route}` | 1 |
| HeadersCriterion | `HeadersCriterion{Key: key, Value: value}` | 3 |
| QueryParamsCriterion | `QueryParamsCriterion{}` | 4 |
| ContentNegotiationCriterion | `ContentNegotiationCriterion{}` | 3-5 |
| BodyFuzzyCriterion | `NewBodyFuzzyCriterion(paths ...string) *BodyFuzzyCriterion` | 6 |
| BodyHashCriterion | `BodyHashCriterion{}` | 8 |

**Details:** [Matching](matching.md)

---

## Templating

```go
func ResolveTemplateBodySimple(body []byte, r *http.Request, strict bool) ([]byte, error)
```

Backward-compatible convenience wrapper for template resolution using only request data (no path params, counters, or faker). See [Template Helpers](template-helpers.md) for the full template expression reference.

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
    Version string         `json:"version"`
    Matcher *MatcherConfig `json:"matcher,omitempty"`
    Rules   []Rule         `json:"rules"`
}

type MatcherConfig struct {
    Criteria []CriterionConfig `json:"criteria"`
}

type CriterionConfig struct {
    Type  string   `json:"type"`
    Paths []string `json:"paths,omitempty"`
}

type Rule struct {
    Action  string         `json:"action"`
    Headers []string       `json:"headers,omitempty"`
    Paths   []string       `json:"paths,omitempty"`
    Seed    string         `json:"seed,omitempty"`
    Fields  map[string]any `json:"fields,omitempty"`
}

func LoadConfig(r io.Reader) (*Config, error)
func LoadConfigFile(path string) (*Config, error)
func (c *Config) Validate() error
func (c *Config) BuildPipeline() *Pipeline
func (c *Config) BuildMatcher() *CompositeMatcher // returns nil when no matcher section
```

For `fake` rules, set either `Paths` (for auto-detect, mapping to `FakeFields`) or `Fields` (for typed fakers, mapping to `FakeFieldsWith`) -- the two are mutually exclusive. Each value in `Fields` is either a string shorthand (for example `"email"`) or an object (for example `{"type": "numeric", "length": 3}`). See [Config -> Typed fake fields](config.md#typed-fake-fields) for the full syntax.

Programmatic example:

```go
cfg := &httptape.Config{
    Version: "1",
    Rules: []httptape.Rule{
        {
            Action: httptape.ActionFake,
            Seed:   "my-seed",
            Fields: map[string]any{
                "$.user.email": "email",
                "$.user.cvv":   map[string]any{"type": "numeric", "length": 3},
            },
        },
    },
}
pipeline := cfg.BuildPipeline()
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

---

## Mock DSL

```go
type MockServer struct { *httptest.Server }

func Mock(stubs ...Stub) *MockServer

func When(m Method) *StubBuilder
func (b *StubBuilder) Respond(status int, body ...Body) *StubBuilder
func (b *StubBuilder) WithHeader(key, value string) *StubBuilder
func (b *StubBuilder) Build() Stub

// Method helpers
func GET(path string) Method
func POST(path string) Method
func PUT(path string) Method
func DELETE(path string) Method
func PATCH(path string) Method
func HEAD(path string) Method

// Body helpers
func JSON(s string) Body
func Text(s string) Body
func Binary(b []byte) Body
```

---

## Fixtures

```go
func LoadFixtures(dir string) (*FileStore, error)
func LoadFixturesFS(fsys fs.FS, dir string) (*MemoryStore, error)
```
