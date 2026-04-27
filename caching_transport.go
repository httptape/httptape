package httptape

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// CachingTransport is an http.RoundTripper that consults a tape Store on
// each request. On cache hit, it returns the recorded response without
// contacting upstream. On cache miss, it forwards to upstream, records the
// response (after sanitization), and returns it.
//
// CachingTransport is NOT an RFC 7234 HTTP cache. It does not honor
// Cache-Control, Vary, or any other HTTP caching headers. It is a
// tape-match layer: identical requests (as defined by the Matcher) get
// identical recorded responses.
//
// CachingTransport is safe for concurrent use by multiple goroutines.
type CachingTransport struct {
	upstream    http.RoundTripper
	store       Store
	matcher     Matcher
	sanitizer   Sanitizer
	route       string
	onError     func(error)
	cacheFilter func(*http.Response) bool

	singleFlight   bool
	lookupDisabled bool
	maxBodySize    int
	sseRecording   bool

	// response timing
	replayTiming ResponseTimingMode
	nowFunc      func() time.Time
	sleepFunc    func(context.Context, time.Duration) error

	// stale-fallback (#164)
	staleFallback   bool
	upstreamTimeout time.Duration

	// single-flight coordination (stdlib-only)
	sfMu    sync.Mutex
	sfCalls map[string]*sfCall
}

// sfCall tracks an in-flight upstream request for single-flight dedup.
type sfCall struct {
	wg   sync.WaitGroup
	resp *http.Response // set before wg.Done
	err  error          // set before wg.Done
	body []byte         // buffered response body for sharing
	sse  bool           // true when the leader's response was SSE
}

// CachingOption configures a CachingTransport.
type CachingOption func(*CachingTransport)

// WithCacheMatcher sets the Matcher used to identify equivalent requests.
// Default: NewCompositeMatcher(MethodCriterion{}, PathCriterion{}, BodyHashCriterion{}).
func WithCacheMatcher(m Matcher) CachingOption {
	return func(ct *CachingTransport) {
		ct.matcher = m
	}
}

// WithCacheSanitizer sets the sanitization pipeline applied to recorded
// tapes before store persistence. Default: NewPipeline() (no-op).
func WithCacheSanitizer(s Sanitizer) CachingOption {
	return func(ct *CachingTransport) {
		if s == nil {
			ct.sanitizer = NewPipeline()
			return
		}
		ct.sanitizer = s
	}
}

// WithCacheFilter sets a predicate controlling which upstream responses
// are cached. Only responses for which fn returns true are persisted.
// Default: cache 2xx responses only.
func WithCacheFilter(fn func(*http.Response) bool) CachingOption {
	return func(ct *CachingTransport) {
		ct.cacheFilter = fn
	}
}

// WithCacheSingleFlight controls single-flight deduplication of concurrent
// cache misses. When true (default), concurrent requests with the same match
// key share a single upstream call.
//
// Known limitation: waiters do not observe their request's context cancellation
// until the leader's upstream call completes. This is a consequence of using
// sync.WaitGroup (not context-aware) for waiter coordination in v0.13. For
// callers whose request contexts carry strict deadlines, either disable
// single-flight (WithCacheSingleFlight(false)) or set an aggressive
// WithCacheUpstreamTimeout to bound the leader's wait.
//
// Disable only if you want "accidentally record multiple variants of the same
// request" behavior, which is rare.
func WithCacheSingleFlight(enabled bool) CachingOption {
	return func(ct *CachingTransport) {
		ct.singleFlight = enabled
	}
}

// WithCacheMaxBodySize sets the maximum request body size in bytes for
// cache participation. Requests whose body exceeds this limit bypass the
// cache entirely (forwarded to upstream, response not recorded).
// Default: 10 MiB (10 * 1024 * 1024).
// A value of 0 means no limit.
func WithCacheMaxBodySize(n int) CachingOption {
	return func(ct *CachingTransport) {
		if n < 0 {
			n = 0
		}
		ct.maxBodySize = n
	}
}

// WithCacheRoute sets the route label applied to all tapes created by
// this transport.
func WithCacheRoute(route string) CachingOption {
	return func(ct *CachingTransport) {
		ct.route = route
	}
}

// WithCacheOnError sets a callback invoked when a non-fatal error occurs
// (store failure, body read failure on the record path). Defaults to no-op.
func WithCacheOnError(fn func(error)) CachingOption {
	return func(ct *CachingTransport) {
		ct.onError = fn
	}
}

