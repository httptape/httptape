package httptape

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// Proxy is an http.RoundTripper that forwards requests to a real backend,
// records successful responses to a two-tier cache (L1 in-memory, L2 on disk),
// and falls back to cached tapes on transport failure.
//
// Internally, Proxy composes a CachingTransport for upstream forwarding,
// sanitization, and L2 recording. Fallback logic remains a Proxy concern.
// An unexported l1RecordingTransport wraps the upstream to intercept
// responses on the miss path, saving raw (unsanitized) tapes to L1
// before CachingTransport sanitizes and saves to L2.
//
// L1 must be an ephemeral, in-memory store (typically *MemoryStore). Because
// L1 holds unsanitized data, using a persistent store for L1 would bypass
// the sanitize-on-write guarantee and persist raw secrets and PII to disk.
// This constraint is documented rather than enforced at runtime: a type check
// against *MemoryStore would not catch custom ephemeral Store implementations,
// and would false-positive on wrappers embedding *MemoryStore.
//
// On success:
//   - Raw (unsanitized) tape saved to L1 via l1RecordingTransport
//   - Sanitized tape saved to L2 via CachingTransport
//   - Real response returned to caller
//
// On failure:
//   - Match from L1 (raw, best UX within session)
//   - Match from L2 (sanitized, persistent)
//   - If neither matches, return original error
//
// Proxy is safe for concurrent use by multiple goroutines.
type Proxy struct {
	// ct is the composed CachingTransport that handles L2 cache lookup,
	// upstream forwarding, sanitization, and recording. Constructed in
	// NewProxy from the provided l2 store and relevant options.
	ct *CachingTransport

	// l1 is the raw/ephemeral store for unsanitized tapes. Proxy manages
	// L1 independently -- CachingTransport knows nothing about it.
	l1 Store

	// l2 is retained for direct fallback lookups (L2 fallback path).
	// CachingTransport also holds a reference to l2 as its store.
	l2 Store

	// matcher is used for L1 and L2 fallback lookups.
	matcher Matcher

	// isFallback determines when to enter the fallback path.
	// Proxy-specific: CachingTransport has staleFallback (bool) which
	// only covers transport errors. Proxy's isFallback also handles
	// 5xx-triggered fallback.
	isFallback func(err error, resp *http.Response) bool

	// route label for L1 tape creation.
	route string

	// onError callback for non-fatal errors.
	onError func(error)

	// sanitizer is captured during construction from WithProxySanitizer
	// and routed to CachingTransport via WithCacheSanitizer. Not used
	// after NewProxy returns.
	sanitizer Sanitizer

	// sseReplayTiming controls SSE replay timing for fallback responses.
	sseReplayTiming SSETimingMode

	// transport is retained solely for the health monitor's probe
	// (which calls transport.RoundTrip directly, not via CachingTransport).
	transport http.RoundTripper

	// health is the optional HealthMonitor enabled via WithProxyHealthEndpoint.
	// nil when the option is absent -- every call site is nil-receiver-safe so
	// the default behavior is byte-for-byte identical to a Proxy without the
	// health surface.
	health *HealthMonitor

	// healthEnabled tracks whether WithProxyHealthEndpoint was set so the
	// other health-related options can apply (or noop) deterministically.
	healthEnabled   bool
	healthOpts      []HealthMonitorOption
	probeInterval   time.Duration
	probePath       string
	healthErrorFunc func(error)
	upstreamURLHint string
}

// l1RecordingTransport wraps an http.RoundTripper to intercept upstream
// responses and save raw (unsanitized) tapes to the L1 store. This is
// used by Proxy to inject L1 recording into CachingTransport's miss path
// without CachingTransport knowing about L1.
//
// On cache miss, CachingTransport calls l1RecordingTransport.RoundTrip,
// which forwards to the real upstream, captures the raw response, saves
// it to L1, and returns the response unchanged. CachingTransport then
// sanitizes and saves to L2.
//
// For SSE responses, the body is wrapped in an sseRecordingReader that
// saves the raw SSE tape to L1 on stream completion. CachingTransport
// then wraps the returned body again with its own SSE tee recording
// reader for L2.
//
// Responses that would trigger Proxy's fallback (per isFallback) are
// not recorded to L1.
type l1RecordingTransport struct {
	inner      http.RoundTripper
	l1         Store
	route      string
	onError    func(error)
	health     *HealthMonitor
	isFallback func(err error, resp *http.Response) bool
}

