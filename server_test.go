package httptape

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
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

func (f *errListStore) Save(_ context.Context, _ Tape) error            { return errors.New("fail") }
func (f *errListStore) Load(_ context.Context, _ string) (Tape, error)  { return Tape{}, errors.New("fail") }
func (f *errListStore) List(_ context.Context, _ Filter) ([]Tape, error) { return nil, errors.New("fail") }
func (f *errListStore) Delete(_ context.Context, _ string) error        { return errors.New("fail") }

func TestServer_BasicReplay(t *testing.T) {
	store := NewMemoryStore()
	tape := storeTape(t, store, "GET", "/api/users", 200, `{"users":[]}`, http.Header{
		"Content-Type": {"application/json"},
	})

	srv := NewServer(store)
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

	srv := NewServer(store)
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
	srv := NewServer(store)

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
	srv := NewServer(store,
		WithFallbackStatus(http.StatusServiceUnavailable),
		WithFallbackBody([]byte("custom fallback")),
	)

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

	srv := NewServer(store, WithOnNoMatch(func(r *http.Request) {
		called.Add(1)
		mu.Lock()
		capturedPath = r.URL.Path
		mu.Unlock()
	}))

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
	srv := NewServer(&errListStore{})

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

	srv := NewServer(store, WithMatcher(customMatcher))

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

	srv := NewServer(store)
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

	srv := NewServer(store)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/bar", nil)

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestServer_EmptyStore(t *testing.T) {
	store := NewMemoryStore()
	srv := NewServer(store)

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

	srv := NewServer(store)
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

	srv := NewServer(store)

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

	srv := NewServer(store)
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
