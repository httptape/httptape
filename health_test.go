package httptape

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// healthNoopTransport is a stand-in transport for HealthMonitor construction in
// tests where the probe loop is disabled.
type healthNoopTransport struct{}

func (healthNoopTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("healthNoopTransport: not implemented")
}

// recordingTransport counts RoundTrip calls and returns the configured
// response/error.
type recordingTransport struct {
	mu     sync.Mutex
	calls  int
	respFn func(*http.Request) (*http.Response, error)
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	if r.respFn == nil {
		return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}}, nil
	}
	return r.respFn(req)
}

func (r *recordingTransport) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// waitForGoroutineCount polls runtime.NumGoroutine with backoff and returns
// true if the count returns to (or below) the baseline within the deadline.
// Tests that spawn workers can use this as a leak check after Close.
func waitForGoroutineCount(t *testing.T, baseline int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		got := runtime.NumGoroutine()
		if got <= baseline {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("goroutine count did not return to baseline=%d within %s (got %d)", baseline, within, got)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestHealthMonitor_InitialState(t *testing.T) {
	now := time.Date(2026, 4, 16, 10, 0, 0, 0, time.UTC)
	h := NewHealthMonitor("http://example.com", healthNoopTransport{},
		WithHealthClock(func() time.Time { return now }))

	snap := h.snapshot()
	if snap.State != StateLive {
		t.Errorf("State=%q, want %q", snap.State, StateLive)
	}
	if !snap.Since.Equal(now) {
		t.Errorf("Since=%v, want %v", snap.Since, now)
	}
	if snap.LastProbedAt != nil {
		t.Errorf("LastProbedAt=%v, want nil", snap.LastProbedAt)
	}
	if snap.UpstreamURL != "http://example.com" {
		t.Errorf("UpstreamURL=%q", snap.UpstreamURL)
	}
	if snap.ProbeIntervalMS != 0 {
		t.Errorf("ProbeIntervalMS=%d, want 0", snap.ProbeIntervalMS)
	}
}

func TestNewHealthMonitor_PanicsOnEmptyUpstream(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on empty upstream URL")
		}
	}()
	NewHealthMonitor("", healthNoopTransport{})
}

func TestNewHealthMonitor_PanicsOnNilTransport(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on nil transport")
		}
	}()
	NewHealthMonitor("http://example.com", nil)
}

func TestHealthMonitor_ObserveTransition(t *testing.T) {
	tests := []struct {
		name   string
		input  []SourceState
		wantSt SourceState
	}{
		{"single live", []SourceState{StateLive}, StateLive},
		{"live to l1", []SourceState{StateLive, StateL1Cache}, StateL1Cache},
		{"live to l2 to live", []SourceState{StateLive, StateL2Cache, StateLive}, StateLive},
		{"unknown ignored", []SourceState{StateLive, "garbage", StateL1Cache}, StateL1Cache},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHealthMonitor("http://x", healthNoopTransport{})
			for _, s := range tt.input {
				h.observe(s)
			}
			if got := h.snapshot().State; got != tt.wantSt {
				t.Errorf("State=%q, want %q", got, tt.wantSt)
			}
		})
	}
}

func TestHealthMonitor_ObserveUpdatesSinceOnlyOnTransition(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 4, 16, 10, 0, 0, 0, time.UTC)}
	h := NewHealthMonitor("http://x", healthNoopTransport{},
		WithHealthClock(clock.Now))
	initial := h.snapshot().Since

	clock.advance(5 * time.Second)
	h.observe(StateLive) // same state, no transition
	if !h.snapshot().Since.Equal(initial) {
		t.Error("Since changed despite no transition")
	}

	clock.advance(5 * time.Second)
	h.observe(StateL1Cache) // transition
	got := h.snapshot().Since
	if got.Equal(initial) {
		t.Error("Since did not change on transition")
	}
}

func TestHealthMonitor_ObserveOnNilReceiverIsNoop(t *testing.T) {
	var h *HealthMonitor
	// Should not panic.
	h.observe(StateLive)
	h.observeProbe(StateLive)
	h.recordProbeAttempt()
	if err := h.Close(); err != nil {
		t.Errorf("nil Close returned %v, want nil", err)
	}
	h.start()
}

