package httptape

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestMemoryStore_ConcurrentSaveLoad exercises concurrent Save and Load
// operations on MemoryStore under the race detector.
func TestMemoryStore_ConcurrentSaveLoad(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	const n = 100

	var wg sync.WaitGroup

	// Concurrent saves.
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tape := makeTape("route", "GET", fmt.Sprintf("http://example.com/%d", i))
			if err := store.Save(ctx, tape); err != nil {
				t.Errorf("Save(%d) error: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	// Concurrent loads.
	tapes, err := store.List(ctx, Filter{})
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	for _, tape := range tapes {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if _, err := store.Load(ctx, id); err != nil {
				t.Errorf("Load(%s) error: %v", id, err)
			}
		}(tape.ID)
	}
	wg.Wait()

	// Concurrent mixed reads and writes.
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			tape := makeTape("mixed", "POST", fmt.Sprintf("http://example.com/mixed/%d", i))
			_ = store.Save(ctx, tape)
		}(i)
		go func() {
			defer wg.Done()
			_, _ = store.List(ctx, Filter{})
		}()
	}
	wg.Wait()
}

// TestFileStore_ConcurrentSaveLoad exercises concurrent Save and Load
// operations on FileStore under the race detector.
func TestFileStore_ConcurrentSaveLoad(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "httptape-race-filestore-"+t.Name())
	os.RemoveAll(dir)
	defer os.RemoveAll(dir)

	store, err := NewFileStore(WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewFileStore() error: %v", err)
	}

	ctx := context.Background()
	const n = 50

	var wg sync.WaitGroup

	// Concurrent saves.
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tape := makeTape("route", "GET", fmt.Sprintf("http://example.com/%d", i))
			if err := store.Save(ctx, tape); err != nil {
				t.Errorf("Save(%d) error: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	// Concurrent loads.
	tapes, listErr := store.List(ctx, Filter{})
	if listErr != nil {
		t.Fatalf("List() error: %v", listErr)
	}
	for _, tape := range tapes {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if _, err := store.Load(ctx, id); err != nil {
				t.Errorf("Load(%s) error: %v", id, err)
			}
		}(tape.ID)
	}
	wg.Wait()

	// Concurrent mixed reads and writes.
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			tape := makeTape("mixed", "POST", fmt.Sprintf("http://example.com/mixed/%d", i))
			_ = store.Save(ctx, tape)
		}(i)
		go func() {
			defer wg.Done()
			_, _ = store.List(ctx, Filter{})
		}()
	}
	wg.Wait()
}

// TestRecorder_ConcurrentRoundTrip exercises concurrent RoundTrip calls
// followed by Close under the race detector, verifying the TOCTOU fix.
func TestRecorder_ConcurrentRoundTrip(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer backend.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store,
		WithTransport(backend.Client().Transport),
		WithRoute("race-test"),
		WithBufferSize(256),
	)

	const n = 100
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req, err := http.NewRequest("GET", backend.URL+fmt.Sprintf("/path/%d", i), nil)
			if err != nil {
				t.Errorf("NewRequest error: %v", err)
				return
			}
			resp, err := rec.RoundTrip(req)
			if err != nil {
				t.Errorf("RoundTrip(%d) error: %v", i, err)
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}(i)
	}
	wg.Wait()

	if err := rec.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
}

// TestRecorder_ConcurrentRoundTripAndClose exercises the specific race between
// RoundTrip and Close that the sendMu fix addresses.
func TestRecorder_ConcurrentRoundTripAndClose(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer backend.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store,
		WithTransport(backend.Client().Transport),
		WithRoute("close-race"),
		WithBufferSize(16),
	)

	const n = 50
	var wg sync.WaitGroup

	// Launch many concurrent RoundTrips.
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req, err := http.NewRequest("POST", backend.URL+"/api",
				strings.NewReader(fmt.Sprintf(`{"i":%d}`, i)))
			if err != nil {
				t.Errorf("NewRequest error: %v", err)
				return
			}
			resp, err := rec.RoundTrip(req)
			if err != nil {
				// Errors after close are acceptable.
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}(i)
	}

	// Close concurrently while RoundTrips are in flight.
	wg.Add(1)
	go func() {
		defer wg.Done()
		rec.Close()
	}()

	wg.Wait()
}