// WithCacheSSERecording controls whether SSE stream recording is enabled.
// When true (default), SSE responses on the miss path are tee'd to the
// caller and accumulated for tape persistence.
func WithCacheSSERecording(enabled bool) CachingOption {
	return func(ct *CachingTransport) {
		ct.sseRecording = enabled
	}
}

// WithCacheLookupDisabled disables the cache hit path entirely.
// Every request is treated as a miss: forwarded to upstream, recorded
// via the sanitization pipeline (if configured), and returned.
// Single-flight dedup, SSE tee, and sanitization remain active.
//
// The configured Matcher is still used by stale fallback
// (WithCacheUpstreamDownFallback), so the two options compose:
// disable the hit path but still serve stale tapes when upstream is down.
//
// Useful when the embedder owns its own hit-path logic (e.g., Proxy
// uses an L1 store consulted before this transport runs) and wants
// CachingTransport's other cross-cutting concerns without the cache
// lookup it would otherwise perform.
func WithCacheLookupDisabled() CachingOption {
	return func(ct *CachingTransport) {
		ct.lookupDisabled = true
	}
}

// WithCacheUpstreamDownFallback enables stale-response fallback when
// upstream is unreachable or returns a transport error on a cache miss.
// When enabled, CachingTransport searches the store for the best-matching
// tape (using the configured Matcher) and returns it with an
// X-Httptape-Stale: true header. When disabled (default), transport errors
// are propagated to the caller.
//
// This is useful for demo hosting (upstream flakiness should not break the
// demo) but wrong for integration tests (which should see the real failure).
func WithCacheUpstreamDownFallback(enabled bool) CachingOption {
	return func(ct *CachingTransport) {
		ct.staleFallback = enabled
	}
}

// WithCacheUpstreamTimeout sets a timeout for upstream requests on cache
// miss. When set, the request context is wrapped with a deadline before
// forwarding. Default: 0 (no timeout; the caller's http.Client timeout
// dominates).
func WithCacheUpstreamTimeout(d time.Duration) CachingOption {
	return func(ct *CachingTransport) {
		ct.upstreamTimeout = d
	}
}

// WithCacheReplayTiming sets the response timing mode for cache-hit
// responses. Defaults to ResponseTimingInstant() (no delay, preserving
// pre-feature behavior).
//
// Timing composition: WithCacheReplayTiming composes ADDITIVELY with any
// delays the caller adds after receiving the response. Pre-feature
// fixtures (ElapsedMS == 0) incur no replay timing delay regardless
// of mode. The delay is applied in tapeToResponse before returning the
// *http.Response, so the caller of RoundTrip perceives the delay.
func WithCacheReplayTiming(mode ResponseTimingMode) CachingOption {
	return func(ct *CachingTransport) {
		ct.replayTiming = mode
	}
}

// withCacheNowFunc overrides the clock for elapsed-time measurement.
// Unexported -- only used in tests.
func withCacheNowFunc(fn func() time.Time) CachingOption {
	return func(ct *CachingTransport) { ct.nowFunc = fn }
}

// withCacheSleepFunc overrides the sleep function for testing.
// Unexported -- only used in tests.
func withCacheSleepFunc(fn func(context.Context, time.Duration) error) CachingOption {
	return func(ct *CachingTransport) { ct.sleepFunc = fn }
}

// defaultCacheMaxBodySize is the default maximum request body size for cache
// participation: 10 MiB.
const defaultCacheMaxBodySize = 10 * 1024 * 1024

// NewCachingTransport creates a new CachingTransport wrapping the given
// upstream transport with store-backed caching.
//
// On RoundTrip, it consults the store for a matching tape. On hit, it
// returns the recorded response. On miss, it forwards to upstream, records
// the response (after sanitization), and returns it.
//
// upstream and store must not be nil. Panics on nil (constructor guard per
// CLAUDE.md).
//
// Default configuration:
//   - matcher: method + path + body_hash
//   - sanitizer: no-op Pipeline
//   - cache filter: 2xx only
//   - single-flight: enabled
//   - max body size: 10 MiB
//   - SSE recording: enabled
//   - stale fallback: disabled
//   - upstream timeout: 0 (no timeout)
//   - route: ""
//   - onError: no-op
func NewCachingTransport(upstream http.RoundTripper, store Store, opts ...CachingOption) *CachingTransport {
	if upstream == nil {
		panic("httptape: NewCachingTransport requires a non-nil upstream RoundTripper")
	}
	if store == nil {
		panic("httptape: NewCachingTransport requires a non-nil Store")
	}

	ct := &CachingTransport{
		upstream:     upstream,
		store:        store,
		matcher:      NewCompositeMatcher(MethodCriterion{}, PathCriterion{}, BodyHashCriterion{}),
		sanitizer:    NewPipeline(),
		singleFlight: true,
		maxBodySize:  defaultCacheMaxBodySize,
		sseRecording: true,
		replayTiming: ResponseTimingInstant(),
		nowFunc:      time.Now,
		sleepFunc:    defaultSleepFunc,
		cacheFilter: func(resp *http.Response) bool {
			return resp.StatusCode >= 200 && resp.StatusCode < 300
		},
		sfCalls: make(map[string]*sfCall),
	}

	for _, opt := range opts {
		opt(ct)
	}

	return ct
}

