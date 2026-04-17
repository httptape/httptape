package httptape

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Sanitizer transforms a Tape before it is persisted, redacting or faking
// sensitive data. The Sanitizer implementation is defined in a later milestone;
// this interface is declared here so the Recorder can accept it.
type Sanitizer interface {
	// Sanitize transforms the given tape in place and returns it.
	// Implementations must not return a nil Tape for a non-nil input.
	Sanitize(Tape) Tape
}

// Recorder is an http.RoundTripper that transparently records HTTP interactions
// to a Store. It delegates the actual HTTP call to an inner transport and captures
// the request/response pair as a Tape.
//
// By default, recording is asynchronous — tapes are sent to a buffered channel
// and a background goroutine drains them to the store. Call Close to flush
// pending recordings and release resources.
//
// Recorder is safe for concurrent use by multiple goroutines. RoundTrip may be
// called from multiple goroutines simultaneously. Close must be called exactly
// once when recording is complete.
type Recorder struct {
	transport     http.RoundTripper // inner transport to delegate to
	store         Store             // where to persist tapes
	route         string            // logical route label for all tapes produced
	sanitizer     Sanitizer         // always set; defaults to no-op Pipeline
	async         bool              // true = non-blocking writes via channel
	sampleRate    float64           // 0.0–1.0; 1.0 = record everything
	randFloat     func() float64    // returns [0.0, 1.0); injectable for testing
	bufSize       int               // channel buffer size (only used when async=true)
	onError       func(error)       // callback for async write errors; defaults to no-op
	maxBodySize   int               // max body size in bytes; 0 = no limit
	skipRedirects bool              // when true, skip recording 3xx responses
	sseRecording  bool              // true = detect and record SSE streams (default true)

	// async internals
	sendMu    sync.Mutex    // coordinates closed-check-then-send with close-channel
	closed    atomic.Bool   // set to true when Close is called; guards against send-on-closed-channel
	tapeCh    chan Tape     // buffered channel for async mode
	done      chan struct{} // closed when background goroutine exits
	closeOnce sync.Once     // ensures Close is idempotent
}

// RecorderOption configures a Recorder.
type RecorderOption func(*Recorder)

// WithTransport sets the inner http.RoundTripper. Defaults to http.DefaultTransport.
func WithTransport(rt http.RoundTripper) RecorderOption {
	return func(r *Recorder) {
		r.transport = rt
	}
}

// WithRoute sets the route label applied to all tapes created by this recorder.
func WithRoute(route string) RecorderOption {
	return func(r *Recorder) {
		r.route = route
	}
}

// WithSanitizer sets a Sanitizer to transform tapes before persistence.
// If s is nil, the sanitizer is set to a no-op Pipeline (NewPipeline()).
func WithSanitizer(s Sanitizer) RecorderOption {
	return func(r *Recorder) {
		if s == nil {
			r.sanitizer = NewPipeline()
			return
		}
		r.sanitizer = s
	}
}

// WithSampling sets the probabilistic sampling rate. Must be in [0.0, 1.0].
// 1.0 means record every request (default). 0.0 means record nothing.
// Values outside [0.0, 1.0] are clamped.
func WithSampling(rate float64) RecorderOption {
	return func(r *Recorder) {
		if rate < 0.0 {
			rate = 0.0
		}
		if rate > 1.0 {
			rate = 1.0
		}
		r.sampleRate = rate
	}
}

// WithAsync controls whether recording is asynchronous (default: true).
// When true, tapes are sent to a buffered channel and written by a background
// goroutine. When false, tapes are written synchronously inside RoundTrip.
func WithAsync(enabled bool) RecorderOption {
	return func(r *Recorder) {
		r.async = enabled
	}
}

// WithBufferSize sets the channel buffer size for async mode. Defaults to 1024.
// Ignored when async is disabled. Must be >= 1; values < 1 are set to 1.
func WithBufferSize(size int) RecorderOption {
	return func(r *Recorder) {
		if size < 1 {
			size = 1
		}
		r.bufSize = size
	}
}