func TestHealthMonitor_SubscribeReceivesInitial(t *testing.T) {
	h := NewHealthMonitor("http://x", healthNoopTransport{})
	ch, unsub := h.subscribe()
	defer unsub()

	select {
	case snap := <-ch:
		if snap.State != StateLive {
			t.Errorf("State=%q, want %q", snap.State, StateLive)
		}
	case <-time.After(time.Second):
		t.Fatal("did not receive initial snapshot")
	}
}

func TestHealthMonitor_NoEventOnSameState(t *testing.T) {
	h := NewHealthMonitor("http://x", healthNoopTransport{})
	ch, unsub := h.subscribe()
	defer unsub()

	// Drain the initial snapshot.
	<-ch

	h.observe(StateLive) // no transition
	h.observe(StateLive)

	select {
	case snap, ok := <-ch:
		if ok {
			t.Errorf("unexpected event on no-transition: %+v", snap)
		}
	case <-time.After(50 * time.Millisecond):
		// Expected: nothing sent.
	}
}

func TestHealthMonitor_BroadcastFanOut(t *testing.T) {
	const subs = 100
	h := NewHealthMonitor("http://x", healthNoopTransport{})

	channels := make([]<-chan HealthSnapshot, subs)
	unsubs := make([]func(), subs)
	for i := 0; i < subs; i++ {
		channels[i], unsubs[i] = h.subscribe()
	}
	defer func() {
		for _, u := range unsubs {
			u()
		}
	}()

	// Drain initial snapshots.
	for i := 0; i < subs; i++ {
		<-channels[i]
	}

	h.observe(StateL2Cache)

	for i, ch := range channels {
		select {
		case snap := <-ch:
			if snap.State != StateL2Cache {
				t.Errorf("subscriber %d got state=%q, want %q", i, snap.State, StateL2Cache)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d did not receive transition event", i)
		}
	}
}

func TestHealthMonitor_SlowSubscriberDropped(t *testing.T) {
	h := NewHealthMonitor("http://x", healthNoopTransport{})

	// Subscribe two subscribers; one will be drained, the other will not.
	slowCh, _ := h.subscribe()
	fastCh, fastUnsub := h.subscribe()
	defer fastUnsub()

	// Drain initial seeds so the buffers start empty.
	<-slowCh
	<-fastCh

	// Push more transitions than the buffer can hold without draining the slow one.
	states := []SourceState{StateL1Cache, StateLive, StateL1Cache, StateLive,
		StateL1Cache, StateLive, StateL1Cache, StateLive,
		StateL1Cache, StateLive, StateL1Cache, StateLive}
	for _, s := range states {
		h.observe(s)
	}

	// Drain the fast subscriber to keep it healthy.
	go func() {
		for range fastCh {
			// drain
		}
	}()

	// Slow subscriber's channel must be closed within a reasonable time.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-slowCh:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("slow subscriber was not dropped")
		}
	}
}

func TestHealthMonitor_CloseDrainsSubscribers(t *testing.T) {
	h := NewHealthMonitor("http://x", healthNoopTransport{})
	ch, _ := h.subscribe()

	// Drain initial seed.
	<-ch

	if err := h.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected channel closed")
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber channel was not closed by Close()")
	}

	// Idempotent.
	if err := h.Close(); err != nil {
		t.Errorf("second Close returned %v, want nil", err)
	}
}

