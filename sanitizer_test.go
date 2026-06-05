package httptape

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Compile-time check: *Pipeline must implement the Sanitizer interface.
var _ Sanitizer = (*Pipeline)(nil)

// makeTapeWithHeaders creates a minimal Tape with the given request and response headers.
func makeTapeWithHeaders(reqHeaders, respHeaders http.Header) Tape {
	return Tape{
		ID:    "test-id",
		Route: "test-route",
		Request: RecordedReq{
			Method:  "GET",
			URL:     "https://example.com/test",
			Headers: reqHeaders,
			Body:    []byte("request body"),
		},
		Response: RecordedResp{
			StatusCode: 200,
			Headers:    respHeaders,
			Body:       []byte("response body"),
		},
	}
}

func TestRedactHeaders_SingleHeader(t *testing.T) {
	reqHeaders := http.Header{"Authorization": {"Bearer token123"}}
	respHeaders := http.Header{"Content-Type": {"application/json"}}
	tape := makeTapeWithHeaders(reqHeaders, respHeaders)

	fn := RedactHeaders("Authorization")
	result := fn(tape)

	if got := result.Request.Headers.Get("Authorization"); got != Redacted {
		t.Errorf("expected %q, got %q", Redacted, got)
	}
	if got := result.Response.Headers.Get("Content-Type"); got != "application/json" {
		t.Errorf("expected Content-Type unchanged, got %q", got)
	}
}

func TestRedactHeaders_MultipleHeaders(t *testing.T) {
	reqHeaders := http.Header{
		"Authorization": {"Bearer token123"},
		"Cookie":        {"session=abc"},
	}
	respHeaders := http.Header{}
	tape := makeTapeWithHeaders(reqHeaders, respHeaders)

	fn := RedactHeaders("Authorization", "Cookie")
	result := fn(tape)

	if got := result.Request.Headers.Get("Authorization"); got != Redacted {
		t.Errorf("Authorization: expected %q, got %q", Redacted, got)
	}
	if got := result.Request.Headers.Get("Cookie"); got != Redacted {
		t.Errorf("Cookie: expected %q, got %q", Redacted, got)
	}
}

func TestRedactHeaders_DefaultHeaders(t *testing.T) {
	reqHeaders := http.Header{
		"Authorization":       {"Bearer abc"},
		"Cookie":              {"session=xyz"},
		"X-Api-Key":           {"key-123"},
		"Proxy-Authorization": {"Basic creds"},
		"X-Forwarded-For":     {"1.2.3.4"},
		"Content-Type":        {"text/plain"},
	}
	respHeaders := http.Header{
		"Set-Cookie":   {"sid=abc123"},
		"Content-Type": {"application/json"},
	}
	tape := makeTapeWithHeaders(reqHeaders, respHeaders)

	fn := RedactHeaders() // no args => DefaultSensitiveHeaders()
	result := fn(tape)

	// All default sensitive headers in request should be redacted.
	for _, name := range DefaultSensitiveHeaders() {
		if val := result.Request.Headers.Get(name); val != "" && val != Redacted {
			t.Errorf("request header %q: expected %q, got %q", name, Redacted, val)
		}
	}
	// Set-Cookie in response should be redacted.
	if got := result.Response.Headers.Get("Set-Cookie"); got != Redacted {
		t.Errorf("response Set-Cookie: expected %q, got %q", Redacted, got)
	}
	// Non-sensitive headers should be untouched.
	if got := result.Request.Headers.Get("Content-Type"); got != "text/plain" {
		t.Errorf("request Content-Type should be unchanged, got %q", got)
	}
	if got := result.Response.Headers.Get("Content-Type"); got != "application/json" {
		t.Errorf("response Content-Type should be unchanged, got %q", got)
	}
}

func TestRedactHeaders_CustomHeaders(t *testing.T) {
	reqHeaders := http.Header{
		"Authorization":   {"Bearer token"},
		"X-Custom-Secret": {"my-secret"},
	}
	tape := makeTapeWithHeaders(reqHeaders, http.Header{})

	fn := RedactHeaders("X-Custom-Secret")
	result := fn(tape)

	// X-Custom-Secret should be redacted.
	if got := result.Request.Headers.Get("X-Custom-Secret"); got != Redacted {
		t.Errorf("X-Custom-Secret: expected %q, got %q", Redacted, got)
	}
	// Authorization should NOT be redacted (custom list only).
	if got := result.Request.Headers.Get("Authorization"); got != "Bearer token" {
		t.Errorf("Authorization should be unchanged, got %q", got)
	}
}

func TestRedactHeaders_CaseInsensitive(t *testing.T) {
	reqHeaders := http.Header{
		"Authorization": {"Bearer token123"},
	}
	tape := makeTapeWithHeaders(reqHeaders, http.Header{})

	// Use lowercase name.
	fn := RedactHeaders("authorization")
	result := fn(tape)

	if got := result.Request.Headers.Get("Authorization"); got != Redacted {
		t.Errorf("expected %q, got %q", Redacted, got)
	}
}

func TestRedactHeaders_HeaderNotPresent(t *testing.T) {
	reqHeaders := http.Header{"Content-Type": {"text/plain"}}
	respHeaders := http.Header{"Content-Type": {"application/json"}}
	tape := makeTapeWithHeaders(reqHeaders, respHeaders)

	fn := RedactHeaders("Authorization")
	result := fn(tape)

	// No Authorization header exists; tape should be effectively unchanged.
	if got := result.Request.Headers.Get("Content-Type"); got != "text/plain" {
		t.Errorf("expected Content-Type unchanged, got %q", got)
	}
	if got := result.Response.Headers.Get("Content-Type"); got != "application/json" {
		t.Errorf("expected Content-Type unchanged, got %q", got)
	}
}

func TestRedactHeaders_MultiValueHeader(t *testing.T) {
	respHeaders := http.Header{
		"Set-Cookie": {"a=1", "b=2"},
	}
	tape := makeTapeWithHeaders(http.Header{}, respHeaders)

	fn := RedactHeaders("Set-Cookie")
	result := fn(tape)

	values := result.Response.Headers["Set-Cookie"]
	if len(values) != 2 {
		t.Fatalf("expected 2 Set-Cookie values, got %d", len(values))
	}
	for i, v := range values {
		if v != Redacted {
			t.Errorf("Set-Cookie[%d]: expected %q, got %q", i, Redacted, v)
		}
	}
}

func TestRedactHeaders_BothRequestAndResponse(t *testing.T) {
	reqHeaders := http.Header{"Authorization": {"Bearer req-token"}}
	respHeaders := http.Header{"Authorization": {"Bearer resp-token"}}
	tape := makeTapeWithHeaders(reqHeaders, respHeaders)

	fn := RedactHeaders("Authorization")
	result := fn(tape)

	if got := result.Request.Headers.Get("Authorization"); got != Redacted {
		t.Errorf("request Authorization: expected %q, got %q", Redacted, got)
	}
	if got := result.Response.Headers.Get("Authorization"); got != Redacted {
		t.Errorf("response Authorization: expected %q, got %q", Redacted, got)
	}
}

func TestRedactHeaders_NilHeaders(t *testing.T) {
	tape := makeTapeWithHeaders(nil, nil)

	fn := RedactHeaders("Authorization")
	result := fn(tape)

	if result.Request.Headers != nil {
		t.Error("expected nil request headers")
	}
	if result.Response.Headers != nil {
		t.Error("expected nil response headers")
	}
}

func TestRedactHeaders_PreservesOtherHeaders(t *testing.T) {
	reqHeaders := http.Header{
		"Content-Type":  {"application/json"},
		"Accept":        {"*/*"},
		"Authorization": {"Bearer secret"},
	}
	tape := makeTapeWithHeaders(reqHeaders, http.Header{})

	fn := RedactHeaders("Authorization")
	result := fn(tape)

	if got := result.Request.Headers.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type: expected %q, got %q", "application/json", got)
	}
	if got := result.Request.Headers.Get("Accept"); got != "*/*" {
		t.Errorf("Accept: expected %q, got %q", "*/*", got)
	}
	if got := result.Request.Headers.Get("Authorization"); got != Redacted {
		t.Errorf("Authorization: expected %q, got %q", Redacted, got)
	}
}

func TestRedactHeaders_LeavesOriginalTapeUnchanged(t *testing.T) {
	reqHeaders := http.Header{"Authorization": {"Bearer original"}}
	respHeaders := http.Header{"Set-Cookie": {"session=original"}}
	tape := makeTapeWithHeaders(reqHeaders, respHeaders)

	fn := RedactHeaders("Authorization", "Set-Cookie")
	_ = fn(tape)

	// Original tape must be unchanged.
	if got := tape.Request.Headers.Get("Authorization"); got != "Bearer original" {
		t.Errorf("original request header mutated: got %q", got)
	}
	if got := tape.Response.Headers.Get("Set-Cookie"); got != "session=original" {
		t.Errorf("original response header mutated: got %q", got)
	}
}

func TestRedactHeaders_PreservesBody(t *testing.T) {
	tape := makeTapeWithHeaders(
		http.Header{"Authorization": {"Bearer token"}},
		http.Header{"Set-Cookie": {"sid=123"}},
	)

	fn := RedactHeaders()
	result := fn(tape)

	if string(result.Request.Body) != "request body" {
		t.Errorf("request body changed: got %q", result.Request.Body)
	}
	if string(result.Response.Body) != "response body" {
		t.Errorf("response body changed: got %q", result.Response.Body)
	}
}

// --- Pipeline tests ---