// RoundTrip forwards the request to the real upstream and saves the raw
// response to L1. For SSE responses, the body is wrapped in an
// sseRecordingReader that saves the raw SSE tape to L1 on stream
// completion. CachingTransport then wraps the returned body again with
// its own SSE tee recording reader for L2. Returns the upstream response
// (possibly with a wrapped body for SSE).
func (t *l1RecordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.inner.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	// Check if the response would trigger fallback. If so, skip L1 save --
	// fallback responses should not pollute L1 (which would cause the
	// fallback matcher to find the error response instead of the good one).
	if t.isFallback(nil, resp) {
		return resp, nil
	}

	// Retrieve request body via GetBody (set by Proxy.RoundTrip).
	var reqBody []byte
	if req.GetBody != nil {
		body, gbErr := req.GetBody()
		if gbErr == nil {
			var readErr error
			reqBody, readErr = io.ReadAll(body)
			body.Close()
			if readErr != nil {
				t.onErrorSafe(fmt.Errorf("httptape: l1 recording read request body: %w", readErr))
			}
		}
	}

	// SSE responses: wrap body with an sseRecordingReader for L1 recording.
	// CachingTransport will wrap the returned body again for L2 recording.
	if isSSEContentType(resp.Header.Get("Content-Type")) {
		return t.roundTripSSE(req, resp, reqBody), nil
	}

	// Non-SSE: read response body, build raw tape, save to L1, restore body.
	var respBody []byte
	if resp.Body != nil {
		var readErr error
		respBody, readErr = io.ReadAll(resp.Body)
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
		if readErr != nil {
			t.onErrorSafe(fmt.Errorf("httptape: l1 recording read response body: %w", readErr))
			// Update health state even on partial read.
			t.health.observe(StateLive)
			return resp, nil
		}
	}

	recordedReq := RecordedReq{
		Method:   req.Method,
		URL:      req.URL.String(),
		Headers:  req.Header.Clone(),
		Body:     reqBody,
		BodyHash: BodyHashFromBytes(reqBody),
	}
	recordedResp := RecordedResp{
		StatusCode: resp.StatusCode,
		Headers:    resp.Header.Clone(),
		Body:       respBody,
	}

	rawTape := NewTape(t.route, recordedReq, recordedResp)

	if saveErr := t.l1.Save(req.Context(), rawTape); saveErr != nil {
		t.onErrorSafe(saveErr)
	}

	// Update health state for successful upstream call.
	t.health.observe(StateLive)

	return resp, nil
}

// roundTripSSE handles SSE responses in the L1 recording path. The body
// is wrapped in an sseRecordingReader that accumulates events and saves a
// raw tape to L1 when the stream completes.
func (t *l1RecordingTransport) roundTripSSE(req *http.Request, resp *http.Response, reqBody []byte) *http.Response {
	startTime := time.Now()
	respHeaders := resp.Header.Clone()

	recordedReq := RecordedReq{
		Method:   req.Method,
		URL:      req.URL.String(),
		Headers:  req.Header.Clone(),
		Body:     reqBody,
		BodyHash: BodyHashFromBytes(reqBody),
	}

	var mu sync.Mutex
	var events []SSEEvent

	onEvent := func(ev SSEEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}

	onDone := func(parseErr error) {
		mu.Lock()
		collectedEvents := make([]SSEEvent, len(events))
		copy(collectedEvents, events)
		mu.Unlock()

		truncated := parseErr != nil
		if truncated {
			t.onErrorSafe(fmt.Errorf("httptape: l1 SSE stream truncated: %w", parseErr))
		}

		recordedResp := RecordedResp{
			StatusCode: resp.StatusCode,
			Headers:    respHeaders,
			Body:       nil,
			SSEEvents:  collectedEvents,
			Truncated:  truncated,
		}

		rawTape := NewTape(t.route, recordedReq, recordedResp)

		// Save raw to L1.
		if saveErr := t.l1.Save(context.Background(), rawTape); saveErr != nil {
			t.onErrorSafe(saveErr)
		}

		// Update health state.
		t.health.observe(StateLive)
	}

	wrapper := newSSERecordingReader(resp.Body, startTime, onEvent, onDone)
	resp.Body = wrapper

	return resp
}

// onErrorSafe calls the error callback if it is set.
func (t *l1RecordingTransport) onErrorSafe(err error) {
	if t.onError != nil {
		t.onError(err)
	}
}

