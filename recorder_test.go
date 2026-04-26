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
	"testing"
	"time"
)

// --- test helpers ---

// fakeSanitizer implements Sanitizer by uppercasing the route.
type fakeSanitizer struct{}

func (fakeSanitizer) Sanitize(t Tape) Tape {
	t.Route = strings.ToUpper(t.Route)
	return t
}

// failStore is a Store that always returns an error on Save.
type failStore struct {
	MemoryStore
	saveErr error
}

func (s *failStore) Save(_ context.Context, _ Tape) error {
	return s.saveErr
}

// --- NewRecorder tests ---

func TestNewRecorder_Defaults(t *testing.T) {
	store := NewMemoryStore()
	rec := NewRecorder(store)
	defer rec.Close()

	if rec.store != store {
		t.Error("store not set")
	}
	if rec.transport != http.DefaultTransport {
		t.Error("transport should default to http.DefaultTransport")
	}
	if !rec.async {
		t.Error("async should default to true")
	}
	if rec.sampleRate != 1.0 {
		t.Errorf("sampleRate = %v, want 1.0", rec.sampleRate)
	}
	if rec.bufSize != 1024 {
		t.Errorf("bufSize = %d, want 1024", rec.bufSize)
	}
	if rec.route != "" {
		t.Errorf("route = %q, want empty", rec.route)
	}
	if rec.sanitizer == nil {
		t.Fatal("sanitizer should default to non-nil Pipeline")
	}
	if _, ok := rec.sanitizer.(*Pipeline); !ok {
		t.Errorf("sanitizer type = %T, want *Pipeline", rec.sanitizer)
	}
	if rec.tapeCh == nil {
		t.Error("tapeCh should be initialized in async mode")
	}
	if rec.done == nil {
		t.Error("done channel should be initialized in async mode")
	}
}

func TestNewRecorder_WithOptions(t *testing.T) {
	store := NewMemoryStore()
	transport := http.DefaultTransport
	san := fakeSanitizer{}
	var capturedErr error
	onErr := func(err error) { capturedErr = err }
	_ = capturedErr

	rec := NewRecorder(store,
		WithTransport(transport),
		WithRoute("my-route"),
		WithSanitizer(san),
		WithSampling(0.5),
		WithAsync(false),
		WithBufferSize(64),
		WithOnError(onErr),
	)

	if rec.route != "my-route" {
		t.Errorf("route = %q, want %q", rec.route, "my-route")
	}
	if rec.sampleRate != 0.5 {
		t.Errorf("sampleRate = %v, want 0.5", rec.sampleRate)
	}
	if rec.async {
		t.Error("async should be false")
	}
	if rec.bufSize != 64 {
		t.Errorf("bufSize = %d, want 64", rec.bufSize)
	}
	// In sync mode, channels should be nil.
	if rec.tapeCh != nil {
		t.Error("tapeCh should be nil in sync mode")
	}
	if rec.done != nil {
		t.Error("done should be nil in sync mode")
	}
}

// --- WithSampling clamping tests ---

func TestWithSampling_Clamping(t *testing.T) {
	tests := []struct {
		name string
		rate float64
		want float64
	}{
		{"negative clamped to 0", -1.0, 0.0},
		{"zero stays zero", 0.0, 0.0},
		{"normal value", 0.5, 0.5},
		{"one stays one", 1.0, 1.0},
		{"above one clamped", 2.0, 1.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewMemoryStore()
			rec := NewRecorder(store, WithSampling(tt.rate), WithAsync(false))
			if rec.sampleRate != tt.want {
				t.Errorf("sampleRate = %v, want %v", rec.sampleRate, tt.want)
			}
		})
	}
}

// --- WithBufferSize clamping ---

func TestWithBufferSize_Clamping(t *testing.T) {
	tests := []struct {
		name string
		size int
		want int
	}{
		{"zero clamped to 1", 0, 1},
		{"negative clamped to 1", -10, 1},
		{"valid size", 512, 512},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewMemoryStore()
			rec := NewRecorder(store, WithBufferSize(tt.size), WithAsync(false))
			if rec.bufSize != tt.want {
				t.Errorf("bufSize = %d, want %d", rec.bufSize, tt.want)
			}
		})
	}
}

// --- RoundTrip tests ---

func TestRecorder_RoundTrip_SyncMode(t *testing.T) {
	// Set up a test server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Custom", "value")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("response body"))
	}))
	defer srv.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store,
		WithTransport(srv.Client().Transport),
		WithRoute("test-route"),
		WithAsync(false),
	)

	req, err := http.NewRequest("POST", srv.URL+"/test", strings.NewReader("request body"))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "text/plain")

	resp, err := rec.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	defer resp.Body.Close()

	// Verify the response is passed through correctly.
	if resp.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "response body" {
		t.Errorf("response body = %q, want %q", body, "response body")
	}

	// Verify a tape was saved.
	tapes, err := store.List(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	if len(tapes) != 1 {
		t.Fatalf("len(tapes) = %d, want 1", len(tapes))
	}

	tape := tapes[0]
	if tape.Route != "test-route" {
		t.Errorf("tape.Route = %q, want %q", tape.Route, "test-route")
	}
	if tape.Request.Method != "POST" {
		t.Errorf("tape.Request.Method = %q, want %q", tape.Request.Method, "POST")
	}
	if !strings.HasSuffix(tape.Request.URL, "/test") {
		t.Errorf("tape.Request.URL = %q, want suffix /test", tape.Request.URL)
	}
	if string(tape.Request.Body) != "request body" {
		t.Errorf("tape.Request.Body = %q, want %q", tape.Request.Body, "request body")
	}
	if tape.Request.BodyHash != BodyHashFromBytes([]byte("request body")) {
		t.Errorf("tape.Request.BodyHash mismatch")
	}
	if string(tape.Response.Body) != "response body" {
		t.Errorf("tape.Response.Body = %q, want %q", tape.Response.Body, "response body")
	}
	if tape.Response.StatusCode != 200 {
		t.Errorf("tape.Response.StatusCode = %d, want 200", tape.Response.StatusCode)
	}
}

