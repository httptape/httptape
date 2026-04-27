package httptape

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// storeTape creates and saves a minimal tape to the store, returning the saved tape.
func storeTape(t *testing.T, store *MemoryStore, method, path string, status int, body string, headers http.Header) Tape {
	t.Helper()
	tape := NewTape("", RecordedReq{
		Method: method,
		URL:    path,
	}, RecordedResp{
		StatusCode: status,
		Headers:    headers,
		Body:       []byte(body),
	})
	if err := store.Save(context.Background(), tape); err != nil {
		t.Fatalf("storeTape: failed to save tape: %v", err)
	}
	return tape
}

// errListStore is a Store implementation whose List always returns an error.
type errListStore struct{}

func (f *errListStore) Save(_ context.Context, _ Tape) error { return errors.New("fail") }
func (f *errListStore) Load(_ context.Context, _ string) (Tape, error) {
	return Tape{}, errors.New("fail")
}
func (f *errListStore) List(_ context.Context, _ Filter) ([]Tape, error) {
	return nil, errors.New("fail")
}
func (f *errListStore) Delete(_ context.Context, _ string) error { return errors.New("fail") }

func TestServer_BasicReplay(t *testing.T) {
	store := NewMemoryStore()
	tape := storeTape(t, store, "GET", "/api/users", 200, `{"users":[]}`, http.Header{
		"Content-Type": {"application/json"},
	})

	srv, err := NewServer(store)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/users", nil)

	srv.ServeHTTP(rec, req)

	if rec.Code != tape.Response.StatusCode {
		t.Errorf("status = %d, want %d", rec.Code, tape.Response.StatusCode)
	}
	if got := rec.Body.String(); got != `{"users":[]}` {
		t.Errorf("body = %q, want %q", got, `{"users":[]}`)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}
}

func TestServer_ResponseHeaders(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/cookies", 200, "ok", http.Header{
		"Set-Cookie": {"a=1", "b=2"},
		"X-Custom":   {"val"},
	})

	srv, err := NewServer(store)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/cookies", nil)

	srv.ServeHTTP(rec, req)

	cookies := rec.Header()["Set-Cookie"]
	if len(cookies) != 2 {
		t.Fatalf("Set-Cookie count = %d, want 2", len(cookies))
	}
	if cookies[0] != "a=1" || cookies[1] != "b=2" {
		t.Errorf("Set-Cookie = %v, want [a=1 b=2]", cookies)
	}
	if got := rec.Header().Get("X-Custom"); got != "val" {
		t.Errorf("X-Custom = %q, want %q", got, "val")
	}
}

func TestServer_NoMatch_DefaultFallback(t *testing.T) {
	store := NewMemoryStore()
	srv, err := NewServer(store)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/nonexistent", nil)

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if got := rec.Body.String(); got != "httptape: no matching tape found" {
		t.Errorf("body = %q, want %q", got, "httptape: no matching tape found")
	}
}

func TestServer_NoMatch_CustomFallback(t *testing.T) {
	store := NewMemoryStore()
	srv, err := NewServer(store,
		WithFallbackStatus(http.StatusServiceUnavailable),
		WithFallbackBody([]byte("custom fallback")),
	)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/missing", nil)

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if got := rec.Body.String(); got != "custom fallback" {
		t.Errorf("body = %q, want %q", got, "custom fallback")
	}
}

func TestServer_NoMatch_Callback(t *testing.T) {
	store := NewMemoryStore()
	var called atomic.Int32
	var capturedPath string
	var mu sync.Mutex

	srv, err := NewServer(store, WithOnNoMatch(func(r *http.Request) {
		called.Add(1)
		mu.Lock()
		capturedPath = r.URL.Path
		mu.Unlock()
	}))
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/unmatched", nil)

	srv.ServeHTTP(rec, req)

	if called.Load() != 1 {
		t.Errorf("onNoMatch called %d times, want 1", called.Load())
	}
	mu.Lock()
	if capturedPath != "/unmatched" {
		t.Errorf("captured path = %q, want %q", capturedPath, "/unmatched")
	}
	mu.Unlock()
}

func TestNewServer_NilStore_Panics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for nil store, got none")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("expected string panic, got %T: %v", r, r)
		}
		if !strings.Contains(msg, "nil Store") {
			t.Errorf("panic message = %q, want it to contain 'nil Store'", msg)
		}
	}()
	NewServer(nil)
}

func TestServer_StoreError(t *testing.T) {
	srv, err := NewServer(&errListStore{})
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/anything", nil)

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(rec.Body.String(), "store error") {
		t.Errorf("body = %q, want it to contain 'store error'", rec.Body.String())
	}
}

func TestServer_CustomMatcher(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/path", 200, "matched-by-header", http.Header{
		"X-Match": {"yes"},
	})

	// Custom matcher that matches on X-Match-Key header.
	customMatcher := MatcherFunc(func(req *http.Request, candidates []Tape) (Tape, bool) {
		key := req.Header.Get("X-Match-Key")
		for _, c := range candidates {
			if c.Response.Headers.Get("X-Match") == key {
				return c, true
			}
		}
		return Tape{}, false
	})

	srv, err := NewServer(store, WithMatcher(customMatcher))
	if err != nil {
		t.Fatal(err)
	}

	// Request with matching header.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/irrelevant", nil)
	req.Header.Set("X-Match-Key", "yes")
	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "matched-by-header" {
		t.Errorf("body = %q, want %q", got, "matched-by-header")
	}

	// Request without matching header.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/irrelevant", nil)
	req2.Header.Set("X-Match-Key", "no")
	srv.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec2.Code, http.StatusNotFound)
	}
}

