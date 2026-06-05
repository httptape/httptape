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

// --- Test helpers ---

// countingTransport wraps a transport and counts calls.
type countingTransport struct {
	inner http.RoundTripper
	count atomic.Int64
}

func (c *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.count.Add(1)
	return c.inner.RoundTrip(req)
}

// failingStore is a Store that fails on Save for testing error paths.
type failingStore struct {
	*MemoryStore
	saveFail bool
}

func (fs *failingStore) Save(ctx context.Context, tape Tape) error {
	if fs.saveFail {
		return errors.New("store write failure")
	}
	return fs.MemoryStore.Save(ctx, tape)
}

// flakeyListStore wraps a MemoryStore but returns an error on the first
// N List calls, then delegates to the underlying store. This simulates
// transient store failures for stale-fallback testing.
type flakeyListStore struct {
	*MemoryStore
	failCount atomic.Int64 // number of List calls that should fail
}

func (fs *flakeyListStore) List(ctx context.Context, filter Filter) ([]Tape, error) {
	if fs.failCount.Add(-1) >= 0 {
		return nil, errors.New("transient store error")
	}
	return fs.MemoryStore.List(ctx, filter)
}

// --- Tests per ADR-44 test strategy ---

func TestCachingTransport_CacheHitNonSSE(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	tape := NewTape("", RecordedReq{
		Method:   "GET",
		URL:      "http://example.com/api/users",
		Headers:  http.Header{},
		BodyHash: "",
	}, RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{"X-Custom": {"cached"}},
		Body:       []byte("cached-response"),
	})
	store.Save(context.Background(), tape)

	upstreamCalled := false
	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		upstreamCalled = true
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("upstream"))}, nil
	})

	ct := NewCachingTransport(upstream, store)

	req, _ := http.NewRequest("GET", "http://example.com/api/users", nil)
	resp, err := ct.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if upstreamCalled {
		t.Error("upstream was called on cache hit; expected no upstream call")
	}
	if resp.StatusCode != 200 {
		t.Errorf("got status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "cached-response" {
		t.Errorf("got body %q, want %q", string(body), "cached-response")
	}
}

func TestCachingTransport_CacheHitSSE(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	tape := NewTape("", RecordedReq{
		Method:   "POST",
		URL:      "http://example.com/api/chat",
		Headers:  http.Header{},
		Body:     []byte(`{"prompt":"hello"}`),
		BodyHash: BodyHashFromBytes([]byte(`{"prompt":"hello"}`)),
	}, RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{"Content-Type": {"text/event-stream"}},
		SSEEvents: []SSEEvent{
			{OffsetMS: 0, Data: "event1"},
			{OffsetMS: 100, Data: "event2"},
		},
	})
	store.Save(context.Background(), tape)

	upstreamCalled := false
	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		upstreamCalled = true
		return nil, errors.New("should not be called")
	})

	ct := NewCachingTransport(upstream, store)

	req, _ := http.NewRequest("POST", "http://example.com/api/chat",
		bytes.NewReader([]byte(`{"prompt":"hello"}`)))
	resp, err := ct.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if upstreamCalled {
		t.Error("upstream was called on SSE cache hit")
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("got Content-Type %q, want text/event-stream", ct)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "event1") || !strings.Contains(string(body), "event2") {
		t.Errorf("SSE body missing events: %q", string(body))
	}
}

func TestCachingTransport_CacheMissNonSSE(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()

	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"X-Upstream": {"true"}},
			Body:       io.NopCloser(strings.NewReader("from-upstream")),
		}, nil
	})

	ct := NewCachingTransport(upstream, store)

	// First request: cache miss, should forward to upstream.
	req, _ := http.NewRequest("GET", "http://example.com/api/data", nil)
	resp, err := ct.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if string(body) != "from-upstream" {
		t.Errorf("got body %q, want %q", string(body), "from-upstream")
	}

	// Verify tape was stored.
	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 1 {
		t.Fatalf("store has %d tapes, want 1", len(tapes))
	}
	if string(tapes[0].Response.Body) != "from-upstream" {
		t.Errorf("stored body %q, want %q", string(tapes[0].Response.Body), "from-upstream")
	}

	// Second request: should be a cache hit.
	req2, _ := http.NewRequest("GET", "http://example.com/api/data", nil)
	resp2, err := ct.RoundTrip(req2)
	if err != nil {
		t.Fatalf("unexpected error on second request: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()

	if string(body2) != "from-upstream" {
		t.Errorf("got body %q on second request, want %q", string(body2), "from-upstream")
	}
}

func TestCachingTransport_CacheMissSSETee(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()

	// Create a test SSE upstream.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher, _ := w.(http.Flusher)
		fmt.Fprintln(w, "data: hello")
		fmt.Fprintln(w, "")
		flusher.Flush()
		fmt.Fprintln(w, "data: world")
		fmt.Fprintln(w, "")
		flusher.Flush()
	}))
	defer srv.Close()

	ct := NewCachingTransport(http.DefaultTransport, store)

	req, _ := http.NewRequest("GET", srv.URL+"/stream", nil)
	resp, err := ct.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Read the streamed body.
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if !strings.Contains(string(body), "hello") || !strings.Contains(string(body), "world") {
		t.Errorf("SSE tee body missing events: %q", string(body))
	}

	// Wait briefly for the async onDone callback to fire.
	time.Sleep(100 * time.Millisecond)

	// Verify tape was stored with SSE events.
	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 1 {
		t.Fatalf("store has %d tapes, want 1", len(tapes))
	}
	if !tapes[0].Response.IsSSE() {
		t.Error("stored tape is not SSE")
	}
	if len(tapes[0].Response.SSEEvents) < 2 {
		t.Errorf("stored tape has %d SSE events, want >= 2", len(tapes[0].Response.SSEEvents))
	}
}

