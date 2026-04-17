package httptape

import (
	"bytes"
	"context"
	"crypto/tls"
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
// On success:
//   - Raw (unsanitized) tape saved to L1 (MemoryStore)
//   - Sanitized tape saved to L2 (FileStore)
//   - Real response returned to caller
//
// On failure:
//   - Match from L1 (raw, best UX within session)
//   - Match from L2 (sanitized, persistent)
//   - If neither matches, return original error
//
// Proxy is safe for concurrent use by multiple goroutines.
type Proxy struct {
	transport  http.RoundTripper                         // real backend transport
	l1         Store                                     // raw/ephemeral (typically *MemoryStore)
	l2         Store                                     // sanitized/persistent (typically *FileStore)
	sanitizer  Sanitizer                                 // applied to L2 writes only
	matcher    Matcher                                   // for fallback lookups
	route      string                                    // logical route label
	onError    func(error)                               // error callback
	isFallback func(err error, resp *http.Response) bool // determines when to fall back

	// sseReplayTiming controls SSE replay timing for L2 fallback.
	sseReplayTiming SSETimingMode

	// health is the optional HealthMonitor enabled via WithProxyHealthEndpoint.
	// nil when the option is absent — every call site is nil-receiver-safe so
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
// the first time Proxy.Start is called — never at construction time, so
// embedders that build a Proxy without ever serving HTTP do not leak
// goroutines.
//
// With this option absent, Proxy.HealthHandler() returns nil and the request
// path takes a no-op branch when recording state — preserving byte-for-byte
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
// Defaults:
//   - transport: http.DefaultTransport
//   - sanitizer: NewPipeline() (no-op)
//   - matcher: DefaultMatcher()
//   - isFallback: transport errors only (not 5xx)
//   - route: ""
//
// Both l1 and l2 must be non-nil. Panics on nil stores (constructor guard
// convention per CLAUDE.md).
func NewProxy(l1, l2 Store, opts ...ProxyOption) *Proxy {
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

	if p.healthEnabled {
		// Default the upstream URL to "" when the embedder didn't provide one.
		// HealthMonitor's constructor guard panics on empty upstream — give a
		// clearer message here pointing at the right option.
		if p.upstreamURLHint == "" {
			panic("httptape: WithProxyHealthEndpoint requires WithProxyUpstreamURL")
		}

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

	return p
}

// HealthHandler returns the http.Handler that serves /__httptape/health and
// /__httptape/health/stream. Returns nil when WithProxyHealthEndpoint was not
// set — callers should mount the handler conditionally.
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
// Close does NOT close the L1/L2 stores — they are owned by the caller.
func (p *Proxy) Close() error {
	if p.health == nil {
		return nil
	}
	return p.health.Close()
}

// RoundTrip executes the HTTP request via the inner transport. On success,
// the raw response is saved to L1 and a sanitized copy is saved to L2, then
// the real response is returned. On failure (as determined by the isFallback
// function), cached tapes are consulted: L1 first, then L2. If neither cache
// has a match, the original transport error is returned.
//
// The response body is fully read into memory and replaced with a new
// io.ReadCloser (same pattern as Recorder.RoundTrip).
func (p *Proxy) RoundTrip(req *http.Request) (*http.Response, error) {
	// 1. Capture request body before forwarding (needed for tape + fallback matching).
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

	// 2. Forward to real backend.
	resp, transportErr := p.transport.RoundTrip(req)

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

	// 4. SSE detection on success path.
	if isSSEContentType(resp.Header.Get("Content-Type")) {
		return p.roundTripSSE(req, resp, reqBody)
	}

	// 5. Success path: capture response body (non-SSE).
	var respBody []byte
	if resp.Body != nil {
		var err error
		respBody, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
		if err != nil {
			p.onErrorSafe(fmt.Errorf("httptape: proxy read response body: %w", err))
			return resp, nil
		}
	}

	// 6. Detect body encodings.
	reqBodyEncoding := detectBodyEncoding(req.Header.Get("Content-Type"))
	respBodyEncoding := detectBodyEncoding(resp.Header.Get("Content-Type"))

	// 7. Build raw tape.
	recordedReq := RecordedReq{
		Method:       req.Method,
		URL:          req.URL.String(),
		Headers:      req.Header.Clone(),
		Body:         reqBody,
		BodyHash:     BodyHashFromBytes(reqBody),
		BodyEncoding: reqBodyEncoding,
	}
	recordedResp := RecordedResp{
		StatusCode:   resp.StatusCode,
		Headers:      resp.Header.Clone(),
		Body:         respBody,
		BodyEncoding: respBodyEncoding,
	}

	rawTape := NewTape(p.route, recordedReq, recordedResp)

	// 8. Save raw to L1 (synchronous, in-memory, fast).
	if saveErr := p.l1.Save(req.Context(), rawTape); saveErr != nil {
		p.onErrorSafe(saveErr)
	}

	// 9. Sanitize and save to L2 (synchronous).
	sanitizedTape := p.sanitizer.Sanitize(rawTape)
	if saveErr := p.l2.Save(req.Context(), sanitizedTape); saveErr != nil {
		p.onErrorSafe(saveErr)
	}

	// 10. Update health state (no-op when health surface disabled).
	p.health.observe(StateLive)

	// 11. Return real response (with body restored).
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

// roundTripSSE handles SSE responses in the proxy success path. The body
// is wrapped in an sseRecordingReader that delivers bytes to the caller
// unchanged while a background goroutine parses SSE events. When the
// stream completes, the tape is saved to L1 and L2.
func (p *Proxy) roundTripSSE(req *http.Request, resp *http.Response, reqBody []byte) (*http.Response, error) {
	startTime := time.Now()
	respHeaders := resp.Header.Clone()

	reqBodyEncoding := detectBodyEncoding(req.Header.Get("Content-Type"))

	recordedReq := RecordedReq{
		Method:       req.Method,
		URL:          req.URL.String(),
		Headers:      req.Header.Clone(),
		Body:         reqBody,
		BodyHash:     BodyHashFromBytes(reqBody),
		BodyEncoding: reqBodyEncoding,
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
			p.onErrorSafe(fmt.Errorf("httptape: proxy SSE stream truncated: %w", parseErr))
		}

		recordedResp := RecordedResp{
			StatusCode: resp.StatusCode,
			Headers:    respHeaders,
			Body:       nil,
			SSEEvents:  collectedEvents,
			Truncated:  truncated,
		}

		rawTape := NewTape(p.route, recordedReq, recordedResp)

		// Save raw to L1.
		if saveErr := p.l1.Save(context.Background(), rawTape); saveErr != nil {
			p.onErrorSafe(saveErr)
		}

		// Sanitize and save to L2.
		sanitizedTape := p.sanitizer.Sanitize(rawTape)
		if saveErr := p.l2.Save(context.Background(), sanitizedTape); saveErr != nil {
			p.onErrorSafe(saveErr)
		}

		// Update health state.
		p.health.observe(StateLive)
	}

	wrapper := newSSERecordingReader(resp.Body, startTime, onEvent, onDone)
	resp.Body = wrapper

	return resp, nil
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
	// Remove stale Content-Length — the body may differ from the original
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
