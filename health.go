package httptape

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// SourceState identifies which tier produced the most recent serve through
// the proxy. Values mirror the X-Httptape-Source response header semantics
// established in ADR-26.
type SourceState string

const (
	// StateLive indicates that the last serve came from the real upstream.
	// On the per-request response, this corresponds to the absence of an
	// X-Httptape-Source header.
	StateLive SourceState = "live"
	// StateL1Cache indicates that the last serve came from the L1 (in-memory)
	// fallback cache. Mirrors X-Httptape-Source: l1-cache.
	StateL1Cache SourceState = "l1-cache"
	// StateL2Cache indicates that the last serve came from the L2 (on-disk)
	// fallback cache. Mirrors X-Httptape-Source: l2-cache.
	StateL2Cache SourceState = "l2-cache"
)

// HealthSnapshot is the JSON payload returned by GET /__httptape/health and
// emitted on every SSE event on /__httptape/health/stream. The snapshot
// endpoint and the SSE stream agree byte-for-byte at any instant — both
// surfaces read the same protected state.
type HealthSnapshot struct {
	// State is the current "most-recently-served source".
	State SourceState `json:"state"`
	// UpstreamURL is the configured upstream URL the proxy targets.
	UpstreamURL string `json:"upstream_url"`
	// LastProbedAt is the timestamp of the most recent active probe attempt
	// (RFC 3339). Nil/omitted when probing is disabled or no probe has run yet.
	LastProbedAt *time.Time `json:"last_probed_at,omitempty"`
	// ProbeIntervalMS is the configured probe cadence in milliseconds.
	// Zero when probing is disabled.
	ProbeIntervalMS int64 `json:"probe_interval_ms"`
	// Since is when the proxy last transitioned into the current state
	// (RFC 3339).
	Since time.Time `json:"since"`
}

// HealthMonitor owns the proxy's "most-recently-served source" state, the SSE
// subscriber set, and the active probe loop. It is created by the Proxy when
// WithProxyHealthEndpoint is enabled. With the option absent, the Proxy holds
// a nil *HealthMonitor and the request path takes a fast no-op branch — this
// preserves byte-for-byte default behavior.
//
// HealthMonitor is safe for concurrent use.
type HealthMonitor struct {
	upstreamURL string
	interval    time.Duration // 0 = no probe loop
	probePath   string
	transport   http.RoundTripper
	onError     func(error)
	now         func() time.Time

	probeMethodMu sync.Mutex
	probeMethod   string // dynamically promoted from HEAD to GET on 405/501

	// state machine + subscriber set
	mu          sync.Mutex
	state       SourceState
	since       time.Time
	lastProbed  *time.Time
	subscribers map[*healthSubscriber]struct{}

	// lifecycle
	startOnce sync.Once
	closeOnce sync.Once
	done      chan struct{}
	wg        sync.WaitGroup
}

// HealthMonitorOption configures a HealthMonitor.
type HealthMonitorOption func(*HealthMonitor)

// healthSubscriber is one connected SSE client. The buffer is bounded; if a
// send would block (buffer full) the broadcast routine drops the subscriber,
// closes the buffer, and the handler goroutine exits writing EOF to the
// underlying connection.
type healthSubscriber struct {
	ch chan HealthSnapshot
}

const (
	sseBufferSize        = 8 // per-subscriber bounded buffer
	defaultProbeInterval = 2 * time.Second
	defaultProbePath     = "/"
	defaultProbeMethod   = http.MethodHead
	healthEndpointPath   = "/__httptape/health"
	healthStreamPath     = "/__httptape/health/stream"
	sseRetryBackoffMS    = 2000
	probeHeaderName      = "X-Httptape-Probe"
)

// WithHealthClock injects a clock for tests. Defaults to time.Now.
func WithHealthClock(now func() time.Time) HealthMonitorOption {
	return func(h *HealthMonitor) {
		if now != nil {
			h.now = now
		}
	}
}

// WithHealthInterval sets the active probe cadence on the HealthMonitor
// directly. Library users typically configure this through
// WithProxyProbeInterval; this option exists so callers constructing a
// HealthMonitor by hand (tests, advanced embedding) can set the interval.
func WithHealthInterval(d time.Duration) HealthMonitorOption {
	return func(h *HealthMonitor) {
		if d < 0 {
			d = 0
		}
		h.interval = d
	}
}