func TestCachingTransport_SSEPartialDisconnect(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()

	var onErrorCalled atomic.Int32

	// Create a test SSE upstream that sends events indefinitely.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher, _ := w.(http.Flusher)
		for i := 0; ; i++ {
			select {
			case <-r.Context().Done():
				return
			default:
			}
			fmt.Fprintf(w, "data: event-%d\n\n", i)
			flusher.Flush()
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer srv.Close()

	ct := NewCachingTransport(http.DefaultTransport, store,
		WithCacheOnError(func(_ error) { onErrorCalled.Add(1) }),
	)

	req, _ := http.NewRequest("GET", srv.URL+"/stream", nil)
	resp, err := ct.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Read a few bytes then close (simulating client disconnect).
	buf := make([]byte, 64)
	resp.Body.Read(buf) //nolint:errcheck
	resp.Body.Close()

	// Wait for onDone to fire.
	time.Sleep(200 * time.Millisecond)

	// Partial tape should NOT be stored.
	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 0 {
		t.Errorf("store has %d tapes after partial disconnect, want 0", len(tapes))
	}
}

func TestCachingTransport_UpstreamErrorPropagation(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()

	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})

	ct := NewCachingTransport(upstream, store)

	req, _ := http.NewRequest("GET", "http://example.com/api", nil)
	resp, err := ct.RoundTrip(req)
	if resp != nil {
		t.Errorf("expected nil response, got status %d", resp.StatusCode)
	}
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("expected connection refused error, got %v", err)
	}
}

func TestCachingTransport_StaleFallbackHit(t *testing.T) {
	t.Parallel()

	// Use a flaky store that fails the initial List (causing a cache miss)
	// but succeeds on the second List (stale fallback lookup).
	ms := NewMemoryStore()
	flakey := &flakeyListStore{MemoryStore: ms}
	flakey.failCount.Store(1) // first List call fails

	// Pre-populate store with a tape.
	tape := NewTape("", RecordedReq{
		Method:   "GET",
		URL:      "http://example.com/api/data",
		Headers:  http.Header{},
		BodyHash: "",
	}, RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{},
		Body:       []byte("stale-data"),
	})
	ms.Save(context.Background(), tape)

	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return nil, errors.New("upstream down")
	})

	ct := NewCachingTransport(upstream, flakey,
		WithCacheUpstreamDownFallback(true),
		WithCacheSingleFlight(false),
	)

	req, _ := http.NewRequest("GET", "http://example.com/api/data", nil)
	resp, err := ct.RoundTrip(req)
	if err != nil {
		t.Fatalf("expected stale fallback, got error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("got status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "stale-data" {
		t.Errorf("got body %q, want %q", string(body), "stale-data")
	}
	if stale := resp.Header.Get("X-Httptape-Stale"); stale != "true" {
		t.Errorf("got X-Httptape-Stale=%q, want %q", stale, "true")
	}
}

func TestCachingTransport_StaleFallbackMiss(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore() // empty

	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return nil, errors.New("upstream down")
	})

	ct := NewCachingTransport(upstream, store,
		WithCacheUpstreamDownFallback(true),
	)

	req, _ := http.NewRequest("GET", "http://example.com/api/data", nil)
	resp, err := ct.RoundTrip(req)
	if resp != nil {
		t.Errorf("expected nil response, got status %d", resp.StatusCode)
	}
	if err == nil || !strings.Contains(err.Error(), "upstream down") {
		t.Errorf("expected upstream down error, got %v", err)
	}
}

func TestCachingTransport_CacheFilterRejects(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()

	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 500,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("server error")),
		}, nil
	})

	ct := NewCachingTransport(upstream, store)

	req, _ := http.NewRequest("GET", "http://example.com/api/data", nil)
	resp, err := ct.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	// Response returned to caller.
	if resp.StatusCode != 500 {
		t.Errorf("got status %d, want 500", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "server error" {
		t.Errorf("got body %q, want %q", string(body), "server error")
	}

	// But NOT stored in cache.
	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 0 {
		t.Errorf("store has %d tapes, want 0 (500 filtered out)", len(tapes))
	}
}

func TestCachingTransport_SanitizationApplies(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()

	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	})

	sanitizer := NewPipeline(RedactHeaders("Authorization"))

	ct := NewCachingTransport(upstream, store,
		WithCacheSanitizer(sanitizer),
	)

	req, _ := http.NewRequest("GET", "http://example.com/api/data", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	resp, err := ct.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	// The stored tape should have the Authorization header redacted.
	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 1 {
		t.Fatalf("store has %d tapes, want 1", len(tapes))
	}
	auth := tapes[0].Request.Headers.Get("Authorization")
	if auth != Redacted {
		t.Errorf("stored Authorization=%q, want %q", auth, Redacted)
	}
}

func TestCachingTransport_SingleFlightDedup(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()

	var callCount atomic.Int64
	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		callCount.Add(1)
		time.Sleep(50 * time.Millisecond) // simulate slow upstream
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("response")),
		}, nil
	})

	ct := NewCachingTransport(upstream, store,
		WithCacheSingleFlight(true),
	)

	const N = 10
	var wg sync.WaitGroup
	wg.Add(N)
	errs := make([]error, N)
	bodies := make([]string, N)

	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			req, _ := http.NewRequest("GET", "http://example.com/api/data", nil)
			resp, err := ct.RoundTrip(req)
			errs[idx] = err
			if resp != nil {
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				bodies[idx] = string(b)
			}
		}(i)
	}
	wg.Wait()

	// Only 1 upstream call should have been made.
	if c := callCount.Load(); c != 1 {
		t.Errorf("upstream called %d times, want 1 (single-flight dedup)", c)
	}

	// All callers should get the response.
	for i := 0; i < N; i++ {
		if errs[i] != nil {
			t.Errorf("caller %d got error: %v", i, errs[i])
		}
		if bodies[i] != "response" {
			t.Errorf("caller %d got body %q, want %q", i, bodies[i], "response")
		}
	}
}

func TestCachingTransport_SingleFlightDisabled(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()

	var callCount atomic.Int64
	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		callCount.Add(1)
		time.Sleep(50 * time.Millisecond)
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("response")),
		}, nil
	})

	ct := NewCachingTransport(upstream, store,
		WithCacheSingleFlight(false),
	)

	const N = 5
	var wg sync.WaitGroup
	wg.Add(N)

	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("GET", "http://example.com/api/data", nil)
			resp, err := ct.RoundTrip(req)
			if err == nil {
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()

	// Without single-flight, each request should call upstream.
	if c := callCount.Load(); c != int64(N) {
		t.Errorf("upstream called %d times, want %d (single-flight disabled)", c, N)
	}
}