func TestRecorder_RoundTrip_AsyncMode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("async response"))
	}))
	defer srv.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store,
		WithTransport(srv.Client().Transport),
		WithRoute("async-route"),
		WithAsync(true),
		WithBufferSize(16),
	)

	req, err := http.NewRequest("GET", srv.URL+"/async", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	resp, err := rec.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("StatusCode = %d, want %d", resp.StatusCode, http.StatusCreated)
	}

	// Close to flush pending tapes.
	if err := rec.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	tapes, err := store.List(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	if len(tapes) != 1 {
		t.Fatalf("len(tapes) = %d, want 1", len(tapes))
	}
	if tapes[0].Route != "async-route" {
		t.Errorf("tape.Route = %q, want %q", tapes[0].Route, "async-route")
	}
}

func TestRecorder_RoundTrip_NilRequestBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store,
		WithTransport(srv.Client().Transport),
		WithAsync(false),
	)

	req, err := http.NewRequest("GET", srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	resp, err := rec.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	resp.Body.Close()

	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 1 {
		t.Fatalf("len(tapes) = %d, want 1", len(tapes))
	}
	if tapes[0].Request.Body != nil {
		t.Errorf("request body should be nil for GET, got %v", tapes[0].Request.Body)
	}
	if tapes[0].Request.BodyHash != "" {
		t.Errorf("body hash should be empty, got %q", tapes[0].Request.BodyHash)
	}
}

func TestRecorder_RoundTrip_TransportError(t *testing.T) {
	// Use a transport that always fails.
	failTransport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})

	store := NewMemoryStore()
	rec := NewRecorder(store,
		WithTransport(failTransport),
		WithAsync(false),
	)

	req, err := http.NewRequest("GET", "http://unreachable.example.com", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	resp, err := rec.RoundTrip(req)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if resp != nil {
		t.Fatal("expected nil response on error")
	}

	// No tape should have been created.
	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 0 {
		t.Errorf("len(tapes) = %d, want 0 (no tape for transport errors)", len(tapes))
	}
}

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// --- Sampling tests ---

func TestRecorder_RoundTrip_Sampling_AllRecorded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store,
		WithTransport(srv.Client().Transport),
		WithSampling(1.0),
		WithAsync(false),
	)

	for i := 0; i < 5; i++ {
		req, _ := http.NewRequest("GET", srv.URL, nil)
		resp, err := rec.RoundTrip(req)
		if err != nil {
			t.Fatalf("RoundTrip error: %v", err)
		}
		resp.Body.Close()
	}

	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 5 {
		t.Errorf("len(tapes) = %d, want 5 with sampleRate=1.0", len(tapes))
	}
}

func TestRecorder_RoundTrip_Sampling_NoneRecorded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := NewMemoryStore()
	// randFloat always returns 0.5, sampleRate=0.0 means nothing recorded.
	rec := NewRecorder(store,
		WithTransport(srv.Client().Transport),
		WithSampling(0.0),
		WithAsync(false),
	)
	rec.randFloat = func() float64 { return 0.5 }

	for i := 0; i < 5; i++ {
		req, _ := http.NewRequest("GET", srv.URL, nil)
		resp, err := rec.RoundTrip(req)
		if err != nil {
			t.Fatalf("RoundTrip error: %v", err)
		}
		resp.Body.Close()
	}

	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 0 {
		t.Errorf("len(tapes) = %d, want 0 with sampleRate=0.0", len(tapes))
	}
}

func TestRecorder_RoundTrip_Sampling_Probabilistic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := NewMemoryStore()
	callCount := 0
	rec := NewRecorder(store,
		WithTransport(srv.Client().Transport),
		WithSampling(0.5),
		WithAsync(false),
	)
	// Alternating: 0.3 (< 0.5, recorded), 0.7 (>= 0.5, skipped)
	rec.randFloat = func() float64 {
		callCount++
		if callCount%2 == 1 {
			return 0.3
		}
		return 0.7
	}

	for i := 0; i < 6; i++ {
		req, _ := http.NewRequest("GET", srv.URL, nil)
		resp, err := rec.RoundTrip(req)
		if err != nil {
			t.Fatalf("RoundTrip error: %v", err)
		}
		resp.Body.Close()
	}

	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 3 {
		t.Errorf("len(tapes) = %d, want 3 (half of 6 with alternating sampling)", len(tapes))
	}
}

// --- Sanitizer tests ---

func TestRecorder_RoundTrip_WithSanitizer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store,
		WithTransport(srv.Client().Transport),
		WithRoute("lower-route"),
		WithSanitizer(fakeSanitizer{}),
		WithAsync(false),
	)

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := rec.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	resp.Body.Close()

	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 1 {
		t.Fatalf("len(tapes) = %d, want 1", len(tapes))
	}
	if tapes[0].Route != "LOWER-ROUTE" {
		t.Errorf("tape.Route = %q, want %q (sanitizer should uppercase)", tapes[0].Route, "LOWER-ROUTE")
	}
}