// WithHealthProbePath sets the URL path the active probe targets on the
// upstream (default "/"). Library-only knob.
func WithHealthProbePath(path string) HealthMonitorOption {
	return func(h *HealthMonitor) {
		if path != "" {
			h.probePath = path
		}
	}
}

// WithHealthErrorHandler sets a callback for non-fatal errors inside the
// health surface (probe transport errors that are not state transitions,
// SSE write errors, etc.).
func WithHealthErrorHandler(fn func(error)) HealthMonitorOption {
	return func(h *HealthMonitor) {
		if fn != nil {
			h.onError = fn
		}
	}
}

// NewHealthMonitor constructs a HealthMonitor. transport is the
// http.RoundTripper the active probe will hit — in production this is the
// Proxy itself, so the probe takes the same resolution path as real client
// traffic. upstreamURL is reported in the snapshot. opts may inject a clock,
// configure the probe interval, or provide an error callback.
//
// upstreamURL must be non-empty and transport must be non-nil. Both are
// programming errors and panic per the constructor-guard convention.
func NewHealthMonitor(upstreamURL string, transport http.RoundTripper, opts ...HealthMonitorOption) *HealthMonitor {
	if upstreamURL == "" {
		panic("httptape: NewHealthMonitor requires a non-empty upstream URL")
	}
	if transport == nil {
		panic("httptape: NewHealthMonitor requires a non-nil transport")
	}

	h := &HealthMonitor{
		upstreamURL: upstreamURL,
		transport:   transport,
		probePath:   defaultProbePath,
		probeMethod: defaultProbeMethod,
		now:         time.Now,
		subscribers: make(map[*healthSubscriber]struct{}),
		done:        make(chan struct{}),
	}

	for _, opt := range opts {
		opt(h)
	}

	h.state = StateLive
	h.since = h.now().UTC()

	return h
}

// observe is called by the Proxy on every served request (excluding probe
// requests) with the source the response was served from. It updates the
// state machine and broadcasts to SSE subscribers on transitions.
//
// observe is a no-op when called on a nil receiver — this is the fast path
// for proxies built without WithProxyHealthEndpoint.
func (h *HealthMonitor) observe(src SourceState) {
	if h == nil {
		return
	}
	h.applyObservation(src, false)
}

// observeProbe is the variant called from the probe loop. In addition to the
// state machine update + broadcast it always bumps lastProbed.
func (h *HealthMonitor) observeProbe(src SourceState) {
	if h == nil {
		return
	}
	h.applyObservation(src, true)
}

// recordProbeAttempt updates lastProbed without altering the state machine.
// Called by the probe loop on every tick regardless of outcome.
func (h *HealthMonitor) recordProbeAttempt() {
	if h == nil {
		return
	}
	now := h.now().UTC()
	h.mu.Lock()
	h.lastProbed = &now
	h.mu.Unlock()
}

// applyObservation runs the observe algorithm: detect transition, update
// state under the lock, then broadcast to subscribers while still holding the
// lock. Holding h.mu during the send is what protects against the
// send-on-closed-channel race: dropSubscriber (the single owner of close) also
// requires h.mu, so a subscriber's channel cannot be closed mid-send. The
// per-subscriber buffer is bounded; select/default keeps the broadcast
// non-blocking and overflowed subscribers are dropped in-place under the same
// lock.
func (h *HealthMonitor) applyObservation(src SourceState, fromProbe bool) {
	if !validSourceState(src) {
		return
	}

	now := h.now().UTC()

	h.mu.Lock()
	defer h.mu.Unlock()

	transitioned := src != h.state
	if transitioned {
		h.state = src
		h.since = now
	}
	if fromProbe {
		probedAt := now
		h.lastProbed = &probedAt
	}

	if !transitioned {
		return
	}

	snap := h.snapshotLocked()
	h.broadcastLocked(snap)
}

// broadcastLocked sends snap to every subscriber non-blockingly. Subscribers
// whose buffer is full are dropped (channel closed, removed from the set)
// under the same lock. Caller MUST hold h.mu.
func (h *HealthMonitor) broadcastLocked(snap HealthSnapshot) {
	var toDrop []*healthSubscriber
	for sub := range h.subscribers {
		select {
		case sub.ch <- snap:
		default:
			// Bounded buffer overflow: schedule a drop after we finish iterating
			// (don't mutate the map while ranging over it).
			toDrop = append(toDrop, sub)
		}
	}
	for _, sub := range toDrop {
		delete(h.subscribers, sub)
		close(sub.ch)
	}
}

