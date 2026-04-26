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