func TestHealthMonitor_CloseStopsProbe(t *testing.T) {
	rt := &recordingTransport{}
	h := NewHealthMonitor("http://x", rt,
		WithHealthInterval(20*time.Millisecond))

	baseline := runtime.NumGoroutine()
	h.start()

	// Allow a couple of probe ticks.
	time.Sleep(80 * time.Millisecond)
	if rt.callCount() == 0 {
		t.Fatal("probe never fired")
	}

	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Sample AFTER Close: by then any in-flight probe has finished (Close
	// waits on wg) and the recheck inside runProbe guarantees no further
	// probe will start. This makes the post-Close growth check race-free —
	// sampling before Close races with an in-flight tick at the boundary.
	afterClose := rt.callCount()

	// After close, no further calls within 3x the interval.
	time.Sleep(80 * time.Millisecond)
	if got := rt.callCount(); got > afterClose {
		t.Errorf("probe kept firing after Close: %d -> %d", afterClose, got)
	}

	waitForGoroutineCount(t, baseline, time.Second)
}

func TestHealthMonitor_StartIdempotent(t *testing.T) {
	rt := &recordingTransport{}
	h := NewHealthMonitor("http://x", rt,
		WithHealthInterval(20*time.Millisecond))
	defer h.Close() //nolint:errcheck

	baseline := runtime.NumGoroutine()
	h.start()
	h.start()
	h.start()

	time.Sleep(60 * time.Millisecond)

	// Only one probe goroutine should be running, so calls should be roughly
	// 60ms / 20ms = 3 (allow some slack), not 9.
	got := rt.callCount()
	if got > 6 {
		t.Errorf("Start spawned multiple probe loops: %d calls in 60ms", got)
	}
	_ = baseline // we don't enforce a goroutine count here; CloseStopsProbe does.
}

func TestHealthMonitor_ProbeHEADtoGETPromotion(t *testing.T) {
	var seenHead, seenGet atomic.Int32
	rt := &recordingTransport{
		respFn: func(req *http.Request) (*http.Response, error) {
			switch req.Method {
			case http.MethodHead:
				seenHead.Add(1)
				return &http.Response{StatusCode: 405, Body: http.NoBody, Header: http.Header{}}, nil
			case http.MethodGet:
				seenGet.Add(1)
				return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}}, nil
			}
			return nil, errors.New("unexpected method")
		},
	}
	h := NewHealthMonitor("http://x", rt,
		WithHealthInterval(15*time.Millisecond))
	defer h.Close() //nolint:errcheck

	h.start()

	deadline := time.After(2 * time.Second)
	for {
		if seenHead.Load() >= 1 && seenGet.Load() >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("did not promote HEAD->GET (head=%d, get=%d)",
				seenHead.Load(), seenGet.Load())
		default:
			time.Sleep(15 * time.Millisecond)
		}
	}
}

func TestHealthMonitor_ProbeFedThroughTransport(t *testing.T) {
	// Phase 1: probe sees live, no transition (state already live).
	// Phase 2: switch transport to return l2-cache header — state transitions
	// to StateL2Cache and SSE subscriber receives the event.
	var phase atomic.Int32 // 0 = live, 1 = l2

	rt := &recordingTransport{
		respFn: func(req *http.Request) (*http.Response, error) {
			if phase.Load() == 0 {
				return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}}, nil
			}
			h := http.Header{}
			h.Set("X-Httptape-Source", string(StateL2Cache))
			return &http.Response{StatusCode: 200, Body: http.NoBody, Header: h}, nil
		},
	}

	h := NewHealthMonitor("http://x", rt,
		WithHealthInterval(15*time.Millisecond))
	defer h.Close() //nolint:errcheck

	ch, unsub := h.subscribe()
	defer unsub()

	// Drain initial seed.
	if snap := <-ch; snap.State != StateLive {
		t.Fatalf("initial state %q, want %q", snap.State, StateLive)
	}

	h.start()

	// Trigger the transition.
	phase.Store(1)

	select {
	case snap := <-ch:
		if snap.State != StateL2Cache {
			t.Errorf("got state %q, want %q", snap.State, StateL2Cache)
		}
		if snap.LastProbedAt == nil {
			t.Error("LastProbedAt should be set after probe ticks")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive l2-cache transition event from probe")
	}

	// Phase 3: revert to live; expect another transition.
	phase.Store(0)

	select {
	case snap := <-ch:
		if snap.State != StateLive {
			t.Errorf("got state %q, want %q", snap.State, StateLive)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive recovery transition to live")
	}
}

func TestHealthMonitor_SnapshotJSONShape(t *testing.T) {
	clock := time.Date(2026, 4, 16, 10, 0, 0, 0, time.UTC)
	h := NewHealthMonitor("http://example.com", healthNoopTransport{},
		WithHealthClock(func() time.Time { return clock }),
		WithHealthInterval(2*time.Second))

	// Before any probe.
	got, err := json.Marshal(h.snapshot())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	gotStr := string(got)
	if strings.Contains(gotStr, "last_probed_at") {
		t.Errorf("unexpected last_probed_at in snapshot before probe: %s", gotStr)
	}
	if !strings.Contains(gotStr, `"state":"live"`) {
		t.Errorf("missing state field: %s", gotStr)
	}
	if !strings.Contains(gotStr, `"upstream_url":"http://example.com"`) {
		t.Errorf("missing upstream_url: %s", gotStr)
	}
	if !strings.Contains(gotStr, `"probe_interval_ms":2000`) {
		t.Errorf("wrong probe_interval_ms: %s", gotStr)
	}

	// After a probe attempt.
	h.recordProbeAttempt()
	got, _ = json.Marshal(h.snapshot())
	if !strings.Contains(string(got), "last_probed_at") {
		t.Errorf("expected last_probed_at after probe: %s", got)
	}
}

func TestHealthMonitor_HTTPSnapshotEndpoint(t *testing.T) {
	h := NewHealthMonitor("http://x", healthNoopTransport{})
	defer h.Close() //nolint:errcheck

	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + healthEndpointPath)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status=%d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type=%q, want application/json", ct)
	}

	var snap HealthSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snap.State != StateLive {
		t.Errorf("State=%q, want %q", snap.State, StateLive)
	}
}