func TestPipeline_Empty(t *testing.T) {
	p := NewPipeline()
	tape := makeTapeWithHeaders(
		http.Header{"Authorization": {"Bearer token"}},
		http.Header{},
	)

	result := p.Sanitize(tape)

	if got := result.Request.Headers.Get("Authorization"); got != "Bearer token" {
		t.Errorf("expected header unchanged, got %q", got)
	}
}

func TestPipeline_SingleFunc(t *testing.T) {
	p := NewPipeline(RedactHeaders("Authorization"))
	tape := makeTapeWithHeaders(
		http.Header{"Authorization": {"Bearer token"}},
		http.Header{},
	)

	result := p.Sanitize(tape)

	if got := result.Request.Headers.Get("Authorization"); got != Redacted {
		t.Errorf("expected %q, got %q", Redacted, got)
	}
}

func TestPipeline_MultipleFuncs(t *testing.T) {
	p := NewPipeline(
		RedactHeaders("Authorization"),
		RedactHeaders("Cookie"),
	)
	tape := makeTapeWithHeaders(
		http.Header{
			"Authorization": {"Bearer token"},
			"Cookie":        {"session=abc"},
		},
		http.Header{},
	)

	result := p.Sanitize(tape)

	if got := result.Request.Headers.Get("Authorization"); got != Redacted {
		t.Errorf("Authorization: expected %q, got %q", Redacted, got)
	}
	if got := result.Request.Headers.Get("Cookie"); got != Redacted {
		t.Errorf("Cookie: expected %q, got %q", Redacted, got)
	}
}

func TestPipeline_Ordering(t *testing.T) {
	var observedValue string

	// First func observes the Authorization value.
	observer := func(t Tape) Tape {
		observedValue = t.Request.Headers.Get("Authorization")
		return t
	}

	p := NewPipeline(
		observer,
		RedactHeaders("Authorization"),
	)

	tape := makeTapeWithHeaders(
		http.Header{"Authorization": {"Bearer original"}},
		http.Header{},
	)

	result := p.Sanitize(tape)

	// The observer should have seen the un-redacted value.
	if observedValue != "Bearer original" {
		t.Errorf("observer saw %q, expected %q", observedValue, "Bearer original")
	}
	// The final result should be redacted.
	if got := result.Request.Headers.Get("Authorization"); got != Redacted {
		t.Errorf("Authorization: expected %q, got %q", Redacted, got)
	}
}

func TestPipeline_ImplementsSanitizer(t *testing.T) {
	// This is primarily a compile-time check (see var _ above), but we also
	// verify at runtime that the interface method works via a Sanitizer variable.
	var s Sanitizer = NewPipeline(RedactHeaders("Authorization"))

	tape := makeTapeWithHeaders(
		http.Header{"Authorization": {"Bearer token"}},
		http.Header{},
	)

	result := s.Sanitize(tape)

	if got := result.Request.Headers.Get("Authorization"); got != Redacted {
		t.Errorf("expected %q, got %q", Redacted, got)
	}
}

// --- Body path redaction tests ---

// makeTapeWithBody creates a minimal Tape with the given request and response bodies.
func makeTapeWithBody(reqBody, respBody []byte) Tape {
	return Tape{
		ID:    "test-id",
		Route: "test-route",
		Request: RecordedReq{
			Method:   "POST",
			URL:      "https://example.com/test",
			Headers:  http.Header{"Content-Type": {"application/json"}},
			Body:     reqBody,
			BodyHash: BodyHashFromBytes(reqBody),
		},
		Response: RecordedResp{
			StatusCode: 200,
			Headers:    http.Header{"Content-Type": {"application/json"}},
			Body:       respBody,
		},
	}
}

// jsonEqual compares two byte slices as JSON, ignoring key order and whitespace.
func jsonEqual(t *testing.T, got, want []byte) bool {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("failed to unmarshal got: %v", err)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("failed to unmarshal want: %v", err)
	}
	// Re-marshal both to canonical form for comparison.
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	return string(gb) == string(wb)
}

func TestRedactBodyPaths_TopLevelString(t *testing.T) {
	body := []byte(`{"api_key":"secret"}`)
	tape := makeTapeWithBody(body, body)

	fn := RedactBodyPaths("$.api_key")
	result := fn(tape)

	want := []byte(`{"api_key":"[REDACTED]"}`)
	if !jsonEqual(t, result.Request.Body, want) {
		t.Errorf("request body: got %s, want %s", result.Request.Body, want)
	}
	if !jsonEqual(t, result.Response.Body, want) {
		t.Errorf("response body: got %s, want %s", result.Response.Body, want)
	}
}

func TestRedactBodyPaths_TopLevelNumber(t *testing.T) {
	body := []byte(`{"balance":1234.56}`)
	tape := makeTapeWithBody(body, body)

	fn := RedactBodyPaths("$.balance")
	result := fn(tape)

	want := []byte(`{"balance":0}`)
	if !jsonEqual(t, result.Request.Body, want) {
		t.Errorf("request body: got %s, want %s", result.Request.Body, want)
	}
}

func TestRedactBodyPaths_TopLevelBool(t *testing.T) {
	body := []byte(`{"active":true}`)
	tape := makeTapeWithBody(body, body)

	fn := RedactBodyPaths("$.active")
	result := fn(tape)

	want := []byte(`{"active":false}`)
	if !jsonEqual(t, result.Request.Body, want) {
		t.Errorf("request body: got %s, want %s", result.Request.Body, want)
	}
}

func TestRedactBodyPaths_NestedField(t *testing.T) {
	body := []byte(`{"user":{"email":"a@b.c"}}`)
	tape := makeTapeWithBody(body, body)

	fn := RedactBodyPaths("$.user.email")
	result := fn(tape)

	want := []byte(`{"user":{"email":"[REDACTED]"}}`)
	if !jsonEqual(t, result.Request.Body, want) {
		t.Errorf("request body: got %s, want %s", result.Request.Body, want)
	}
}

func TestRedactBodyPaths_ArrayWildcard(t *testing.T) {
	body := []byte(`{"users":[{"ssn":"123"},{"ssn":"456"}]}`)
	tape := makeTapeWithBody(body, body)

	fn := RedactBodyPaths("$.users[*].ssn")
	result := fn(tape)

	want := []byte(`{"users":[{"ssn":"[REDACTED]"},{"ssn":"[REDACTED]"}]}`)
	if !jsonEqual(t, result.Request.Body, want) {
		t.Errorf("request body: got %s, want %s", result.Request.Body, want)
	}
}

func TestRedactBodyPaths_MultiplePaths(t *testing.T) {
	body := []byte(`{"a":"x","b":1}`)
	tape := makeTapeWithBody(body, body)

	fn := RedactBodyPaths("$.a", "$.b")
	result := fn(tape)

	want := []byte(`{"a":"[REDACTED]","b":0}`)
	if !jsonEqual(t, result.Request.Body, want) {
		t.Errorf("request body: got %s, want %s", result.Request.Body, want)
	}
}

func TestRedactBodyPaths_MissingPath(t *testing.T) {
	body := []byte(`{"foo":"bar"}`)
	tape := makeTapeWithBody(body, body)

	fn := RedactBodyPaths("$.nonexistent")
	result := fn(tape)

	want := []byte(`{"foo":"bar"}`)
	if !jsonEqual(t, result.Request.Body, want) {
		t.Errorf("request body: got %s, want %s", result.Request.Body, want)
	}
}

func TestRedactBodyPaths_NonJSONBody(t *testing.T) {
	body := []byte("plain text body")
	tape := makeTapeWithBody(body, body)

	fn := RedactBodyPaths("$.field")
	result := fn(tape)

	if string(result.Request.Body) != "plain text body" {
		t.Errorf("request body changed: got %q", result.Request.Body)
	}
	if string(result.Response.Body) != "plain text body" {
		t.Errorf("response body changed: got %q", result.Response.Body)
	}
}

func TestRedactBodyPaths_NilBody(t *testing.T) {
	tape := makeTapeWithBody(nil, nil)

	fn := RedactBodyPaths("$.field")
	result := fn(tape)

	if result.Request.Body != nil {
		t.Errorf("expected nil request body, got %v", result.Request.Body)
	}
	if result.Response.Body != nil {
		t.Errorf("expected nil response body, got %v", result.Response.Body)
	}
}

func TestRedactBodyPaths_EmptyBody(t *testing.T) {
	tape := makeTapeWithBody([]byte{}, []byte{})

	fn := RedactBodyPaths("$.field")
	result := fn(tape)

	if len(result.Request.Body) != 0 {
		t.Errorf("expected empty request body, got %v", result.Request.Body)
	}
	if len(result.Response.Body) != 0 {
		t.Errorf("expected empty response body, got %v", result.Response.Body)
	}
}

func TestRedactBodyPaths_NullValue(t *testing.T) {
	body := []byte(`{"token":null}`)
	tape := makeTapeWithBody(body, body)

	fn := RedactBodyPaths("$.token")
	result := fn(tape)

	want := []byte(`{"token":null}`)
	if !jsonEqual(t, result.Request.Body, want) {
		t.Errorf("request body: got %s, want %s", result.Request.Body, want)
	}
}

func TestRedactBodyPaths_ObjectValue(t *testing.T) {
	body := []byte(`{"data":{"nested":"val"}}`)
	tape := makeTapeWithBody(body, body)

	fn := RedactBodyPaths("$.data")
	result := fn(tape)

	want := []byte(`{"data":{"nested":"val"}}`)
	if !jsonEqual(t, result.Request.Body, want) {
		t.Errorf("request body: got %s, want %s", result.Request.Body, want)
	}
}