func TestServer_ExactMatcher_MethodMismatch(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/api/data", 200, "data", nil)

	srv, err := NewServer(store)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/data", nil)

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestServer_ExactMatcher_PathMismatch(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/foo", 200, "foo", nil)

	srv, err := NewServer(store)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/bar", nil)

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestServer_EmptyStore(t *testing.T) {
	store := NewMemoryStore()
	srv, err := NewServer(store)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/anything", nil)

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestServer_NilResponseBody(t *testing.T) {
	store := NewMemoryStore()
	tape := NewTape("", RecordedReq{
		Method: "GET",
		URL:    "/empty",
	}, RecordedResp{
		StatusCode: 204,
		Headers:    nil,
		Body:       nil,
	})
	if err := store.Save(context.Background(), tape); err != nil {
		t.Fatalf("save: %v", err)
	}

	srv, err := NewServer(store)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/empty", nil)

	srv.ServeHTTP(rec, req)

	if rec.Code != 204 {
		t.Errorf("status = %d, want 204", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body len = %d, want 0", rec.Body.Len())
	}
}

func TestServer_ConcurrentRequests(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/a", 200, "response-a", nil)
	storeTape(t, store, "GET", "/b", 200, "response-b", nil)

	srv, err := NewServer(store)
	if err != nil {
		t.Fatal(err)
	}

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		path := "/a"
		wantBody := "response-a"
		if i%2 == 1 {
			path = "/b"
			wantBody = "response-b"
		}
		go func(p, want string) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", p, nil)
			srv.ServeHTTP(rec, req)

			if rec.Code != 200 {
				t.Errorf("status = %d, want 200 for %s", rec.Code, p)
			}
			if got := rec.Body.String(); got != want {
				t.Errorf("body = %q, want %q for %s", got, want, p)
			}
		}(path, wantBody)
	}

	wg.Wait()
}

func TestServer_MatcherFunc(t *testing.T) {
	called := false
	fn := MatcherFunc(func(req *http.Request, candidates []Tape) (Tape, bool) {
		called = true
		if len(candidates) > 0 {
			return candidates[0], true
		}
		return Tape{}, false
	})

	req := httptest.NewRequest("GET", "/test", nil)
	tape := Tape{Response: RecordedResp{StatusCode: 200}}
	result, ok := fn.Match(req, []Tape{tape})

	if !called {
		t.Error("MatcherFunc was not called")
	}
	if !ok {
		t.Error("expected match, got no match")
	}
	if result.Response.StatusCode != 200 {
		t.Errorf("status = %d, want 200", result.Response.StatusCode)
	}
}

func TestServer_WithHTTPTestServer(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/api/hello", 200, `{"msg":"hello"}`, http.Header{
		"Content-Type": {"application/json"},
	})

	srv, err := NewServer(store)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/hello")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != `{"msg":"hello"}` {
		t.Errorf("body = %q, want %q", string(body), `{"msg":"hello"}`)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}
}

// --- Benchmarks ---

// BenchmarkServerServeHTTP_ExactMatch measures response latency with exact match.
// Scales from 1 to 1000 candidate tapes to show matching cost.
func BenchmarkServerServeHTTP_ExactMatch(b *testing.B) {
	tapeCounts := []int{1, 10, 100, 1000}

	for _, n := range tapeCounts {
		b.Run(fmt.Sprintf("%dtapes", n), func(b *testing.B) {
			b.ReportAllocs()

			store := NewMemoryStore()
			ctx := context.Background()

			// Populate store with n-1 non-matching tapes plus 1 matching tape.
			for i := 0; i < n-1; i++ {
				tape := NewTape("bench-route", RecordedReq{
					Method: "GET",
					URL:    fmt.Sprintf("/api/other/%d", i),
				}, RecordedResp{
					StatusCode: 200,
					Headers:    http.Header{"Content-Type": {"application/json"}},
					Body:       []byte(`{"id":1}`),
				})
				if err := store.Save(ctx, tape); err != nil {
					b.Fatalf("Save: %v", err)
				}
			}

			// Add the matching tape.
			matchingTape := NewTape("bench-route", RecordedReq{
				Method: "GET",
				URL:    "/api/users",
			}, RecordedResp{
				StatusCode: 200,
				Headers:    http.Header{"Content-Type": {"application/json"}},
				Body:       []byte(`{"users":[]}`),
			})
			if err := store.Save(ctx, matchingTape); err != nil {
				b.Fatalf("Save: %v", err)
			}

			srv, err := NewServer(store)
			if err != nil {
				b.Fatal(err)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest("GET", "/api/users", nil)
				srv.ServeHTTP(rec, req)
			}
		})
	}
}

func TestExactMatcher(t *testing.T) {
	tests := []struct {
		name       string
		reqMethod  string
		reqPath    string
		candidates []Tape
		wantMatch  bool
		wantStatus int
	}{
		{
			name:      "exact match",
			reqMethod: "GET",
			reqPath:   "/users",
			candidates: []Tape{
				{Request: RecordedReq{Method: "GET", URL: "/users"}, Response: RecordedResp{StatusCode: 200}},
			},
			wantMatch:  true,
			wantStatus: 200,
		},
		{
			name:      "method mismatch",
			reqMethod: "POST",
			reqPath:   "/users",
			candidates: []Tape{
				{Request: RecordedReq{Method: "GET", URL: "/users"}, Response: RecordedResp{StatusCode: 200}},
			},
			wantMatch: false,
		},
		{
			name:      "path mismatch",
			reqMethod: "GET",
			reqPath:   "/accounts",
			candidates: []Tape{
				{Request: RecordedReq{Method: "GET", URL: "/users"}, Response: RecordedResp{StatusCode: 200}},
			},
			wantMatch: false,
		},
		{
			name:       "empty candidates",
			reqMethod:  "GET",
			reqPath:    "/users",
			candidates: []Tape{},
			wantMatch:  false,
		},
		{
			name:      "first match wins",
			reqMethod: "GET",
			reqPath:   "/dup",
			candidates: []Tape{
				{Request: RecordedReq{Method: "GET", URL: "/dup"}, Response: RecordedResp{StatusCode: 200}},
				{Request: RecordedReq{Method: "GET", URL: "/dup"}, Response: RecordedResp{StatusCode: 201}},
			},
			wantMatch:  true,
			wantStatus: 200,
		},
	}

	matcher := ExactMatcher()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.reqMethod, tt.reqPath, nil)
			tape, ok := matcher.Match(req, tt.candidates)
			if ok != tt.wantMatch {
				t.Errorf("Match() ok = %v, want %v", ok, tt.wantMatch)
			}
			if tt.wantMatch && tape.Response.StatusCode != tt.wantStatus {
				t.Errorf("Match() status = %d, want %d", tape.Response.StatusCode, tt.wantStatus)
			}
		})
	}
}

