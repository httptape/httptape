package httptape

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

	// Metadata holds optional key-value pairs for fixture-level
	// configuration (e.g., delay, error simulation). Not used for
	// matching. Values are preserved through JSON round-trip.
	Metadata map[string]any `json:"metadata,omitempty"`

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

// RecordedReq captures the essential parts of an HTTP request for matching and replay.
//
// The Body field is always stored as []byte in Go. When marshaled to JSON,
// the body representation depends on the Content-Type header:
//   - JSON Content-Type (application/json, +json suffix): native JSON object/array
//   - Text Content-Type (text/*, application/xml, etc.): JSON string
//   - Binary or missing Content-Type: base64-encoded JSON string
//   - Nil or empty body: JSON null
type RecordedReq struct {
	// Method is the HTTP method (GET, POST, etc.).
	Method string `json:"method"`

	// URL is the full request URL as a string.
	URL string `json:"url"`

	// URLPattern is a colon-prefixed path pattern (e.g., "/users/:id") used
	// by exemplar tapes for pattern-based matching. Mutually exclusive with
	// URL: a tape must have either URL (exact match) or URLPattern (pattern
	// match via PathPatternCriterion), never both.
	//
	// Only meaningful when the parent Tape has Exemplar set to true.
	// Validation enforces: Exemplar==true requires URLPattern!="",
	// and URLPattern!="" requires Exemplar==true.
	URLPattern string `json:"url_pattern,omitempty"`

	// Headers contains the request headers. Only non-sensitive headers are stored
	// after sanitization (handled by the sanitizer, not by this type).
	Headers http.Header `json:"headers"`

	// Body is the full request body bytes. May be nil for bodiless requests.
	Body []byte `json:"-"`

	// BodyHash is a hex-encoded SHA-256 hash of the original request body.
	// Used for matching without comparing full bodies.
	BodyHash string `json:"body_hash"`

	// Truncated is true if the body was truncated due to exceeding the
	// configured maximum body size.
	Truncated bool `json:"truncated,omitempty"`

	// OriginalBodySize is the original body size in bytes before truncation.
	// Only set when Truncated is true.
	OriginalBodySize int64 `json:"original_body_size,omitempty"`
}

// RecordedResp captures the essential parts of an HTTP response for replay.
//
// The Body field is always stored as []byte in Go. When marshaled to JSON,
// the body representation depends on the Content-Type header (same rules as
// RecordedReq). For SSE (text/event-stream) responses, the discrete events
// are stored in SSEEvents and Body is nil.
//
// During replay, if SSEEvents is non-nil and non-empty the tape is treated
// as an SSE tape and Body is ignored (even if present).
type RecordedResp struct {
	// StatusCode is the HTTP response status code.
	StatusCode int `json:"status_code"`

	// Headers contains the response headers.
	Headers http.Header `json:"headers"`

	// Body is the full response body bytes.
	Body []byte `json:"-"`

	// Truncated is true if the body was truncated due to exceeding the
	// configured maximum body size, or if an SSE stream was disconnected
	// before a clean termination.
	Truncated bool `json:"truncated,omitempty"`

	// OriginalBodySize is the original body size in bytes before truncation.
	// Only set when Truncated is true.
	OriginalBodySize int64 `json:"original_body_size,omitempty"`

	// SSEEvents holds the parsed Server-Sent Events for text/event-stream
	// responses. When non-nil and non-empty, this tape represents an SSE
	// response and Body is ignored during replay.
	// When nil or empty (including for tapes created before SSE support was
	// added), the tape is treated as a regular HTTP response.
	SSEEvents []SSEEvent `json:"sse_events,omitempty"`
}

// IsSSE reports whether this response represents an SSE stream.
// It returns true when SSEEvents is non-nil and non-empty.
func (r RecordedResp) IsSSE() bool {
	return len(r.SSEEvents) > 0
}

// MarshalJSON implements json.Marshaler for RecordedReq.
// The body field's JSON representation depends on the Content-Type from Headers:
//   - JSON Content-Type: native JSON value (object/array/primitive)
//   - Text Content-Type: JSON string (UTF-8)
//   - Binary or missing Content-Type: base64-encoded JSON string
//   - Nil or empty body: JSON null
func (r RecordedReq) MarshalJSON() ([]byte, error) {
	type alias struct {
		Method           string      `json:"method"`
		URL              string      `json:"url"`
		URLPattern       string      `json:"url_pattern,omitempty"`
		Headers          http.Header `json:"headers"`
		Body             any         `json:"body"`
		BodyHash         string      `json:"body_hash"`
		Truncated        bool        `json:"truncated,omitempty"`
		OriginalBodySize int64       `json:"original_body_size,omitempty"`
	}

	a := alias{
		Method:           r.Method,
		URL:              r.URL,
		URLPattern:       r.URLPattern,
		Headers:          r.Headers,
		Body:             nil, // default: null
		BodyHash:         r.BodyHash,
		Truncated:        r.Truncated,
		OriginalBodySize: r.OriginalBodySize,
	}

	a.Body = marshalBody(r.Body, r.Headers)
	return json.Marshal(a)
}