// snapshot returns the current state as a HealthSnapshot value. Safe to call
// concurrently with observe and broadcast.
func (h *HealthMonitor) snapshot() HealthSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.snapshotLocked()
}

// snapshotLocked builds the snapshot value. Caller must hold h.mu.
func (h *HealthMonitor) snapshotLocked() HealthSnapshot {
	snap := HealthSnapshot{
		State:           h.state,
		UpstreamURL:     h.upstreamURL,
		ProbeIntervalMS: h.interval.Milliseconds(),
		Since:           h.since,
	}
	if h.lastProbed != nil {
		t := *h.lastProbed
		snap.LastProbedAt = &t
	}
	return snap
}

// subscribe registers a new SSE subscriber and returns its receive channel
// plus an unsubscribe func. The channel is closed when the subscriber is
// dropped (either by overflow or by Close, which goes through dropSubscriber).
//
// The initial seed is delivered while holding h.mu. The channel is fresh with
// capacity sseBufferSize so the send is guaranteed not to block; holding the
// lock makes the registration + seed atomic with respect to broadcast and
// dropSubscriber, eliminating any send-on-closed-channel window.
func (h *HealthMonitor) subscribe() (<-chan HealthSnapshot, func()) {
	sub := &healthSubscriber{ch: make(chan HealthSnapshot, sseBufferSize)}

	h.mu.Lock()
	// Reject post-Close subscribes deterministically: return a closed channel
	// and a no-op unsub so callers don't block forever.
	select {
	case <-h.done:
		h.mu.Unlock()
		close(sub.ch)
		return sub.ch, func() {}
	default:
	}
	snap := h.snapshotLocked()
	h.subscribers[sub] = struct{}{}
	// Initial seed: send under the lock so dropSubscriber cannot race us. The
	// buffer is fresh, so this cannot block; select/default is defensive.
	select {
	case sub.ch <- snap:
	default:
	}
	h.mu.Unlock()

	unsub := func() {
		h.dropSubscriber(sub)
	}
	return sub.ch, unsub
}

// dropSubscriber removes sub from the set and closes its channel exactly once.
func (h *HealthMonitor) dropSubscriber(sub *healthSubscriber) {
	h.mu.Lock()
	if _, ok := h.subscribers[sub]; !ok {
		h.mu.Unlock()
		return
	}
	delete(h.subscribers, sub)
	close(sub.ch)
	h.mu.Unlock()
}

// start launches the probe loop exactly once if an interval is configured.
// Idempotent: subsequent calls are no-ops.
func (h *HealthMonitor) start() {
	if h == nil {
		return
	}
	h.startOnce.Do(func() {
		if h.interval <= 0 {
			return
		}
		h.wg.Add(1)
		go h.runProbe()
	})
}

// runProbe is the active probe loop. Started exactly once by start().
// Exits when h.done is closed.
func (h *HealthMonitor) runProbe() {
	defer h.wg.Done()

	if h.interval <= 0 {
		return
	}

	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()

	// Probe context cancels when the monitor is closed so any in-flight
	// RoundTrip is aborted promptly. The watcher goroutine is tracked in wg
	// so Close() waits for it to exit before returning.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		select {
		case <-h.done:
			cancel()
		case <-ctx.Done():
		}
	}()

	for {
		select {
		case <-h.done:
			return
		case <-ticker.C:
			// Re-check h.done non-blockingly: when both <-h.done and <-ticker.C
			// are ready, Go's select picks at random. Without this guard a
			// probe could fire after Close() has already been observed, which
			// breaks the "no probes after Close" contract.
			select {
			case <-h.done:
				return
			default:
				h.runProbeOnce(ctx)
			}
		}
	}
}