// --- ADR-17: Server edge case tests ---

// TestServer_Replay204NoContent verifies that the server correctly replays
// a 204 No Content response with empty body.
func TestServer_Replay204NoContent(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "DELETE", "/resource/1", 204, "", nil)

	srv, err := NewServer(store)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/resource/1", nil)

	srv.ServeHTTP(rec, req)

	if rec.Code != 204 {
		t.Errorf("status = %d, want 204", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body should be empty, got %d bytes: %q", rec.Body.Len(), rec.Body.String())
	}
}

// TestServer_ReplayBinaryBody verifies that binary bodies are replayed correctly.
func TestServer_ReplayBinaryBody(t *testing.T) {
	binaryData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0xFF}

	store := NewMemoryStore()
	tape := NewTape("", RecordedReq{
		Method: "GET",
		URL:    "/image.png",
	}, RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{"Content-Type": {"image/png"}},
		Body:       binaryData,
	})
	if err := store.Save(context.Background(), tape); err != nil {
		t.Fatalf("save tape: %v", err)
	}

	srv, err := NewServer(store)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/image.png", nil)

	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), binaryData) {
		t.Error("binary body not replayed correctly")
	}
	if rec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", rec.Header().Get("Content-Type"))
	}
}

// TestServer_ReplayTruncatedBody verifies that a truncated body is replayed as-is.
func TestServer_ReplayTruncatedBody(t *testing.T) {
	truncatedBody := []byte("partial content...")

	store := NewMemoryStore()
	tape := NewTape("", RecordedReq{
		Method: "GET",
		URL:    "/large",
	}, RecordedResp{
		StatusCode:       200,
		Headers:          http.Header{"Content-Type": {"text/plain"}},
		Body:             truncatedBody,
		Truncated:        true,
		OriginalBodySize: 100000,
	})
	if err := store.Save(context.Background(), tape); err != nil {
		t.Fatalf("save tape: %v", err)
	}

	srv, err := NewServer(store)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/large", nil)

	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), truncatedBody) {
		t.Errorf("truncated body not replayed correctly, got %q", rec.Body.String())
	}
}

// --- ADR-24: CORS support tests ---

func TestServer_CORS_HeadersPresent(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/api/data", 200, "ok", nil)

	srv, err := NewServer(store, WithCORS())
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)

	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Errorf("status = %d, want 200", rec.Code)
	}

	tests := []struct {
		header string
		want   string
	}{
		{"Access-Control-Allow-Origin", "*"},
		{"Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, OPTIONS, HEAD"},
		{"Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, Accept"},
		{"Access-Control-Max-Age", "86400"},
	}
	for _, tt := range tests {
		if got := rec.Header().Get(tt.header); got != tt.want {
			t.Errorf("%s = %q, want %q", tt.header, got, tt.want)
		}
	}
}

func TestServer_CORS_Disabled_NoHeaders(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/api/data", 200, "ok", nil)

	srv, err := NewServer(store) // no WithCORS
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)

	srv.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin should be absent, got %q", got)
	}
}

func TestServer_CORS_OptionsPreflight(t *testing.T) {
	store := NewMemoryStore()
	srv, err := NewServer(store, WithCORS())
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("OPTIONS", "/api/data", nil)

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, "*")
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body should be empty for OPTIONS, got %d bytes", rec.Body.Len())
	}
}

func TestServer_CORS_OptionsWithoutCORS(t *testing.T) {
	store := NewMemoryStore()
	srv, err := NewServer(store) // no WithCORS
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("OPTIONS", "/api/data", nil)

	srv.ServeHTTP(rec, req)

	// Without CORS enabled, OPTIONS is treated as a normal request (no match -> 404).
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestServer_CORS_WithVariousMethods(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "POST", "/api/data", 201, "created", nil)
	storeTape(t, store, "DELETE", "/api/data", 204, "", nil)

	srv, err := NewServer(store, WithCORS())
	if err != nil {
		t.Fatal(err)
	}

	methods := []struct {
		method string
		want   int
	}{
		{"POST", 201},
		{"DELETE", 204},
	}
	for _, tt := range methods {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(tt.method, "/api/data", nil)
		srv.ServeHTTP(rec, req)

		if rec.Code != tt.want {
			t.Errorf("%s status = %d, want %d", tt.method, rec.Code, tt.want)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
			t.Errorf("%s CORS header missing", tt.method)
		}
	}
}

// --- ADR-24: Latency simulation tests ---