func TestHealthMonitor_HTTPSnapshotMethodNotAllowed(t *testing.T) {
	h := NewHealthMonitor("http://x", healthNoopTransport{})
	defer h.Close() //nolint:errcheck

	srv := httptest.NewServer(h)
	defer srv.Close()

	for _, path := range []string{healthEndpointPath, healthStreamPath} {
		req, _ := http.NewRequest("POST", srv.URL+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("POST %s status=%d, want 405", path, resp.StatusCode)
		}
		if a := resp.Header.Get("Allow"); a != "GET" {
			t.Errorf("POST %s Allow=%q, want GET", path, a)
		}
	}
}

func TestHealthMonitor_HTTPUnknownPath(t *testing.T) {
	h := NewHealthMonitor("http://x", healthNoopTransport{})
	defer h.Close() //nolint:errcheck

	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/__httptape/unknown")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status=%d, want 404", resp.StatusCode)
	}
}

func TestHealthMonitor_HTTPStreamEndpoint(t *testing.T) {
	h := NewHealthMonitor("http://x", healthNoopTransport{})
	defer h.Close() //nolint:errcheck

	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+healthStreamPath, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status=%d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type=%q, want text/event-stream", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control=%q, want no-cache", cc)
	}

	br := bufio.NewReader(resp.Body)

	// Read until we see the first data: line (initial event).
	initial, err := readSSEEvent(br, 2*time.Second)
	if err != nil {
		t.Fatalf("read initial event: %v", err)
	}
	var snap HealthSnapshot
	if err := json.Unmarshal([]byte(initial), &snap); err != nil {
		t.Fatalf("unmarshal initial: %v (raw=%q)", err, initial)
	}
	if snap.State != StateLive {
		t.Errorf("initial state=%q, want %q", snap.State, StateLive)
	}

	// Trigger a transition.
	h.observe(StateL1Cache)
	payload, err := readSSEEvent(br, 2*time.Second)
	if err != nil {
		t.Fatalf("read transition event: %v", err)
	}
	var snap2 HealthSnapshot
	if err := json.Unmarshal([]byte(payload), &snap2); err != nil {
		t.Fatalf("unmarshal transition: %v (raw=%q)", err, payload)
	}
	if snap2.State != StateL1Cache {
		t.Errorf("transition state=%q, want %q", snap2.State, StateL1Cache)
	}
}

func TestHealthMonitor_HTTPStreamGracefulShutdown(t *testing.T) {
	h := NewHealthMonitor("http://x", healthNoopTransport{})

	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+healthStreamPath, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	br := bufio.NewReader(resp.Body)
	if _, err := readSSEEvent(br, 2*time.Second); err != nil {
		t.Fatalf("read initial: %v", err)
	}

	// Close the monitor; the SSE handler should return and the body should hit EOF.
	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Read should EOF (or the connection should be closed).
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, resp.Body)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil && err != io.EOF && !strings.Contains(err.Error(), "closed") && !strings.Contains(err.Error(), "EOF") {
			// Any clean termination is acceptable.
		}
	case <-time.After(2 * time.Second):
		t.Error("stream body did not unblock after Close")
	}
}

func TestHealthMonitor_StreamCloseUnblocksOnClientCancel(t *testing.T) {
	h := NewHealthMonitor("http://x", healthNoopTransport{})
	defer h.Close() //nolint:errcheck

	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+healthStreamPath, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}

	br := bufio.NewReader(resp.Body)
	if _, err := readSSEEvent(br, 2*time.Second); err != nil {
		t.Fatalf("read initial: %v", err)
	}

	cancel()
	// Read remaining bytes should terminate quickly.
	done := make(chan struct{})
	go func() {
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("client cancel did not unblock the stream")
	}
}