// WithOnError sets a callback invoked when an async store write fails.
// The callback is called from the background goroutine, so it must be
// safe for concurrent use. Defaults to a no-op (errors are discarded).
func WithOnError(fn func(error)) RecorderOption {
	return func(r *Recorder) {
		r.onError = fn
	}
}

// WithMaxBodySize sets the maximum body size in bytes for both request and
// response bodies. Bodies exceeding this size are truncated and the
// Truncated flag is set on the recorded request/response.
// A value of 0 means no limit (default).
// Negative values are treated as 0 (no limit).
func WithMaxBodySize(n int) RecorderOption {
	return func(r *Recorder) {
		if n < 0 {
			n = 0
		}
		r.maxBodySize = n
	}
}

// WithSSERecording controls whether SSE detection and stream-aware
// recording is enabled. When true (default), responses with Content-Type
// text/event-stream are parsed into discrete SSEEvent entries with timing
// metadata. When false, SSE responses are buffered as regular bodies.
func WithSSERecording(enabled bool) RecorderOption {
	return func(r *Recorder) {
		r.sseRecording = enabled
	}
}

// WithSkipRedirects controls whether intermediate redirect responses (3xx)
// are skipped during recording. When true, 3xx responses are not recorded --
// only the final non-redirect response is stored. When false (default),
// all responses are recorded including redirects.
//
// This is useful when using an http.Client that follows redirects
// automatically: each redirect hop produces a separate RoundTrip call to
// the Recorder. With SkipRedirects(true), only the terminal response is
// stored, keeping fixtures clean.
func WithSkipRedirects(skip bool) RecorderOption {
	return func(r *Recorder) {
		r.skipRedirects = skip
	}
}

// WithRecorderTLSConfig sets the TLS configuration for outbound connections.
// The provided config is applied to the inner http.Transport's TLSClientConfig.
// If the current transport is not an *http.Transport, a new *http.Transport is
// created with the TLS config set.
//
// If cfg is nil, this option is a no-op.
func WithRecorderTLSConfig(cfg *tls.Config) RecorderOption {
	return func(r *Recorder) {
		if cfg == nil {
			return
		}
		if t, ok := r.transport.(*http.Transport); ok {
			if t == http.DefaultTransport.(*http.Transport) {
				r.transport = &http.Transport{TLSClientConfig: cfg}
				return
			}
			t.TLSClientConfig = cfg
			return
		}
		r.transport = &http.Transport{TLSClientConfig: cfg}
	}
}

// NewRecorder creates a new Recorder wrapping the given store.
// If transport is nil, http.DefaultTransport is used.
//
// By default:
//   - async mode is enabled with a buffer size of 1024
//   - sample rate is 1.0 (record every request)
//   - route is "" (empty — caller should set via WithRoute)
//   - default no-op sanitizer (tapes are stored as-is unless WithSanitizer configures redaction)
//   - errors during async writes are silently discarded
//
// The caller must call Close when done to flush pending recordings.
func NewRecorder(store Store, opts ...RecorderOption) *Recorder {
	if store == nil {
		panic("httptape: NewRecorder requires a non-nil Store")
	}

	r := &Recorder{
		transport:    http.DefaultTransport,
		store:        store,
		sanitizer:    NewPipeline(), // default no-op sanitizer
		async:        true,
		sampleRate:   1.0,
		randFloat:    rand.Float64,
		bufSize:      1024,
		sseRecording: true,
	}

	for _, opt := range opts {
		opt(r)
	}

	if r.async {
		r.tapeCh = make(chan Tape, r.bufSize)
		r.done = make(chan struct{})
		go r.drain()
	}

	return r
}