// runProbeOnce performs a single probe attempt. The result feeds the same
// state machine real client traffic does (via observeProbe). recordProbeAttempt
// is always called so the snapshot reports "did the probe at least run".
func (h *HealthMonitor) runProbeOnce(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			h.reportError(fmt.Errorf("httptape: panic in probe loop: %v", r))
		}
	}()
	defer h.recordProbeAttempt()

	method := h.currentProbeMethod()
	target := h.upstreamURL + h.probePath

	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		h.reportError(fmt.Errorf("httptape: probe build request: %w", err))
		return
	}
	req.Header.Set(probeHeaderName, "1")

	resp, err := h.transport.RoundTrip(req)
	if err != nil {
		// http.RoundTripper allows (resp != nil, err != nil) — e.g. partial
		// body reads. We always close the body if present to avoid leaking
		// connections, surface the error, and return: the response cannot be
		// trusted to drive the state machine in this case.
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		h.reportError(fmt.Errorf("httptape: probe transport: %w", err))
		return
	}
	if resp == nil {
		return
	}
	if resp.Body != nil {
		// Drain and close to release the connection. We don't need the bytes.
		defer resp.Body.Close()
	}

	// HEAD-to-GET sticky promotion if the upstream rejects HEAD.
	if method == http.MethodHead && (resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotImplemented) {
		h.promoteProbeMethod(http.MethodGet)
		return
	}

	// Decode the source from the X-Httptape-Source header. If absent and the
	// status is non-5xx, the upstream answered live.
	src, ok := classifyProbeResponse(resp)
	if !ok {
		return
	}
	h.observeProbe(src)
}

// classifyProbeResponse maps a probe response to a SourceState. Returns
// (state, true) if the response carries a meaningful tier signal; (zero,
// false) otherwise (e.g. 5xx with no header, in which case state stays as-is).
func classifyProbeResponse(resp *http.Response) (SourceState, bool) {
	if resp == nil {
		return "", false
	}
	if v := resp.Header.Get("X-Httptape-Source"); v != "" {
		switch SourceState(v) {
		case StateL1Cache:
			return StateL1Cache, true
		case StateL2Cache:
			return StateL2Cache, true
		}
		// Unknown header value: ignore.
		return "", false
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 500 {
		return StateLive, true
	}
	return "", false
}

func (h *HealthMonitor) currentProbeMethod() string {
	h.probeMethodMu.Lock()
	defer h.probeMethodMu.Unlock()
	return h.probeMethod
}

func (h *HealthMonitor) promoteProbeMethod(m string) {
	h.probeMethodMu.Lock()
	h.probeMethod = m
	h.probeMethodMu.Unlock()
}

// ServeHTTP routes /__httptape/health and /__httptape/health/stream; returns
// 404 for any other path. HealthMonitor implements http.Handler so it can be
// mounted directly.
func (h *HealthMonitor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case healthEndpointPath:
		h.serveSnapshot(w, r)
	case healthStreamPath:
		h.serveStream(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *HealthMonitor) serveSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snap := h.snapshot()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	if err := json.NewEncoder(w).Encode(snap); err != nil {
		h.reportError(fmt.Errorf("httptape: snapshot encode: %w", err))
	}
}

func (h *HealthMonitor) serveStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "httptape: streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Suggest a fast reconnect cadence to EventSource clients.
	fmt.Fprintf(w, "retry: %d\n\n", sseRetryBackoffMS)
	flusher.Flush()

	ch, unsub := h.subscribe()
	defer unsub()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.done:
			return
		case snap, ok := <-ch:
			if !ok {
				return
			}
			payload, err := json.Marshal(snap)
			if err != nil {
				// Cannot happen for these field types; report and exit.
				h.reportError(fmt.Errorf("httptape: stream encode: %w", err))
				return
			}
			if _, werr := fmt.Fprintf(w, "data: %s\n\n", payload); werr != nil {
				h.reportError(fmt.Errorf("httptape: stream write: %w", werr))
				return
			}
			flusher.Flush()
		}
	}
}

// Close drains the probe loop, closes all subscriber channels, and is
// idempotent. Returns nil; the signature returns an error to leave room for
// future failure modes without an API break.
func (h *HealthMonitor) Close() error {
	if h == nil {
		return nil
	}
	h.closeOnce.Do(func() {
		close(h.done)
		h.wg.Wait()

		h.mu.Lock()
		subs := make([]*healthSubscriber, 0, len(h.subscribers))
		for sub := range h.subscribers {
			subs = append(subs, sub)
		}
		for _, sub := range subs {
			delete(h.subscribers, sub)
			close(sub.ch)
		}
		h.mu.Unlock()
	})
	return nil
}

func (h *HealthMonitor) reportError(err error) {
	if h == nil || err == nil || h.onError == nil {
		return
	}
	defer func() {
		// User-supplied callback must not crash the probe loop.
		_ = recover()
	}()
	h.onError(err)
}

func validSourceState(s SourceState) bool {
	switch s {
	case StateLive, StateL1Cache, StateL2Cache:
		return true
	}
	return false
}
