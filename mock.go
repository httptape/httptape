package httptape

import (
	"context"
	"net/http"
	"net/http/httptest"
)

// MockServer wraps an httptest.Server configured with stub-based tapes.
// It embeds *httptest.Server so callers get URL, Client(), and Close() for free.
//
// Usage:
//
//	srv := httptape.Mock(
//	    httptape.When(httptape.GET("/api/users")).
//	        Respond(200, httptape.JSON(`{"users": [{"id": 1}]}`)).
//	        WithHeader("X-Request-Id", "abc123").
//	        Build(),
//	    httptape.When(httptape.POST("/api/users")).
//	        Respond(201, httptape.JSON(`{"id": 2}`)).
//	        Build(),
//	    httptape.When(httptape.GET("/api/health")).
//	        Respond(204).
//	        Build(),
//	)
//	defer srv.Close()
type MockServer struct {
	*httptest.Server
}

// Stub represents a single request-response pair for the mock DSL.
// Create stubs using When() and the StubBuilder fluent API.
type Stub struct {
	method  string
	path    string
	status  int
	body    []byte
	headers http.Header
}

// Method is a request matcher that captures an HTTP method and URL path.
type Method struct {
	method string
	path   string
}

// GET returns a Method matching GET requests to the given path.
func GET(path string) Method { return Method{method: http.MethodGet, path: path} }

// POST returns a Method matching POST requests to the given path.
func POST(path string) Method { return Method{method: http.MethodPost, path: path} }

// PUT returns a Method matching PUT requests to the given path.
func PUT(path string) Method { return Method{method: http.MethodPut, path: path} }

// DELETE returns a Method matching DELETE requests to the given path.
func DELETE(path string) Method { return Method{method: http.MethodDelete, path: path} }

// PATCH returns a Method matching PATCH requests to the given path.
func PATCH(path string) Method { return Method{method: http.MethodPatch, path: path} }

// HEAD returns a Method matching HEAD requests to the given path.
func HEAD(path string) Method { return Method{method: http.MethodHead, path: path} }

// Body represents a response body with an optional auto-detected content type.
type Body struct {
	data        []byte
	contentType string
}

// JSON creates a Body with the given JSON string and content type application/json.
func JSON(s string) Body {
	return Body{data: []byte(s), contentType: "application/json"}
}

// Text creates a Body with the given string and content type text/plain; charset=utf-8.
func Text(s string) Body {
	return Body{data: []byte(s), contentType: "text/plain; charset=utf-8"}
}

// Binary creates a Body with the given bytes and no automatic content type.
func Binary(b []byte) Body {
	return Body{data: b}
}

// StubBuilder provides a fluent API for constructing a Stub.
type StubBuilder struct {
	stub Stub
}

// When starts building a Stub that matches the given HTTP method and path.
func When(m Method) *StubBuilder {
	return &StubBuilder{
		stub: Stub{
			method:  m.method,
			path:    m.path,
			headers: make(http.Header),
		},
	}
}

// Respond sets the response status code and optional body.
// If no body is provided, the response has an empty body.
// If a body helper (JSON, Text) is used and no explicit Content-Type header
// has been set, the content type from the body helper is added automatically.
func (b *StubBuilder) Respond(status int, body ...Body) *StubBuilder {
	b.stub.status = status
	if len(body) > 0 {
		b.stub.body = body[0].data
		// Auto-set Content-Type from body helper if not explicitly set.
		if body[0].contentType != "" && b.stub.headers.Get("Content-Type") == "" {
			b.stub.headers.Set("Content-Type", body[0].contentType)
		}
	}
	return b
}

// WithHeader adds a response header to the stub. Can be called multiple
// times to set multiple headers. If called with "Content-Type", it overrides
// any content type set by a body helper.
func (b *StubBuilder) WithHeader(key, value string) *StubBuilder {
	b.stub.headers.Set(key, value)
	return b
}

// Build finalizes and returns the Stub value.
func (b *StubBuilder) Build() Stub {
	return b.stub
}

// Mock creates an httptest.Server backed by a MemoryStore populated with
// the given stubs. Each Stub is converted to a Tape and stored internally.
// The returned MockServer must be closed by the caller (defer srv.Close()).
//
// Mock panics if a stub cannot be saved to the store. This follows the
// constructor-panic convention for programming errors (see CLAUDE.md L-11).
func Mock(stubs ...Stub) *MockServer {
	store := NewMemoryStore()
	ctx := context.Background()

	for _, s := range stubs {
		tape := NewTape("",
			RecordedReq{
				Method: s.method,
				URL:    "http://mock" + s.path,
			},
			RecordedResp{
				StatusCode: s.status,
				Body:       s.body,
				Headers:    s.headers,
			},
		)
		if err := store.Save(ctx, tape); err != nil {
			panic("httptape: Mock failed to save stub: " + err.Error())
		}
	}

	handler := NewServer(store)
	ts := httptest.NewServer(handler)
	return &MockServer{Server: ts}
}