func TestServer_Delay_GlobalDelay(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/api/data", 200, "ok", nil)

	srv, err := NewServer(store, WithDelay(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)

	start := time.Now()
	srv.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if rec.Code != 200 {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if elapsed < 40*time.Millisecond {
		t.Errorf("elapsed = %v, want >= 40ms", elapsed)
	}
}

func TestServer_Delay_ZeroDelay(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/api/data", 200, "ok", nil)

	srv, err := NewServer(store, WithDelay(0))
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)

	start := time.Now()
	srv.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if rec.Code != 200 {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	// Should be near-instant.
	if elapsed > 50*time.Millisecond {
		t.Errorf("zero delay took %v, expected near-instant", elapsed)
	}
}

func TestServer_Delay_PerFixtureOverride(t *testing.T) {
	store := NewMemoryStore()
	tape := NewTape("", RecordedReq{
		Method: "GET",
		URL:    "/api/slow",
	}, RecordedResp{
		StatusCode: 200,
		Body:       []byte("slow response"),
	})
	tape.Metadata = map[string]any{"delay": "50ms"}
	if err := store.Save(context.Background(), tape); err != nil {
		t.Fatalf("save: %v", err)
	}

	srv, err := NewServer(store, WithDelay(1*time.Millisecond)) // global is 1ms
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/slow", nil)

	start := time.Now()
	srv.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if rec.Code != 200 {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	// Per-fixture delay of 50ms should override the 1ms global.
	if elapsed < 40*time.Millisecond {
		t.Errorf("per-fixture delay elapsed = %v, want >= 40ms", elapsed)
	}
}

func TestServer_Delay_ContextCancellation(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/api/data", 200, "ok", nil)

	srv, err := NewServer(store, WithDelay(5*time.Second)) // very long delay
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/api/data", nil).WithContext(ctx)

	done := make(chan struct{})
	go func() {
		srv.ServeHTTP(rec, req)
		close(done)
	}()

	// Cancel after a short time.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Good, returned quickly.
	case <-time.After(2 * time.Second):
		t.Fatal("ServeHTTP did not return after context cancellation")
	}
}

func TestServer_Delay_NoDelayOnNoMatch(t *testing.T) {
	store := NewMemoryStore() // empty store, no tapes

	srv, err := NewServer(store, WithDelay(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/nonexistent", nil)

	start := time.Now()
	srv.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	// No-match should not be delayed.
	if elapsed > 100*time.Millisecond {
		t.Errorf("no-match took %v, expected near-instant", elapsed)
	}
}

// --- ADR-24: Error simulation tests ---

func TestServer_ErrorRate_Zero_NoErrors(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/api/data", 200, "ok", nil)

	// randFloat always returns 0.5, but errorRate is 0 so it should never trigger.
	srv, err := NewServer(store, withRandFloat(func() float64 { return 0.5 }))
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)
	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestServer_ErrorRate_One_AllErrors(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/api/data", 200, "ok", nil)

	// randFloat returns 0.5, errorRate is 1.0 so all requests fail.
	srv, err := NewServer(store,
		WithErrorRate(1.0),
		withRandFloat(func() float64 { return 0.5 }),
	)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if got := rec.Header().Get("X-Httptape-Error"); got != "simulated" {
		t.Errorf("X-Httptape-Error = %q, want %q", got, "simulated")
	}
	if !strings.Contains(rec.Body.String(), "simulated error") {
		t.Errorf("body = %q, want it to contain 'simulated error'", rec.Body.String())
	}
}

func TestServer_ErrorRate_Deterministic(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/api/data", 200, "ok", nil)

	// Error rate 0.5: randFloat < 0.5 -> error, randFloat >= 0.5 -> success.
	callCount := 0
	srv, err := NewServer(store,
		WithErrorRate(0.5),
		withRandFloat(func() float64 {
			callCount++
			if callCount%2 == 1 {
				return 0.1 // below rate -> error
			}
			return 0.9 // above rate -> success
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	// First request: error.
	rec1 := httptest.NewRecorder()
	req1 := httptest.NewRequest("GET", "/api/data", nil)
	srv.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusInternalServerError {
		t.Errorf("req1 status = %d, want 500", rec1.Code)
	}

	// Second request: success.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/api/data", nil)
	srv.ServeHTTP(rec2, req2)
	if rec2.Code != 200 {
		t.Errorf("req2 status = %d, want 200", rec2.Code)
	}
}

func TestServer_ErrorRate_WithCORS(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/api/data", 200, "ok", nil)

	srv, err := NewServer(store,
		WithCORS(),
		WithErrorRate(1.0),
		withRandFloat(func() float64 { return 0.0 }),
	)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)
	srv.ServeHTTP(rec, req)

	// Even on error, CORS headers must be present.
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("CORS header missing on error response: %q", got)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestNewServer_InvalidErrorRate(t *testing.T) {
	tests := []struct {
		name string
		rate float64
	}{
		{"negative", -0.1},
		{"above one", 1.1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewServer(NewMemoryStore(), WithErrorRate(tt.rate))
			if err == nil {
				t.Fatal("expected error for invalid error rate, got nil")
			}
			if !strings.Contains(err.Error(), "between 0.0 and 1.0") {
				t.Errorf("error = %q, want it to contain 'between 0.0 and 1.0'", err.Error())
			}
		})
	}
}

func TestServer_PerFixtureError(t *testing.T) {
	store := NewMemoryStore()
	tape := NewTape("", RecordedReq{
		Method: "GET",
		URL:    "/api/broken",
	}, RecordedResp{
		StatusCode: 200,
		Body:       []byte("should not see this"),
	})
	tape.Metadata = map[string]any{
		"error": map[string]any{
			"status": float64(503),
			"body":   "service unavailable",
		},
	}
	if err := store.Save(context.Background(), tape); err != nil {
		t.Fatalf("save: %v", err)
	}

	srv, err := NewServer(store)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/broken", nil)
	srv.ServeHTTP(rec, req)

	if rec.Code != 503 {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "service unavailable") {
		t.Errorf("body = %q, want it to contain 'service unavailable'", rec.Body.String())
	}
	if got := rec.Header().Get("X-Httptape-Error"); got != "simulated" {
		t.Errorf("X-Httptape-Error = %q, want %q", got, "simulated")
	}
}

func TestServer_PerFixtureError_DefaultStatus(t *testing.T) {
	store := NewMemoryStore()
	tape := NewTape("", RecordedReq{
		Method: "GET",
		URL:    "/api/err",
	}, RecordedResp{
		StatusCode: 200,
		Body:       []byte("normal"),
	})
	// error with no status field -> default 500
	tape.Metadata = map[string]any{
		"error": map[string]any{},
	}
	if err := store.Save(context.Background(), tape); err != nil {
		t.Fatalf("save: %v", err)
	}

	srv, err := NewServer(store)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/err", nil)
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestServer_PerFixtureError_WithCORS(t *testing.T) {
	store := NewMemoryStore()
	tape := NewTape("", RecordedReq{
		Method: "GET",
		URL:    "/api/broken",
	}, RecordedResp{
		StatusCode: 200,
		Body:       []byte("ok"),
	})
	tape.Metadata = map[string]any{
		"error": map[string]any{
			"status": float64(429),
			"body":   "rate limited",
		},
	}
	if err := store.Save(context.Background(), tape); err != nil {
		t.Fatalf("save: %v", err)
	}

	srv, err := NewServer(store, WithCORS())
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/broken", nil)
	srv.ServeHTTP(rec, req)

	if rec.Code != 429 {
		t.Errorf("status = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("CORS header missing on per-fixture error: %q", got)
	}
}

// --- ADR-24: Metadata field on Tape ---

func TestTape_MetadataOmitEmpty(t *testing.T) {
	tape := NewTape("route", RecordedReq{Method: "GET", URL: "/test"}, RecordedResp{StatusCode: 200})
	if tape.Metadata != nil {
		t.Errorf("new tape Metadata should be nil, got %v", tape.Metadata)
	}
}

func TestTape_MetadataRoundTrip(t *testing.T) {
	tape := NewTape("route", RecordedReq{Method: "GET", URL: "/test"}, RecordedResp{StatusCode: 200})
	tape.Metadata = map[string]any{
		"delay": "500ms",
		"error": map[string]any{"status": float64(503)},
	}

	if tape.Metadata["delay"] != "500ms" {
		t.Errorf("delay = %v, want 500ms", tape.Metadata["delay"])
	}
}

func TestServer_ReplayHeaders_Override(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/api/data", 200, `{"ok":true}`, http.Header{
		"Authorization": {"Bearer original-token"},
		"Content-Type":  {"application/json"},
	})

	srv, err := NewServer(store,
		WithReplayHeaders("Authorization", "Bearer injected-token"),
	)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)

	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Authorization"); got != "Bearer injected-token" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer injected-token")
	}
	// Original header that was NOT overridden should still be present.
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}
}

func TestServer_ReplayHeaders_Multiple(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/multi", 200, "ok", http.Header{})

	srv, err := NewServer(store,
		WithReplayHeaders("X-Request-Id", "req-123"),
		WithReplayHeaders("X-Trace-Id", "trace-456"),
		WithReplayHeaders("Cache-Control", "no-store"),
	)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/multi", nil)

	srv.ServeHTTP(rec, req)

	tests := map[string]string{
		"X-Request-Id":  "req-123",
		"X-Trace-Id":    "trace-456",
		"Cache-Control": "no-store",
	}
	for key, want := range tests {
		if got := rec.Header().Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestServer_ReplayHeaders_NotSetByDefault(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/default", 200, "ok", http.Header{
		"X-Original": {"value"},
	})

	srv, err := NewServer(store) // no WithReplayHeaders
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/default", nil)

	srv.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Original"); got != "value" {
		t.Errorf("X-Original = %q, want %q", got, "value")
	}
}

// --- Templating integration tests ---

func TestServer_Templating_BodySubstitution(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "POST", "/echo", 200,
		`{"method":"{{request.method}}","path":"{{request.path}}"}`, nil)

	srv, err := NewServer(store)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/echo", nil)

	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	want := `{"method":"POST","path":"/echo"}`
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestServer_Templating_HeaderSubstitution(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "POST", "/payments", 200, `{"ok":true}`, http.Header{
		"X-Idempotency-Key": {"{{request.headers.Idempotency-Key}}"},
	})

	srv, err := NewServer(store)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/payments", nil)
	req.Header.Set("Idempotency-Key", "idem-xyz-456")

	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("X-Idempotency-Key"); got != "idem-xyz-456" {
		t.Errorf("X-Idempotency-Key = %q, want %q", got, "idem-xyz-456")
	}
}