// RoundTrip executes the HTTP request via the inner transport, captures the
// request and response as a Tape, and writes it to the store (synchronously
// or asynchronously depending on configuration).
//
// The request and response are never modified. The response body is fully read
// into memory and replaced with a new io.ReadCloser so the caller sees the
// complete body. This is the only observable side effect: the original
// response.Body is consumed and replaced.
//
// If recording fails (e.g., store error in synchronous mode), the original
// response is still returned — recording errors never affect the caller's
// HTTP flow.
//
// Sampling: if a random float is >= sampleRate, the request is passed through
// without recording.
func (r *Recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	// Closed guard: if the recorder has been closed, pass through without recording.
	// This prevents sending on a closed channel in async mode.
	if r.closed.Load() {
		return r.transport.RoundTrip(req)
	}

	// Sampling check: skip recording if not sampled.
	if r.sampleRate < 1.0 && r.randFloat() >= r.sampleRate {
		return r.transport.RoundTrip(req)
	}

	// Capture request body before forwarding.
	var reqBody []byte
	if req.Body != nil {
		var err error
		reqBody, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("httptape: recorder read request body: %w", err)
		}
		req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
		bodyBytes := reqBody // capture for closure
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(bodyBytes)), nil
		}
	}

	// Execute the actual HTTP call.
	resp, err := r.transport.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	// Skip redirect responses if configured.
	if r.skipRedirects && resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return resp, nil
	}

	// SSE detection: if the response is text/event-stream and SSE recording
	// is enabled, use the streaming recording path instead of buffering.
	if r.sseRecording && isSSEContentType(resp.Header.Get("Content-Type")) {
		return r.roundTripSSE(req, resp, reqBody)
	}

	// Capture response body.
	var respBody []byte
	if resp.Body != nil {
		respBody, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
		if err != nil {
			if r.onError != nil {
				r.onError(fmt.Errorf("httptape: recorder read response body: %w", err))
			}
			return resp, nil
		}
	}

	// Detect body encodings based on Content-Type headers.
	reqBodyEncoding := detectBodyEncoding(req.Header.Get("Content-Type"))
	respBodyEncoding := detectBodyEncoding(resp.Header.Get("Content-Type"))

	// Apply body truncation if maxBodySize is set.
	var reqTruncated, respTruncated bool
	var reqOrigSize, respOrigSize int64

	if r.maxBodySize > 0 {
		if len(reqBody) > r.maxBodySize {
			reqOrigSize = int64(len(reqBody))
			reqBody = reqBody[:r.maxBodySize]
			reqTruncated = true
			if r.onError != nil {
				r.onError(fmt.Errorf("httptape: request body truncated from %d to %d bytes", reqOrigSize, r.maxBodySize))
			}
		}
		if len(respBody) > r.maxBodySize {
			respOrigSize = int64(len(respBody))
			respBody = respBody[:r.maxBodySize]
			respTruncated = true
			if r.onError != nil {
				r.onError(fmt.Errorf("httptape: response body truncated from %d to %d bytes", respOrigSize, r.maxBodySize))
			}
		}
	}

	// Build the tape.
	recordedReq := RecordedReq{
		Method:           req.Method,
		URL:              req.URL.String(),
		Headers:          req.Header.Clone(),
		Body:             reqBody,
		BodyHash:         BodyHashFromBytes(reqBody),
		BodyEncoding:     reqBodyEncoding,
		Truncated:        reqTruncated,
		OriginalBodySize: reqOrigSize,
	}
	recordedResp := RecordedResp{
		StatusCode:       resp.StatusCode,
		Headers:          resp.Header.Clone(),
		Body:             respBody,
		BodyEncoding:     respBodyEncoding,
		Truncated:        respTruncated,
		OriginalBodySize: respOrigSize,
	}

	tape := NewTape(r.route, recordedReq, recordedResp)

	// Apply sanitizer (always present — defaults to no-op Pipeline).
	tape = r.sanitizer.Sanitize(tape)

	// Persist the tape.
	r.persistTape(req.Context(), tape)

	return resp, nil
}