// --- Error handling tests ---

func TestRecorder_RoundTrip_SyncMode_StoreErrorIsolatedFromCaller(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("all good"))
	}))
	defer srv.Close()

	saveErr := errors.New("disk full")
	store := &failStore{saveErr: saveErr}
	var capturedErr error
	rec := NewRecorder(store,
		WithTransport(srv.Client().Transport),
		WithAsync(false),
		WithOnError(func(err error) { capturedErr = err }),
	)

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := rec.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip should not return error on store failure, got: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "all good" {
		t.Errorf("response body = %q, want %q", body, "all good")
	}
	if capturedErr != saveErr {
		t.Errorf("onError got %v, want %v", capturedErr, saveErr)
	}
}

func TestRecorder_RoundTrip_AsyncMode_StoreError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	saveErr := errors.New("write failed")
	store := &failStore{saveErr: saveErr}
	var mu sync.Mutex
	var capturedErrs []error
	rec := NewRecorder(store,
		WithTransport(srv.Client().Transport),
		WithAsync(true),
		WithBufferSize(16),
		WithOnError(func(err error) {
			mu.Lock()
			capturedErrs = append(capturedErrs, err)
			mu.Unlock()
		}),
	)

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := rec.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	resp.Body.Close()

	rec.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(capturedErrs) != 1 {
		t.Fatalf("len(capturedErrs) = %d, want 1", len(capturedErrs))
	}
	if capturedErrs[0] != saveErr {
		t.Errorf("onError got %v, want %v", capturedErrs[0], saveErr)
	}
}

func TestRecorder_RoundTrip_AsyncMode_BufferFull(t *testing.T) {
	// Use a transport that is slow so the channel fills up.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Use a store that blocks Save so the channel can't drain.
	blockCh := make(chan struct{})
	blockingStore := &blockingSaveStore{
		MemoryStore: *NewMemoryStore(),
		blockCh:     blockCh,
	}

	var mu sync.Mutex
	var dropErrors []error
	rec := NewRecorder(blockingStore,
		WithTransport(srv.Client().Transport),
		WithAsync(true),
		WithBufferSize(1),
		WithOnError(func(err error) {
			mu.Lock()
			dropErrors = append(dropErrors, err)
			mu.Unlock()
		}),
	)

	// Fill the buffer: first call occupies the single slot.
	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, _ := rec.RoundTrip(req)
	resp.Body.Close()

	// Wait a moment for the drain goroutine to pick up the tape and block.
	time.Sleep(50 * time.Millisecond)

	// Second call: the drain goroutine is blocked, so this tape fills the buffer.
	req2, _ := http.NewRequest("GET", srv.URL, nil)
	resp2, _ := rec.RoundTrip(req2)
	resp2.Body.Close()

	// Third call: buffer is full, should drop.
	req3, _ := http.NewRequest("GET", srv.URL, nil)
	resp3, _ := rec.RoundTrip(req3)
	resp3.Body.Close()

	// Unblock the store and close.
	close(blockCh)
	rec.Close()

	mu.Lock()
	defer mu.Unlock()
	hasDropError := false
	for _, e := range dropErrors {
		if strings.Contains(e.Error(), "buffer full") {
			hasDropError = true
		}
	}
	if !hasDropError {
		t.Error("expected at least one 'buffer full' error from onError")
	}
}

// blockingSaveStore blocks on Save until blockCh is closed.
type blockingSaveStore struct {
	MemoryStore
	blockCh chan struct{}
	once    sync.Once
}

func (s *blockingSaveStore) Save(ctx context.Context, tape Tape) error {
	s.once.Do(func() {
		<-s.blockCh
	})
	return s.MemoryStore.Save(ctx, tape)
}

// --- Close tests ---

func TestRecorder_Close_Idempotent(t *testing.T) {
	store := NewMemoryStore()
	rec := NewRecorder(store, WithAsync(true), WithBufferSize(8))

	// Close multiple times should not panic.
	if err := rec.Close(); err != nil {
		t.Errorf("first Close error: %v", err)
	}
	if err := rec.Close(); err != nil {
		t.Errorf("second Close error: %v", err)
	}
	if err := rec.Close(); err != nil {
		t.Errorf("third Close error: %v", err)
	}
}

func TestRecorder_Close_SyncMode_NoOp(t *testing.T) {
	store := NewMemoryStore()
	rec := NewRecorder(store, WithAsync(false))

	if err := rec.Close(); err != nil {
		t.Errorf("Close in sync mode error: %v", err)
	}
}

func TestRecorder_Close_FlushesAllPending(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store,
		WithTransport(srv.Client().Transport),
		WithAsync(true),
		WithBufferSize(1024),
	)

	// Send multiple requests.
	for i := 0; i < 20; i++ {
		req, _ := http.NewRequest("GET", srv.URL, nil)
		resp, err := rec.RoundTrip(req)
		if err != nil {
			t.Fatalf("RoundTrip %d error: %v", i, err)
		}
		resp.Body.Close()
	}

	rec.Close()

	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 20 {
		t.Errorf("after Close, len(tapes) = %d, want 20", len(tapes))
	}
}

// --- Request body restore tests ---