// RoundTrip implements http.RoundTripper. It consults the cache for a
// matching tape, returning a cached response on hit. On miss, it forwards
// to upstream, optionally records the response, and returns it.
func (ct *CachingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// 1. Read request body into buffer (respecting maxBodySize).
	reqBody, oversize, err := ct.bufferRequestBody(req)
	if err != nil {
		return nil, fmt.Errorf("httptape: caching transport read request body: %w", err)
	}

	// If body exceeds maxBodySize: pass through to upstream directly.
	if oversize {
		return ct.upstream.RoundTrip(req)
	}

	// 2. Compute body hash from buffer.
	bodyHash := BodyHashFromBytes(reqBody)

	// 3. Replace req.Body with fresh reader.
	if reqBody != nil {
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
		bodyBytes := reqBody
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(bodyBytes)), nil
		}
	}

	// 4. Cache lookup (skipped when lookupDisabled is true).
	if !ct.lookupDisabled {
		tapes, listErr := ct.store.List(req.Context(), Filter{Route: ct.route})
		if listErr != nil {
			// Store failure on lookup: proceed as miss.
			ct.onErrorSafe(fmt.Errorf("httptape: caching transport store list: %w", listErr))
			tapes = nil
		}

		// Restore body for matcher consumption.
		if reqBody != nil {
			req.Body = io.NopCloser(bytes.NewReader(reqBody))
		}

		if tapes != nil {
			tape, ok := ct.matcher.Match(req, tapes)
			if ok {
				// 5. HIT: synthesize response from tape.
				if tape.Response.IsSSE() {
					return ct.sseResponseFromTape(tape), nil
				}
				return ct.tapeToResponse(req.Context(), tape), nil
			}
		}
	}

	// Restore body before upstream call.
	if reqBody != nil {
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
	}

	// 6. MISS: forward to upstream.
	sfKey := req.Method + "|" + req.URL.Path + "|" + bodyHash

	if ct.singleFlight {
		return ct.roundTripWithSingleFlight(req, reqBody, bodyHash, sfKey)
	}

	return ct.roundTripUpstream(req, reqBody, bodyHash)
}