// roundTripSSE handles the SSE recording path. The response body is wrapped
// in an sseRecordingReader that delivers bytes to the caller unchanged while
// parsing SSE events in a background goroutine. Tape persistence is deferred
// until the caller finishes consuming the body (Close or EOF).
func (r *Recorder) roundTripSSE(req *http.Request, resp *http.Response, reqBody []byte) (*http.Response, error) {
	startTime := time.Now()
	respHeaders := resp.Header.Clone()

	var mu sync.Mutex
	var events []SSEEvent

	reqBodyEncoding := detectBodyEncoding(req.Header.Get("Content-Type"))

	// Apply body truncation to request body if configured.
	var reqTruncated bool
	var reqOrigSize int64
	if r.maxBodySize > 0 && len(reqBody) > r.maxBodySize {
		reqOrigSize = int64(len(reqBody))
		reqBody = reqBody[:r.maxBodySize]
		reqTruncated = true
		if r.onError != nil {
			r.onError(fmt.Errorf("httptape: request body truncated from %d to %d bytes", reqOrigSize, r.maxBodySize))
		}
	}

	// Build the request portion of the tape now (it won't change).
	recordedReq := RecordedReq{
		Method:           req.Method,
		URL:              req.URL.String(),
		Headers:          req.Header.Clone(),
		Body:             reqBody,
		BodyHash:         BodyHashFromBytes(reqBody),
		BodyEncoding:     reqBodyEncoding,
		Truncated:        reqTruncated,
		OriginalBodySize: reqOrigSize,
	}

	// Capture the request context for use in onDone. The context may be
	// cancelled by the time the body is fully consumed, so we use
	// context.Background for the save operation.
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
		if truncated && r.onError != nil {
			r.onError(fmt.Errorf("httptape: SSE stream truncated: %w", parseErr))
		}

		recordedResp := RecordedResp{
			StatusCode: resp.StatusCode,
			Headers:    respHeaders,
			Body:       nil,
			SSEEvents:  collectedEvents,
			Truncated:  truncated,
		}

		tape := NewTape(r.route, recordedReq, recordedResp)
		tape = r.sanitizer.Sanitize(tape)
		r.persistTape(context.Background(), tape)
	}

	wrapper := newSSERecordingReader(resp.Body, startTime, onEvent, onDone)
	resp.Body = wrapper

	return resp, nil
}

// persistTape saves a tape to the store, using the async channel if
// configured, or synchronously otherwise.
func (r *Recorder) persistTape(ctx context.Context, tape Tape) {
	if r.async {
		r.sendMu.Lock()
		if r.closed.Load() {
			r.sendMu.Unlock()
			// recorder closed -- drop tape silently
		} else {
			select {
			case r.tapeCh <- tape:
				// sent
			default:
				// channel full -- drop tape, call onError if set
				if r.onError != nil {
					r.onError(fmt.Errorf("httptape: recorder buffer full, tape dropped"))
				}
			}
			r.sendMu.Unlock()
		}
	} else {
		saveErr := r.store.Save(ctx, tape)
		if saveErr != nil && r.onError != nil {
			r.onError(saveErr)
		}
	}
}

// Close flushes all pending asynchronous recordings and waits for the
// background goroutine to finish. It is safe to call multiple times.
// After Close returns, no more tapes will be written.
//
// In synchronous mode, Close is a no-op.
func (r *Recorder) Close() error {
	if !r.async {
		return nil
	}
	r.closeOnce.Do(func() {
		r.sendMu.Lock()
		r.closed.Store(true)
		close(r.tapeCh)
		r.sendMu.Unlock()
		<-r.done
	})
	return nil
}

// drain is the background goroutine that reads tapes from the channel and
// persists them to the store.
//
// Note: drain uses context.Background() for store.Save calls because there is
// no parent context available in the background goroutine. This means pending
// saves cannot be cancelled during shutdown — Close() will block until all
// buffered tapes are saved. If the store is slow (e.g., network-backed), this
// could cause Close() to hang. A future improvement would be to accept a
// context in Close() or use a context with a timeout.
//
// TODO: consider accepting a context in Close() to allow cancellation of
// pending saves during shutdown.
func (r *Recorder) drain() {
	defer close(r.done)
	for tape := range r.tapeCh {
		err := r.store.Save(context.Background(), tape)
		if err != nil && r.onError != nil {
			r.onError(err)
		}
	}
}