func TestRecorder_RoundTrip_RequestBodyRestoredForTransport(t *testing.T) {
	var transportReceivedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		transportReceivedBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store,
		WithTransport(srv.Client().Transport),
		WithAsync(false),
	)

	req, _ := http.NewRequest("POST", srv.URL, strings.NewReader("important data"))
	resp, err := rec.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	resp.Body.Close()

	if transportReceivedBody != "important data" {
		t.Errorf("transport received body = %q, want %q", transportReceivedBody, "important data")
	}
}

func TestRecorder_RoundTrip_GetBodySetForRetries(t *testing.T) {
	store := NewMemoryStore()
	var getBodyCalled bool
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.GetBody != nil {
			getBodyCalled = true
			newBody, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			b, _ := io.ReadAll(newBody)
			if string(b) != "retry body" {
				return nil, fmt.Errorf("GetBody returned %q, want %q", b, "retry body")
			}
		}
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(bytes.NewReader(nil)),
			Header:     make(http.Header),
		}, nil
	})

	rec := NewRecorder(store,
		WithTransport(transport),
		WithAsync(false),
	)

	req, _ := http.NewRequest("POST", "http://example.com", strings.NewReader("retry body"))
	resp, err := rec.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	resp.Body.Close()

	if !getBodyCalled {
		t.Error("GetBody was not set or called")
	}
}

// --- Headers captured tests ---

func TestRecorder_RoundTrip_HeadersCaptured(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Response-Header", "resp-val")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store,
		WithTransport(srv.Client().Transport),
		WithAsync(false),
	)

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("X-Request-Header", "req-val")
	resp, err := rec.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	resp.Body.Close()

	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 1 {
		t.Fatalf("len(tapes) = %d, want 1", len(tapes))
	}
	if tapes[0].Request.Headers.Get("X-Request-Header") != "req-val" {
		t.Errorf("request header X-Request-Header = %q, want %q",
			tapes[0].Request.Headers.Get("X-Request-Header"), "req-val")
	}
	if tapes[0].Response.Headers.Get("X-Response-Header") != "resp-val" {
		t.Errorf("response header X-Response-Header = %q, want %q",
			tapes[0].Response.Headers.Get("X-Response-Header"), "resp-val")
	}
}

// --- Concurrent usage test ---

func TestRecorder_RoundTrip_ConcurrentRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store,
		WithTransport(srv.Client().Transport),
		WithAsync(true),
		WithBufferSize(1024),
	)

	const numRequests = 50
	var wg sync.WaitGroup
	wg.Add(numRequests)

	for i := 0; i < numRequests; i++ {
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("GET", srv.URL, nil)
			resp, err := rec.RoundTrip(req)
			if err != nil {
				t.Errorf("RoundTrip error: %v", err)
				return
			}
			resp.Body.Close()
		}()
	}

	wg.Wait()
	rec.Close()

	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != numRequests {
		t.Errorf("len(tapes) = %d, want %d", len(tapes), numRequests)
	}
}

// --- Nil store guard ---

func TestNewRecorder_NilStore_Panics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for nil store, got none")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("expected string panic, got %T: %v", r, r)
		}
		if !strings.Contains(msg, "non-nil Store") {
			t.Errorf("panic message = %q, want it to mention non-nil Store", msg)
		}
	}()

	NewRecorder(nil)
}

// --- Response body read failure ---

func TestRecorder_RoundTrip_ResponseBodyReadError_ReportsViaOnError(t *testing.T) {
	readErr := errors.New("body read exploded")

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       &failingReader{data: []byte("partial"), err: readErr},
			Header:     make(http.Header),
		}, nil
	})

	store := NewMemoryStore()
	var capturedErr error
	rec := NewRecorder(store,
		WithTransport(transport),
		WithAsync(false),
		WithOnError(func(err error) { capturedErr = err }),
	)

	req, _ := http.NewRequest("GET", "http://example.com", nil)
	resp, err := rec.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip should not return error, got: %v", err)
	}
	defer resp.Body.Close()

	// The caller should still get partial bytes back via the replaced body.
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "partial" {
		t.Errorf("response body = %q, want %q", body, "partial")
	}

	// The error should have been reported via onError.
	if capturedErr == nil {
		t.Fatal("expected onError to be called, got nil")
	}
	if !errors.Is(capturedErr, readErr) {
		t.Errorf("onError got %v, want wrapping of %v", capturedErr, readErr)
	}

	// No tape should have been saved (we bail out before persisting).
	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 0 {
		t.Errorf("len(tapes) = %d, want 0 (no tape when body read fails)", len(tapes))
	}
}

// failingReader returns data then an error.
type failingReader struct {
	data []byte
	err  error
	read bool
}

func (r *failingReader) Read(p []byte) (int, error) {
	if !r.read {
		r.read = true
		n := copy(p, r.data)
		return n, nil
	}
	return 0, r.err
}

func (r *failingReader) Close() error { return nil }

// --- RoundTrip after Close (closed guard) ---

func TestRecorder_RoundTripAfterClose(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("still works"))
	}))
	defer srv.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store,
		WithTransport(srv.Client().Transport),
		WithRoute("after-close"),
		WithAsync(true),
		WithBufferSize(16),
	)

	// Close the recorder first.
	if err := rec.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	// RoundTrip after Close must not panic and must pass through to the transport.
	req, _ := http.NewRequest("GET", srv.URL+"/after-close", nil)
	resp, err := rec.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip after Close should succeed, got: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "still works" {
		t.Errorf("response body = %q, want %q", body, "still works")
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	// No new tapes should have been recorded after Close.
	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 0 {
		t.Errorf("len(tapes) = %d, want 0 (no recording after Close)", len(tapes))
	}
}