func TestServer_Templating_Disabled(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/raw", 200, `{{request.method}}`, nil)

	srv, err := NewServer(store, WithTemplating(false))
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/raw", nil)

	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// With templating disabled, body should be returned verbatim.
	if got := rec.Body.String(); got != "{{request.method}}" {
		t.Errorf("body = %q, want %q", got, "{{request.method}}")
	}
}

func TestServer_Templating_StrictMode_Error(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/strict", 200, `{{request.headers.Missing}}`, nil)

	srv, err := NewServer(store, WithStrictTemplating(true))
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/strict", nil)

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if got := rec.Header().Get("X-Httptape-Error"); got != "template" {
		t.Errorf("X-Httptape-Error = %q, want %q", got, "template")
	}
	if !strings.Contains(rec.Body.String(), "request.headers.Missing") {
		t.Errorf("body = %q, should mention the failed expression", rec.Body.String())
	}
}

func TestServer_Templating_StrictMode_HeaderError(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/strict-hdr", 200, "ok", http.Header{
		"X-Echo": {"{{request.headers.Missing}}"},
	})

	srv, err := NewServer(store, WithStrictTemplating(true))
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/strict-hdr", nil)

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if got := rec.Header().Get("X-Httptape-Error"); got != "template" {
		t.Errorf("X-Httptape-Error = %q, want %q", got, "template")
	}
}

func TestServer_Templating_LenientMode_MissingRef(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/lenient", 200,
		`key={{request.headers.Missing}}`, nil)

	srv, err := NewServer(store) // default: lenient
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/lenient", nil)

	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "key=" {
		t.Errorf("body = %q, want %q", got, "key=")
	}
}

func TestServer_Templating_NoTemplates_FastPath(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/plain", 200, `{"static":"response"}`, nil)

	srv, err := NewServer(store)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/plain", nil)

	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != `{"static":"response"}` {
		t.Errorf("body = %q, want %q", got, `{"static":"response"}`)
	}
}

func TestServer_Templating_QueryParam(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/search", 200,
		`{"q":"{{request.query.q}}","page":"{{request.query.page}}"}`, nil)

	srv, err := NewServer(store)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/search?q=hello&page=3", nil)

	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// JSON-aware resolution re-marshals, which sorts keys alphabetically.
	want := `{"page":"3","q":"hello"}`
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestServer_Templating_BodyField(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "POST", "/echo-body", 200,
		`{"echo_email":"{{request.body.user.email}}"}`, nil)

	srv, err := NewServer(store)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/echo-body",
		strings.NewReader(`{"user":{"email":"test@example.com"}}`))

	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	want := `{"echo_email":"test@example.com"}`
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestServer_Templating_LeavesStoredFixtureUnchanged(t *testing.T) {
	store := NewMemoryStore()
	tape := storeTape(t, store, "GET", "/immutable", 200,
		`{{request.method}}`, nil)

	srv, err := NewServer(store)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/immutable", nil)

	srv.ServeHTTP(rec, req)

	// The response should be resolved.
	if got := rec.Body.String(); got != "GET" {
		t.Errorf("body = %q, want %q", got, "GET")
	}

	// The stored fixture should be unchanged.
	loaded, err := store.Load(context.Background(), tape.ID)
	if err != nil {
		t.Fatalf("load tape: %v", err)
	}
	if string(loaded.Response.Body) != "{{request.method}}" {
		t.Errorf("stored body = %q, should be unchanged", string(loaded.Response.Body))
	}
}