// TestServer_ConcurrentServeHTTP exercises concurrent ServeHTTP calls
// under the race detector.
func TestServer_ConcurrentServeHTTP(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	// Pre-load some tapes.
	for i := 0; i < 10; i++ {
		tape := makeTape("server-race", "GET", fmt.Sprintf("/items/%d", i))
		if err := store.Save(ctx, tape); err != nil {
			t.Fatalf("Save() error: %v", err)
		}
	}

	srv, err := NewServer(store)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	const n = 100
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path := fmt.Sprintf("/items/%d", i%10)
			resp, err := http.Get(ts.URL + path)
			if err != nil {
				t.Errorf("GET %s error: %v", path, err)
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}(i)
	}
	wg.Wait()
}

// TestRecorder_ConcurrentSSERecording exercises concurrent SSE recording
// under the race detector, verifying the goroutine + io.Pipe path is safe.
func TestRecorder_ConcurrentSSERecording(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		fmt.Fprint(w, "data: event1\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: event2\n\n")
		flusher.Flush()
	}))
	defer backend.Close()

	store := NewMemoryStore()
	rec := NewRecorder(store,
		WithTransport(backend.Client().Transport),
		WithRoute("sse-race"),
	)

	const n = 20
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req, err := http.NewRequest("GET", backend.URL+fmt.Sprintf("/stream/%d", i), nil)
			if err != nil {
				t.Errorf("NewRequest error: %v", err)
				return
			}
			resp, err := rec.RoundTrip(req)
			if err != nil {
				t.Errorf("RoundTrip(%d) error: %v", i, err)
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}(i)
	}
	wg.Wait()

	if err := rec.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
}

// TestServer_ConcurrentSSEReplay exercises concurrent SSE replay under the
// race detector.
func TestServer_ConcurrentSSEReplay(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	// Pre-load SSE tapes.
	for i := 0; i < 5; i++ {
		tape := NewTape("sse-race", RecordedReq{
			Method:  "GET",
			URL:     fmt.Sprintf("/stream/%d", i),
			Headers: http.Header{},
		}, RecordedResp{
			StatusCode: 200,
			Headers:    http.Header{"Content-Type": {"text/event-stream"}},
			SSEEvents: []SSEEvent{
				{OffsetMS: 0, Data: fmt.Sprintf("event-%d-a", i)},
				{OffsetMS: 10, Data: fmt.Sprintf("event-%d-b", i)},
			},
		})
		if err := store.Save(ctx, tape); err != nil {
			t.Fatalf("Save() error: %v", err)
		}
	}

	srv, err := NewServer(store, WithSSETiming(SSETimingInstant()))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	const n = 50
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path := fmt.Sprintf("/stream/%d", i%5)
			resp, err := http.Get(ts.URL + path)
			if err != nil {
				t.Errorf("GET %s error: %v", path, err)
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}(i)
	}
	wg.Wait()
}