// readSSEEvent reads lines until it finds a "data: " line and returns the
// payload (without the prefix). Returns an error if the deadline elapses or
// the stream ends.
func readSSEEvent(br *bufio.Reader, within time.Duration) (string, error) {
	type result struct {
		payload string
		err     error
	}
	out := make(chan result, 1)
	go func() {
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				out <- result{err: err}
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if strings.HasPrefix(line, "data: ") {
				out <- result{payload: strings.TrimPrefix(line, "data: ")}
				return
			}
		}
	}()
	select {
	case r := <-out:
		return r.payload, r.err
	case <-time.After(within):
		return "", errors.New("readSSEEvent: deadline exceeded")
	}
}

func TestClassifyProbeResponse(t *testing.T) {
	tests := []struct {
		name string
		resp *http.Response
		want SourceState
		ok   bool
	}{
		{"nil response", nil, "", false},
		{"live 200", &http.Response{StatusCode: 200, Header: http.Header{}}, StateLive, true},
		{"live 404", &http.Response{StatusCode: 404, Header: http.Header{}}, StateLive, true},
		{"5xx no header", &http.Response{StatusCode: 500, Header: http.Header{}}, "", false},
		{"l1 header", &http.Response{StatusCode: 200, Header: http.Header{"X-Httptape-Source": {"l1-cache"}}}, StateL1Cache, true},
		{"l2 header", &http.Response{StatusCode: 200, Header: http.Header{"X-Httptape-Source": {"l2-cache"}}}, StateL2Cache, true},
		{"unknown header", &http.Response{StatusCode: 200, Header: http.Header{"X-Httptape-Source": {"weird"}}}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := classifyProbeResponse(tt.resp)
			if ok != tt.ok || got != tt.want {
				t.Errorf("got (%q, %v), want (%q, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestHealthMonitor_ErrorHandlerInvokedOnProbeError(t *testing.T) {
	rt := &recordingTransport{
		respFn: func(*http.Request) (*http.Response, error) {
			return nil, errors.New("boom")
		},
	}

	var captured atomic.Int32
	h := NewHealthMonitor("http://x", rt,
		WithHealthInterval(15*time.Millisecond),
		WithHealthErrorHandler(func(error) {
			captured.Add(1)
		}))
	defer h.Close() //nolint:errcheck

	h.start()
	deadline := time.After(2 * time.Second)
	for captured.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("error handler not called")
		default:
			time.Sleep(15 * time.Millisecond)
		}
	}
}

func TestHealthMonitor_PanicInOnErrorRecovered(t *testing.T) {
	rt := &recordingTransport{
		respFn: func(*http.Request) (*http.Response, error) {
			return nil, errors.New("boom")
		},
	}

	var calls atomic.Int32
	h := NewHealthMonitor("http://x", rt,
		WithHealthInterval(15*time.Millisecond),
		WithHealthErrorHandler(func(error) {
			calls.Add(1)
			panic("intentional")
		}))
	defer h.Close() //nolint:errcheck

	h.start()

	deadline := time.After(2 * time.Second)
	for calls.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("probe loop stopped after panic in onError (calls=%d)", calls.Load())
		default:
			time.Sleep(15 * time.Millisecond)
		}
	}
}

func TestHealthMonitor_ProbeIntervalReportedInSnapshot(t *testing.T) {
	h := NewHealthMonitor("http://x", healthNoopTransport{},
		WithHealthInterval(750*time.Millisecond))
	if got := h.snapshot().ProbeIntervalMS; got != 750 {
		t.Errorf("ProbeIntervalMS=%d, want 750", got)
	}
}

func TestWithHealthInterval_NegativeClampedToZero(t *testing.T) {
	h := NewHealthMonitor("http://x", healthNoopTransport{},
		WithHealthInterval(-time.Second))
	if got := h.snapshot().ProbeIntervalMS; got != 0 {
		t.Errorf("ProbeIntervalMS=%d, want 0", got)
	}
}

func TestWithHealthErrorHandler_NilIgnored(t *testing.T) {
	// Should not panic, should not crash the constructor.
	h := NewHealthMonitor("http://x", healthNoopTransport{},
		WithHealthErrorHandler(nil))
	if h.onError != nil {
		t.Errorf("nil error handler should not be set")
	}
}

func TestWithHealthClock_NilIgnored(t *testing.T) {
	h := NewHealthMonitor("http://x", healthNoopTransport{},
		WithHealthClock(nil))
	if h.now == nil {
		t.Error("nil clock should not unset the default")
	}
}