func TestServer_Templating_WithCORS(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/cors-template", 200,
		`{{request.method}}`, nil)

	srv, err := NewServer(store, WithCORS())
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/cors-template", nil)

	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "GET" {
		t.Errorf("body = %q, want %q", got, "GET")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("CORS header missing: %q", got)
	}
}

func TestServer_Templating_EnabledByDefault(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/default-on", 200, `{{request.method}}`, nil)

	// NewServer without any templating options — should be enabled by default.
	srv, err := NewServer(store)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/default-on", nil)

	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "GET" {
		t.Errorf("body = %q, want %q (templating should be on by default)", got, "GET")
	}
}

func TestServer_Templating_UnknownNamespace_Lenient(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/state", 200,
		`count={{state.counter}}`, nil)

	srv, err := NewServer(store)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/state", nil)

	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// Unknown namespace expressions are replaced with empty string in lenient mode.
	if got := rec.Body.String(); got != "count=" {
		t.Errorf("body = %q, want %q", got, "count=")
	}
}

func TestServer_Templating_URL(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/url-echo", 200,
		`url={{request.url}}`, nil)

	srv, err := NewServer(store)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/url-echo?key=val", nil)

	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "url=/url-echo?key=val" {
		t.Errorf("body = %q, want %q", got, "url=/url-echo?key=val")
	}
}

func TestServer_ResetCounter(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/count", 200, `{{counter}}`, nil)

	srv, err := NewServer(store)
	if err != nil {
		t.Fatal(err)
	}

	// Increment counter.
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/count", nil))
	if got := rec.Body.String(); got != "1" {
		t.Errorf("first call = %q, want %q", got, "1")
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/count", nil))
	if got := rec.Body.String(); got != "2" {
		t.Errorf("second call = %q, want %q", got, "2")
	}

	// Reset all counters.
	srv.ResetCounter("")

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/count", nil))
	if got := rec.Body.String(); got != "1" {
		t.Errorf("after reset = %q, want %q", got, "1")
	}
}

func TestServer_PathParamTemplating(t *testing.T) {
	store := NewMemoryStore()

	// Store a tape with a path that matches the pattern.
	tape := NewTape("", RecordedReq{
		Method: "GET",
		URL:    "/users/42",
	}, RecordedResp{
		StatusCode: 200,
		Body:       []byte(`id={{pathParam.id}}`),
	})
	if err := store.Save(context.Background(), tape); err != nil {
		t.Fatal(err)
	}

	criterion, err := NewPathPatternCriterion("/users/:id")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(store, WithMatcher(NewCompositeMatcher(
		MethodCriterion{},
		criterion,
	)))
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/users/42", nil))

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "id=42" {
		t.Errorf("body = %q, want %q", got, "id=42")
	}
}

func TestServer_HelperTemplating(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/helpers", 200,
		`counter={{counter name=c1}} faker={{faker.email seed=test}}`, nil)

	srv, err := NewServer(store)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/helpers", nil))

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "counter=1") {
		t.Errorf("expected counter=1 in body, got %q", body)
	}
	if !strings.Contains(body, "faker=") {
		t.Errorf("expected faker= in body, got %q", body)
	}
	if !strings.Contains(body, "@example.com") {
		t.Errorf("expected email format in body, got %q", body)
	}
}

func TestServer_CounterAcrossRequests(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/inc", 200, `{{counter}}`, nil)

	srv, err := NewServer(store)
	if err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 3; i++ {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest("GET", "/inc", nil))
		want := fmt.Sprintf("%d", i)
		if got := rec.Body.String(); got != want {
			t.Errorf("request %d: body = %q, want %q", i, got, want)
		}
	}
}

// --- ADR-43: Synthesis mode tests ---

// storeExemplarTape creates and saves an exemplar tape to the store.
func storeExemplarTape(t *testing.T, store *MemoryStore, method, urlPattern string, status int, body string, headers http.Header) Tape {
	t.Helper()
	tape := Tape{
		ID:       newUUID(),
		Exemplar: true,
		Request: RecordedReq{
			Method:     method,
			URLPattern: urlPattern,
		},
		Response: RecordedResp{
			StatusCode: status,
			Headers:    headers,
			Body:       []byte(body),
		},
	}
	if err := store.Save(context.Background(), tape); err != nil {
		t.Fatalf("storeExemplarTape: failed to save tape: %v", err)
	}
	return tape
}

func TestServer_SynthesisDisabled_ExemplarIgnored(t *testing.T) {
	store := NewMemoryStore()
	storeExemplarTape(t, store, "GET", "/users/:id", 200,
		`{"id":"{{pathParam.id | int}}"}`,
		http.Header{"Content-Type": {"application/json"}})

	srv, err := NewServer(store) // No WithSynthesis
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/users/42", nil)
	srv.ServeHTTP(rec, req)

	if rec.Code != 404 {
		t.Errorf("status = %d, want 404 (exemplar should be ignored without synthesis)", rec.Code)
	}
}