func TestCachingTransport_MaxBodySizeBypass(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()

	var upstreamBody string
	upstream := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(req.Body)
		upstreamBody = string(b)
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	})

	ct := NewCachingTransport(upstream, store,
		WithCacheMaxBodySize(10), // 10 bytes limit
	)

	// Send a body larger than the limit.
	bigBody := strings.Repeat("x", 20)
	req, _ := http.NewRequest("POST", "http://example.com/api", strings.NewReader(bigBody))
	resp, err := ct.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	// Upstream should receive the full body.
	if upstreamBody != bigBody {
		t.Errorf("upstream got body len %d, want %d", len(upstreamBody), len(bigBody))
	}

	// Nothing should be cached.
	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 0 {
		t.Errorf("store has %d tapes, want 0 (body too large)", len(tapes))
	}
}

func TestCachingTransport_StoreSaveFailure(t *testing.T) {
	t.Parallel()

	fStore := &failingStore{MemoryStore: NewMemoryStore(), saveFail: true}

	var capturedErr error
	var errMu sync.Mutex

	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	})

	ct := NewCachingTransport(upstream, fStore,
		WithCacheOnError(func(err error) {
			errMu.Lock()
			capturedErr = err
			errMu.Unlock()
		}),
	)

	req, _ := http.NewRequest("GET", "http://example.com/api", nil)
	resp, err := ct.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	// Response still returned despite store failure.
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("got body %q, want %q", string(body), "ok")
	}

	// onError should have been called.
	errMu.Lock()
	if capturedErr == nil {
		t.Error("onError not called on store save failure")
	} else if !strings.Contains(capturedErr.Error(), "store write failure") {
		t.Errorf("onError got %v, want store write failure", capturedErr)
	}
	errMu.Unlock()
}

func TestCachingTransport_UpstreamTimeout(t *testing.T) {
	t.Parallel()

	// Use a flaky store: first List (cache lookup) fails, causing a miss.
	// Stale fallback will call List again successfully.
	ms := NewMemoryStore()
	flakey := &flakeyListStore{MemoryStore: ms}
	flakey.failCount.Store(1)

	// Pre-populate store for stale fallback.
	tape := NewTape("", RecordedReq{
		Method:   "GET",
		URL:      "http://example.com/api/data",
		Headers:  http.Header{},
		BodyHash: "",
	}, RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{},
		Body:       []byte("stale-data"),
	})
	ms.Save(context.Background(), tape)

	upstream := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		// Simulate a slow upstream that respects context cancellation.
		select {
		case <-time.After(5 * time.Second):
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader("slow")),
			}, nil
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	})

	ct := NewCachingTransport(upstream, flakey,
		WithCacheUpstreamTimeout(50*time.Millisecond),
		WithCacheUpstreamDownFallback(true),
		WithCacheSingleFlight(false),
	)

	req, _ := http.NewRequest("GET", "http://example.com/api/data", nil)
	resp, err := ct.RoundTrip(req)
	if err != nil {
		t.Fatalf("expected stale fallback on timeout, got error: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "stale-data" {
		t.Errorf("got body %q, want %q", string(body), "stale-data")
	}
	if stale := resp.Header.Get("X-Httptape-Stale"); stale != "true" {
		t.Errorf("got X-Httptape-Stale=%q, want %q", stale, "true")
	}
}

func TestCachingTransport_RouteFilter(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()

	// Store a tape with route "other".
	tape := NewTape("other", RecordedReq{
		Method:   "GET",
		URL:      "http://example.com/api/data",
		Headers:  http.Header{},
		BodyHash: "",
	}, RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{},
		Body:       []byte("other-route"),
	})
	store.Save(context.Background(), tape)

	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("from-upstream")),
		}, nil
	})

	ct := NewCachingTransport(upstream, store,
		WithCacheRoute("api"),
	)

	// Request should NOT match the "other" route tape.
	req, _ := http.NewRequest("GET", "http://example.com/api/data", nil)
	resp, err := ct.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if string(body) != "from-upstream" {
		t.Errorf("got body %q, want %q (route should not match)", string(body), "from-upstream")
	}

	// Verify the new tape has the "api" route.
	tapes, _ := store.List(context.Background(), Filter{Route: "api"})
	if len(tapes) != 1 {
		t.Fatalf("store has %d tapes with route 'api', want 1", len(tapes))
	}
	if tapes[0].Route != "api" {
		t.Errorf("stored tape route=%q, want %q", tapes[0].Route, "api")
	}
}

func TestCachingTransport_BodyRestoreAfterRead(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()

	var upstreamBody string
	upstream := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(req.Body)
		upstreamBody = string(b)
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	})

	ct := NewCachingTransport(upstream, store)

	bodyStr := `{"prompt":"hello world"}`
	req, _ := http.NewRequest("POST", "http://example.com/api/chat", strings.NewReader(bodyStr))
	resp, err := ct.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	// Upstream should have received the exact body.
	if upstreamBody != bodyStr {
		t.Errorf("upstream got body %q, want %q", upstreamBody, bodyStr)
	}
}

func TestCachingTransport_DefaultMatcherDedups(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()

	var callCount atomic.Int64
	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		callCount.Add(1)
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("response")),
		}, nil
	})

	ct := NewCachingTransport(upstream, store)

	prompt := `{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`

	// First call: miss.
	req1, _ := http.NewRequest("POST", "http://example.com/v1/completions", strings.NewReader(prompt))
	resp1, err := ct.RoundTrip(req1)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	resp1.Body.Close()

	// Second call with identical body: should be a hit.
	req2, _ := http.NewRequest("POST", "http://example.com/v1/completions", strings.NewReader(prompt))
	resp2, err := ct.RoundTrip(req2)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	resp2.Body.Close()

	if c := callCount.Load(); c != 1 {
		t.Errorf("upstream called %d times, want 1 (dedup by body hash)", c)
	}
}

func TestCachingTransport_ConcurrentSafety(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()

	var callCount atomic.Int64
	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		callCount.Add(1)
		time.Sleep(10 * time.Millisecond)
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	})

	ct := NewCachingTransport(upstream, store)

	var wg sync.WaitGroup
	const N = 50
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			path := fmt.Sprintf("/api/path-%d", idx%5)
			req, _ := http.NewRequest("GET", "http://example.com"+path, nil)
			resp, err := ct.RoundTrip(req)
			if err == nil {
				resp.Body.Close()
			}
		}(i)
	}
	wg.Wait()
	// No panic = success for concurrent safety test.
}