func TestWithHealthProbePath_EmptyIgnored(t *testing.T) {
	h := NewHealthMonitor("http://x", healthNoopTransport{},
		WithHealthProbePath(""))
	if h.probePath != defaultProbePath {
		t.Errorf("probePath=%q, want %q", h.probePath, defaultProbePath)
	}
}

// TestHealthMonitor_SubscribeObserveCloseStress races subscribe/unsubscribe
// against concurrent observe and Close. The single-owner-of-close discipline
// (only dropSubscriber closes channels, both producers and dropSubscriber hold
// h.mu) must prevent send-on-closed-channel panics under -race.
func TestHealthMonitor_SubscribeObserveCloseStress(t *testing.T) {
	const (
		subscribers = 32
		observers   = 8
		duration    = 50 * time.Millisecond
	)

	h := NewHealthMonitor("http://x", healthNoopTransport{})

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Observers: continuously toggle state, broadcasting to subscribers.
	for i := 0; i < observers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			states := []SourceState{StateLive, StateL1Cache, StateL2Cache}
			j := id
			for {
				select {
				case <-stop:
					return
				default:
				}
				h.observe(states[j%len(states)])
				j++
			}
		}(i)
	}

	// Subscribers: continuously subscribe, drain a few events, unsubscribe.
	for i := 0; i < subscribers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				ch, unsub := h.subscribe()
				// Drain whatever is buffered without blocking.
			drain:
				for k := 0; k < 4; k++ {
					select {
					case _, ok := <-ch:
						if !ok {
							break drain
						}
					default:
						break drain
					}
				}
				unsub()
			}
		}()
	}

	// Let the workload race for a bit, then close mid-flight.
	time.Sleep(duration)
	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Stop the workers and wait for them to exit. They should observe
	// post-Close state cleanly: subscribe returns a closed channel + no-op
	// unsub, observe still runs harmlessly (transitions broadcast to an empty
	// subscriber set), and no goroutine should panic.
	close(stop)
	wg.Wait()
}

// TestHealthMonitor_SubscribeAfterCloseReturnsClosedChannel asserts that
// subscribe() called after Close() does not block and returns a closed
// channel + no-op unsub. This is the single-owner-of-close contract.
func TestHealthMonitor_SubscribeAfterCloseReturnsClosedChannel(t *testing.T) {
	h := NewHealthMonitor("http://x", healthNoopTransport{})
	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ch, unsub := h.subscribe()
	if unsub == nil {
		t.Fatal("subscribe returned nil unsub after Close")
	}
	// Calling the no-op unsub must be safe.
	unsub()

	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected closed channel from subscribe after Close")
		}
	case <-time.After(time.Second):
		t.Fatal("subscribe channel was not closed after Close")
	}
}

// fakeClock is a controllable clock for tests.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// --- Coverage gap tests (issue #219) ---

func TestHealthMonitor_ReportError_NilError(t *testing.T) {
	var called bool
	h := NewHealthMonitor("http://x", healthNoopTransport{},
		WithHealthErrorHandler(func(err error) {
			called = true
		}))
	defer h.Close() //nolint:errcheck

	// Calling reportError with nil should be a no-op.
	h.reportError(nil)
	if called {
		t.Error("reportError(nil) should not invoke the error handler")
	}
}

func TestHealthMonitor_ReportError_NoHandler(t *testing.T) {
	// HealthMonitor without error handler: reportError should not panic.
	h := NewHealthMonitor("http://x", healthNoopTransport{})
	defer h.Close() //nolint:errcheck

	h.reportError(errors.New("test error"))
	// No panic means success.
}

