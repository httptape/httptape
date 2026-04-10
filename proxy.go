package httptape

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
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
	transport  http.RoundTripper                   // real backend transport
	l1         Store                               // raw/ephemeral (typically *MemoryStore)
	l2         Store                               // sanitized/persistent (typically *FileStore)
	sanitizer  Sanitizer                           // applied to L2 writes only
	matcher    Matcher                             // for fallback lookups
	route      string                              // logical route label
	onError    func(error)                         // error callback
	isFallback func(err error, resp *http.Response) bool // determines when to fall back
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
			t.TLSClientConfig = cfg
			return
		}
		p.transport = &http.Transport{TLSClientConfig: cfg}
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
		transport: http.DefaultTransport,
		l1:        l1,
		l2:        l2,
		sanitizer: NewPipeline(),
		matcher:   DefaultMatcher(),
		isFallback: func(err error, _ *http.Response) bool {
			return err != nil
		},
	}

	for _, opt := range opts {
		opt(p)
	}

	return p
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

	// 4. Success path: capture response body.
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

	// 5. Detect body encodings.
	reqBodyEncoding := detectBodyEncoding(req.Header.Get("Content-Type"))
	respBodyEncoding := detectBodyEncoding(resp.Header.Get("Content-Type"))

	// 6. Build raw tape.
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

	// 7. Save raw to L1 (synchronous, in-memory, fast).
	if saveErr := p.l1.Save(req.Context(), rawTape); saveErr != nil {
		p.onErrorSafe(saveErr)
	}

	// 8. Sanitize and save to L2 (synchronous).
	sanitizedTape := p.sanitizer.Sanitize(rawTape)
	if saveErr := p.l2.Save(req.Context(), sanitizedTape); saveErr != nil {
		p.onErrorSafe(saveErr)
	}

	// 9. Return real response (with body restored).
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
// where the response came from ("l1-cache" or "l2-cache").
func (p *Proxy) tapeToResponse(tape Tape, source string) *http.Response {
	header := make(http.Header)
	if tape.Response.Headers != nil {
		header = tape.Response.Headers.Clone()
	}
	header.Set("X-Httptape-Source", source)

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

// onErrorSafe calls the error callback if it is set.
func (p *Proxy) onErrorSafe(err error) {
	if p.onError != nil {
		p.onError(err)
	}
}