// --- http.RoundTripper interface compliance ---

func TestRecorder_ImplementsRoundTripper(t *testing.T) {
	store := NewMemoryStore()
	rec := NewRecorder(store, WithAsync(false))

	// Compile-time check that Recorder implements http.RoundTripper.
	var _ http.RoundTripper = rec
}

// --- Integration: full client usage ---

func TestRecorder_Integration_FullClientUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf(`{"echo":"%s"}`, body)))
	}))
	defer srv.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store,
		WithTransport(srv.Client().Transport),
		WithRoute("echo-service"),
		WithAsync(true),
		WithBufferSize(64),
	)

	client := &http.Client{Transport: rec}

	// Make a request using the standard http.Client.
	resp, err := client.Post(srv.URL+"/echo", "text/plain", strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("client.Post error: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if !strings.Contains(string(body), "hello") {
		t.Errorf("response body = %q, want it to contain 'hello'", body)
	}

	rec.Close()

	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 1 {
		t.Fatalf("len(tapes) = %d, want 1", len(tapes))
	}
	if tapes[0].Route != "echo-service" {
		t.Errorf("tape.Route = %q, want %q", tapes[0].Route, "echo-service")
	}
}

// --- Integration: sanitizer redaction tests ---

func TestRecorder_Integration_SanitizerRedactsFixtures(t *testing.T) {
	// Server returns a JSON body with a password field and an Authorization echo.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Authorization", "Bearer server-token")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"username":"alice","password":"s3cret"}`))
	}))
	defer srv.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store,
		WithTransport(srv.Client().Transport),
		WithRoute("redact-test"),
		WithSanitizer(NewPipeline(
			RedactHeaders("Authorization"),
			RedactBodyPaths("$.password"),
		)),
		WithAsync(false),
	)

	req, err := http.NewRequest("GET", srv.URL+"/api", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer caller-token")

	resp, err := rec.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	defer resp.Body.Close()

	// Caller's response must be unmodified.
	if resp.Header.Get("Authorization") != "Bearer server-token" {
		t.Errorf("caller resp Authorization = %q, want %q",
			resp.Header.Get("Authorization"), "Bearer server-token")
	}
	callerBody, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(callerBody), "s3cret") {
		t.Errorf("caller body should contain original password, got %q", callerBody)
	}

	// Verify the tape in the store is redacted.
	tapes, err := store.List(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	if len(tapes) != 1 {
		t.Fatalf("len(tapes) = %d, want 1", len(tapes))
	}
	tape := tapes[0]

	// Request Authorization header must be redacted.
	if tape.Request.Headers.Get("Authorization") != Redacted {
		t.Errorf("tape request Authorization = %q, want %q",
			tape.Request.Headers.Get("Authorization"), Redacted)
	}
	// Response Authorization header must be redacted.
	if tape.Response.Headers.Get("Authorization") != Redacted {
		t.Errorf("tape response Authorization = %q, want %q",
			tape.Response.Headers.Get("Authorization"), Redacted)
	}
	// Response body password must be redacted.
	if strings.Contains(string(tape.Response.Body), "s3cret") {
		t.Errorf("tape response body still contains password: %s", tape.Response.Body)
	}
	if !strings.Contains(string(tape.Response.Body), Redacted) {
		t.Errorf("tape response body should contain %q, got %s", Redacted, tape.Response.Body)
	}
}

func TestRecorder_Integration_DefaultNoOpSanitizer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Custom", "custom-value")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"key":"value"}`))
	}))
	defer srv.Close()

	store := NewMemoryStore()
	// No WithSanitizer — default no-op Pipeline should be used.
	rec := NewRecorder(store,
		WithTransport(srv.Client().Transport),
		WithRoute("noop-test"),
		WithAsync(false),
	)

	req, err := http.NewRequest("POST", srv.URL+"/data", strings.NewReader(`{"input":"data"}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := rec.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	resp.Body.Close()

	tapes, err := store.List(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	if len(tapes) != 1 {
		t.Fatalf("len(tapes) = %d, want 1", len(tapes))
	}
	tape := tapes[0]

	// Tape should match original request/response exactly (no-op sanitizer).
	if string(tape.Request.Body) != `{"input":"data"}` {
		t.Errorf("tape request body = %q, want %q", tape.Request.Body, `{"input":"data"}`)
	}
	if string(tape.Response.Body) != `{"key":"value"}` {
		t.Errorf("tape response body = %q, want %q", tape.Response.Body, `{"key":"value"}`)
	}
	if tape.Response.Headers.Get("X-Custom") != "custom-value" {
		t.Errorf("tape response X-Custom = %q, want %q",
			tape.Response.Headers.Get("X-Custom"), "custom-value")
	}
}

func TestRecorder_Integration_CallerResponseUnmodified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Authorization", "Bearer upstream-secret")
		w.Header().Set("X-Request-Id", "req-123")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"password":"hunter2","email":"alice@corp.com"}`))
	}))
	defer srv.Close()

	store := NewMemoryStore()
	// Aggressive sanitizer: redact Authorization + password + email.
	rec := NewRecorder(store,
		WithTransport(srv.Client().Transport),
		WithSanitizer(NewPipeline(
			RedactHeaders("Authorization", "X-Request-Id"),
			RedactBodyPaths("$.password", "$.email"),
		)),
		WithAsync(false),
	)

	req, err := http.NewRequest("GET", srv.URL+"/sensitive", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	resp, err := rec.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	defer resp.Body.Close()

	// Verify caller's response is completely untouched.
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("StatusCode = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	if resp.Header.Get("Authorization") != "Bearer upstream-secret" {
		t.Errorf("resp Authorization = %q, want %q",
			resp.Header.Get("Authorization"), "Bearer upstream-secret")
	}
	if resp.Header.Get("X-Request-Id") != "req-123" {
		t.Errorf("resp X-Request-Id = %q, want %q",
			resp.Header.Get("X-Request-Id"), "req-123")
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "hunter2") {
		t.Errorf("caller body should contain original password, got %q", body)
	}
	if !strings.Contains(string(body), "alice@corp.com") {
		t.Errorf("caller body should contain original email, got %q", body)
	}
}