func TestRedactBodyPaths_ArrayValue(t *testing.T) {
	body := []byte(`{"items":[1,2]}`)
	tape := makeTapeWithBody(body, body)

	fn := RedactBodyPaths("$.items")
	result := fn(tape)

	want := []byte(`{"items":[1,2]}`)
	if !jsonEqual(t, result.Request.Body, want) {
		t.Errorf("request body: got %s, want %s", result.Request.Body, want)
	}
}

func TestRedactBodyPaths_BothRequestAndResponse(t *testing.T) {
	reqBody := []byte(`{"secret":"req-secret"}`)
	respBody := []byte(`{"secret":"resp-secret"}`)
	tape := makeTapeWithBody(reqBody, respBody)

	fn := RedactBodyPaths("$.secret")
	result := fn(tape)

	want := []byte(`{"secret":"[REDACTED]"}`)
	if !jsonEqual(t, result.Request.Body, want) {
		t.Errorf("request body: got %s, want %s", result.Request.Body, want)
	}
	if !jsonEqual(t, result.Response.Body, want) {
		t.Errorf("response body: got %s, want %s", result.Response.Body, want)
	}
}

func TestRedactBodyPaths_LeavesOriginalTapeUnchanged(t *testing.T) {
	body := []byte(`{"a":"b"}`)
	original := make([]byte, len(body))
	copy(original, body)
	tape := makeTapeWithBody(body, body)

	fn := RedactBodyPaths("$.a")
	_ = fn(tape)

	// Original tape body must be unchanged.
	if string(tape.Request.Body) != string(original) {
		t.Errorf("original request body mutated: got %q", tape.Request.Body)
	}
	if string(tape.Response.Body) != string(original) {
		t.Errorf("original response body mutated: got %q", tape.Response.Body)
	}
}

func TestRedactBodyPaths_BodyHashRecalculated(t *testing.T) {
	body := []byte(`{"pw":"x"}`)
	tape := makeTapeWithBody(body, body)
	originalHash := tape.Request.BodyHash

	fn := RedactBodyPaths("$.pw")
	result := fn(tape)

	// Hash should have changed since body was modified.
	if result.Request.BodyHash == originalHash {
		t.Error("expected BodyHash to change after body redaction")
	}

	// Hash should match the hash of the redacted body.
	expectedHash := BodyHashFromBytes(result.Request.Body)
	if result.Request.BodyHash != expectedHash {
		t.Errorf("BodyHash mismatch: got %q, want %q", result.Request.BodyHash, expectedHash)
	}
}

func TestRedactBodyPaths_InvalidPath(t *testing.T) {
	body := []byte(`{"a":"b"}`)
	tape := makeTapeWithBody(body, body)

	// "foo.bar" is invalid (missing $. prefix).
	fn := RedactBodyPaths("foo.bar")
	result := fn(tape)

	want := []byte(`{"a":"b"}`)
	if !jsonEqual(t, result.Request.Body, want) {
		t.Errorf("request body: got %s, want %s", result.Request.Body, want)
	}
}

func TestRedactBodyPaths_DeepNested(t *testing.T) {
	body := []byte(`{"a":{"b":{"c":"s"}}}`)
	tape := makeTapeWithBody(body, body)

	fn := RedactBodyPaths("$.a.b.c")
	result := fn(tape)

	want := []byte(`{"a":{"b":{"c":"[REDACTED]"}}}`)
	if !jsonEqual(t, result.Request.Body, want) {
		t.Errorf("request body: got %s, want %s", result.Request.Body, want)
	}
}

func TestRedactBodyPaths_NestedArrayWildcard(t *testing.T) {
	body := []byte(`{"d":{"rows":[{"v":"s"}]}}`)
	tape := makeTapeWithBody(body, body)

	fn := RedactBodyPaths("$.d.rows[*].v")
	result := fn(tape)

	want := []byte(`{"d":{"rows":[{"v":"[REDACTED]"}]}}`)
	if !jsonEqual(t, result.Request.Body, want) {
		t.Errorf("request body: got %s, want %s", result.Request.Body, want)
	}
}

func TestRedactBodyPaths_NoPaths(t *testing.T) {
	body := []byte(`{"a":"b"}`)
	tape := makeTapeWithBody(body, body)

	fn := RedactBodyPaths() // no paths => no-op
	result := fn(tape)

	want := []byte(`{"a":"b"}`)
	if !jsonEqual(t, result.Request.Body, want) {
		t.Errorf("request body: got %s, want %s", result.Request.Body, want)
	}
}

func TestRedactBodyPaths_ScalarBody(t *testing.T) {
	body := []byte(`"hello"`)
	tape := makeTapeWithBody(body, body)

	fn := RedactBodyPaths("$.field")
	result := fn(tape)

	// Scalar JSON body -- no fields to match, body unchanged.
	if string(result.Request.Body) != `"hello"` {
		t.Errorf("request body: got %s, want %s", result.Request.Body, `"hello"`)
	}
}

func TestRedactBodyPaths_MultipleWildcards(t *testing.T) {
	body := []byte(`{"data":{"rows":[{"tags":[{"value":"secret1"},{"value":"secret2"}]}]}}`)
	tape := makeTapeWithBody(body, body)

	fn := RedactBodyPaths("$.data.rows[*].tags[*].value")
	result := fn(tape)

	want := []byte(`{"data":{"rows":[{"tags":[{"value":"[REDACTED]"},{"value":"[REDACTED]"}]}]}}`)
	if !jsonEqual(t, result.Request.Body, want) {
		t.Errorf("request body: got %s, want %s", result.Request.Body, want)
	}
}

func TestRedactBodyPaths_ArrayElementNotObject(t *testing.T) {
	// Array elements are primitives, not objects -- path should be silently skipped.
	body := []byte(`{"items":[1,2,3]}`)
	tape := makeTapeWithBody(body, body)

	fn := RedactBodyPaths("$.items[*].field")
	result := fn(tape)

	want := []byte(`{"items":[1,2,3]}`)
	if !jsonEqual(t, result.Request.Body, want) {
		t.Errorf("request body: got %s, want %s", result.Request.Body, want)
	}
}

func TestRedactBodyPaths_BodyHashUnchangedForNonJSON(t *testing.T) {
	body := []byte("not json")
	tape := makeTapeWithBody(body, body)
	originalHash := tape.Request.BodyHash

	fn := RedactBodyPaths("$.field")
	result := fn(tape)

	if result.Request.BodyHash != originalHash {
		t.Errorf("BodyHash should not change for non-JSON body: got %q, want %q",
			result.Request.BodyHash, originalHash)
	}
}

func TestRedactBodyPaths_PipelineComposition(t *testing.T) {
	body := []byte(`{"secret":"value"}`)
	tape := Tape{
		ID:    "test-id",
		Route: "test-route",
		Request: RecordedReq{
			Method:   "POST",
			URL:      "https://example.com/test",
			Headers:  http.Header{"Authorization": {"Bearer token"}, "Content-Type": {"application/json"}},
			Body:     body,
			BodyHash: BodyHashFromBytes(body),
		},
		Response: RecordedResp{
			StatusCode: 200,
			Headers:    http.Header{"Content-Type": {"application/json"}},
			Body:       body,
		},
	}

	p := NewPipeline(
		RedactHeaders("Authorization"),
		RedactBodyPaths("$.secret"),
	)
	result := p.Sanitize(tape)

	// Headers should be redacted.
	if got := result.Request.Headers.Get("Authorization"); got != Redacted {
		t.Errorf("Authorization: expected %q, got %q", Redacted, got)
	}
	// Body should be redacted.
	want := []byte(`{"secret":"[REDACTED]"}`)
	if !jsonEqual(t, result.Request.Body, want) {
		t.Errorf("request body: got %s, want %s", result.Request.Body, want)
	}
}

// --- parsePath tests (tested through the exported API and indirectly) ---

func TestParsePath_InvalidPaths(t *testing.T) {
	invalidPaths := []string{
		"foo.bar",      // missing $ prefix
		"$",            // no dot after $
		"$.",           // empty after $.
		"$..foo",       // empty segment (double dot)
		"$.foo.",       // trailing dot (empty segment)
		"$.a[0]",       // index access not supported
		"$.a[0].b",     // index access not supported
		"$[*].field",   // missing key before [*] at root
		"",             // empty string
		"$.foo[1].bar", // numeric index not supported
	}

	body := []byte(`{"a":"b"}`)

	for _, path := range invalidPaths {
		tape := makeTapeWithBody(body, body)
		fn := RedactBodyPaths(path)
		result := fn(tape)

		want := []byte(`{"a":"b"}`)
		if !jsonEqual(t, result.Request.Body, want) {
			t.Errorf("path %q: expected body unchanged, got %s", path, result.Request.Body)
		}
	}
}

func TestRedactBodyPaths_WildcardAtLeaf(t *testing.T) {
	// Wildcard at leaf means the target is array elements (containers) -- should be skipped.
	body := []byte(`{"items":[{"a":1},{"a":2}]}`)
	tape := makeTapeWithBody(body, body)

	fn := RedactBodyPaths("$.items[*]")
	// This path parses to a single segment with wildcard=true.
	// But [*] requires something after it -- the segment key is "items" with wildcard,
	// and rest is empty. ADR says: skip.
	result := fn(tape)

	want := []byte(`{"items":[{"a":1},{"a":2}]}`)
	if !jsonEqual(t, result.Request.Body, want) {
		t.Errorf("request body: got %s, want %s", result.Request.Body, want)
	}
}

// --- FakeFields tests ---