// UnmarshalJSON implements json.Unmarshaler for RecordedReq.
// It detects the JSON token type of the body field and decodes accordingly:
//   - JSON object/array: body stored as compact JSON bytes
//   - JSON string: text or base64 based on Content-Type
//   - JSON null: body is nil
func (r *RecordedReq) UnmarshalJSON(data []byte) error {
	type alias struct {
		Method           string          `json:"method"`
		URL              string          `json:"url"`
		URLPattern       string          `json:"url_pattern,omitempty"`
		Headers          http.Header     `json:"headers"`
		Body             json.RawMessage `json:"body"`
		BodyHash         string          `json:"body_hash"`
		Truncated        bool            `json:"truncated,omitempty"`
		OriginalBodySize int64           `json:"original_body_size,omitempty"`
	}

	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return fmt.Errorf("unmarshal RecordedReq: %w", err)
	}

	r.Method = a.Method
	r.URL = a.URL
	r.URLPattern = a.URLPattern
	r.Headers = a.Headers
	r.BodyHash = a.BodyHash
	r.Truncated = a.Truncated
	r.OriginalBodySize = a.OriginalBodySize

	body, err := unmarshalBody(a.Body, a.Headers)
	if err != nil {
		return fmt.Errorf("unmarshal RecordedReq body: %w", err)
	}
	r.Body = body
	return nil
}

// MarshalJSON implements json.Marshaler for RecordedResp.
// The body field's JSON representation depends on the Content-Type from Headers
// (same rules as RecordedReq.MarshalJSON).
func (r RecordedResp) MarshalJSON() ([]byte, error) {
	type alias struct {
		StatusCode       int         `json:"status_code"`
		Headers          http.Header `json:"headers"`
		Body             any         `json:"body"`
		Truncated        bool        `json:"truncated,omitempty"`
		OriginalBodySize int64       `json:"original_body_size,omitempty"`
		SSEEvents        []SSEEvent  `json:"sse_events,omitempty"`
	}

	a := alias{
		StatusCode:       r.StatusCode,
		Headers:          r.Headers,
		Body:             nil,
		Truncated:        r.Truncated,
		OriginalBodySize: r.OriginalBodySize,
		SSEEvents:        r.SSEEvents,
	}

	a.Body = marshalBody(r.Body, r.Headers)
	return json.Marshal(a)
}

// UnmarshalJSON implements json.Unmarshaler for RecordedResp.
// It detects the JSON token type of the body field and decodes accordingly
// (same rules as RecordedReq.UnmarshalJSON).
func (r *RecordedResp) UnmarshalJSON(data []byte) error {
	type alias struct {
		StatusCode       int             `json:"status_code"`
		Headers          http.Header     `json:"headers"`
		Body             json.RawMessage `json:"body"`
		Truncated        bool            `json:"truncated,omitempty"`
		OriginalBodySize int64           `json:"original_body_size,omitempty"`
		SSEEvents        []SSEEvent      `json:"sse_events,omitempty"`
	}

	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return fmt.Errorf("unmarshal RecordedResp: %w", err)
	}

	r.StatusCode = a.StatusCode
	r.Headers = a.Headers
	r.Truncated = a.Truncated
	r.OriginalBodySize = a.OriginalBodySize
	r.SSEEvents = a.SSEEvents

	body, err := unmarshalBody(a.Body, a.Headers)
	if err != nil {
		return fmt.Errorf("unmarshal RecordedResp body: %w", err)
	}
	r.Body = body
	return nil
}

// marshalBody returns the appropriate JSON value for the body field based on
// the Content-Type from headers. Returns nil for nil/empty bodies.
func marshalBody(body []byte, headers http.Header) any {
	if len(body) == 0 {
		return nil
	}

	ct := ""
	if headers != nil {
		ct = headers.Get("Content-Type")
	}

	mt, err := ParseMediaType(ct)
	if err != nil || ct == "" {
		// Unknown/missing CT: base64
		return base64.StdEncoding.EncodeToString(body)
	}

	if IsJSON(mt) {
		// Verify the body is valid JSON before emitting as native.
		if json.Valid(body) {
			return json.RawMessage(body)
		}
		// Invalid JSON despite JSON CT: fall back to base64.
		return base64.StdEncoding.EncodeToString(body)
	}

	if IsText(mt) {
		return string(body)
	}

	// Binary: base64
	return base64.StdEncoding.EncodeToString(body)
}