func TestHealthMonitor_RunProbeOnce_PanicRecovery(t *testing.T) {
	// Transport that panics on RoundTrip. The panic-recovery in runProbeOnce
	// should catch it and report it via the error handler.
	var capturedErr atomic.Value
	rt := &recordingTransport{
		respFn: func(*http.Request) (*http.Response, error) {
			panic("probe-panic")
		},
	}

	h := NewHealthMonitor("http://x", rt,
		WithHealthInterval(15*time.Millisecond),
		WithHealthErrorHandler(func(err error) {
			capturedErr.Store(err)
		}))
	defer h.Close() //nolint:errcheck

	h.start()

	deadline := time.After(2 * time.Second)
	for {
		if v := capturedErr.Load(); v != nil {
			errStr := v.(error).Error()
			if !strings.Contains(errStr, "panic in probe loop") {
				t.Errorf("error = %q, want 'panic in probe loop'", errStr)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("panic was not caught by runProbeOnce")
		default:
			time.Sleep(15 * time.Millisecond)
		}
	}
}

func TestHealthMonitor_RunProbeOnce_TransportNilResponse(t *testing.T) {
	// Transport that returns (nil, nil) — an unusual but valid edge case.
	// runProbeOnce should handle this gracefully (the nil response guard at L440).
	rt := &recordingTransport{
		respFn: func(*http.Request) (*http.Response, error) {
			return nil, nil
		},
	}

	h := NewHealthMonitor("http://x", rt,
		WithHealthInterval(15*time.Millisecond))
	defer h.Close() //nolint:errcheck

	h.start()

	// Let a few probe cycles run without crashing.
	time.Sleep(60 * time.Millisecond)
	if rt.callCount() == 0 {
		t.Fatal("probe never fired")
	}
}

func TestHealthMonitor_RunProbeOnce_ResponseWithBodyOnError(t *testing.T) {
	// Transport returns (resp with body, error) — the rare partial-read scenario.
	// runProbeOnce should close the body to avoid leaking connections.
	var bodyClosed atomic.Int32
	rt := &recordingTransport{
		respFn: func(*http.Request) (*http.Response, error) {
			body := &trackingCloser{closed: &bodyClosed}
			resp := &http.Response{
				StatusCode: 200,
				Body:       body,
				Header:     http.Header{},
			}
			return resp, errors.New("partial read error")
		},
	}

	h := NewHealthMonitor("http://x", rt,
		WithHealthInterval(15*time.Millisecond))
	defer h.Close() //nolint:errcheck

	h.start()

	deadline := time.After(2 * time.Second)
	for bodyClosed.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("body was never closed on transport error with response")
		default:
			time.Sleep(15 * time.Millisecond)
		}
	}
}

// trackingCloser is an io.ReadCloser that tracks Close calls.
type trackingCloser struct {
	closed *atomic.Int32
}

func (tc *trackingCloser) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (tc *trackingCloser) Close() error {
	tc.closed.Add(1)
	return nil
}

func TestHealthMonitor_RunProbeOnce_5xxNoTransition(t *testing.T) {
	// Transport returns 500 without X-Httptape-Source header.
	// classifyProbeResponse returns (_, false), so state stays unchanged.
	rt := &recordingTransport{
		respFn: func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 500, Body: http.NoBody, Header: http.Header{}}, nil
		},
	}

	h := NewHealthMonitor("http://x", rt,
		WithHealthInterval(15*time.Millisecond))
	defer h.Close() //nolint:errcheck

	h.start()

	time.Sleep(60 * time.Millisecond)

	// State should remain live (no transition from 5xx without header).
	if got := h.snapshot().State; got != StateLive {
		t.Errorf("State=%q, want %q (5xx should not cause transition)", got, StateLive)
	}
}

func TestHealthMonitor_RunProbeOnce_501PromotesToGET(t *testing.T) {
	// Test that 501 (Not Implemented) also triggers HEAD-to-GET promotion,
	// in addition to 405 (tested elsewhere).
	var seenGet atomic.Int32
	rt := &recordingTransport{
		respFn: func(req *http.Request) (*http.Response, error) {
			if req.Method == http.MethodHead {
				return &http.Response{StatusCode: 501, Body: http.NoBody, Header: http.Header{}}, nil
			}
			seenGet.Add(1)
			return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}}, nil
		},
	}

	h := NewHealthMonitor("http://x", rt,
		WithHealthInterval(15*time.Millisecond))
	defer h.Close() //nolint:errcheck

	h.start()

	deadline := time.After(2 * time.Second)
	for seenGet.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("never promoted to GET on 501")
		default:
			time.Sleep(15 * time.Millisecond)
		}
	}
}