func TestCachingTransport_PanicsOnNilUpstream(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on nil upstream")
		}
	}()
	NewCachingTransport(nil, NewMemoryStore())
}

func TestCachingTransport_PanicsOnNilStore(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on nil store")
		}
	}()
	NewCachingTransport(http.DefaultTransport, nil)
}

func TestCachingTransport_ImplementsRoundTripper(t *testing.T) {
	t.Parallel()
	ct := NewCachingTransport(http.DefaultTransport, NewMemoryStore())
	var _ http.RoundTripper = ct
}

func TestCachingTransport_NilBody(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()

	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 204,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	})

	ct := NewCachingTransport(upstream, store)

	req, _ := http.NewRequest("GET", "http://example.com/api", nil)
	req.Body = nil
	resp, err := ct.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
}

func TestCachingTransport_CustomCacheFilter(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()

	callNum := 0
	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		callNum++
		status := 200
		if callNum == 1 {
			status = 301
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(fmt.Sprintf("call-%d", callNum))),
		}, nil
	})

	// Custom filter: cache everything, including 3xx.
	ct := NewCachingTransport(upstream, store,
		WithCacheFilter(func(resp *http.Response) bool {
			return true
		}),
	)

	req, _ := http.NewRequest("GET", "http://example.com/redirect", nil)
	resp, err := ct.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	// 301 should be cached.
	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 1 {
		t.Fatalf("store has %d tapes, want 1", len(tapes))
	}
}

func TestCachingTransport_WithCacheMaxBodySizeZero(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()

	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	})

	// Zero means no limit.
	ct := NewCachingTransport(upstream, store,
		WithCacheMaxBodySize(0),
	)

	bigBody := strings.Repeat("x", 20*1024*1024) // 20 MiB
	req, _ := http.NewRequest("POST", "http://example.com/api", strings.NewReader(bigBody))
	resp, err := ct.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	// Should be cached even though body is large.
	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 1 {
		t.Errorf("store has %d tapes, want 1 (no body size limit)", len(tapes))
	}
}

func TestCachingTransport_Integration(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()

	// Create a real upstream server.
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		fmt.Fprintf(w, `{"count":%d}`, callCount)
	}))
	defer srv.Close()

	ct := NewCachingTransport(http.DefaultTransport, store)
	client := &http.Client{Transport: ct}

	// First request: hits upstream.
	resp1, err := client.Get(srv.URL + "/api/data")
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	body1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()
	if string(body1) != `{"count":1}` {
		t.Errorf("first request body=%q, want %q", string(body1), `{"count":1}`)
	}

	// Second request: should be a cache hit (count stays 1).
	resp2, err := client.Get(srv.URL + "/api/data")
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if string(body2) != `{"count":1}` {
		t.Errorf("second request body=%q, want %q (cache hit)", string(body2), `{"count":1}`)
	}

	// Upstream should have been called only once.
	if callCount != 1 {
		t.Errorf("upstream called %d times, want 1", callCount)
	}
}

func TestCachingTransport_StaleSSEFallback(t *testing.T) {
	t.Parallel()

	// Use a flaky store: first List fails (miss), second List succeeds (stale fallback).
	ms := NewMemoryStore()
	flakey := &flakeyListStore{MemoryStore: ms}
	flakey.failCount.Store(1)

	// Pre-populate store with an SSE tape.
	tape := NewTape("", RecordedReq{
		Method:   "GET",
		URL:      "http://example.com/stream",
		Headers:  http.Header{},
		BodyHash: "",
	}, RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{"Content-Type": {"text/event-stream"}},
		SSEEvents: []SSEEvent{
			{OffsetMS: 0, Data: "stale-event"},
		},
	})
	ms.Save(context.Background(), tape)

	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return nil, errors.New("upstream down")
	})

	ct := NewCachingTransport(upstream, flakey,
		WithCacheUpstreamDownFallback(true),
		WithCacheSingleFlight(false),
	)

	req, _ := http.NewRequest("GET", "http://example.com/stream", nil)
	resp, err := ct.RoundTrip(req)
	if err != nil {
		t.Fatalf("expected stale SSE fallback, got error: %v", err)
	}
	defer resp.Body.Close()

	if stale := resp.Header.Get("X-Httptape-Stale"); stale != "true" {
		t.Errorf("got X-Httptape-Stale=%q, want %q", stale, "true")
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "stale-event") {
		t.Errorf("stale SSE body missing event: %q", string(body))
	}
}

func TestCachingTransport_SingleFlightSSEWaiters(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()

	// Create a test SSE upstream that streams events with a small delay.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher, _ := w.(http.Flusher)
		for i := 0; i < 3; i++ {
			fmt.Fprintf(w, "data: event-%d\n\n", i)
			flusher.Flush()
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer srv.Close()

	ct := NewCachingTransport(http.DefaultTransport, store,
		WithCacheSingleFlight(true),
	)

	const N = 5
	var wg sync.WaitGroup
	wg.Add(N)
	bodies := make([]string, N)
	errs := make([]error, N)

	// Use a barrier to ensure all goroutines start simultaneously.
	barrier := make(chan struct{})

	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			<-barrier
			req, _ := http.NewRequest("GET", srv.URL+"/stream", nil)
			resp, err := ct.RoundTrip(req)
			errs[idx] = err
			if resp != nil {
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				bodies[idx] = string(b)
			}
		}(i)
	}

	// Release all goroutines at once.
	close(barrier)
	wg.Wait()

	// All callers should succeed and receive all 3 events.
	for i := 0; i < N; i++ {
		if errs[i] != nil {
			t.Errorf("caller %d got error: %v", i, errs[i])
			continue
		}
		for _, ev := range []string{"event-0", "event-1", "event-2"} {
			if !strings.Contains(bodies[i], ev) {
				t.Errorf("caller %d body missing %q: %q", i, ev, bodies[i])
			}
		}
	}
}

func TestWithCacheSanitizer_NilDefaultsToNoopPipeline(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	})

	ct := NewCachingTransport(upstream, store, WithCacheSanitizer(nil))

	req, _ := http.NewRequest("GET", "http://example.com/api", nil)
	resp, err := ct.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 1 {
		t.Fatalf("got %d tapes, want 1", len(tapes))
	}
}

