package httptape

import "net/http"

// Redacted is the replacement value used for redacted header values.
const Redacted = "[REDACTED]"

// DefaultSensitiveHeaders is the predefined set of HTTP header names that
// commonly contain sensitive data. These headers are redacted by default
// when using RedactHeaders without explicit header names.
//
// The set includes:
//   - Authorization -- bearer tokens, basic auth credentials
//   - Cookie -- session tokens, tracking identifiers
//   - Set-Cookie -- server-set session tokens
//   - X-Api-Key -- API key authentication
//   - Proxy-Authorization -- proxy authentication credentials
//   - X-Forwarded-For -- client IP addresses (PII)
var DefaultSensitiveHeaders = []string{
	"Authorization",
	"Cookie",
	"Set-Cookie",
	"X-Api-Key",
	"Proxy-Authorization",
	"X-Forwarded-For",
}

// SanitizeFunc is a function that transforms a Tape as part of a sanitization
// pipeline. Each function receives a Tape and returns a (possibly modified)
// copy. Implementations must not mutate the input Tape -- they must copy any
// fields they modify.
type SanitizeFunc func(Tape) Tape

// Pipeline is a composable Sanitizer that applies an ordered sequence of
// SanitizeFunc transformations to a Tape. It implements the Sanitizer
// interface declared in recorder.go.
//
// Pipeline is safe for concurrent use -- it is immutable after construction.
type Pipeline struct {
	funcs []SanitizeFunc
}

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

// Sanitize applies all sanitization functions in order to the given tape
// and returns the result. It implements the Sanitizer interface.
func (p *Pipeline) Sanitize(t Tape) Tape {
	for _, fn := range p.funcs {
		t = fn(t)
	}
	return t
}

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
// The returned function does not mutate the input Tape -- it clones the
// header maps before modification.
//
// Example:
//
//	// Redact default sensitive headers:
//	sanitizer := NewPipeline(RedactHeaders())
//
//	// Redact specific headers:
//	sanitizer := NewPipeline(RedactHeaders("Authorization", "X-Custom-Secret"))
//
//	// Use with Recorder:
//	recorder := NewRecorder(store, WithSanitizer(
//	    NewPipeline(RedactHeaders()),
//	))
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