func TestWithSanitizer_NilFallsBackToNoOp(t *testing.T) {
	store := NewMemoryStore()
	rec := NewRecorder(store, WithSanitizer(nil), WithAsync(false))

	if rec.sanitizer == nil {
		t.Fatal("sanitizer should not be nil when WithSanitizer(nil) is passed")
	}
	if _, ok := rec.sanitizer.(*Pipeline); !ok {
		t.Errorf("sanitizer type = %T, want *Pipeline", rec.sanitizer)
	}
}

// --- Benchmarks ---

// noopTransport is an http.RoundTripper that returns a fixed response without
// making any network call. Used to isolate recorder overhead in benchmarks.
type noopTransport struct {
	statusCode int
	body       []byte
	header     http.Header
}

func (t *noopTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: t.statusCode,
		Header:     t.header.Clone(),
		Body:       io.NopCloser(bytes.NewReader(t.body)),
	}, nil
}

func newNoopTransport(body []byte) *noopTransport {
	return &noopTransport{
		statusCode: 200,
		body:       body,
		header:     http.Header{"Content-Type": {"application/json"}},
	}
}

func makeBenchRequest(body []byte) *http.Request {
	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}
	req, _ := http.NewRequest("POST", "http://example.com/api/users", bodyReader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token-12345")
	return req
}

// BenchmarkRecorderRoundTrip_Async measures the overhead of async recording.
// The inner transport is a no-op stub returning a fixed response.
func BenchmarkRecorderRoundTrip_Async(b *testing.B) {
	smallBody := []byte(`{"ok":true}`)
	mediumBody := bytes.Repeat([]byte(`{"key":"value"},`), 625) // ~10KB
	mediumBody = append([]byte(`[`), mediumBody...)
	mediumBody[len(mediumBody)-1] = ']'

	benchCases := []struct {
		name      string
		sanitizer Sanitizer
		reqBody   []byte
		respBody  []byte
	}{
		{
			name:      "NoSanitizer",
			sanitizer: NewPipeline(),
			reqBody:   smallBody,
			respBody:  smallBody,
		},
		{
			name:      "WithRedactHeaders",
			sanitizer: NewPipeline(RedactHeaders("Authorization")),
			reqBody:   smallBody,
			respBody:  smallBody,
		},
		{
			name:      "SmallBody",
			sanitizer: NewPipeline(),
			reqBody:   make([]byte, 100),
			respBody:  make([]byte, 100),
		},
		{
			name:      "MediumBody",
			sanitizer: NewPipeline(),
			reqBody:   mediumBody,
			respBody:  mediumBody,
		},
	}

	for _, bc := range benchCases {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()

			store := NewMemoryStore()
			transport := newNoopTransport(bc.respBody)
			rec := NewRecorder(store,
				WithTransport(transport),
				WithRoute("bench-route"),
				WithSanitizer(bc.sanitizer),
				WithAsync(true),
				WithBufferSize(4096),
			)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				req := makeBenchRequest(bc.reqBody)
				resp, err := rec.RoundTrip(req)
				if err != nil {
					b.Fatalf("RoundTrip error: %v", err)
				}
				resp.Body.Close()
			}
			b.StopTimer()
			rec.Close()
		})
	}
}

// BenchmarkRecorderRoundTrip_Sync measures sync recording overhead for comparison.
func BenchmarkRecorderRoundTrip_Sync(b *testing.B) {
	smallBody := []byte(`{"ok":true}`)

	benchCases := []struct {
		name      string
		sanitizer Sanitizer
		reqBody   []byte
		respBody  []byte
	}{
		{
			name:      "NoSanitizer",
			sanitizer: NewPipeline(),
			reqBody:   smallBody,
			respBody:  smallBody,
		},
		{
			name:      "WithRedactHeaders",
			sanitizer: NewPipeline(RedactHeaders("Authorization")),
			reqBody:   smallBody,
			respBody:  smallBody,
		},
	}

	for _, bc := range benchCases {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()

			store := NewMemoryStore()
			transport := newNoopTransport(bc.respBody)
			rec := NewRecorder(store,
				WithTransport(transport),
				WithRoute("bench-route"),
				WithSanitizer(bc.sanitizer),
				WithAsync(false),
			)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				req := makeBenchRequest(bc.reqBody)
				resp, err := rec.RoundTrip(req)
				if err != nil {
					b.Fatalf("RoundTrip error: %v", err)
				}
				resp.Body.Close()
			}
		})
	}
}

// --- ADR-17/ADR-41: Edge case tests ---