// ProxyOption configures a Proxy.
type ProxyOption func(*Proxy)

// WithProxyTransport sets the inner http.RoundTripper for real backend calls.
func WithProxyTransport(rt http.RoundTripper) ProxyOption {
	return func(p *Proxy) {
		p.transport = rt
	}
}

// WithProxySanitizer sets the Sanitizer applied to L2 writes.
// L1 writes are always raw (unsanitized).
func WithProxySanitizer(s Sanitizer) ProxyOption {
	return func(p *Proxy) {
		if s == nil {
			p.sanitizer = NewPipeline()
			return
		}
		p.sanitizer = s
	}
}

// WithProxyMatcher sets the Matcher used for fallback lookups.
func WithProxyMatcher(m Matcher) ProxyOption {
	return func(p *Proxy) {
		p.matcher = m
	}
}

// WithProxyRoute sets the route label for all tapes.
func WithProxyRoute(route string) ProxyOption {
	return func(p *Proxy) {
		p.route = route
	}
}

// WithProxyOnError sets the error callback.
func WithProxyOnError(fn func(error)) ProxyOption {
	return func(p *Proxy) {
		p.onError = fn
	}
}

// WithProxyFallbackOn sets a custom function that decides whether a given
// transport error and/or HTTP response should trigger fallback.
//
// The function receives:
//   - err: the error from transport.RoundTrip (nil if the call succeeded)
//   - resp: the HTTP response (nil if err is non-nil)
//
// It returns true if fallback should be attempted.
//
// Default: fall back only on transport errors (err != nil).
// Common alternative: also fall back on 5xx responses.
func WithProxyFallbackOn(fn func(err error, resp *http.Response) bool) ProxyOption {
	return func(p *Proxy) {
		p.isFallback = fn
	}
}

// WithProxySSETiming sets the SSE timing mode used when replaying cached
// SSE tapes from L2 fallback. Defaults to SSETimingInstant (emit all
// events immediately in degraded mode).
func WithProxySSETiming(mode SSETimingMode) ProxyOption {
	return func(p *Proxy) {
		p.sseReplayTiming = mode
	}
}

// WithProxyTLSConfig sets the TLS configuration for outbound connections.
// The provided config is applied to the inner http.Transport's TLSClientConfig.
// If the current transport is not an *http.Transport, a new *http.Transport is
// created with the TLS config set.
//
// If cfg is nil, this option is a no-op.
func WithProxyTLSConfig(cfg *tls.Config) ProxyOption {
	return func(p *Proxy) {
		if cfg == nil {
			return
		}
		if t, ok := p.transport.(*http.Transport); ok {
			if t == http.DefaultTransport.(*http.Transport) {
				p.transport = &http.Transport{TLSClientConfig: cfg}
				return
			}
			t.TLSClientConfig = cfg
			return
		}
		p.transport = &http.Transport{TLSClientConfig: cfg}
	}
}

// WithProxyHealthEndpoint enables the technical health surface on this Proxy.
// When set, Proxy.HealthHandler() returns a non-nil http.Handler that serves
// GET /__httptape/health (JSON snapshot) and GET /__httptape/health/stream
// (text/event-stream).
//
// The active probe loop (if configured via WithProxyProbeInterval) is started
// the first time Proxy.Start is called -- never at construction time, so
// embedders that build a Proxy without ever serving HTTP do not leak
// goroutines.
//
// With this option absent, Proxy.HealthHandler() returns nil and the request
// path takes a no-op branch when recording state -- preserving byte-for-byte
// default behavior.
//
// opts may inject a clock, an error handler, or other HealthMonitor knobs.
func WithProxyHealthEndpoint(opts ...HealthMonitorOption) ProxyOption {
	return func(p *Proxy) {
		p.healthEnabled = true
		p.healthOpts = append(p.healthOpts, opts...)
	}
}

// WithProxyProbeInterval sets the active probe cadence. Zero disables the
// probe loop (the request path still updates state, but no synthetic probe
// runs). When WithProxyHealthEndpoint is set and this option is absent, the
// default is 2s.
//
// This option is a no-op unless WithProxyHealthEndpoint is also set.
func WithProxyProbeInterval(d time.Duration) ProxyOption {
	return func(p *Proxy) {
		if d < 0 {
			d = 0
		}
		p.probeInterval = d
	}
}