func TestWithCacheMaxBodySize_NegativeClampedToZero(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	})

	// Negative value should be clamped to 0 (no limit).
	ct := NewCachingTransport(upstream, store, WithCacheMaxBodySize(-5))

	bigBody := strings.Repeat("x", 20*1024*1024) // 20 MiB
	req, _ := http.NewRequest("POST", "http://example.com/api", strings.NewReader(bigBody))
	resp, err := ct.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	// Should be cached (no limit means everything passes).
	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 1 {
		t.Errorf("got %d tapes, want 1 (negative clamped to no limit)", len(tapes))
	}
}

func TestCachingTransport_ReQueryStoreForSSE_StoreListError(t *testing.T) {
	t.Parallel()

	// Pre-populate a store with an SSE tape, then make the store fail on List.
	ms := NewMemoryStore()
	tape := NewTape("", RecordedReq{
		Method:   "GET",
		URL:      "http://example.com/stream",
		Headers:  http.Header{},
		BodyHash: "",
	}, RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{"Content-Type": {"text/event-stream"}},
		SSEEvents: []SSEEvent{
			{OffsetMS: 0, Data: "event1"},
		},
	})
	ms.Save(context.Background(), tape)

	// Flakey store fails on first List (the reQueryStoreForSSE call), then
	// succeeds for the fallback roundTripUpstream -> store.List.
	flakey := &flakeyListStore{MemoryStore: ms}
	flakey.failCount.Store(1)

	var capturedErrors []string
	var mu sync.Mutex

	// The upstream for the fallback roundTripUpstream path.
	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("data: fallback-event\n\n")),
		}, nil
	})

	ct := NewCachingTransport(upstream, flakey,
		WithCacheSingleFlight(false),
		WithCacheOnError(func(err error) {
			mu.Lock()
			capturedErrors = append(capturedErrors, err.Error())
			mu.Unlock()
		}),
	)

	// Call reQueryStoreForSSE directly by simulating the waiter path.
	req, _ := http.NewRequest("GET", "http://example.com/stream", nil)
	resp, err := ct.reQueryStoreForSSE(req, nil)
	if err != nil {
		t.Fatalf("reQueryStoreForSSE: unexpected error: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "fallback-event") {
		t.Errorf("expected fallback-event in body, got %q", string(body))
	}

	mu.Lock()
	defer mu.Unlock()
	if len(capturedErrors) == 0 {
		t.Error("expected onError to be called for store list failure")
	}
}

func TestCachingTransport_ReQueryStoreForSSE_NoMatchingTape(t *testing.T) {
	t.Parallel()

	// Empty store: no matching tape found after leader completed.
	store := NewMemoryStore()

	var capturedErrors []string
	var mu sync.Mutex

	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		}, nil
	})

	ct := NewCachingTransport(upstream, store,
		WithCacheSingleFlight(false),
		WithCacheOnError(func(err error) {
			mu.Lock()
			capturedErrors = append(capturedErrors, err.Error())
			mu.Unlock()
		}),
	)

	req, _ := http.NewRequest("GET", "http://example.com/stream", nil)
	resp, err := ct.reQueryStoreForSSE(req, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"ok":true}` {
		t.Errorf("body = %q, want upstream response", string(body))
	}

	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, msg := range capturedErrors {
		if strings.Contains(msg, "no matching tape found") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'no matching tape found' in errors, got %v", capturedErrors)
	}
}

func TestCachingTransport_ReQueryStoreForSSE_NonSSETapeMatch(t *testing.T) {
	t.Parallel()

	// Store has a non-SSE tape that matches the request.
	store := NewMemoryStore()
	tape := NewTape("", RecordedReq{
		Method:   "GET",
		URL:      "http://example.com/data",
		Headers:  http.Header{},
		BodyHash: "",
	}, RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{"Content-Type": {"application/json"}},
		Body:       []byte(`{"cached":"value"}`),
	})
	store.Save(context.Background(), tape)

	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		t.Error("upstream should not be called when store has a matching tape")
		return nil, errors.New("should not reach")
	})

	ct := NewCachingTransport(upstream, store, WithCacheSingleFlight(false))

	req, _ := http.NewRequest("GET", "http://example.com/data", nil)
	resp, err := ct.reQueryStoreForSSE(req, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"cached":"value"}` {
		t.Errorf("body = %q, want cached value", string(body))
	}
}

func TestCachingTransport_SingleFlightStaleFallbackForWaiters(t *testing.T) {
	t.Parallel()

	// Use a flaky store: initial List calls fail (one per goroutine,
	// causing cache misses), then the stale fallback List succeeds.
	const N = 5
	ms := NewMemoryStore()
	flakey := &flakeyListStore{MemoryStore: ms}
	// All N goroutines call store.List in RoundTrip before entering
	// single-flight. Set failCount to N so all initial lookups fail.
	flakey.failCount.Store(N)

	// Pre-populate store for stale fallback.
	tape := NewTape("", RecordedReq{
		Method:   "GET",
		URL:      "http://example.com/api/data",
		Headers:  http.Header{},
		BodyHash: "",
	}, RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{},
		Body:       []byte("stale-data"),
	})
	ms.Save(context.Background(), tape)

	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		time.Sleep(50 * time.Millisecond)
		return nil, errors.New("upstream down")
	})

	ct := NewCachingTransport(upstream, flakey,
		WithCacheSingleFlight(true),
		WithCacheUpstreamDownFallback(true),
	)

	var wg sync.WaitGroup
	wg.Add(N)
	responses := make([]*http.Response, N)
	errs := make([]error, N)

	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			req, _ := http.NewRequest("GET", "http://example.com/api/data", nil)
			resp, err := ct.RoundTrip(req)
			responses[idx] = resp
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	// All callers should get a stale fallback response.
	for i := 0; i < N; i++ {
		if errs[i] != nil {
			t.Errorf("caller %d got error: %v", i, errs[i])
			continue
		}
		if responses[i] == nil {
			t.Errorf("caller %d got nil response", i)
			continue
		}
		body, _ := io.ReadAll(responses[i].Body)
		responses[i].Body.Close()
		if string(body) != "stale-data" {
			t.Errorf("caller %d got body %q, want %q", i, string(body), "stale-data")
		}
		if stale := responses[i].Header.Get("X-Httptape-Stale"); stale != "true" {
			t.Errorf("caller %d got X-Httptape-Stale=%q, want %q", i, stale, "true")
		}
	}
}