// TestRecorder_BinaryBody verifies that binary response bodies are recorded
// and preserved correctly.
func TestRecorder_BinaryBody(t *testing.T) {
	binaryData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A} // PNG header

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		w.Write(binaryData)
	}))
	defer srv.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store,
		WithTransport(srv.Client().Transport),
		WithRoute("binary-test"),
		WithAsync(false),
	)

	req, _ := http.NewRequest("GET", srv.URL+"/image.png", nil)
	resp, err := rec.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	resp.Body.Close()

	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 1 {
		t.Fatalf("expected 1 tape, got %d", len(tapes))
	}

	tape := tapes[0]
	if !bytes.Equal(tape.Response.Body, binaryData) {
		t.Error("binary body not preserved correctly")
	}
}

// TestRecorder_TextBody verifies that text response bodies are recorded correctly.
func TestRecorder_TextBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store,
		WithTransport(srv.Client().Transport),
		WithAsync(false),
	)

	req, _ := http.NewRequest("GET", srv.URL+"/api", nil)
	resp, err := rec.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	resp.Body.Close()

	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 1 {
		t.Fatalf("expected 1 tape, got %d", len(tapes))
	}
	if string(tapes[0].Response.Body) != `{"ok":true}` {
		t.Errorf("response body = %q, want %q", string(tapes[0].Response.Body), `{"ok":true}`)
	}
}

// TestRecorder_MaxBodySize_Truncation verifies that bodies exceeding
// the max size are truncated and metadata is set.
func TestRecorder_MaxBodySize_Truncation(t *testing.T) {
	largeBody := bytes.Repeat([]byte("A"), 2000)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write(largeBody)
	}))
	defer srv.Close()

	var warnings []string
	store := NewMemoryStore()
	rec := NewRecorder(store,
		WithTransport(srv.Client().Transport),
		WithMaxBodySize(500),
		WithAsync(false),
		WithOnError(func(err error) {
			warnings = append(warnings, err.Error())
		}),
	)

	reqBody := bytes.Repeat([]byte("B"), 1000)
	req, _ := http.NewRequest("POST", srv.URL+"/upload", io.NopCloser(bytes.NewReader(reqBody)))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := rec.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	resp.Body.Close()

	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 1 {
		t.Fatalf("expected 1 tape, got %d", len(tapes))
	}

	tape := tapes[0]

	// Request body should be truncated.
	if !tape.Request.Truncated {
		t.Error("request Truncated should be true")
	}
	if tape.Request.OriginalBodySize != 1000 {
		t.Errorf("request OriginalBodySize = %d, want 1000", tape.Request.OriginalBodySize)
	}
	if len(tape.Request.Body) != 500 {
		t.Errorf("request body len = %d, want 500", len(tape.Request.Body))
	}

	// Response body should be truncated.
	if !tape.Response.Truncated {
		t.Error("response Truncated should be true")
	}
	if tape.Response.OriginalBodySize != 2000 {
		t.Errorf("response OriginalBodySize = %d, want 2000", tape.Response.OriginalBodySize)
	}
	if len(tape.Response.Body) != 500 {
		t.Errorf("response body len = %d, want 500", len(tape.Response.Body))
	}

	// BodyHash should be computed on truncated body.
	expectedHash := BodyHashFromBytes(tape.Request.Body)
	if tape.Request.BodyHash != expectedHash {
		t.Errorf("request BodyHash does not match truncated body hash")
	}

	// Warnings should be emitted.
	if len(warnings) != 2 {
		t.Errorf("expected 2 warnings, got %d: %v", len(warnings), warnings)
	}
}

// TestRecorder_MaxBodySize_NoTruncation verifies that bodies within the
// limit are not truncated.
func TestRecorder_MaxBodySize_NoTruncation(t *testing.T) {
	smallBody := []byte("small")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write(smallBody)
	}))
	defer srv.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store,
		WithTransport(srv.Client().Transport),
		WithMaxBodySize(1000),
		WithAsync(false),
	)

	req, _ := http.NewRequest("GET", srv.URL+"/small", nil)
	resp, err := rec.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	resp.Body.Close()

	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 1 {
		t.Fatalf("expected 1 tape, got %d", len(tapes))
	}

	tape := tapes[0]
	if tape.Response.Truncated {
		t.Error("response should not be truncated")
	}
	if tape.Response.OriginalBodySize != 0 {
		t.Errorf("OriginalBodySize = %d, want 0", tape.Response.OriginalBodySize)
	}
}

// TestRecorder_MaxBodySize_Zero verifies that maxBodySize=0 means no limit.
func TestRecorder_MaxBodySize_Zero(t *testing.T) {
	largeBody := bytes.Repeat([]byte("X"), 10000)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(largeBody)
	}))
	defer srv.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store,
		WithTransport(srv.Client().Transport),
		WithMaxBodySize(0),
		WithAsync(false),
	)

	req, _ := http.NewRequest("GET", srv.URL+"/big", nil)
	resp, err := rec.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	resp.Body.Close()

	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 1 {
		t.Fatalf("expected 1 tape, got %d", len(tapes))
	}
	if len(tapes[0].Response.Body) != 10000 {
		t.Errorf("body len = %d, want 10000", len(tapes[0].Response.Body))
	}
}

// TestWithMaxBodySize_Negative verifies that negative maxBodySize is treated as 0.
func TestWithMaxBodySize_Negative(t *testing.T) {
	store := NewMemoryStore()
	rec := NewRecorder(store, WithMaxBodySize(-100), WithAsync(false))
	if rec.maxBodySize != 0 {
		t.Errorf("maxBodySize = %d, want 0", rec.maxBodySize)
	}
}