// WithProxyProbePath sets the URL path the active probe targets on the
// upstream (default "/"). No-op unless WithProxyHealthEndpoint is also set.
func WithProxyProbePath(path string) ProxyOption {
	return func(p *Proxy) {
		if path != "" {
			p.probePath = path
		}
	}
}

// WithProxyHealthErrorHandler sets a callback for non-fatal errors inside the
// health surface (probe transport errors, SSE write errors). Defaults to the
// existing onError callback set by WithProxyOnError; if neither is set,
// errors are swallowed. No-op unless WithProxyHealthEndpoint is also set.
func WithProxyHealthErrorHandler(fn func(error)) ProxyOption {
	return func(p *Proxy) {
		p.healthErrorFunc = fn
	}
}

// WithProxyUpstreamURL sets the upstream URL reported in the health snapshot
// and used as the base URL for the active probe. The CLI passes this
// automatically; library users embedding Proxy with the health surface
// must set it explicitly.
//
// No-op unless WithProxyHealthEndpoint is also set.
func WithProxyUpstreamURL(url string) ProxyOption {
	return func(p *Proxy) {
		p.upstreamURLHint = url
	}
}

// NewProxy creates a new Proxy with the given L1 (ephemeral) and L2 (persistent)
// stores.
//
// Internally, NewProxy constructs a CachingTransport that uses l2 as its store
// and routes the configured sanitizer, matcher, route, and onError callback.
// The upstream transport is wrapped in an l1RecordingTransport that saves raw
// tapes to L1 on the miss path.
//
// Defaults:
//   - transport: http.DefaultTransport
//   - sanitizer: NewPipeline() (no-op)
//   - matcher: DefaultMatcher()
//   - isFallback: transport errors only (not 5xx)
//   - route: ""
//
// L1 must be an ephemeral store (typically NewMemoryStore()) because it holds
// unsanitized data. See the Proxy type documentation for details.
//
// Both l1 and l2 must be non-nil. Panics on nil stores (constructor guard
// convention per CLAUDE.md).
//
// Returns an error if any cross-option constraints are violated (e.g.,
// WithProxyHealthEndpoint without WithProxyUpstreamURL). All validation
// errors are accumulated and returned together.
func NewProxy(l1, l2 Store, opts ...ProxyOption) (*Proxy, error) {
	if l1 == nil {
		panic("httptape: NewProxy requires a non-nil L1 Store")
	}
	if l2 == nil {
		panic("httptape: NewProxy requires a non-nil L2 Store")
	}

	p := &Proxy{
		transport:       http.DefaultTransport,
		l1:              l1,
		l2:              l2,
		sanitizer:       NewPipeline(),
		matcher:         DefaultMatcher(),
		sseReplayTiming: SSETimingInstant(),
		isFallback: func(err error, _ *http.Response) bool {
			return err != nil
		},
	}

	for _, opt := range opts {
		opt(p)
	}

	// Validate after all options are applied.
	var errs []error
	if p.healthEnabled && p.upstreamURLHint == "" {
		errs = append(errs, fmt.Errorf("httptape: WithProxyHealthEndpoint requires WithProxyUpstreamURL"))
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	if p.healthEnabled {
		// Resolve the error handler precedence: explicit health handler beats
		// the proxy-wide onError, which beats nothing.
		var errFn func(error)
		switch {
		case p.healthErrorFunc != nil:
			errFn = p.healthErrorFunc
		case p.onError != nil:
			errFn = p.onError
		}

		// Default probe cadence: 2s when the option is unset.
		interval := p.probeInterval
		if interval == 0 {
			interval = defaultProbeInterval
		}

		hOpts := []HealthMonitorOption{
			WithHealthInterval(interval),
		}
		if p.probePath != "" {
			hOpts = append(hOpts, WithHealthProbePath(p.probePath))
		}
		if errFn != nil {
			hOpts = append(hOpts, WithHealthErrorHandler(errFn))
		}
		// Caller-supplied options come last so they can override the defaults.
		hOpts = append(hOpts, p.healthOpts...)

		p.health = NewHealthMonitor(p.upstreamURLHint, p, hOpts...)
	}

	// Construct the l1RecordingTransport that wraps the upstream transport.
	// This intercepts upstream responses on cache-miss and saves raw tapes
	// to L1 before CachingTransport sanitizes and saves to L2.
	l1RT := &l1RecordingTransport{
		inner:      p.transport,
		l1:         p.l1,
		route:      p.route,
		onError:    p.onError,
		health:     p.health,
		isFallback: p.isFallback,
	}

	// Construct CachingTransport with l1RecordingTransport as upstream.
	// CachingTransport handles: upstream forwarding (via l1RecordingTransport),
	// sanitization, L2 recording, SSE tee recording, single-flight dedup.
	//
	// WithCacheLookupDisabled ensures CachingTransport skips the cache hit
	// path entirely (no Store.List, no Matcher.Match). Proxy handles its own
	// L1/L2 lookup in the fallback path using the user-configured matcher.
	// This preserves the original Proxy semantics where the upstream is always
	// consulted first (caches are fallback-only).
	//
	// The cacheFilter prevents CachingTransport from recording responses that
	// would trigger Proxy's fallback. In the original Proxy, fallback-triggering
	// responses (e.g. 5xx when WithProxyFallbackOn is configured) were never
	// saved to L2.
	isFallback := p.isFallback
	cacheFilter := func(resp *http.Response) bool {
		return !isFallback(nil, resp)
	}

	p.ct = NewCachingTransport(l1RT, p.l2,
		WithCacheLookupDisabled(),
		WithCacheSanitizer(p.sanitizer),
		WithCacheRoute(p.route),
		WithCacheOnError(p.onError),
		WithCacheSSERecording(true),
		WithCacheFilter(cacheFilter),
	)

	return p, nil
}

// HealthHandler returns the http.Handler that serves /__httptape/health and
// /__httptape/health/stream. Returns nil when WithProxyHealthEndpoint was not
// set -- callers should mount the handler conditionally.
//
// The handler routes only the two health paths; any other path returns 404.
// Callers compose it into their own mux.
func (p *Proxy) HealthHandler() http.Handler {
	if p.health == nil {
		return nil
	}
	return p.health
}

// Start initializes background workers (currently: the active probe loop).
// Safe to call zero or more times; subsequent calls are no-ops. Must be
// called before serving HTTP if WithProxyHealthEndpoint and a non-zero probe
// interval are set, otherwise the probe loop never runs.
//
// The CLI wires Start() into the proxy command's startup. Library users
// embedding the Proxy choose when (and whether) to call it.
func (p *Proxy) Start() {
	p.health.start()
}

// Close stops background workers and closes all open SSE subscribers. Safe
// to call zero or more times; idempotent. Returns when all goroutines spawned
// by the Proxy have exited.
//
// Close does NOT close the L1/L2 stores -- they are owned by the caller.
func (p *Proxy) Close() error {
	if p.health == nil {
		return nil
	}
	return p.health.Close()
}

// RoundTrip implements http.RoundTripper. The request flow is:
//
//  1. Capture request body (needed for fallback matching).
//  2. Forward to CachingTransport.RoundTrip. CachingTransport has cache
//     lookup disabled (WithCacheLookupDisabled) so it always forwards to
//     upstream via l1RecordingTransport (saves raw to L1), then sanitizes
//     and saves to L2.
//  3. On success: check isFallback. If true (e.g., 5xx fallback), enter
//     fallback path. Otherwise return response.
//  4. On error: enter fallback path (L1 -> L2 -> original error).
func (p *Proxy) RoundTrip(req *http.Request) (*http.Response, error) {
	// 1. Capture request body before forwarding (needed for L1 matching + fallback).
	var reqBody []byte
	if req.Body != nil {
		var err error
		reqBody, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("httptape: proxy read request body: %w", err)
		}
		req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
		bodyBytes := reqBody
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(bodyBytes)), nil
		}
	}

	// 2. Forward to CachingTransport. CachingTransport has cache lookup
	// disabled so it always forwards to upstream via l1RecordingTransport
	// (which saves raw to L1). CachingTransport then sanitizes and saves
	// to L2.
	resp, transportErr := p.ct.RoundTrip(req)

	// 3. Decide: success or fallback?
	if p.isFallback(transportErr, resp) {
		// Drain and close the upstream body if we received a response (e.g. 5xx)
		// but are choosing to fall back instead of returning it. We keep the
		// bytes so the original response can be returned if no cache match exists.
		if resp != nil && resp.Body != nil {
			respBodyBytes, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr == nil {
				resp.Body = io.NopCloser(bytes.NewReader(respBodyBytes))
			} else {
				resp.Body = io.NopCloser(bytes.NewReader(nil))
			}
		}
		// Restore body for matcher consumption.
		if reqBody != nil {
			req.Body = io.NopCloser(bytes.NewReader(reqBody))
		}
		return p.fallback(req, resp, transportErr)
	}

	// 4. Success path: L1 save already happened in l1RecordingTransport
	// (if this was a miss). L2 save already happened in CachingTransport.
	// No additional action needed here.
	return resp, nil
}