func TestHealthMonitor_ServeStream_MonitorCloseMidStream(t *testing.T) {
	// Test that closing the monitor while a client is connected to the SSE
	// stream causes the handler to return cleanly (the h.done branch in
	// serveStream's select).
	h := NewHealthMonitor("http://x", healthNoopTransport{})

	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+healthStreamPath, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	br := bufio.NewReader(resp.Body)
	if _, err := readSSEEvent(br, 2*time.Second); err != nil {
		t.Fatalf("read initial event: %v", err)
	}

	// Close the monitor while the stream is active. This should trigger the
	// <-h.done branch in serveStream's select loop.
	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Read should terminate (EOF or connection closed).
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, resp.Body)
		done <- err
	}()
	select {
	case <-done:
		// clean termination
	case <-time.After(2 * time.Second):
		t.Error("stream did not close after monitor shutdown")
	}
}

func TestHealthMonitor_ServeStream_SubscriberDroppedByOverflow(t *testing.T) {
	// When a subscriber's buffer overflows, broadcastLocked drops it by
	// closing the channel. The serveStream handler should see ok=false on
	// the next receive and return cleanly.
	h := NewHealthMonitor("http://x", healthNoopTransport{})
	defer h.Close() //nolint:errcheck

	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+healthStreamPath, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	br := bufio.NewReader(resp.Body)
	// Read initial event to drain it.
	if _, err := readSSEEvent(br, 2*time.Second); err != nil {
		t.Fatalf("read initial: %v", err)
	}

	// Now flood transitions to overflow the subscriber's buffer (size 8).
	// The subscriber is NOT being drained, so it will overflow.
	for i := 0; i < 20; i++ {
		if i%2 == 0 {
			h.observe(StateL1Cache)
		} else {
			h.observe(StateLive)
		}
	}

	// The stream should terminate (channel closed by overflow drop).
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, resp.Body)
		done <- err
	}()
	select {
	case <-done:
		// clean termination
	case <-time.After(2 * time.Second):
		t.Error("stream did not close after subscriber overflow")
	}
}

func TestHealthMonitor_StartWithZeroInterval(t *testing.T) {
	// When interval is 0, start() enters startOnce.Do but returns early
	// because interval <= 0. No goroutine should be spawned.
	baseline := runtime.NumGoroutine()
	h := NewHealthMonitor("http://x", healthNoopTransport{})
	defer h.Close() //nolint:errcheck

	h.start()
	h.start() // idempotent

	// Goroutine count should not increase.
	time.Sleep(50 * time.Millisecond)
	waitForGoroutineCount(t, baseline+2, time.Second) // +2 for test overhead
}

func TestHealthMonitor_RunProbeOnce_InvalidUpstreamURL(t *testing.T) {
	// Use an upstream URL that causes http.NewRequestWithContext to fail.
	// This exercises the request-build error path (L422-425).
	var capturedErr atomic.Value
	h := NewHealthMonitor("http://x", healthNoopTransport{},
		WithHealthInterval(15*time.Millisecond),
		WithHealthErrorHandler(func(err error) {
			capturedErr.Store(err)
		}))
	defer h.Close() //nolint:errcheck

	// Override the upstreamURL to something that makes NewRequest fail.
	// The method needs to be invalid. Promote probeMethod to something invalid.
	h.promoteProbeMethod("BAD\nMETHOD")

	h.start()

	deadline := time.After(2 * time.Second)
	for {
		if v := capturedErr.Load(); v != nil {
			errStr := v.(error).Error()
			if !strings.Contains(errStr, "probe build request") {
				t.Errorf("error = %q, want 'probe build request'", errStr)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("NewRequest error was not reported")
		default:
			time.Sleep(15 * time.Millisecond)
		}
	}
}

func TestHealthMonitor_ServeStream_NonFlusher(t *testing.T) {
	// Test that serveStream returns 500 when the ResponseWriter does not
	// implement http.Flusher.
	h := NewHealthMonitor("http://x", healthNoopTransport{})
	defer h.Close() //nolint:errcheck

	w := &nonFlusherResponseWriter{
		header: http.Header{},
	}
	r := httptest.NewRequest("GET", healthStreamPath, nil)

	h.ServeHTTP(w, r)

	if w.statusCode != http.StatusInternalServerError {
		t.Errorf("status=%d, want 500", w.statusCode)
	}
}

// nonFlusherResponseWriter implements http.ResponseWriter but NOT http.Flusher.
type nonFlusherResponseWriter struct {
	header     http.Header
	statusCode int
	body       []byte
}

func (w *nonFlusherResponseWriter) Header() http.Header {
	return w.header
}

func (w *nonFlusherResponseWriter) Write(b []byte) (int, error) {
	w.body = append(w.body, b...)
	return len(b), nil
}

func (w *nonFlusherResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
}