// TestRecorder_204NoContent verifies that recording a 204 response produces
// a tape with empty body and correct status code.
func TestRecorder_204NoContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store,
		WithTransport(srv.Client().Transport),
		WithAsync(false),
	)

	req, _ := http.NewRequest("DELETE", srv.URL+"/resource", nil)
	resp, err := rec.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	resp.Body.Close()

	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 1 {
		t.Fatalf("expected 1 tape, got %d", len(tapes))
	}

	tape := tapes[0]
	if tape.Response.StatusCode != 204 {
		t.Errorf("status code = %d, want 204", tape.Response.StatusCode)
	}
	if len(tape.Response.Body) != 0 {
		t.Errorf("body should be empty, got %d bytes", len(tape.Response.Body))
	}
	if tape.Request.BodyHash != "" {
		t.Errorf("request BodyHash should be empty for nil body, got %q", tape.Request.BodyHash)
	}
}

// TestRecorder_204NoContent_ExplicitEmptyBody verifies that a 204 response
// with an explicit empty body also records correctly.
func TestRecorder_204NoContent_ExplicitEmptyBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
		w.Write([]byte{}) // explicit empty write
	}))
	defer srv.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store,
		WithTransport(srv.Client().Transport),
		WithAsync(false),
	)

	req, _ := http.NewRequest("DELETE", srv.URL+"/resource", nil)
	resp, err := rec.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	resp.Body.Close()

	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 1 {
		t.Fatalf("expected 1 tape, got %d", len(tapes))
	}

	tape := tapes[0]
	if tape.Response.StatusCode != 204 {
		t.Errorf("status code = %d, want 204", tape.Response.StatusCode)
	}
}

// TestRecorder_SkipRedirects verifies that 3xx responses are not recorded
// when skipRedirects is true.
func TestRecorder_SkipRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			w.Header().Set("Location", "/final")
			w.WriteHeader(http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("final response"))
	}))
	defer srv.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store,
		WithTransport(srv.Client().Transport),
		WithSkipRedirects(true),
		WithAsync(false),
	)

	// First call: redirect response (302) -- should be skipped.
	req, _ := http.NewRequest("GET", srv.URL+"/redirect", nil)
	resp, err := rec.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Errorf("expected 302, got %d", resp.StatusCode)
	}

	// Second call: final response (200) -- should be recorded.
	req2, _ := http.NewRequest("GET", srv.URL+"/final", nil)
	resp2, err := rec.RoundTrip(req2)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	resp2.Body.Close()

	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 1 {
		t.Fatalf("expected 1 tape (redirect skipped), got %d", len(tapes))
	}
	if tapes[0].Response.StatusCode != 200 {
		t.Errorf("recorded tape status = %d, want 200", tapes[0].Response.StatusCode)
	}
}

// TestRecorder_SkipRedirects_False verifies that 3xx responses are recorded
// when skipRedirects is false (default).
func TestRecorder_SkipRedirects_False(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/final")
		w.WriteHeader(http.StatusMovedPermanently)
	}))
	defer srv.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store,
		WithTransport(srv.Client().Transport),
		WithSkipRedirects(false),
		WithAsync(false),
	)

	req, _ := http.NewRequest("GET", srv.URL+"/old", nil)
	resp, err := rec.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	resp.Body.Close()

	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 1 {
		t.Fatalf("expected 1 tape (redirects recorded), got %d", len(tapes))
	}
	if tapes[0].Response.StatusCode != 301 {
		t.Errorf("recorded tape status = %d, want 301", tapes[0].Response.StatusCode)
	}
}

// TestRecorder_SkipRedirects_AllCodes verifies that all 3xx codes are skipped.
func TestRecorder_SkipRedirects_AllCodes(t *testing.T) {
	codes := []int{300, 301, 302, 303, 304, 307, 308}
	for _, code := range codes {
		t.Run(fmt.Sprintf("status_%d", code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "/target")
				w.WriteHeader(code)
			}))
			defer srv.Close()

			store := NewMemoryStore()
			rec := NewRecorder(store,
				WithTransport(srv.Client().Transport),
				WithSkipRedirects(true),
				WithAsync(false),
			)

			req, _ := http.NewRequest("GET", srv.URL+"/test", nil)
			resp, err := rec.RoundTrip(req)
			if err != nil {
				t.Fatalf("RoundTrip error: %v", err)
			}
			resp.Body.Close()

			tapes, _ := store.List(context.Background(), Filter{})
			if len(tapes) != 0 {
				t.Errorf("expected 0 tapes for status %d, got %d", code, len(tapes))
			}
		})
	}
}

// TestRecorder_MalformedJSON verifies that malformed JSON bodies are
// stored as raw bytes without error.
func TestRecorder_MalformedJSON(t *testing.T) {
	malformed := []byte(`{"broken: json}`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(malformed)
	}))
	defer srv.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store,
		WithTransport(srv.Client().Transport),
		WithSanitizer(NewPipeline(RedactBodyPaths("$.secret"))),
		WithAsync(false),
	)

	req, _ := http.NewRequest("POST", srv.URL+"/api", io.NopCloser(bytes.NewReader(malformed)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := rec.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}
	resp.Body.Close()

	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 1 {
		t.Fatalf("expected 1 tape, got %d", len(tapes))
	}

	// Body should be stored as-is (sanitizer skips malformed JSON).
	if !bytes.Equal(tapes[0].Response.Body, malformed) {
		t.Error("malformed JSON body was unexpectedly modified")
	}
	if !bytes.Equal(tapes[0].Request.Body, malformed) {
		t.Error("malformed JSON request body was unexpectedly modified")
	}
}