// --- WithCacheLookupDisabled tests ---

// spyStore wraps a MemoryStore and counts List calls. Used to verify that
// Store.List is not called when cache lookup is disabled.
type spyStore struct {
	*MemoryStore
	listCalls atomic.Int64
}

func (s *spyStore) List(ctx context.Context, filter Filter) ([]Tape, error) {
	s.listCalls.Add(1)
	return s.MemoryStore.List(ctx, filter)
}

func TestCachingTransport_LookupDisabledAlwaysMisses(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()

	// Pre-seed the store with a tape that would match.
	tape := NewTape("", RecordedReq{
		Method:   "GET",
		URL:      "http://example.com/api/data",
		Headers:  http.Header{},
		BodyHash: "",
	}, RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{},
		Body:       []byte("cached-response"),
	})
	store.Save(context.Background(), tape)

	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("from-upstream")),
		}, nil
	})

	ct := NewCachingTransport(upstream, store,
		WithCacheLookupDisabled(),
	)

	req, _ := http.NewRequest("GET", "http://example.com/api/data", nil)
	resp, err := ct.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "from-upstream" {
		t.Errorf("got body %q, want %q (lookup disabled should bypass cache)", string(body), "from-upstream")
	}
}

func TestCachingTransport_LookupDisabledStillRecords(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()

	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("recorded-response")),
		}, nil
	})

	ct := NewCachingTransport(upstream, store,
		WithCacheLookupDisabled(),
	)

	req, _ := http.NewRequest("GET", "http://example.com/api/data", nil)
	resp, err := ct.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	// Verify the response was persisted to the store.
	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 1 {
		t.Fatalf("store has %d tapes, want 1 (recording-only mode)", len(tapes))
	}
	if string(tapes[0].Response.Body) != "recorded-response" {
		t.Errorf("stored body %q, want %q", string(tapes[0].Response.Body), "recorded-response")
	}
}

func TestCachingTransport_LookupDisabledWithStaleFallback(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()

	// Pre-seed the store with a matching tape.
	tape := NewTape("", RecordedReq{
		Method:   "GET",
		URL:      "http://example.com/api/data",
		Headers:  http.Header{},
		BodyHash: "",
	}, RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{},
		Body:       []byte("stale-data"),
	})
	store.Save(context.Background(), tape)

	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return nil, errors.New("upstream down")
	})

	ct := NewCachingTransport(upstream, store,
		WithCacheLookupDisabled(),
		WithCacheUpstreamDownFallback(true),
		WithCacheSingleFlight(false),
	)

	req, _ := http.NewRequest("GET", "http://example.com/api/data", nil)
	resp, err := ct.RoundTrip(req)
	if err != nil {
		t.Fatalf("expected stale fallback, got error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("got status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "stale-data" {
		t.Errorf("got body %q, want %q", string(body), "stale-data")
	}
	if stale := resp.Header.Get("X-Httptape-Stale"); stale != "true" {
		t.Errorf("got X-Httptape-Stale=%q, want %q", stale, "true")
	}
}

func TestCachingTransport_LookupDisabledSkipsStoreList(t *testing.T) {
	t.Parallel()

	spy := &spyStore{MemoryStore: NewMemoryStore()}

	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	})

	ct := NewCachingTransport(upstream, spy,
		WithCacheLookupDisabled(),
	)

	req, _ := http.NewRequest("GET", "http://example.com/api/data", nil)
	resp, err := ct.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	if calls := spy.listCalls.Load(); calls != 0 {
		t.Errorf("Store.List was called %d times, want 0 (lookup disabled should skip store query)", calls)
	}
}

// ---------------------------------------------------------------------------
// ElapsedMS recording on cache miss (non-SSE)
// ---------------------------------------------------------------------------

func TestCachingTransport_ElapsedMS_NonSSE(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	callCount := 0
	fakeNow := func() time.Time {
		callCount++
		if callCount == 1 {
			return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		}
		return time.Date(2026, 1, 1, 0, 0, 0, 200*int(time.Millisecond), time.UTC)
	}

	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	})

	ct := NewCachingTransport(upstream, store,
		WithCacheSingleFlight(false),
		withCacheNowFunc(fakeNow),
	)

	req, _ := http.NewRequest("GET", "http://example.com/api", nil)
	resp, err := ct.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 1 {
		t.Fatalf("expected 1 tape, got %d", len(tapes))
	}
	if tapes[0].Response.ElapsedMS != 200 {
		t.Errorf("ElapsedMS = %d, want 200", tapes[0].Response.ElapsedMS)
	}
}

// ---------------------------------------------------------------------------
// ElapsedMS recording on cache miss (SSE)
// ---------------------------------------------------------------------------

func TestCachingTransport_ElapsedMS_SSE(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var callCount atomic.Int64
	fakeNow := func() time.Time {
		n := callCount.Add(1)
		if n == 1 {
			return baseTime
		}
		return baseTime.Add(750 * time.Millisecond)
	}

	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		body := "data: event1\n\ndata: event2\n\n"
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})

	ct := NewCachingTransport(upstream, store,
		WithCacheSingleFlight(false),
		withCacheNowFunc(fakeNow),
	)

	req, _ := http.NewRequest("GET", "http://example.com/stream", nil)
	resp, err := ct.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	// Consume body to trigger SSE recording completion.
	io.ReadAll(resp.Body)
	resp.Body.Close()

	tapes, _ := store.List(context.Background(), Filter{})
	if len(tapes) != 1 {
		t.Fatalf("expected 1 tape, got %d", len(tapes))
	}
	if !tapes[0].Response.IsSSE() {
		t.Fatal("expected SSE tape")
	}
	if tapes[0].Response.ElapsedMS != 750 {
		t.Errorf("ElapsedMS = %d, want 750", tapes[0].Response.ElapsedMS)
	}
}

// ---------------------------------------------------------------------------
// WithCacheReplayTiming on cache hit
// ---------------------------------------------------------------------------