// TestCachingTransport_SingleFlightDedupesConcurrentMisses verifies that
// concurrent cache misses for the same request key result in exactly one
// upstream call. All goroutines are released via a barrier to ensure they
// arrive before the store is populated, exercising the single-flight
// WaitGroup path under the race detector.
func TestCachingTransport_SingleFlightDedupesConcurrentMisses(t *testing.T) {
	const N = 50

	var upstreamHits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		time.Sleep(100 * time.Millisecond) // hold the leader long enough for followers to arrive
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"deduped":true}`)
	}))
	defer srv.Close()

	store := NewMemoryStore()
	ct := NewCachingTransport(http.DefaultTransport, store,
		WithCacheSingleFlight(true),
	)

	barrier := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(N)
	errs := make([]error, N)
	statuses := make([]int, N)
	bodies := make([]string, N)

	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			<-barrier // wait for all goroutines to be ready
			req, _ := http.NewRequest("GET", srv.URL+"/api/data", nil)
			resp, err := ct.RoundTrip(req)
			errs[idx] = err
			if resp != nil {
				statuses[idx] = resp.StatusCode
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				bodies[idx] = string(b)
			}
		}(i)
	}

	// Release all goroutines simultaneously.
	close(barrier)
	wg.Wait()

	// Single upstream call.
	if hits := upstreamHits.Load(); hits != 1 {
		t.Errorf("upstream hit count = %d, want 1 (single-flight should deduplicate)", hits)
	}

	// Every goroutine received a valid response.
	for i := 0; i < N; i++ {
		if errs[i] != nil {
			t.Errorf("goroutine %d error: %v", i, errs[i])
			continue
		}
		if statuses[i] != http.StatusOK {
			t.Errorf("goroutine %d status = %d, want %d", i, statuses[i], http.StatusOK)
		}
		if bodies[i] != `{"deduped":true}` {
			t.Errorf("goroutine %d body = %q, want %q", i, bodies[i], `{"deduped":true}`)
		}
	}

	// Exactly one tape was persisted.
	tapes, err := store.List(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("store.List error: %v", err)
	}
	if len(tapes) != 1 {
		t.Errorf("store contains %d tapes, want 1", len(tapes))
	}
}

// TestCachingTransport_SingleFlightPropagatesLeaderError verifies that when
// the leader's upstream call fails, all waiting followers receive the same
// error without hanging. A context timeout bounds the test so a deadlock
// would surface as a clear failure rather than a CI-wide timeout.
func TestCachingTransport_SingleFlightPropagatesLeaderError(t *testing.T) {
	const N = 50

	var upstreamHits atomic.Int64
	upstream := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamHits.Add(1)
		time.Sleep(100 * time.Millisecond) // hold the leader
		return nil, errors.New("upstream exploded")
	})

	store := NewMemoryStore()
	ct := NewCachingTransport(upstream, store,
		WithCacheSingleFlight(true),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	barrier := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(N)
	errs := make([]error, N)

	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			<-barrier
			req, _ := http.NewRequestWithContext(ctx, "GET", "http://example.com/api/fail", nil)
			resp, err := ct.RoundTrip(req)
			if resp != nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
			errs[idx] = err
		}(i)
	}

	close(barrier)

	// Use a channel to detect if wg.Wait never returns (deadlock guard).
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All goroutines returned -- no deadlock.
	case <-ctx.Done():
		t.Fatal("test timed out: followers appear to be hanging (deadlock in single-flight error path)")
	}

	// Upstream was called exactly once (the leader).
	if hits := upstreamHits.Load(); hits != 1 {
		t.Errorf("upstream hit count = %d, want 1", hits)
	}

	// Every goroutine received an error containing the upstream failure message.
	for i := 0; i < N; i++ {
		if errs[i] == nil {
			t.Errorf("goroutine %d: expected error, got nil", i)
			continue
		}
		if !strings.Contains(errs[i].Error(), "upstream exploded") {
			t.Errorf("goroutine %d: error = %q, want substring %q", i, errs[i].Error(), "upstream exploded")
		}
	}

	// No tapes stored (error responses are not cached).
	tapes, err := store.List(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("store.List error: %v", err)
	}
	if len(tapes) != 0 {
		t.Errorf("store contains %d tapes, want 0 (error should not be cached)", len(tapes))
	}
}

// Note on SSE single-flight dedup (issue #220, Test 3):
//
// SSE responses intentionally bypass single-flight response-body cloning.
// In roundTripWithSingleFlight (caching_transport.go, lines 349-359), when
// the leader's upstream response has Content-Type text/event-stream, the
// sfCall is marked sse=true and wg.Done() is called immediately. Followers
// then re-query the store via reQueryStoreForSSE (line 322) rather than
// cloning the leader's streaming body. This is correct: an SSE body is a
// live io.ReadCloser that cannot be safely cloned across goroutines.
//
// Concurrent SSE single-flight behavior is already covered by
// TestCachingTransport_SingleFlightSSEWaiters in caching_transport_test.go.
// A dedicated race-detector stress test is not added here because the SSE
// waiter path does not share mutable response state -- it performs an
// independent store read -- so the race surface is identical to
// TestServer_ConcurrentSSEReplay above.

// TestCounterState_Race stresses the counterState under heavy concurrent
// access to verify mutex correctness with the race detector.
func TestCounterState_Race(t *testing.T) {
	cs := &counterState{counters: make(map[string]int64)}
	n := 1000
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			cs.Next("shared")
		}()
	}
	wg.Wait()

	got := cs.counters["shared"]
	if got != int64(n) {
		t.Errorf("after %d concurrent increments, counter = %d", n, got)
	}
}