// unmarshalBody decodes the body JSON value based on its token type and the
// Content-Type from headers. Returns nil for JSON null or missing body.
func unmarshalBody(raw json.RawMessage, headers http.Header) ([]byte, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	// Determine the JSON token type.
	firstByte := raw[0]

	switch {
	case firstByte == '{' || firstByte == '[':
		// Native JSON object or array: compact to normalize whitespace.
		// This ensures consistent Body bytes regardless of fixture indentation,
		// which is critical for BodyHashCriterion and round-trip consistency.
		var buf bytes.Buffer
		if err := json.Compact(&buf, []byte(raw)); err != nil {
			// If compact fails, store the raw bytes as-is.
			return []byte(raw), nil
		}
		return buf.Bytes(), nil

	case firstByte == '"':
		// JSON string: could be text or base64.
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("decode body string: %w", err)
		}

		ct := ""
		if headers != nil {
			ct = headers.Get("Content-Type")
		}

		mt, parseErr := ParseMediaType(ct)

		if parseErr == nil && IsJSON(mt) {
			// JSON CT with string value: could be a legacy base64-encoded body
			// or a scalar JSON string value.
			if json.Valid([]byte(s)) {
				return []byte(s), nil
			}
			// Not valid JSON: try base64 decode (legacy fixture).
			decoded, b64Err := base64.StdEncoding.DecodeString(s)
			if b64Err == nil {
				return decoded, nil
			}
			// Neither valid JSON nor valid base64: store as UTF-8 bytes.
			return []byte(s), nil
		}

		if parseErr == nil && IsText(mt) {
			// Text CT: store string as UTF-8 bytes.
			return []byte(s), nil
		}

		// Binary or unknown CT: base64-decode.
		decoded, b64Err := base64.StdEncoding.DecodeString(s)
		if b64Err == nil {
			return decoded, nil
		}
		// Graceful degradation: store as UTF-8 if base64 decode fails.
		return []byte(s), nil

	case firstByte == 't' || firstByte == 'f':
		// JSON boolean: unexpected for body, store raw.
		return raw, nil

	default:
		// JSON number or other: store raw.
		return raw, nil
	}
}

// NewTape creates a new Tape with a generated ID and the current UTC timestamp.
func NewTape(route string, req RecordedReq, resp RecordedResp) Tape {
	return Tape{
		ID:         newUUID(),
		Route:      route,
		RecordedAt: time.Now().UTC(),
		Request:    req,
		Response:   resp,
	}
}

// ValidateTape checks a tape for structural validity. Currently validates
// exemplar-specific constraints via ValidateExemplar. Returns nil if valid.
func ValidateTape(t Tape) error {
	return ValidateExemplar(t)
}

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
// ValidateExemplar does not validate the URLPattern syntax -- that is the
// responsibility of PathPatternCriterion.
func ValidateExemplar(t Tape) error {
	hasURL := t.Request.URL != ""
	hasPattern := t.Request.URLPattern != ""

	if hasURL && hasPattern {
		return fmt.Errorf("httptape: tape %s: url and url_pattern are mutually exclusive", t.ID)
	}

	if t.Exemplar {
		if !hasPattern {
			return fmt.Errorf("httptape: exemplar tape %s: url_pattern is required", t.ID)
		}
		if len(t.Response.SSEEvents) > 0 {
			return fmt.Errorf("httptape: exemplar tape %s: SSE exemplars are not supported", t.ID)
		}
	}

	if hasPattern && !t.Exemplar {
		return fmt.Errorf("httptape: tape %s: url_pattern requires exemplar to be true", t.ID)
	}

	return nil
}

// BodyHashFromBytes computes the hex-encoded SHA-256 hash of the given bytes.
// Returns an empty string if b is nil or empty.
func BodyHashFromBytes(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// newUUID generates a UUID v4 string using crypto/rand.
func newUUID() string {
	var uuid [16]byte
	_, err := rand.Read(uuid[:])
	if err != nil {
		// crypto/rand.Read should never fail on supported platforms.
		// If it does, return a zero UUID rather than panicking (library rule: no panics).
		return "00000000-0000-4000-8000-000000000000"
	}
	// Set version to 4 (bits 12-15 of time_hi_and_version).
	uuid[6] = (uuid[6] & 0x0f) | 0x40
	// Set variant to RFC 4122 (bits 6-7 of clock_seq_hi_and_reserved).
	uuid[8] = (uuid[8] & 0x3f) | 0x80

	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16])
}