// roundTripWithSingleFlight coordinates single-flight dedup for non-SSE
// cache misses. SSE responses are handled differently: the first caller
// gets the live tee'd stream and concurrent callers wait for completion,
// then get the cached tape.
func (ct *CachingTransport) roundTripWithSingleFlight(req *http.Request, reqBody []byte, bodyHash, sfKey string) (*http.Response, error) {
	ct.sfMu.Lock()
	if call, ok := ct.sfCalls[sfKey]; ok {
		ct.sfMu.Unlock()
		call.wg.Wait()
		if call.err != nil {
			// The leader failed. Try stale fallback for this waiter too.
			if ct.staleFallback {
				if reqBody != nil {
					req.Body = io.NopCloser(bytes.NewReader(reqBody))
				}
				resp, fbErr := ct.serveStaleFallback(req)
				if fbErr != nil {
					ct.onErrorSafe(fbErr)
				}
				if resp != nil {
					return resp, nil
				}
			}
			return nil, call.err
		}

		// For SSE responses, the leader's body is a streaming reader that
		// cannot be cloned. Re-query the store for the persisted tape
		// instead (the leader's SSE tee writes the tape on stream completion).
		if call.sse {
			return ct.reQueryStoreForSSE(req, reqBody)
		}

		// Clone the response for each waiter (non-SSE path).
		return cloneResponse(call.resp, call.body), nil
	}

	call := &sfCall{}
	call.wg.Add(1)
	ct.sfCalls[sfKey] = call
	ct.sfMu.Unlock()

	resp, err := ct.roundTripUpstream(req, reqBody, bodyHash)

	// For non-SSE responses, share the result with waiters.
	if err != nil {
		call.err = err
		call.wg.Done()
		ct.sfMu.Lock()
		delete(ct.sfCalls, sfKey)
		ct.sfMu.Unlock()
		return nil, err
	}

	// If SSE, the response body is a streaming reader. We cannot share it
	// via single-flight cloning. Complete the single-flight immediately;
	// waiters that arrive later will find the tape in the store (once the
	// stream completes).
	if isSSEContentType(resp.Header.Get("Content-Type")) {
		call.resp = resp
		call.err = nil
		call.sse = true
		// For SSE, we cannot buffer the body. Mark the call done so waiters
		// fall through and re-query the store after the stream completes.
		call.wg.Done()
		ct.sfMu.Lock()
		delete(ct.sfCalls, sfKey)
		ct.sfMu.Unlock()
		return resp, nil
	}

	// For non-SSE: read the body to share with waiters.
	respBody, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		ct.onErrorSafe(fmt.Errorf("httptape: caching transport read response body for single-flight: %w", readErr))
		// Return what we have.
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
		call.resp = resp
		call.body = respBody
		call.wg.Done()
		ct.sfMu.Lock()
		delete(ct.sfCalls, sfKey)
		ct.sfMu.Unlock()
		return resp, nil
	}

	// Restore body for the leader caller.
	resp.Body = io.NopCloser(bytes.NewReader(respBody))
	call.resp = resp
	call.body = respBody
	call.wg.Done()

	ct.sfMu.Lock()
	delete(ct.sfCalls, sfKey)
	ct.sfMu.Unlock()

	return resp, nil
}

// roundTripUpstream handles the actual upstream call and optional recording.
func (ct *CachingTransport) roundTripUpstream(req *http.Request, reqBody []byte, bodyHash string) (*http.Response, error) {
	// Apply upstream timeout if configured.
	if ct.upstreamTimeout > 0 {
		ctx, cancel := context.WithTimeout(req.Context(), ct.upstreamTimeout)
		defer cancel()
		req = req.WithContext(ctx)
	}

	startTime := ct.nowFunc()
	resp, transportErr := ct.upstream.RoundTrip(req)
	if transportErr != nil {
		// Upstream error path.
		if ct.staleFallback {
			if reqBody != nil {
				req.Body = io.NopCloser(bytes.NewReader(reqBody))
			}
			staleResp, fbErr := ct.serveStaleFallback(req)
			if fbErr != nil {
				ct.onErrorSafe(fbErr)
			}
			if staleResp != nil {
				return staleResp, nil
			}
		}
		return nil, transportErr
	}

	// SSE detection on miss path.
	if ct.sseRecording && isSSEContentType(resp.Header.Get("Content-Type")) {
		return ct.roundTripSSE(req, resp, reqBody, startTime)
	}

	// Read response body into buffer.
	var respBody []byte
	if resp.Body != nil {
		var err error
		respBody, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
		if err != nil {
			ct.onErrorSafe(fmt.Errorf("httptape: caching transport read response body: %w", err))
			return resp, nil
		}
	}

	// Check cache filter.
	if !ct.cacheFilter(resp) {
		return resp, nil
	}

	// Build tape.
	recordedReq := RecordedReq{
		Method:   req.Method,
		URL:      req.URL.String(),
		Headers:  req.Header.Clone(),
		Body:     reqBody,
		BodyHash: bodyHash,
	}
	recordedResp := RecordedResp{
		StatusCode: resp.StatusCode,
		Headers:    resp.Header.Clone(),
		Body:       respBody,
		ElapsedMS:  elapsedMS(startTime, ct.nowFunc),
	}

	tape := NewTape(ct.route, recordedReq, recordedResp)

	// Apply sanitizer.
	tape = ct.sanitizer.Sanitize(tape)

	// Store.Save (synchronous). On failure, call onError; still return response.
	if saveErr := ct.store.Save(req.Context(), tape); saveErr != nil {
		ct.onErrorSafe(fmt.Errorf("httptape: caching transport store save: %w", saveErr))
	}

	return resp, nil
}