// fallback attempts to find a matching cached tape, first from L1 (raw),
// then from L2 (sanitized). Returns the original error if no match is found.
// When triggered by a 5xx response (originalErr is nil) and no cache match
// exists, the original 5xx response is returned to satisfy the RoundTripper
// contract (which forbids returning nil, nil).
func (p *Proxy) fallback(req *http.Request, originalResp *http.Response, originalErr error) (*http.Response, error) {
	ctx := req.Context()

	// Try L1 first (raw, best UX).
	if tape, ok := p.matchFromStore(ctx, req, p.l1); ok {
		return p.tapeToResponse(tape, "l1-cache"), nil
	}

	// Restore body for second match attempt.
	if req.Body != nil {
		bodyBytes, err := io.ReadAll(req.Body)
		if err == nil {
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}
	}

	// Try L2 (sanitized, persistent).
	if tape, ok := p.matchFromStore(ctx, req, p.l2); ok {
		return p.tapeToResponse(tape, "l2-cache"), nil
	}

	// No match in either cache.
	// If triggered by a 5xx (not a transport error), return the original
	// response so we don't violate the RoundTripper contract (nil, nil).
	if originalErr == nil && originalResp != nil {
		return originalResp, nil
	}

	return nil, originalErr
}

// matchFromStore lists all tapes from the store and uses the matcher to find
// the best match for the given request.
func (p *Proxy) matchFromStore(ctx context.Context, req *http.Request, store Store) (Tape, bool) {
	tapes, err := store.List(ctx, Filter{})
	if err != nil {
		p.onErrorSafe(err)
		return Tape{}, false
	}
	return p.matcher.Match(req, tapes)
}