func TestCachingTransport_WithCacheReplayTiming_Recorded(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	tape := NewTape("", RecordedReq{
		Method:   "GET",
		URL:      "http://example.com/api",
		Headers:  http.Header{},
		BodyHash: "",
	}, RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{},
		Body:       []byte("cached"),
		ElapsedMS:  300,
	})
	store.Save(context.Background(), tape)

	var sleepCalled bool
	var sleepDuration time.Duration

	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		t.Error("upstream should not be called on cache hit")
		return nil, fmt.Errorf("unreachable")
	})

	ct := NewCachingTransport(upstream, store,
		WithCacheReplayTiming(ResponseTimingRecorded()),
		withCacheSleepFunc(func(_ context.Context, d time.Duration) error {
			sleepCalled = true
			sleepDuration = d
			return nil
		}),
	)

	req, _ := http.NewRequest("GET", "http://example.com/api", nil)
	resp, err := ct.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if string(body) != "cached" {
		t.Errorf("body = %q, want %q", string(body), "cached")
	}
	if !sleepCalled {
		t.Error("sleepFunc was not called for recorded timing")
	}
	if sleepDuration != 300*time.Millisecond {
		t.Errorf("sleepDuration = %v, want 300ms", sleepDuration)
	}
}

func TestCachingTransport_WithCacheReplayTiming_PreFeatureNoDelay(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	tape := NewTape("", RecordedReq{
		Method:   "GET",
		URL:      "http://example.com/api",
		Headers:  http.Header{},
		BodyHash: "",
	}, RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{},
		Body:       []byte("cached"),
		ElapsedMS:  0, // pre-feature
	})
	store.Save(context.Background(), tape)

	var sleepCalled bool

	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("unreachable")
	})

	ct := NewCachingTransport(upstream, store,
		WithCacheReplayTiming(ResponseTimingRecorded()),
		withCacheSleepFunc(func(_ context.Context, d time.Duration) error {
			sleepCalled = true
			return nil
		}),
	)

	req, _ := http.NewRequest("GET", "http://example.com/api", nil)
	resp, err := ct.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if sleepCalled {
		t.Error("sleepFunc was called for pre-feature fixture (ElapsedMS=0)")
	}
}

// ---------------------------------------------------------------------------
// AC-4: single-flight key distinguishes query string — regression test
// ---------------------------------------------------------------------------

// TestCachingTransport_SingleFlightDistinguishesQueryString verifies that two
// concurrent cache-miss requests differing only by query string each receive
// the correct upstream response and that upstream is called exactly twice.
// This is the exact bug reported in #285 (page=1 waiter received page=2 body).
func TestCachingTransport_SingleFlightDistinguishesQueryString(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()

	var callCount atomic.Int64
	// ready is closed once both goroutines have entered the upstream stub,
	// ensuring they are in the inflight window at the same time.
	var arrivedCount atomic.Int64
	unblock := make(chan struct{})

	upstream := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		n := callCount.Add(1)
		arrivedCount.Add(1)
		// First arrival waits until the second has also arrived (both concurrent misses).
		if n == 1 {
			<-unblock
		}
		page := req.URL.Query().Get("page")
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("page=" + page)),
		}, nil
	})

	ct := NewCachingTransport(upstream, store,
		WithCacheMatcher(NewCompositeMatcher(MethodCriterion{}, PathCriterion{}, QueryParamsCriterion{})),
		WithCacheSingleFlight(true),
	)

	// Release the first goroutine once the second has entered the stub.
	go func() {
		for arrivedCount.Load() < 2 {
			// spin — in tests this resolves within microseconds once the
			// second goroutine starts its upstream call.
		}
		close(unblock)
	}()

	type result struct {
		body string
		err  error
	}
	results := make([]result, 2)
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		req, _ := http.NewRequest("GET", "http://example.com/data?page=1", nil)
		resp, err := ct.RoundTrip(req)
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			results[0] = result{body: string(b)}
		} else {
			results[0] = result{err: err}
		}
	}()

	go func() {
		defer wg.Done()
		req, _ := http.NewRequest("GET", "http://example.com/data?page=2", nil)
		resp, err := ct.RoundTrip(req)
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			results[1] = result{body: string(b)}
		} else {
			results[1] = result{err: err}
		}
	}()

	wg.Wait()

	if c := callCount.Load(); c != 2 {
		t.Errorf("upstream called %d times, want 2 (distinct query strings must not collapse)", c)
	}
	for i, r := range results {
		if r.err != nil {
			t.Errorf("goroutine %d got error: %v", i, r.err)
		}
	}
	// Each goroutine must receive its own page's body.
	if results[0].body != "page=1" && results[1].body != "page=1" {
		t.Errorf("neither goroutine received page=1 body; got %q and %q", results[0].body, results[1].body)
	}
	if results[0].body != "page=2" && results[1].body != "page=2" {
		t.Errorf("neither goroutine received page=2 body; got %q and %q", results[0].body, results[1].body)
	}
	// Verify each goroutine received its own query's response (not the other's).
	for i, r := range results {
		page := fmt.Sprintf("page=%d", i+1)
		if r.body != page {
			t.Errorf("goroutine %d got body %q, want %q", i, r.body, page)
		}
	}
}

// ---------------------------------------------------------------------------
// AC-5: single-flight key still collapses genuinely identical requests
// ---------------------------------------------------------------------------

// TestCachingTransport_SingleFlightCollapsesIdenticalRequests verifies that
// two concurrent cache-miss requests that are genuinely identical (same URL,
// same headers) still share a single upstream call.
func TestCachingTransport_SingleFlightCollapsesIdenticalRequests(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()

	var callCount atomic.Int64
	upstream := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		callCount.Add(1)
		time.Sleep(50 * time.Millisecond) // widen inflight window
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("shared-body")),
		}, nil
	})

	ct := NewCachingTransport(upstream, store,
		WithCacheMatcher(NewCompositeMatcher(MethodCriterion{}, PathCriterion{}, QueryParamsCriterion{})),
		WithCacheSingleFlight(true),
	)

	const N = 5
	var wg sync.WaitGroup
	wg.Add(N)
	bodies := make([]string, N)
	errs := make([]error, N)

	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			req, _ := http.NewRequest("GET", "http://example.com/data?page=1", nil)
			resp, err := ct.RoundTrip(req)
			errs[idx] = err
			if resp != nil {
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				bodies[idx] = string(b)
			}
		}(i)
	}
	wg.Wait()

	if c := callCount.Load(); c != 1 {
		t.Errorf("upstream called %d times, want 1 (identical requests must collapse)", c)
	}
	for i := 0; i < N; i++ {
		if errs[i] != nil {
			t.Errorf("goroutine %d error: %v", i, errs[i])
		}
		if bodies[i] != "shared-body" {
			t.Errorf("goroutine %d body = %q, want %q", i, bodies[i], "shared-body")
		}
	}
}