// roundTripSSE handles SSE responses on the miss path. The body is wrapped
// in an sseRecordingReader that delivers bytes to the caller unchanged
// while a background goroutine parses SSE events. When the stream
// completes cleanly, the tape is persisted. If the stream is truncated
// (client disconnect), the partial tape is discarded.
func (ct *CachingTransport) roundTripSSE(req *http.Request, resp *http.Response, reqBody []byte, startTime time.Time) (*http.Response, error) {
	nowFunc := ct.nowFunc // capture for closure
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

	// Track whether the upstream stream reached a natural EOF.
	// The eofTrackingReader sets this to true when the upstream
	// returns io.EOF, indicating the stream completed normally.
	var streamCompleted atomic.Bool

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

		// If the stream had a parse error, discard.
		if parseErr != nil {
			ct.onErrorSafe(fmt.Errorf("httptape: caching transport SSE stream truncated: %w", parseErr))
			return
		}

		// If the stream didn't reach natural EOF (client closed early),
		// discard the partial tape.
		if !streamCompleted.Load() {
			ct.onErrorSafe(fmt.Errorf("httptape: caching transport SSE stream closed before completion"))
			return
		}

		recordedResp := RecordedResp{
			StatusCode: resp.StatusCode,
			Headers:    respHeaders,
			Body:       nil,
			SSEEvents:  collectedEvents,
			ElapsedMS:  elapsedMS(startTime, nowFunc),
		}

		tape := NewTape(ct.route, recordedReq, recordedResp)
		tape = ct.sanitizer.Sanitize(tape)

		if saveErr := ct.store.Save(context.Background(), tape); saveErr != nil {
			ct.onErrorSafe(fmt.Errorf("httptape: caching transport SSE store save: %w", saveErr))
		}
	}

	// Wrap the upstream body with an EOF tracker before passing to the
	// SSE recording reader. When the upstream sends EOF, the tracker
	// sets streamCompleted to true.
	trackedBody := &eofTrackingReadCloser{
		inner:     resp.Body,
		completed: &streamCompleted,
	}

	wrapper := newSSERecordingReader(trackedBody, startTime, onEvent, onDone)
	resp.Body = wrapper

	return resp, nil
}

// eofTrackingReadCloser wraps an io.ReadCloser and tracks whether the
// underlying reader returned io.EOF, indicating the stream completed
// naturally (as opposed to being closed by the client).
type eofTrackingReadCloser struct {
	inner     io.ReadCloser
	completed *atomic.Bool
}

// Read implements io.Reader. When the inner reader returns io.EOF, the
// completed flag is set to true.
func (r *eofTrackingReadCloser) Read(p []byte) (int, error) {
	n, err := r.inner.Read(p)
	if err == io.EOF {
		r.completed.Store(true)
	}
	return n, err
}

// Close implements io.Closer.
func (r *eofTrackingReadCloser) Close() error {
	return r.inner.Close()
}

// bufferRequestBody reads the request body into a buffer, respecting
// maxBodySize. Returns the body bytes, whether the body exceeded the size
// limit, and any error. If oversize is true, the request body is restored
// with the partially-read bytes concatenated with the remaining stream.
func (ct *CachingTransport) bufferRequestBody(req *http.Request) (body []byte, oversize bool, err error) {
	if req.Body == nil {
		return nil, false, nil
	}

	if ct.maxBodySize == 0 {
		// No limit.
		body, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, false, err
		}
		req.Body.Close()
		return body, false, nil
	}

	// Read up to maxBodySize + 1 to detect oversize.
	limited := io.LimitReader(req.Body, int64(ct.maxBodySize)+1)
	body, err = io.ReadAll(limited)
	if err != nil {
		return nil, false, err
	}

	if len(body) > ct.maxBodySize {
		// Body exceeds limit. Restore req.Body with the bytes we read
		// plus the remaining unread portion.
		remaining := io.NopCloser(io.MultiReader(bytes.NewReader(body), req.Body))
		req.Body = remaining
		return nil, true, nil
	}

	// Body fits within limit.
	req.Body.Close()
	return body, false, nil
}

// serveStaleFallback searches the store for a matching tape and returns it
// with the X-Httptape-Stale: true header. Returns (nil, nil) if no match
// is found. Uses context.Background() for the store query because the
// original request context may have been cancelled by an upstream timeout.
func (ct *CachingTransport) serveStaleFallback(req *http.Request) (*http.Response, error) {
	tapes, err := ct.store.List(context.Background(), Filter{Route: ct.route})
	if err != nil {
		return nil, fmt.Errorf("httptape: stale fallback store list: %w", err)
	}

	tape, ok := ct.matcher.Match(req, tapes)
	if !ok {
		return nil, nil
	}

	var resp *http.Response
	if tape.Response.IsSSE() {
		resp = ct.sseResponseFromTape(tape)
	} else {
		resp = ct.tapeToResponse(context.Background(), tape)
	}
	resp.Header.Set("X-Httptape-Stale", "true")
	return resp, nil
}