func TestFakeFields_GenericString(t *testing.T) {
	body := []byte(`{"name":"Alice"}`)
	tape := makeTapeWithBody(body, body)

	fn := FakeFields("test-seed", "$.name")
	result := fn(tape)

	var got map[string]any
	if err := json.Unmarshal(result.Request.Body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	name, ok := got["name"].(string)
	if !ok {
		t.Fatal("name is not a string")
	}
	if !strings.HasPrefix(name, "fake_") {
		t.Errorf("expected fake_ prefix, got %q", name)
	}
	if len(name) != 13 { // "fake_" (5) + 8 hex chars
		t.Errorf("expected length 13, got %d (%q)", len(name), name)
	}
}

func TestFakeFields_Email(t *testing.T) {
	body := []byte(`{"email":"alice@corp.com"}`)
	tape := makeTapeWithBody(body, body)

	fn := FakeFields("test-seed", "$.email")
	result := fn(tape)

	var got map[string]any
	if err := json.Unmarshal(result.Request.Body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	email, ok := got["email"].(string)
	if !ok {
		t.Fatal("email is not a string")
	}
	if !strings.HasPrefix(email, "user_") {
		t.Errorf("expected user_ prefix, got %q", email)
	}
	if !strings.HasSuffix(email, "@example.com") {
		t.Errorf("expected @example.com suffix, got %q", email)
	}
}

func TestFakeFields_UUID(t *testing.T) {
	body := []byte(`{"id":"550e8400-e29b-41d4-a716-446655440000"}`)
	tape := makeTapeWithBody(body, body)

	fn := FakeFields("test-seed", "$.id")
	result := fn(tape)

	var got map[string]any
	if err := json.Unmarshal(result.Request.Body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	id, ok := got["id"].(string)
	if !ok {
		t.Fatal("id is not a string")
	}
	if len(id) != 36 {
		t.Errorf("expected UUID length 36, got %d (%q)", len(id), id)
	}
	// Check version nibble is 5.
	if id[14] != '5' {
		t.Errorf("expected version 5 at position 14, got %c in %q", id[14], id)
	}
	// Check hyphens at correct positions.
	for _, pos := range []int{8, 13, 18, 23} {
		if id[pos] != '-' {
			t.Errorf("expected '-' at position %d, got %c in %q", pos, id[pos], id)
		}
	}
}

func TestFakeFields_NumericID(t *testing.T) {
	body := []byte(`{"user_id":42}`)
	tape := makeTapeWithBody(body, body)

	fn := FakeFields("test-seed", "$.user_id")
	result := fn(tape)

	var got map[string]any
	if err := json.Unmarshal(result.Request.Body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	uid, ok := got["user_id"].(float64)
	if !ok {
		t.Fatal("user_id is not a number")
	}
	if uid <= 0 {
		t.Errorf("expected positive number, got %f", uid)
	}
	if uid != float64(int64(uid)) {
		t.Errorf("expected integer, got %f", uid)
	}
	if uid > float64(1<<31-1) {
		t.Errorf("expected value <= 2^31-1, got %f", uid)
	}
}

func TestFakeFields_BoolUnchanged(t *testing.T) {
	body := []byte(`{"active":true}`)
	tape := makeTapeWithBody(body, body)

	fn := FakeFields("test-seed", "$.active")
	result := fn(tape)

	want := []byte(`{"active":true}`)
	if !jsonEqual(t, result.Request.Body, want) {
		t.Errorf("request body: got %s, want %s", result.Request.Body, want)
	}
}

func TestFakeFields_NullUnchanged(t *testing.T) {
	body := []byte(`{"token":null}`)
	tape := makeTapeWithBody(body, body)

	fn := FakeFields("test-seed", "$.token")
	result := fn(tape)

	want := []byte(`{"token":null}`)
	if !jsonEqual(t, result.Request.Body, want) {
		t.Errorf("request body: got %s, want %s", result.Request.Body, want)
	}
}

func TestFakeFields_ObjectUnchanged(t *testing.T) {
	body := []byte(`{"data":{"nested":"val"}}`)
	tape := makeTapeWithBody(body, body)

	fn := FakeFields("test-seed", "$.data")
	result := fn(tape)

	want := []byte(`{"data":{"nested":"val"}}`)
	if !jsonEqual(t, result.Request.Body, want) {
		t.Errorf("request body: got %s, want %s", result.Request.Body, want)
	}
}

func TestFakeFields_ArrayUnchanged(t *testing.T) {
	body := []byte(`{"items":[1,2]}`)
	tape := makeTapeWithBody(body, body)

	fn := FakeFields("test-seed", "$.items")
	result := fn(tape)

	want := []byte(`{"items":[1,2]}`)
	if !jsonEqual(t, result.Request.Body, want) {
		t.Errorf("request body: got %s, want %s", result.Request.Body, want)
	}
}

func TestFakeFields_Deterministic(t *testing.T) {
	body := []byte(`{"name":"Alice","email":"alice@corp.com","id":"550e8400-e29b-41d4-a716-446655440000","score":99}`)
	tape := makeTapeWithBody(body, body)

	fn := FakeFields("test-seed", "$.name", "$.email", "$.id", "$.score")

	// Run twice and verify identical output.
	result1 := fn(tape)
	result2 := fn(tape)

	if string(result1.Request.Body) != string(result2.Request.Body) {
		t.Errorf("not deterministic:\nfirst:  %s\nsecond: %s", result1.Request.Body, result2.Request.Body)
	}
	if string(result1.Response.Body) != string(result2.Response.Body) {
		t.Errorf("response not deterministic:\nfirst:  %s\nsecond: %s", result1.Response.Body, result2.Response.Body)
	}
}

func TestFakeFields_CrossFixtureConsistency(t *testing.T) {
	// Same value in two different fixtures should produce the same fake.
	body1 := []byte(`{"email":"shared@corp.com","other":"a"}`)
	body2 := []byte(`{"email":"shared@corp.com","other":"b"}`)
	tape1 := makeTapeWithBody(body1, body1)
	tape2 := makeTapeWithBody(body2, body2)

	fn := FakeFields("test-seed", "$.email")

	result1 := fn(tape1)
	result2 := fn(tape2)

	var got1, got2 map[string]any
	if err := json.Unmarshal(result1.Request.Body, &got1); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := json.Unmarshal(result2.Request.Body, &got2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got1["email"] != got2["email"] {
		t.Errorf("same email should produce same fake across fixtures: %q vs %q", got1["email"], got2["email"])
	}
}

func TestFakeFields_DifferentSeedsDifferentOutput(t *testing.T) {
	body := []byte(`{"name":"Alice"}`)
	tape := makeTapeWithBody(body, body)

	fn1 := FakeFields("seed-a", "$.name")
	fn2 := FakeFields("seed-b", "$.name")

	result1 := fn1(tape)
	result2 := fn2(tape)

	if string(result1.Request.Body) == string(result2.Request.Body) {
		t.Error("different seeds should produce different fakes")
	}
}

func TestFakeFields_DifferentInputsDifferentOutput(t *testing.T) {
	body1 := []byte(`{"name":"Alice"}`)
	body2 := []byte(`{"name":"Bob"}`)
	tape1 := makeTapeWithBody(body1, body1)
	tape2 := makeTapeWithBody(body2, body2)

	fn := FakeFields("test-seed", "$.name")

	result1 := fn(tape1)
	result2 := fn(tape2)

	if string(result1.Request.Body) == string(result2.Request.Body) {
		t.Error("different inputs should produce different fakes")
	}
}

func TestFakeFields_NestedField(t *testing.T) {
	body := []byte(`{"user":{"email":"a@b.c"}}`)
	tape := makeTapeWithBody(body, body)

	fn := FakeFields("test-seed", "$.user.email")
	result := fn(tape)

	var got map[string]any
	if err := json.Unmarshal(result.Request.Body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	user := got["user"].(map[string]any)
	email := user["email"].(string)
	if !strings.HasPrefix(email, "user_") || !strings.HasSuffix(email, "@example.com") {
		t.Errorf("expected fake email, got %q", email)
	}
}

func TestFakeFields_ArrayWildcard(t *testing.T) {
	body := []byte(`{"users":[{"name":"Alice"},{"name":"Bob"}]}`)
	tape := makeTapeWithBody(body, body)

	fn := FakeFields("test-seed", "$.users[*].name")
	result := fn(tape)

	var got map[string]any
	if err := json.Unmarshal(result.Request.Body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	users := got["users"].([]any)
	for i, u := range users {
		name := u.(map[string]any)["name"].(string)
		if !strings.HasPrefix(name, "fake_") {
			t.Errorf("users[%d].name: expected fake_ prefix, got %q", i, name)
		}
	}
	// Alice and Bob should produce different fakes.
	name0 := users[0].(map[string]any)["name"].(string)
	name1 := users[1].(map[string]any)["name"].(string)
	if name0 == name1 {
		t.Errorf("different names should produce different fakes: %q == %q", name0, name1)
	}
}

func TestFakeFields_MultiplePaths(t *testing.T) {
	body := []byte(`{"name":"Alice","email":"alice@corp.com"}`)
	tape := makeTapeWithBody(body, body)

	fn := FakeFields("test-seed", "$.name", "$.email")
	result := fn(tape)

	var got map[string]any
	if err := json.Unmarshal(result.Request.Body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	name := got["name"].(string)
	email := got["email"].(string)
	if !strings.HasPrefix(name, "fake_") {
		t.Errorf("name: expected fake_ prefix, got %q", name)
	}
	if !strings.HasPrefix(email, "user_") {
		t.Errorf("email: expected user_ prefix, got %q", email)
	}
}

func TestFakeFields_MissingPath(t *testing.T) {
	body := []byte(`{"foo":"bar"}`)
	tape := makeTapeWithBody(body, body)

	fn := FakeFields("test-seed", "$.nonexistent")
	result := fn(tape)

	want := []byte(`{"foo":"bar"}`)
	if !jsonEqual(t, result.Request.Body, want) {
		t.Errorf("request body: got %s, want %s", result.Request.Body, want)
	}
}

func TestFakeFields_NonJSONBody(t *testing.T) {
	body := []byte("plain text body")
	tape := makeTapeWithBody(body, body)

	fn := FakeFields("test-seed", "$.field")
	result := fn(tape)

	if string(result.Request.Body) != "plain text body" {
		t.Errorf("request body changed: got %q", result.Request.Body)
	}
}

func TestFakeFields_NilBody(t *testing.T) {
	tape := makeTapeWithBody(nil, nil)

	fn := FakeFields("test-seed", "$.field")
	result := fn(tape)

	if result.Request.Body != nil {
		t.Errorf("expected nil request body, got %v", result.Request.Body)
	}
	if result.Response.Body != nil {
		t.Errorf("expected nil response body, got %v", result.Response.Body)
	}
}

func TestFakeFields_EmptyBody(t *testing.T) {
	tape := makeTapeWithBody([]byte{}, []byte{})

	fn := FakeFields("test-seed", "$.field")
	result := fn(tape)

	if len(result.Request.Body) != 0 {
		t.Errorf("expected empty request body, got %v", result.Request.Body)
	}
}

func TestFakeFields_BothRequestAndResponse(t *testing.T) {
	reqBody := []byte(`{"name":"req-name"}`)
	respBody := []byte(`{"name":"resp-name"}`)
	tape := makeTapeWithBody(reqBody, respBody)

	fn := FakeFields("test-seed", "$.name")
	result := fn(tape)

	var reqGot, respGot map[string]any
	if err := json.Unmarshal(result.Request.Body, &reqGot); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if err := json.Unmarshal(result.Response.Body, &respGot); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	reqName := reqGot["name"].(string)
	respName := respGot["name"].(string)
	if !strings.HasPrefix(reqName, "fake_") {
		t.Errorf("request name: expected fake_ prefix, got %q", reqName)
	}
	if !strings.HasPrefix(respName, "fake_") {
		t.Errorf("response name: expected fake_ prefix, got %q", respName)
	}
	// Different original values should produce different fakes.
	if reqName == respName {
		t.Errorf("different original values should produce different fakes: %q == %q", reqName, respName)
	}
}

func TestFakeFields_LeavesOriginalTapeUnchanged(t *testing.T) {
	body := []byte(`{"name":"Alice"}`)
	original := make([]byte, len(body))
	copy(original, body)
	tape := makeTapeWithBody(body, body)

	fn := FakeFields("test-seed", "$.name")
	_ = fn(tape)

	if string(tape.Request.Body) != string(original) {
		t.Errorf("original request body mutated: got %q", tape.Request.Body)
	}
	if string(tape.Response.Body) != string(original) {
		t.Errorf("original response body mutated: got %q", tape.Response.Body)
	}
}

func TestFakeFields_BodyHashRecalculated(t *testing.T) {
	body := []byte(`{"name":"Alice"}`)
	tape := makeTapeWithBody(body, body)
	originalHash := tape.Request.BodyHash

	fn := FakeFields("test-seed", "$.name")
	result := fn(tape)

	if result.Request.BodyHash == originalHash {
		t.Error("expected BodyHash to change after body faking")
	}
	expectedHash := BodyHashFromBytes(result.Request.Body)
	if result.Request.BodyHash != expectedHash {
		t.Errorf("BodyHash mismatch: got %q, want %q", result.Request.BodyHash, expectedHash)
	}
}

func TestFakeFields_InvalidPath(t *testing.T) {
	body := []byte(`{"a":"b"}`)
	tape := makeTapeWithBody(body, body)

	fn := FakeFields("test-seed", "foo.bar")
	result := fn(tape)

	want := []byte(`{"a":"b"}`)
	if !jsonEqual(t, result.Request.Body, want) {
		t.Errorf("request body: got %s, want %s", result.Request.Body, want)
	}
}

func TestFakeFields_NoPaths(t *testing.T) {
	body := []byte(`{"a":"b"}`)
	tape := makeTapeWithBody(body, body)

	fn := FakeFields("test-seed") // no paths => no-op
	result := fn(tape)

	want := []byte(`{"a":"b"}`)
	if !jsonEqual(t, result.Request.Body, want) {
		t.Errorf("request body: got %s, want %s", result.Request.Body, want)
	}
}

func TestFakeFields_PipelineComposition(t *testing.T) {
	body := []byte(`{"secret":"value","name":"Alice"}`)
	tape := Tape{
		ID:    "test-id",
		Route: "test-route",
		Request: RecordedReq{
			Method:   "POST",
			URL:      "https://example.com/test",
			Headers:  http.Header{"Authorization": {"Bearer token"}, "Content-Type": {"application/json"}},
			Body:     body,
			BodyHash: BodyHashFromBytes(body),
		},
		Response: RecordedResp{
			StatusCode: 200,
			Headers:    http.Header{"Content-Type": {"application/json"}},
			Body:       body,
		},
	}

	p := NewPipeline(
		RedactHeaders("Authorization"),
		FakeFields("test-seed", "$.name"),
	)
	result := p.Sanitize(tape)

	// Headers should be redacted.
	if got := result.Request.Headers.Get("Authorization"); got != Redacted {
		t.Errorf("Authorization: expected %q, got %q", Redacted, got)
	}
	// Name should be faked.
	var got map[string]any
	if err := json.Unmarshal(result.Request.Body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	name := got["name"].(string)
	if !strings.HasPrefix(name, "fake_") {
		t.Errorf("name: expected fake_ prefix, got %q", name)
	}
	// Secret should be unchanged (not targeted).
	if got["secret"] != "value" {
		t.Errorf("secret should be unchanged, got %q", got["secret"])
	}
}

func TestFakeFields_WildcardAtLeaf(t *testing.T) {
	body := []byte(`{"items":[{"a":1},{"a":2}]}`)
	tape := makeTapeWithBody(body, body)

	fn := FakeFields("test-seed", "$.items[*]")
	result := fn(tape)

	want := []byte(`{"items":[{"a":1},{"a":2}]}`)
	if !jsonEqual(t, result.Request.Body, want) {
		t.Errorf("request body: got %s, want %s", result.Request.Body, want)
	}
}

// --- isEmail tests ---

func TestIsEmail(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"alice@corp.com", true},
		{"a@b", true},
		{"user@example.com", true},
		{"", false},
		{"@", false},
		{"@domain", false},
		{"user@", false},
		{"no-at-sign", false},
		{"two@@ats", false},
		{"a@b@c", false},
	}
	for _, tt := range tests {
		got := isEmail(tt.input)
		if got != tt.want {
			t.Errorf("isEmail(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

// --- isUUID tests ---

func TestIsUUID(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"550e8400-e29b-41d4-a716-446655440000", true},
		{"00000000-0000-0000-0000-000000000000", true},
		{"ABCDEF01-2345-6789-abcd-ef0123456789", true},
		{"", false},
		{"not-a-uuid", false},
		{"550e8400e29b41d4a716446655440000", false},      // no hyphens
		{"550e8400-e29b-41d4-a716-44665544000", false},   // too short
		{"550e8400-e29b-41d4-a716-4466554400000", false}, // too long
		{"550e8400-e29b-41d4-a716-44665544000g", false},  // invalid hex char
		{"550e8400+e29b-41d4-a716-446655440000", false},  // wrong separator
	}
	for _, tt := range tests {
		got := isUUID(tt.input)
		if got != tt.want {
			t.Errorf("isUUID(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestFakeFields_FloatNumber(t *testing.T) {
	body := []byte(`{"price":3.14}`)
	tape := makeTapeWithBody(body, body)

	fn := FakeFields("test-seed", "$.price")
	result := fn(tape)

	var got map[string]any
	if err := json.Unmarshal(result.Request.Body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	price, ok := got["price"].(float64)
	if !ok {
		t.Fatal("price is not a number")
	}
	if price <= 0 {
		t.Errorf("expected positive number, got %f", price)
	}
	// Must be an integer (fakeNumericID always returns integers).
	if price != float64(int64(price)) {
		t.Errorf("expected integer, got %f", price)
	}
}

func TestFakeFields_ScalarBody(t *testing.T) {
	body := []byte(`"hello"`)
	tape := makeTapeWithBody(body, body)

	fn := FakeFields("test-seed", "$.field")
	result := fn(tape)

	if string(result.Request.Body) != `"hello"` {
		t.Errorf("request body: got %s, want %s", result.Request.Body, `"hello"`)
	}
}

func TestFakeFields_DeepNested(t *testing.T) {
	body := []byte(`{"a":{"b":{"c":"secret"}}}`)
	tape := makeTapeWithBody(body, body)

	fn := FakeFields("test-seed", "$.a.b.c")
	result := fn(tape)

	var got map[string]any
	if err := json.Unmarshal(result.Request.Body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	c := got["a"].(map[string]any)["b"].(map[string]any)["c"].(string)
	if !strings.HasPrefix(c, "fake_") {
		t.Errorf("expected fake_ prefix, got %q", c)
	}
}

func TestFakeFields_BodyHashUnchangedForNonJSON(t *testing.T) {
	body := []byte("not json")
	tape := makeTapeWithBody(body, body)
	originalHash := tape.Request.BodyHash

	fn := FakeFields("test-seed", "$.field")
	result := fn(tape)

	if result.Request.BodyHash != originalHash {
		t.Errorf("BodyHash should not change for non-JSON body: got %q, want %q",
			result.Request.BodyHash, originalHash)
	}
}

// --- Benchmarks ---

// generateJSONBody creates a JSON object with n user entries, each containing
// email, id, name, and nested tokens array. Returns the JSON bytes.
func generateJSONBody(n int) []byte {
	type token struct {
		Value string `json:"value"`
	}
	type user struct {
		Email  string  `json:"email"`
		ID     int     `json:"id"`
		Name   string  `json:"name"`
		Tokens []token `json:"tokens"`
	}
	type body struct {
		Users []user `json:"users"`
	}

	b := body{Users: make([]user, n)}
	for i := 0; i < n; i++ {
		b.Users[i] = user{
			Email:  fmt.Sprintf("user%d@test.com", i),
			ID:     1000 + i,
			Name:   fmt.Sprintf("User %d", i),
			Tokens: []token{{Value: fmt.Sprintf("tok%d", i)}},
		}
	}

	data, _ := json.Marshal(b)
	return data
}

func makeBenchTapeWithBody(bodyBytes []byte) Tape {
	return Tape{
		ID:    "bench-tape",
		Route: "bench-route",
		Request: RecordedReq{
			Method:   "POST",
			URL:      "https://example.com/api/users",
			Headers:  http.Header{"Content-Type": {"application/json"}, "Authorization": {"Bearer secret-token"}},
			Body:     bodyBytes,
			BodyHash: BodyHashFromBytes(bodyBytes),
		},
		Response: RecordedResp{
			StatusCode: 200,
			Headers:    http.Header{"Content-Type": {"application/json"}, "Set-Cookie": {"session=abc123"}},
			Body:       bodyBytes,
		},
	}
}

// BenchmarkSanitizer_RedactBodyPaths measures RedactBodyPaths throughput.
func BenchmarkSanitizer_RedactBodyPaths(b *testing.B) {
	sizes := []struct {
		name  string
		users int
	}{
		{"1KB", 5},
		{"10KB", 60},
		{"100KB", 600},
	}

	for _, sz := range sizes {
		b.Run(sz.name, func(b *testing.B) {
			body := generateJSONBody(sz.users)
			tape := makeBenchTapeWithBody(body)
			pipeline := NewPipeline(RedactBodyPaths("$.users[*].email", "$.users[*].id"))

			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				pipeline.Sanitize(tape)
			}
		})
	}
}

// BenchmarkSanitizer_FakeFields measures FakeFields throughput (includes HMAC).
func BenchmarkSanitizer_FakeFields(b *testing.B) {
	sizes := []struct {
		name  string
		users int
	}{
		{"1KB", 5},
		{"10KB", 60},
		{"100KB", 600},
	}

	for _, sz := range sizes {
		b.Run(sz.name, func(b *testing.B) {
			body := generateJSONBody(sz.users)
			tape := makeBenchTapeWithBody(body)
			pipeline := NewPipeline(FakeFields("bench-seed", "$.users[*].email", "$.users[*].id"))

			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				pipeline.Sanitize(tape)
			}
		})
	}
}

// BenchmarkSanitizer_FullPipeline measures a realistic pipeline with
// RedactHeaders + RedactBodyPaths + FakeFields combined.
func BenchmarkSanitizer_FullPipeline(b *testing.B) {
	sizes := []struct {
		name  string
		users int
	}{
		{"1KB", 5},
		{"10KB", 60},
		{"100KB", 600},
	}

	for _, sz := range sizes {
		b.Run(sz.name, func(b *testing.B) {
			body := generateJSONBody(sz.users)
			tape := makeBenchTapeWithBody(body)
			pipeline := NewPipeline(
				RedactHeaders(),
				RedactBodyPaths("$.users[*].email"),
				FakeFields("bench-seed", "$.users[*].id", "$.users[*].name"),
			)

			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				pipeline.Sanitize(tape)
			}
		})
	}
}

// --- RedactQueryParams tests ---

// makeTapeWithURL creates a minimal Tape with the given request URL.
func makeTapeWithURL(rawURL string) Tape {
	return Tape{
		ID:    "test-id",
		Route: "test-route",
		Request: RecordedReq{
			Method:  "GET",
			URL:     rawURL,
			Headers: http.Header{"Content-Type": {"application/json"}},
			Body:    []byte("request body"),
		},
		Response: RecordedResp{
			StatusCode: 200,
			Headers:    http.Header{"Content-Type": {"application/json"}},
			Body:       []byte("response body"),
		},
	}
}

func TestRedactQueryParams_MatchingParamIsRedacted(t *testing.T) {
	tape := makeTapeWithURL("https://example.com/api?api_key=supersecret&other=value")
	fn := RedactQueryParams("api_key")
	result := fn(tape)

	u, err := parseURL(t, result.Request.URL)
	if err != nil {
		return
	}
	if got := u.Query().Get("api_key"); got != Redacted {
		t.Errorf("api_key: expected %q, got %q", Redacted, got)
	}
	if got := u.Query().Get("other"); got != "value" {
		t.Errorf("other: expected %q, got %q", "value", got)
	}
}

func TestRedactQueryParams_NonMatchingParamIsUntouched(t *testing.T) {
	tape := makeTapeWithURL("https://example.com/api?keep=value1&secret=value2")
	fn := RedactQueryParams("secret")
	result := fn(tape)

	u, err := parseURL(t, result.Request.URL)
	if err != nil {
		return
	}
	if got := u.Query().Get("keep"); got != "value1" {
		t.Errorf("keep: expected %q, got %q", "value1", got)
	}
	if got := u.Query().Get("secret"); got != Redacted {
		t.Errorf("secret: expected %q, got %q", Redacted, got)
	}
}

func TestRedactQueryParams_MultipleValuesForSameKeyAreEachRedacted(t *testing.T) {
	tape := makeTapeWithURL("https://example.com/api?token=first&token=second")
	fn := RedactQueryParams("token")
	result := fn(tape)

	u, err := parseURL(t, result.Request.URL)
	if err != nil {
		return
	}
	vals := u.Query()["token"]
	if len(vals) != 2 {
		t.Fatalf("expected 2 token values, got %d", len(vals))
	}
	for i, v := range vals {
		if v != Redacted {
			t.Errorf("token[%d]: expected %q, got %q", i, Redacted, v)
		}
	}
}

func TestRedactQueryParams_DefaultsAreUsedWhenNoNamesProvided(t *testing.T) {
	tape := makeTapeWithURL("https://example.com/api?api_key=secret&other=keep")
	fn := RedactQueryParams() // no args => defaults
	result := fn(tape)

	u, err := parseURL(t, result.Request.URL)
	if err != nil {
		return
	}
	if got := u.Query().Get("api_key"); got != Redacted {
		t.Errorf("api_key: expected %q, got %q", Redacted, got)
	}
	if got := u.Query().Get("other"); got != "keep" {
		t.Errorf("other: expected %q, got %q", "keep", got)
	}
}

func TestRedactQueryParams_NameMatchingIsCaseSensitive(t *testing.T) {
	tape := makeTapeWithURL("https://example.com/api?api_key=secret&API_KEY=other")
	fn := RedactQueryParams("api_key")
	result := fn(tape)

	u, err := parseURL(t, result.Request.URL)
	if err != nil {
		return
	}
	if got := u.Query().Get("api_key"); got != Redacted {
		t.Errorf("api_key: expected %q, got %q", Redacted, got)
	}
	// API_KEY must NOT be redacted (different case).
	if got := u.Query().Get("API_KEY"); got != "other" {
		t.Errorf("API_KEY: expected %q (unchanged), got %q", "other", got)
	}
}

func TestRedactQueryParams_UserinfoIsAlwaysStripped(t *testing.T) {
	tape := makeTapeWithURL("https://user:pass@host/path?other=value")
	fn := RedactQueryParams("secret") // no matching param
	result := fn(tape)

	u, err := parseURL(t, result.Request.URL)
	if err != nil {
		return
	}
	if u.User == nil {
		t.Fatal("expected User field to be set, got nil")
	}
	if got := u.User.Username(); got != Redacted {
		t.Errorf("username: expected %q, got %q", Redacted, got)
	}
	if _, hasPassword := u.User.Password(); hasPassword {
		t.Error("password should be cleared after userinfo redaction")
	}
	if got := u.Query().Get("other"); got != "value" {
		t.Errorf("other: expected %q (unchanged), got %q", "value", got)
	}
}

func TestRedactQueryParams_UserinfoAndParamBothRedacted(t *testing.T) {
	tape := makeTapeWithURL("https://user:pass@host/path?api_key=secret")
	fn := RedactQueryParams("api_key")
	result := fn(tape)

	u, err := parseURL(t, result.Request.URL)
	if err != nil {
		return
	}
	if u.User == nil {
		t.Fatal("expected User field to be set")
	}
	if got := u.User.Username(); got != Redacted {
		t.Errorf("username: expected %q, got %q", Redacted, got)
	}
	if got := u.Query().Get("api_key"); got != Redacted {
		t.Errorf("api_key: expected %q, got %q", Redacted, got)
	}
}

func TestRedactQueryParams_URLWithNoQueryParamsAndNoUserinfoIsByteStable(t *testing.T) {
	rawURL := "https://example.com/api/v1/resource"
	tape := makeTapeWithURL(rawURL)
	fn := RedactQueryParams("api_key")
	result := fn(tape)

	if result.Request.URL != rawURL {
		t.Errorf("URL changed unexpectedly: got %q, want %q", result.Request.URL, rawURL)
	}
}

func TestRedactQueryParams_URLWithNonMatchingParamsAndNoUserinfoIsByteStable(t *testing.T) {
	rawURL := "https://example.com/api?foo=bar&baz=qux"
	tape := makeTapeWithURL(rawURL)
	fn := RedactQueryParams("api_key")
	result := fn(tape)

	if result.Request.URL != rawURL {
		t.Errorf("URL changed unexpectedly: got %q, want %q", result.Request.URL, rawURL)
	}
}

func TestRedactQueryParams_MalformedURLIsSilentlySkipped(t *testing.T) {
	rawURL := "://malformed url"
	tape := makeTapeWithURL(rawURL)
	fn := RedactQueryParams("api_key")
	result := fn(tape)

	if result.Request.URL != rawURL {
		t.Errorf("expected URL unchanged on parse error, got %q", result.Request.URL)
	}
}

func TestRedactQueryParams_OnlyRequestURLIsModified(t *testing.T) {
	tape := makeTapeWithURL("https://example.com/api?api_key=secret")
	originalHeaders := tape.Request.Headers.Clone()
	originalBody := string(tape.Request.Body)
	originalRespBody := string(tape.Response.Body)

	fn := RedactQueryParams("api_key")
	result := fn(tape)

	// URL should be changed.
	if result.Request.URL == tape.Request.URL {
		t.Error("expected URL to change")
	}

	// Headers, body, and response must be untouched.
	if result.Request.Headers.Get("Content-Type") != originalHeaders.Get("Content-Type") {
		t.Error("request headers changed unexpectedly")
	}
	if string(result.Request.Body) != originalBody {
		t.Errorf("request body changed: got %q", result.Request.Body)
	}
	if string(result.Response.Body) != originalRespBody {
		t.Errorf("response body changed: got %q", result.Response.Body)
	}
}

func TestRedactQueryParams_DoesNotMutateInputTape(t *testing.T) {
	originalURL := "https://example.com/api?api_key=secret"
	tape := makeTapeWithURL(originalURL)

	fn := RedactQueryParams("api_key")
	_ = fn(tape)

	if tape.Request.URL != originalURL {
		t.Errorf("original tape mutated: URL is now %q", tape.Request.URL)
	}
}

func TestRedactQueryParams_SanitizationHappensOnWrite(t *testing.T) {
	// Verify that the secret value does not appear in the sanitized URL.
	tape := makeTapeWithURL("https://example.com/api?api_key=my-super-secret")
	pipeline := NewPipeline(RedactQueryParams("api_key"))
	result := pipeline.Sanitize(tape)

	if strings.Contains(result.Request.URL, "my-super-secret") {
		t.Errorf("secret value still present in URL: %q", result.Request.URL)
	}
}

// --- FakeQueryParams tests ---

func TestFakeQueryParams_MatchingParamIsFaked(t *testing.T) {
	tape := makeTapeWithURL("https://example.com/api?token=original&other=keep")
	fn := FakeQueryParams("test-seed", "token")
	result := fn(tape)

	u, err := parseURL(t, result.Request.URL)
	if err != nil {
		return
	}
	got := u.Query().Get("token")
	if !strings.HasPrefix(got, "fake_") {
		t.Errorf("token: expected fake_ prefix, got %q", got)
	}
	if got == "original" {
		t.Error("token: value should have changed")
	}
	if u.Query().Get("other") != "keep" {
		t.Errorf("other: expected %q (unchanged), got %q", "keep", u.Query().Get("other"))
	}
}

func TestFakeQueryParams_SameSeedAndValueProduceSameFakeAcrossCalls(t *testing.T) {
	tape := makeTapeWithURL("https://example.com/api?token=original")
	fn := FakeQueryParams("test-seed", "token")

	result1 := fn(tape)
	result2 := fn(tape)

	if result1.Request.URL != result2.Request.URL {
		t.Errorf("not deterministic:\nfirst:  %q\nsecond: %q", result1.Request.URL, result2.Request.URL)
	}
}

func TestFakeQueryParams_DifferentValuesProduceDifferentFakes(t *testing.T) {
	tape1 := makeTapeWithURL("https://example.com/api?token=alice")
	tape2 := makeTapeWithURL("https://example.com/api?token=bob")
	fn := FakeQueryParams("test-seed", "token")

	result1 := fn(tape1)
	result2 := fn(tape2)

	u1, err1 := parseURL(t, result1.Request.URL)
	u2, err2 := parseURL(t, result2.Request.URL)
	if err1 != nil || err2 != nil {
		return
	}

	if u1.Query().Get("token") == u2.Query().Get("token") {
		t.Errorf("different inputs should produce different fakes: both got %q", u1.Query().Get("token"))
	}
}

func TestFakeQueryParams_UserinfoIsAlwaysStripped(t *testing.T) {
	tape := makeTapeWithURL("https://user:pass@host/path?other=value")
	fn := FakeQueryParams("test-seed", "token") // no matching param
	result := fn(tape)

	u, err := parseURL(t, result.Request.URL)
	if err != nil {
		return
	}
	if u.User == nil {
		t.Fatal("expected User field to be set, got nil")
	}
	if got := u.User.Username(); got != Redacted {
		t.Errorf("username: expected %q, got %q", Redacted, got)
	}
	if _, hasPassword := u.User.Password(); hasPassword {
		t.Error("password should be cleared after userinfo redaction")
	}
}

func TestFakeQueryParams_MultipleValuesForSameKeyAreEachFaked(t *testing.T) {
	tape := makeTapeWithURL("https://example.com/api?token=first&token=second")
	fn := FakeQueryParams("test-seed", "token")
	result := fn(tape)

	u, err := parseURL(t, result.Request.URL)
	if err != nil {
		return
	}
	vals := u.Query()["token"]
	if len(vals) != 2 {
		t.Fatalf("expected 2 token values, got %d", len(vals))
	}
	for i, v := range vals {
		if !strings.HasPrefix(v, "fake_") {
			t.Errorf("token[%d]: expected fake_ prefix, got %q", i, v)
		}
	}
	// Different original values should produce different fakes.
	if vals[0] == vals[1] {
		t.Errorf("different original values should produce different fakes: %q == %q", vals[0], vals[1])
	}
}

func TestFakeQueryParams_URLWithNonMatchingParamsAndNoUserinfoIsByteStable(t *testing.T) {
	rawURL := "https://example.com/api?foo=bar&baz=qux"
	tape := makeTapeWithURL(rawURL)
	fn := FakeQueryParams("test-seed", "api_key")
	result := fn(tape)

	if result.Request.URL != rawURL {
		t.Errorf("URL changed unexpectedly: got %q, want %q", result.Request.URL, rawURL)
	}
}

func TestFakeQueryParams_MalformedURLIsSilentlySkipped(t *testing.T) {
	rawURL := "://malformed url"
	tape := makeTapeWithURL(rawURL)
	fn := FakeQueryParams("test-seed", "api_key")
	result := fn(tape)

	if result.Request.URL != rawURL {
		t.Errorf("expected URL unchanged on parse error, got %q", result.Request.URL)
	}
}

// --- Malformed percent-encoding (fail-closed) regression tests ---

// TestRedactQueryParams_MalformedPercentEncodingSecretDoesNotReachPersistedURL is a
// security regression test for the fail-open leak in sanitizeURL. Previously,
// u.Query() silently dropped params with malformed percent-encoding, leaving the
// cleartext secret in the raw URL that would be written to disk.
func TestRedactQueryParams_MalformedPercentEncodingSecretDoesNotReachPersistedURL(t *testing.T) {
	tests := []struct {
		name   string
		rawURL string
		param  string
		secret string
	}{
		{
			name:   "invalid percent sequence in sensitive param value",
			rawURL: "https://example.com/api?api_key=ab%ZZcd&other=value",
			param:  "api_key",
			secret: "ab%ZZcd",
		},
		{
			name:   "truncated percent at end of value",
			rawURL: "https://example.com/api?token=secret%",
			param:  "token",
			secret: "secret%",
		},
		{
			name:   "percent followed by one hex digit only",
			rawURL: "https://example.com/api?password=sec%2ret",
			param:  "password",
			secret: "sec%2ret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tape := makeTapeWithURL(tt.rawURL)
			fn := RedactQueryParams(tt.param)
			result := fn(tape)

			// The secret must NOT appear anywhere in the persisted URL.
			if strings.Contains(result.Request.URL, tt.secret) {
				t.Errorf("cleartext secret %q still present in persisted URL: %q",
					tt.secret, result.Request.URL)
			}

			// The result URL must not be the original raw URL (which contains
			// the cleartext secret).
			if result.Request.URL == tt.rawURL {
				t.Errorf("URL was returned byte-identical to raw input; secret not redacted: %q",
					result.Request.URL)
			}
		})
	}
}

func TestFakeQueryParams_MalformedPercentEncodingSecretDoesNotReachPersistedURL(t *testing.T) {
	rawURL := "https://example.com/api?api_key=ab%ZZcd&other=value"
	tape := makeTapeWithURL(rawURL)
	fn := FakeQueryParams("test-seed", "api_key")
	result := fn(tape)

	// The malformed secret must NOT appear in the persisted URL.
	if strings.Contains(result.Request.URL, "ab%ZZcd") {
		t.Errorf("cleartext secret still present in persisted URL: %q", result.Request.URL)
	}
	// Must not be byte-identical to the raw input.
	if result.Request.URL == rawURL {
		t.Errorf("URL returned byte-identical to raw input; secret not sanitized: %q", result.Request.URL)
	}
}

// TestRedactQueryParams_MalformedQueryStringStillStripsUserinfo verifies that when
// the query string is malformed, userinfo (which was already stripped before the
// query parse attempt) is not leaked back into the output.
func TestRedactQueryParams_MalformedQueryStringStillStripsUserinfo(t *testing.T) {
	rawURL := "https://user:pass@example.com/api?api_key=ab%ZZcd"
	tape := makeTapeWithURL(rawURL)
	fn := RedactQueryParams("api_key")
	result := fn(tape)

	// Userinfo must not be in the result.
	if strings.Contains(result.Request.URL, "user:pass") {
		t.Errorf("userinfo still present in URL after malformed-query redaction: %q", result.Request.URL)
	}
	// Cleartext secret must not be in the result.
	if strings.Contains(result.Request.URL, "ab%ZZcd") {
		t.Errorf("cleartext secret still present in URL after malformed-query redaction: %q", result.Request.URL)
	}
}

// --- Fragment sanitization tests ---

func TestRedactQueryParams_SensitiveParamInFragmentIsRedacted(t *testing.T) {
	// OAuth implicit flow puts access_token in the fragment.
	tape := makeTapeWithURL("https://example.com/callback#access_token=mysecret&token_type=bearer")
	fn := RedactQueryParams("access_token")
	result := fn(tape)

	// Secret must not appear in the persisted URL.
	if strings.Contains(result.Request.URL, "mysecret") {
		t.Errorf("fragment secret still present in persisted URL: %q", result.Request.URL)
	}
	// The non-sensitive fragment param should still be present.
	u, err := url.Parse(result.Request.URL)
	if err != nil {
		t.Fatalf("failed to parse result URL: %v", err)
	}
	fragVals, err := url.ParseQuery(u.Fragment)
	if err != nil {
		t.Fatalf("failed to parse fragment: %v", err)
	}
	if got := fragVals.Get("access_token"); got != Redacted {
		t.Errorf("access_token in fragment: expected %q, got %q", Redacted, got)
	}
	if got := fragVals.Get("token_type"); got != "bearer" {
		t.Errorf("token_type in fragment: expected %q (unchanged), got %q", "bearer", got)
	}
}

func TestRedactQueryParams_NonSensitiveParamInFragmentIsUntouched(t *testing.T) {
	tape := makeTapeWithURL("https://example.com/page#section=intro")
	fn := RedactQueryParams("api_key")
	result := fn(tape)

	// URL should be byte-stable -- no sensitive params anywhere, no userinfo.
	if result.Request.URL != "https://example.com/page#section=intro" {
		t.Errorf("URL changed unexpectedly: got %q", result.Request.URL)
	}
}

func TestRedactQueryParams_MalformedFragmentURLFailsStructuralParse(t *testing.T) {
	// Go's url.Parse eagerly validates percent-encoding in the fragment, unlike
	// in the query string. A URL with malformed percent-encoding in the fragment
	// (e.g. %ZZ) causes url.Parse to fail, so sanitizeURL returns the raw URL
	// unchanged (structural parse error, consistent with body sanitization).
	// This is distinct from the query-string fail-closed path where url.Parse
	// succeeds but url.ParseQuery fails.
	rawURL := "https://example.com/cb#access_token=ab%ZZcd"
	tape := makeTapeWithURL(rawURL)
	fn := RedactQueryParams("access_token")
	result := fn(tape)

	// url.Parse fails entirely for this URL, so we silently skip (return raw).
	// This is the structural-parse-error path, NOT the fragment-parse-error path.
	if result.Request.URL != rawURL {
		t.Errorf("expected URL unchanged on url.Parse error, got %q", result.Request.URL)
	}
}

func TestRedactQueryParams_WellFormedFragmentWithKeyValueIsRedacted(t *testing.T) {
	// A fragment that is valid percent-encoding but contains sensitive param names
	// (e.g., the fragment parse path succeeds) should be redacted.
	// We use a well-formed fragment: no malformed percent-sequences.
	tape := makeTapeWithURL("https://example.com/callback#access_token=mysecret%20value&state=ok")
	fn := RedactQueryParams("access_token")
	result := fn(tape)

	// The secret value must not appear in the persisted URL.
	if strings.Contains(result.Request.URL, "mysecret") {
		t.Errorf("fragment secret still present in persisted URL: %q", result.Request.URL)
	}
	u, err := url.Parse(result.Request.URL)
	if err != nil {
		t.Fatalf("failed to parse result URL: %v", err)
	}
	fragVals, err := url.ParseQuery(u.Fragment)
	if err != nil {
		t.Fatalf("failed to parse fragment of result: %v", err)
	}
	if got := fragVals.Get("access_token"); got != Redacted {
		t.Errorf("access_token in fragment: expected %q, got %q", Redacted, got)
	}
	if got := fragVals.Get("state"); got != "ok" {
		t.Errorf("state in fragment: expected %q (unchanged), got %q", "ok", got)
	}
}

func TestFakeQueryParams_SensitiveParamInFragmentIsFaked(t *testing.T) {
	tape := makeTapeWithURL("https://example.com/callback#access_token=mysecret&other=keep")
	fn := FakeQueryParams("test-seed", "access_token")
	result := fn(tape)

	// Original secret must not appear.
	if strings.Contains(result.Request.URL, "mysecret") {
		t.Errorf("fragment secret still present in persisted URL: %q", result.Request.URL)
	}
	u, err := url.Parse(result.Request.URL)
	if err != nil {
		t.Fatalf("failed to parse result URL: %v", err)
	}
	fragVals, err := url.ParseQuery(u.Fragment)
	if err != nil {
		t.Fatalf("failed to parse fragment: %v", err)
	}
	got := fragVals.Get("access_token")
	if !strings.HasPrefix(got, "fake_") {
		t.Errorf("access_token in fragment: expected fake_ prefix, got %q", got)
	}
	if fragVals.Get("other") != "keep" {
		t.Errorf("other in fragment: expected %q (unchanged), got %q", "keep", fragVals.Get("other"))
	}
}

// --- changed-flag decoupling from value-equality tests ---

// TestRedactQueryParams_SensitiveParamAlreadyRedactedCausesReencode verifies that
// if a sensitive param's value already equals the replacement (e.g. the fixture
// was already sanitized), the URL is still re-encoded authoritatively rather than
// passed through byte-identical. This tests that changed is set by name presence,
// not by value difference.
func TestRedactQueryParams_SensitiveParamAlreadyRedactedCausesReencode(t *testing.T) {
	// The value is already [REDACTED], so byte comparison would say "no change".
	// But the param name is sensitive, so we must re-encode.
	rawURL := "https://example.com/api?api_key=" + url.QueryEscape(Redacted) + "&other=value"
	tape := makeTapeWithURL(rawURL)
	fn := RedactQueryParams("api_key")
	result := fn(tape)

	u, err := url.Parse(result.Request.URL)
	if err != nil {
		t.Fatalf("failed to parse result URL: %v", err)
	}
	if got := u.Query().Get("api_key"); got != Redacted {
		t.Errorf("api_key: expected %q, got %q", Redacted, got)
	}
}

// --- DefaultSensitiveQueryParams tests ---

func TestDefaultSensitiveQueryParams_ReturnsACopy(t *testing.T) {
	p1 := DefaultSensitiveQueryParams()
	p2 := DefaultSensitiveQueryParams()

	if len(p1) != len(p2) {
		t.Fatalf("lengths differ: %d vs %d", len(p1), len(p2))
	}
	for i := range p1 {
		if p1[i] != p2[i] {
			t.Errorf("index %d: %q != %q", i, p1[i], p2[i])
		}
	}

	// Mutating one copy must not affect the internal list.
	p1[0] = "MUTATED"
	p3 := DefaultSensitiveQueryParams()
	if p3[0] == "MUTATED" {
		t.Error("DefaultSensitiveQueryParams returned a shared slice; mutation leaked to internal state")
	}
}

func TestDefaultSensitiveQueryParams_ContainsExpectedParams(t *testing.T) {
	params := DefaultSensitiveQueryParams()
	expected := map[string]bool{
		"api_key":          false,
		"access_token":     false,
		"token":            false,
		"secret":           false,
		"password":         false,
		"sig":              false,
		"signature":        false,
		"X-Amz-Signature":  false,
		"X-Goog-Signature": false,
	}

	for _, p := range params {
		if _, ok := expected[p]; ok {
			expected[p] = true
		}
	}

	for name, found := range expected {
		if !found {
			t.Errorf("expected param %q not found in DefaultSensitiveQueryParams()", name)
		}
	}
}

// parseURL is a test helper that parses a URL or fails the test.
func parseURL(t *testing.T, rawURL string) (*url.URL, error) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("failed to parse result URL %q: %v", rawURL, err)
	}
	return u, nil
}

func TestDefaultSensitiveHeaders_ReturnsACopy(t *testing.T) {
	h1 := DefaultSensitiveHeaders()
	h2 := DefaultSensitiveHeaders()

	// Verify they contain the same values.
	if len(h1) != len(h2) {
		t.Fatalf("lengths differ: %d vs %d", len(h1), len(h2))
	}
	for i := range h1 {
		if h1[i] != h2[i] {
			t.Errorf("index %d: %q != %q", i, h1[i], h2[i])
		}
	}

	// Mutating one copy must not affect the other.
	h1[0] = "MUTATED"
	h3 := DefaultSensitiveHeaders()
	if h3[0] == "MUTATED" {
		t.Error("DefaultSensitiveHeaders returned a shared slice; mutation leaked")
	}
}

func TestDefaultSensitiveHeaders_ContainsExpectedHeaders(t *testing.T) {
	headers := DefaultSensitiveHeaders()
	expected := map[string]bool{
		"Authorization":       false,
		"Cookie":              false,
		"Set-Cookie":          false,
		"X-Api-Key":           false,
		"Proxy-Authorization": false,
		"X-Forwarded-For":     false,
	}

	for _, h := range headers {
		if _, ok := expected[h]; ok {
			expected[h] = true
		}
	}

	for name, found := range expected {
		if !found {
			t.Errorf("expected header %q not found in DefaultSensitiveHeaders()", name)
		}
	}
}