// tapeToResponse synthesizes an *http.Response from a cached Tape.
// The source parameter is set as the X-Httptape-Source header to indicate
// where the response came from ("l1-cache" or "l2-cache"). The same value
// drives the health-monitor state machine.
func (p *Proxy) tapeToResponse(tape Tape, source string) *http.Response {
	header := make(http.Header)
	if tape.Response.Headers != nil {
		header = tape.Response.Headers.Clone()
	}
	header.Set("X-Httptape-Source", source)
	// Remove stale Content-Length -- the body may differ from the original
	// due to sanitization. Let the HTTP stack set it from actual body size.
	header.Del("Content-Length")

	// Update the health state machine (no-op when the health surface is
	// disabled). A nil-receiver call here keeps the default-off path free.
	p.health.observe(SourceState(source))

	// SSE tape: build a streaming body via an io.Pipe.
	if tape.Response.IsSSE() {
		return p.sseResponseFromTape(tape, header)
	}

	body := tape.Response.Body
	if body == nil {
		body = []byte{}
	}

	return &http.Response{
		StatusCode: tape.Response.StatusCode,
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

// sseResponseFromTape synthesizes an *http.Response with a streaming body
// for an SSE tape. A goroutine writes events into a pipe using the
// configured sseReplayTiming mode.
func (p *Proxy) sseResponseFromTape(tape Tape, header http.Header) *http.Response {
	header.Set("Content-Type", "text/event-stream")

	pr, pw := io.Pipe()

	go func() {
		// Create a no-op flusher for pipe writes (flushing is meaningless
		// for a pipe -- the reader sees bytes immediately).
		for i, ev := range tape.Response.SSEEvents {
			d := p.sseReplayTiming.delay(tape.Response.SSEEvents, i)
			if d > 0 {
				time.Sleep(d)
			}
			if err := writeSSEEvent(pw, ev); err != nil {
				pw.CloseWithError(err)
				return
			}
		}
		pw.Close()
	}()

	return &http.Response{
		StatusCode: tape.Response.StatusCode,
		Header:     header,
		Body:       pr,
	}
}

// onErrorSafe calls the error callback if it is set.
func (p *Proxy) onErrorSafe(err error) {
	if p.onError != nil {
		p.onError(err)
	}
}