// tapeToResponse synthesizes an *http.Response from a non-SSE Tape.
// The replay timing delay (opt-in via WithCacheReplayTiming) is applied
// before returning, so the caller of RoundTrip perceives the delay.
// This composes ADDITIVELY with any caller-side delays. Pre-feature
// fixtures (ElapsedMS == 0) incur no delay regardless of mode.
func (ct *CachingTransport) tapeToResponse(ctx context.Context, tape Tape) *http.Response {
	// Apply replay timing delay before building the response.
	if replayDelay := ct.replayTiming.responseDelay(tape.Response.ElapsedMS); replayDelay > 0 {
		_ = ct.sleepFunc(ctx, replayDelay) //nolint:errcheck // context cancellation is non-fatal here
	}

	header := make(http.Header)
	if tape.Response.Headers != nil {
		header = tape.Response.Headers.Clone()
	}
	header.Del("Content-Length")

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
// for an SSE tape. Events are emitted back-to-back with no timing delay
// (instant replay for the RoundTripper path).
func (ct *CachingTransport) sseResponseFromTape(tape Tape) *http.Response {
	header := make(http.Header)
	if tape.Response.Headers != nil {
		header = tape.Response.Headers.Clone()
	}
	header.Set("Content-Type", "text/event-stream")
	header.Del("Content-Length")

	pr, pw := io.Pipe()
	go func() {
		for _, ev := range tape.Response.SSEEvents {
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

// reQueryStoreForSSE is the SSE single-flight waiter path. After the leader's
// stream completes and is persisted, waiters re-query the store for the
// matching tape and serve it. If the tape is not found (e.g., the leader's
// store write failed or the stream was truncated), the waiter falls through
// to a direct upstream call as a last resort.
func (ct *CachingTransport) reQueryStoreForSSE(req *http.Request, reqBody []byte) (*http.Response, error) {
	if reqBody != nil {
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
	}

	tapes, err := ct.store.List(req.Context(), Filter{Route: ct.route})
	if err != nil {
		ct.onErrorSafe(fmt.Errorf("httptape: SSE single-flight waiter store list: %w", err))
		// Fall through to direct upstream call.
		if reqBody != nil {
			req.Body = io.NopCloser(bytes.NewReader(reqBody))
		}
		bodyHash := BodyHashFromBytes(reqBody)
		return ct.roundTripUpstream(req, reqBody, bodyHash)
	}

	if reqBody != nil {
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
	}

	tape, ok := ct.matcher.Match(req, tapes)
	if ok && tape.Response.IsSSE() {
		return ct.sseResponseFromTape(tape), nil
	}
	if ok {
		return ct.tapeToResponse(req.Context(), tape), nil
	}

	// Tape not found -- leader's store write may have failed.
	// Fall through to a direct upstream call.
	ct.onErrorSafe(fmt.Errorf("httptape: SSE single-flight waiter: no matching tape found after leader completed, falling back to direct upstream call"))
	if reqBody != nil {
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
	}
	bodyHash := BodyHashFromBytes(reqBody)
	return ct.roundTripUpstream(req, reqBody, bodyHash)
}

// doSingleFlight coordinates dedup for concurrent cache misses. This is
// only used internally by roundTripWithSingleFlight for the response
// sharing logic.
// Note: the actual single-flight coordination is inlined in
// roundTripWithSingleFlight for better control over SSE vs non-SSE paths.

// onErrorSafe calls the error callback if it is set.
func (ct *CachingTransport) onErrorSafe(err error) {
	if ct.onError != nil {
		ct.onError(err)
	}
}

// cloneResponse creates an independent copy of an http.Response with a
// fresh body reader over the given bytes. This is used by single-flight
// to give each waiter their own consumable response.
func cloneResponse(resp *http.Response, body []byte) *http.Response {
	if resp == nil {
		return nil
	}
	clone := *resp
	clone.Header = resp.Header.Clone()
	clone.Body = io.NopCloser(bytes.NewReader(copyBytes(body)))
	return &clone
}