// ---------------------------------------------------------------------------
// AC-2: single-flight key distinguishes header values
// ---------------------------------------------------------------------------

// TestCachingTransport_SingleFlightDistinguishesHeaders verifies that two
// concurrent cache-miss requests differing only by a matcher-relevant header
// each receive their correct upstream response and that upstream is called
// exactly twice.
func TestCachingTransport_SingleFlightDistinguishesHeaders(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()

	var callCount atomic.Int64
	var arrivedCount atomic.Int64
	unblock := make(chan struct{})

	upstream := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		n := callCount.Add(1)
		arrivedCount.Add(1)
		if n == 1 {
			<-unblock
		}
		accept := req.Header.Get("Accept")
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("accept=" + accept)),
		}, nil
	})

	ct := NewCachingTransport(upstream, store,
		WithCacheMatcher(NewCompositeMatcher(MethodCriterion{}, PathCriterion{})),
		WithCacheSingleFlight(true),
	)

	go func() {
		for arrivedCount.Load() < 2 {
		}
		close(unblock)
	}()

	type result struct {
		body string
		err  error
	}
	results := make([]result, 2)
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		req, _ := http.NewRequest("GET", "http://example.com/api", nil)
		req.Header.Set("Accept", "application/json")
		resp, err := ct.RoundTrip(req)
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			results[0] = result{body: string(b)}
		} else {
			results[0] = result{err: err}
		}
	}()

	go func() {
		defer wg.Done()
		req, _ := http.NewRequest("GET", "http://example.com/api", nil)
		req.Header.Set("Accept", "text/csv")
		resp, err := ct.RoundTrip(req)
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			results[1] = result{body: string(b)}
		} else {
			results[1] = result{err: err}
		}
	}()

	wg.Wait()

	if c := callCount.Load(); c != 2 {
		t.Errorf("upstream called %d times, want 2 (distinct Accept headers must not collapse)", c)
	}
	for i, r := range results {
		if r.err != nil {
			t.Errorf("goroutine %d got error: %v", i, r.err)
		}
	}
	wantBodies := []string{"accept=application/json", "accept=text/csv"}
	for i, r := range results {
		if r.body != wantBodies[i] {
			t.Errorf("goroutine %d got body %q, want %q", i, r.body, wantBodies[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Unit test: singleFlightKey
// ---------------------------------------------------------------------------

// TestSingleFlightKey verifies the key construction logic with a table of
// inputs covering distinct-key and same-key cases.
func TestSingleFlightKey(t *testing.T) {
	t.Parallel()

	makeReq := func(method, rawURL string, headers http.Header) *http.Request {
		req, _ := http.NewRequest(method, rawURL, nil)
		for k, vs := range headers {
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}
		return req
	}

	tests := []struct {
		name      string
		req1      *http.Request
		bodyHash1 string
		req2      *http.Request
		bodyHash2 string
		wantSame  bool
	}{
		{
			name:      "same request produces same key",
			req1:      makeReq("GET", "http://example.com/api?page=1", http.Header{"Accept": {"application/json"}}),
			bodyHash1: "abc",
			req2:      makeReq("GET", "http://example.com/api?page=1", http.Header{"Accept": {"application/json"}}),
			bodyHash2: "abc",
			wantSame:  true,
		},
		{
			name:      "different query string produces different key",
			req1:      makeReq("GET", "http://example.com/api?page=1", nil),
			bodyHash1: "",
			req2:      makeReq("GET", "http://example.com/api?page=2", nil),
			bodyHash2: "",
			wantSame:  false,
		},
		{
			name:      "different Accept header produces different key",
			req1:      makeReq("GET", "http://example.com/api", http.Header{"Accept": {"application/json"}}),
			bodyHash1: "",
			req2:      makeReq("GET", "http://example.com/api", http.Header{"Accept": {"text/csv"}}),
			bodyHash2: "",
			wantSame:  false,
		},
		{
			name:      "different body hash produces different key",
			req1:      makeReq("POST", "http://example.com/api", nil),
			bodyHash1: "hash1",
			req2:      makeReq("POST", "http://example.com/api", nil),
			bodyHash2: "hash2",
			wantSame:  false,
		},
		{
			name:      "header value order does not change the key",
			req1:      makeReq("GET", "http://example.com/api", http.Header{"X-Foo": {"b", "a"}}),
			bodyHash1: "",
			req2:      makeReq("GET", "http://example.com/api", http.Header{"X-Foo": {"a", "b"}}),
			bodyHash2: "",
			wantSame:  true,
		},
		{
			name:      "denylisted Connection header difference does not change the key",
			req1:      makeReq("GET", "http://example.com/api", http.Header{"Connection": {"keep-alive"}}),
			bodyHash1: "",
			req2:      makeReq("GET", "http://example.com/api", http.Header{"Connection": {"close"}}),
			bodyHash2: "",
			wantSame:  true,
		},
		{
			name:      "denylisted Content-Length difference does not change the key",
			req1:      makeReq("POST", "http://example.com/api", http.Header{"Content-Length": {"0"}}),
			bodyHash1: "",
			req2:      makeReq("POST", "http://example.com/api", http.Header{"Content-Length": {"42"}}),
			bodyHash2: "",
			wantSame:  true,
		},
		{
			name:      "nil headers produce same key as empty headers",
			req1:      makeReq("GET", "http://example.com/api", nil),
			bodyHash1: "",
			req2:      makeReq("GET", "http://example.com/api", http.Header{}),
			bodyHash2: "",
			wantSame:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k1 := singleFlightKey(tt.req1, tt.bodyHash1)
			k2 := singleFlightKey(tt.req2, tt.bodyHash2)
			if tt.wantSame && k1 != k2 {
				t.Errorf("expected same key; got\n  k1=%q\n  k2=%q", k1, k2)
			}
			if !tt.wantSame && k1 == k2 {
				t.Errorf("expected different keys; both produced %q", k1)
			}
		})
	}
}