func TestServer_SynthesisEnabled_ExactWins(t *testing.T) {
	store := NewMemoryStore()
	// Exact tape for /users/1
	storeTape(t, store, "GET", "/users/1", 200, `{"id":1,"exact":true}`,
		http.Header{"Content-Type": {"application/json"}})
	// Exemplar for /users/:id
	storeExemplarTape(t, store, "GET", "/users/:id", 200,
		`{"id":"{{pathParam.id | int}}","exact":false}`,
		http.Header{"Content-Type": {"application/json"}})

	srv, err := NewServer(store, WithSynthesis())
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/users/1", nil)
	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"exact":true`) {
		t.Errorf("exact tape should win over exemplar, got: %s", body)
	}
}

func TestServer_SynthesisEnabled_ExemplarFallback(t *testing.T) {
	store := NewMemoryStore()
	// Exact tape for /users/1 only
	storeTape(t, store, "GET", "/users/1", 200, `{"id":1}`,
		http.Header{"Content-Type": {"application/json"}})
	// Exemplar for /users/:id
	storeExemplarTape(t, store, "GET", "/users/:id", 200,
		`{"id":"{{pathParam.id | int}}"}`,
		http.Header{"Content-Type": {"application/json"}})

	srv, err := NewServer(store, WithSynthesis())
	if err != nil {
		t.Fatal(err)
	}

	// Request /users/2 -- no exact match, should fall back to exemplar.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/users/2", nil)
	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	want := `{"id":2}`
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func TestServer_SynthesisEnabled_MostSpecificExemplar(t *testing.T) {
	store := NewMemoryStore()
	// Less specific: /users/:id
	storeExemplarTape(t, store, "GET", "/users/:id", 200,
		`{"type":"user"}`,
		http.Header{"Content-Type": {"application/json"}})
	// More specific: /users/:id/orders
	storeExemplarTape(t, store, "GET", "/users/:id/orders", 200,
		`{"type":"orders"}`,
		http.Header{"Content-Type": {"application/json"}})

	srv, err := NewServer(store, WithSynthesis())
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/users/42/orders", nil)
	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	want := `{"type":"orders"}`
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %s, want %s (more specific exemplar should win)", got, want)
	}
}

func TestServer_SynthesisEnabled_MethodFilter(t *testing.T) {
	store := NewMemoryStore()
	// Exemplar for GET /users/:id only
	storeExemplarTape(t, store, "GET", "/users/:id", 200,
		`{"id":"{{pathParam.id | int}}"}`,
		http.Header{"Content-Type": {"application/json"}})

	srv, err := NewServer(store, WithSynthesis())
	if err != nil {
		t.Fatal(err)
	}

	// POST /users/42 should not match GET exemplar.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/users/42", nil)
	srv.ServeHTTP(rec, req)

	if rec.Code != 404 {
		t.Errorf("status = %d, want 404 (method mismatch should eliminate exemplar)", rec.Code)
	}
}

func TestServer_SynthesisEnabled_NoExemplars(t *testing.T) {
	store := NewMemoryStore()
	storeTape(t, store, "GET", "/users/1", 200, `{"id":1}`,
		http.Header{"Content-Type": {"application/json"}})

	srv, err := NewServer(store, WithSynthesis())
	if err != nil {
		t.Fatal(err)
	}

	// Request for unmatched URL, no exemplars in store.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/users/999", nil)
	srv.ServeHTTP(rec, req)

	if rec.Code != 404 {
		t.Errorf("status = %d, want 404 (no exemplars available)", rec.Code)
	}
}

func TestServer_SynthesisEnabled_DeterministicResponse(t *testing.T) {
	store := NewMemoryStore()
	storeExemplarTape(t, store, "GET", "/users/:id", 200,
		`{"id":"{{pathParam.id | int}}"}`,
		http.Header{"Content-Type": {"application/json"}})

	srv, err := NewServer(store, WithSynthesis())
	if err != nil {
		t.Fatal(err)
	}

	// Request /users/42 twice, expect identical responses.
	var bodies [2]string
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/users/42", nil)
		srv.ServeHTTP(rec, req)
		bodies[i] = rec.Body.String()
	}

	if bodies[0] != bodies[1] {
		t.Errorf("deterministic mismatch: first=%q, second=%q", bodies[0], bodies[1])
	}
}

func TestServer_SynthesisEnabled_ExemplarWithTextBody(t *testing.T) {
	store := NewMemoryStore()
	storeExemplarTape(t, store, "GET", "/greet/:name", 200,
		"Hello, {{pathParam.name}}!",
		http.Header{"Content-Type": {"text/plain"}})

	srv, err := NewServer(store, WithSynthesis())
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/greet/Alice", nil)
	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	want := "Hello, Alice!"
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestServer_SynthesisEnabled_PatternWithNoParams(t *testing.T) {
	store := NewMemoryStore()
	// An exemplar with a pattern that has no parameters (effectively exact path).
	storeExemplarTape(t, store, "GET", "/api/health", 200,
		`{"status":"ok"}`,
		http.Header{"Content-Type": {"application/json"}})

	srv, err := NewServer(store, WithSynthesis())
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/health", nil)
	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	want := `{"status":"ok"}`
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

// ---------------------------------------------------------------------------
// ResponseTimingMode
// ---------------------------------------------------------------------------

func TestResponseTimingInstant(t *testing.T) {
	mode := ResponseTimingInstant()

	tests := []struct {
		name      string
		elapsedMS int64
		want      time.Duration
	}{
		{"zero elapsed", 0, 0},
		{"positive elapsed", 500, 0},
		{"negative elapsed", -1, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mode.responseDelay(tt.elapsedMS)
			if got != tt.want {
				t.Errorf("responseDelay(%d) = %v, want %v", tt.elapsedMS, got, tt.want)
			}
		})
	}
}

func TestResponseTimingRecorded(t *testing.T) {
	mode := ResponseTimingRecorded()

	tests := []struct {
		name      string
		elapsedMS int64
		want      time.Duration
	}{
		{"zero elapsed returns no delay", 0, 0},
		{"positive elapsed returns that duration", 250, 250 * time.Millisecond},
		{"negative elapsed returns no delay", -1, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mode.responseDelay(tt.elapsedMS)
			if got != tt.want {
				t.Errorf("responseDelay(%d) = %v, want %v", tt.elapsedMS, got, tt.want)
			}
		})
	}
}

func TestResponseTimingAccelerated(t *testing.T) {
	t.Run("factor 2 doubles elapsed time", func(t *testing.T) {
		mode, err := ResponseTimingAccelerated(2.0)
		if err != nil {
			t.Fatal(err)
		}
		got := mode.responseDelay(100)
		if got != 200*time.Millisecond {
			t.Errorf("responseDelay(100) with factor 2 = %v, want 200ms", got)
		}
	})

	t.Run("factor 0.5 halves elapsed time", func(t *testing.T) {
		mode, err := ResponseTimingAccelerated(0.5)
		if err != nil {
			t.Fatal(err)
		}
		got := mode.responseDelay(100)
		if got != 50*time.Millisecond {
			t.Errorf("responseDelay(100) with factor 0.5 = %v, want 50ms", got)
		}
	})

	t.Run("zero elapsed returns no delay regardless of factor", func(t *testing.T) {
		mode, err := ResponseTimingAccelerated(2.0)
		if err != nil {
			t.Fatal(err)
		}
		got := mode.responseDelay(0)
		if got != 0 {
			t.Errorf("responseDelay(0) = %v, want 0", got)
		}
	})

	t.Run("negative factor returns error", func(t *testing.T) {
		_, err := ResponseTimingAccelerated(-1.0)
		if err == nil {
			t.Error("expected error for negative factor")
		}
	})

	t.Run("zero factor returns error", func(t *testing.T) {
		_, err := ResponseTimingAccelerated(0.0)
		if err == nil {
			t.Error("expected error for zero factor")
		}
	})
}

// ---------------------------------------------------------------------------
// WithReplayTiming on Server
// ---------------------------------------------------------------------------

func TestServer_WithReplayTiming_Recorded(t *testing.T) {
	store := NewMemoryStore()
	tape := NewTape("", RecordedReq{
		Method: "GET",
		URL:    "/api/data",
	}, RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{"Content-Type": {"text/plain"}},
		Body:       []byte("ok"),
		ElapsedMS:  300,
	})
	store.Save(context.Background(), tape)

	var sleepCalled bool
	var sleepDuration time.Duration

	srv, err := NewServer(store,
		WithReplayTiming(ResponseTimingRecorded()),
		withSleepFunc(func(_ context.Context, d time.Duration) error {
			sleepCalled = true
			sleepDuration = d
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)
	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !sleepCalled {
		t.Error("sleepFunc was not called for recorded timing mode")
	}
	if sleepDuration != 300*time.Millisecond {
		t.Errorf("sleepDuration = %v, want 300ms", sleepDuration)
	}
}

func TestServer_WithReplayTiming_Instant(t *testing.T) {
	store := NewMemoryStore()
	tape := NewTape("", RecordedReq{
		Method: "GET",
		URL:    "/api/data",
	}, RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{"Content-Type": {"text/plain"}},
		Body:       []byte("ok"),
		ElapsedMS:  300,
	})
	store.Save(context.Background(), tape)

	var sleepCalled bool

	srv, err := NewServer(store,
		WithReplayTiming(ResponseTimingInstant()),
		withSleepFunc(func(_ context.Context, d time.Duration) error {
			sleepCalled = true
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)
	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if sleepCalled {
		t.Error("sleepFunc was called with instant timing mode (should not be)")
	}
}

func TestServer_WithReplayTiming_PreFeatureFixtureNoDelay(t *testing.T) {
	store := NewMemoryStore()
	tape := NewTape("", RecordedReq{
		Method: "GET",
		URL:    "/api/data",
	}, RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{"Content-Type": {"text/plain"}},
		Body:       []byte("ok"),
		ElapsedMS:  0, // pre-feature fixture
	})
	store.Save(context.Background(), tape)

	var sleepCalled bool

	srv, err := NewServer(store,
		WithReplayTiming(ResponseTimingRecorded()),
		withSleepFunc(func(_ context.Context, d time.Duration) error {
			sleepCalled = true
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)
	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if sleepCalled {
		t.Error("sleepFunc was called for pre-feature fixture (ElapsedMS=0)")
	}
}

func TestServer_WithReplayTiming_AdditiveWithDelay(t *testing.T) {
	store := NewMemoryStore()
	tape := NewTape("", RecordedReq{
		Method: "GET",
		URL:    "/api/data",
	}, RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{"Content-Type": {"text/plain"}},
		Body:       []byte("ok"),
		ElapsedMS:  200,
	})
	store.Save(context.Background(), tape)

	var sleepDurations []time.Duration

	srv, err := NewServer(store,
		WithDelay(100*time.Millisecond),
		WithReplayTiming(ResponseTimingRecorded()),
		withSleepFunc(func(_ context.Context, d time.Duration) error {
			sleepDurations = append(sleepDurations, d)
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)
	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	// The replay timing delay should be 200ms (recorded elapsed).
	// WithDelay uses its own timer/select, not sleepFunc. The sleepFunc
	// only captures the replay timing delay.
	if len(sleepDurations) != 1 {
		t.Fatalf("sleepFunc called %d times, want 1", len(sleepDurations))
	}
	if sleepDurations[0] != 200*time.Millisecond {
		t.Errorf("replay timing sleep = %v, want 200ms", sleepDurations[0])
	}
}

func TestServer_WithReplayTiming_ContextCancelled(t *testing.T) {
	store := NewMemoryStore()
	tape := NewTape("", RecordedReq{
		Method: "GET",
		URL:    "/api/data",
	}, RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{"Content-Type": {"text/plain"}},
		Body:       []byte("ok"),
		ElapsedMS:  1000,
	})
	store.Save(context.Background(), tape)

	sleepCalled := false
	srv, err := NewServer(store,
		WithReplayTiming(ResponseTimingRecorded()),
		withSleepFunc(func(ctx context.Context, _ time.Duration) error {
			sleepCalled = true
			return ctx.Err()
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	// Use a valid context for the store.List call, but cancel before
	// the sleep runs.
	rec := httptest.NewRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/api/data", nil).WithContext(ctx)
	cancel() // cancel after request is built but before ServeHTTP

	srv.ServeHTTP(rec, req)

	// When context is cancelled, store.List may fail (returning 500
	// "store error") or the sleepFunc may return early. Either path
	// means the normal 200 body is NOT returned.
	if sleepCalled {
		// If sleep was reached, it returned ctx.Err() and the server
		// bailed out without writing the normal response body.
		if rec.Body.String() == "ok" {
			t.Error("expected response body != 'ok' when context was cancelled during sleep")
		}
	}
	// If store.List failed due to the cancelled context, the server
	// returns 500 "store error" -- that's also acceptable.
}

// ---------------------------------------------------------------------------
// defaultSleepFunc
// ---------------------------------------------------------------------------

func TestDefaultSleepFunc_ZeroDuration(t *testing.T) {
	err := defaultSleepFunc(context.Background(), 0)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDefaultSleepFunc_NegativeDuration(t *testing.T) {
	err := defaultSleepFunc(context.Background(), -1*time.Millisecond)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDefaultSleepFunc_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := defaultSleepFunc(ctx, 10*time.Second)
	if err == nil {
		t.Error("expected error from cancelled context")
	}
}
