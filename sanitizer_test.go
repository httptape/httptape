package httptape

import (
	"net/http"
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

	fn := RedactHeaders() // no args => DefaultSensitiveHeaders
	result := fn(tape)

	// All default sensitive headers in request should be redacted.
	for _, name := range DefaultSensitiveHeaders {
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
		"Authorization":  {"Bearer token"},
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

func TestRedactHeaders_DoesNotMutateOriginal(t *testing.T) {
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
