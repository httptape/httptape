package httptape

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
)

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

// segment represents one step in a body redaction path.
type segment struct {
	// key is the object field name to traverse.
	key string
	// wildcard is true when this segment includes [*], meaning "iterate all
	// array elements" before descending into key.
	wildcard bool
}

// parsedPath is a pre-parsed body redaction path.
type parsedPath struct {
	segments []segment
}

// parsePath parses a JSONPath-like string into a parsedPath.
// Returns ok=false if the path is invalid or unsupported.
//
// Supported syntax: $.field, $.nested.field, $.array[*].field
func parsePath(path string) (parsedPath, bool) {
	if !strings.HasPrefix(path, "$.") {
		return parsedPath{}, false
	}
	rest := path[2:] // strip "$."
	if rest == "" {
		return parsedPath{}, false
	}

	tokens := strings.Split(rest, ".")
	segments := make([]segment, 0, len(tokens))

	for _, tok := range tokens {
		if tok == "" {
			return parsedPath{}, false
		}

		var seg segment
		if strings.HasSuffix(tok, "[*]") {
			seg.key = tok[:len(tok)-3]
			seg.wildcard = true
			if seg.key == "" {
				return parsedPath{}, false
			}
		} else if strings.ContainsAny(tok, "[]") {
			// Contains brackets but not [*] suffix -- unsupported (e.g., [0]).
			return parsedPath{}, false
		} else {
			seg.key = tok
		}
		segments = append(segments, seg)
	}

	return parsedPath{segments: segments}, true
}

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
//	sanitizer := NewPipeline(
//	    RedactHeaders(),
//	    RedactBodyPaths("$.password", "$.user.ssn", "$.tokens[*].value"),
//	)
func RedactBodyPaths(paths ...string) SanitizeFunc {
	// Parse all paths at construction time.
	var parsed []parsedPath
	for _, p := range paths {
		if pp, ok := parsePath(p); ok {
			parsed = append(parsed, pp)
		}
	}

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
}

// redactBodyFields unmarshals the body as JSON, applies all path
// redactions, and re-marshals the result. If the body is nil, empty,
// or not valid JSON, it is returned unchanged.
func redactBodyFields(body []byte, paths []parsedPath) []byte {
	if len(body) == 0 {
		return body
	}

	var data any
	if err := json.Unmarshal(body, &data); err != nil {
		return body
	}

	for _, p := range paths {
		redactAtPath(data, p.segments)
	}

	result, err := json.Marshal(data)
	if err != nil {
		return body
	}
	return result
}

// redactAtPath recursively traverses the JSON structure following the
// given segments and redacts the leaf value. It modifies the data
// in-place (caller must ensure data is a fresh copy from json.Unmarshal).
func redactAtPath(data any, segments []segment) {
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
			redactAtPath(elem, rest)
		}
		return
	}

	// Not a wildcard segment.
	if len(rest) == 0 {
		// Leaf: apply type-aware redaction.
		obj[seg.key] = redactValue(val)
		return
	}

	// Intermediate: recurse deeper.
	redactAtPath(val, rest)
}

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
