package httptape

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
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
// or not valid JSON, it is returned unchanged. This is intentional:
// malformed JSON bodies are stored as raw bytes and body-level
// sanitization is silently skipped. See ADR-17.
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
//	sanitizer := NewPipeline(
//	    RedactHeaders(),
//	    FakeFields("my-project-seed",
//	        "$.user.email",
//	        "$.user.id",
//	        "$.tokens[*].value",
//	    ),
//	)
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

// fakeBodyFields unmarshals the body as JSON, applies all path-based
// faking, and re-marshals the result. If the body is nil, empty, or
// not valid JSON, it is returned unchanged. This is intentional:
// malformed JSON bodies are stored as raw bytes and body-level
// sanitization is silently skipped. See ADR-17.
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

// fakeValue returns a deterministic fake replacement for the given JSON
// value. The fake is derived from the HMAC-SHA256 of the value's string
// representation using the provided seed.
//
// Faking strategies:
//   - Email string: user_<hash>@example.com
//   - UUID string: deterministic UUID v5
//   - float64: deterministic positive integer
//   - Generic string: fake_<hash_prefix>
//   - bool, nil, objects, arrays: returned unchanged
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

// computeHMAC returns the HMAC-SHA256 of the given message using the
// provided key. Both key and message are strings; the HMAC operates on
// their UTF-8 byte representations.
func computeHMAC(key, message string) []byte {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(message))
	return mac.Sum(nil)
}

// isEmail returns true if s looks like an email address: contains exactly
// one '@' with non-empty parts on both sides. This is a heuristic, not a
// full RFC 5322 parser.
func isEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	return at > 0 && at < len(s)-1 && strings.Count(s, "@") == 1
}

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

// fakeEmail generates a deterministic fake email from the HMAC hash.
// Format: user_<first 8 hex chars>@example.com
func fakeEmail(hash []byte) string {
	return "user_" + hex.EncodeToString(hash[:4]) + "@example.com"
}

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

// fakeNumericID generates a deterministic positive integer from the HMAC
// hash. Uses the first 4 bytes interpreted as big-endian uint32, masked
// to [1, 2^31-1] to ensure a positive non-zero int.
func fakeNumericID(hash []byte) float64 {
	n := uint32(hash[0])<<24 | uint32(hash[1])<<16 | uint32(hash[2])<<8 | uint32(hash[3])
	n = n & 0x7FFFFFFF // clear sign bit: [0, 2^31-1]
	if n == 0 {
		n = 1 // avoid zero
	}
	return float64(n)
}

// fakeString generates a deterministic fake string from the HMAC hash.
// Format: fake_<first 8 hex chars>
func fakeString(hash []byte) string {
	return "fake_" + hex.EncodeToString(hash[:4])
}
