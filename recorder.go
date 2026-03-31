package httptape

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"sync"
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
type Recorder struct {
	transport http.RoundTripper // inner transport to delegate to
	store     Store             // where to persist tapes
	route     string            // logical route label for all tapes produced
	sanitizer Sanitizer         // optional; may be nil (no-op if nil)
	async     bool              // true = non-blocking writes via channel
	sampleRate float64          // 0.0–1.0; 1.0 = record everything
	randFloat func() float64   // returns [0.0, 1.0); injectable for testing
	bufSize   int               // channel buffer size (only used when async=true)
	onError   func(error)       // callback for async write errors; defaults to no-op

	// async internals
	tapeCh    chan Tape     // buffered channel for async mode
	done      chan struct{} // closed when background goroutine exits
	closeOnce sync.Once    // ensures Close is idempotent
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
func WithSanitizer(s Sanitizer) RecorderOption {
	return func(r *Recorder) {
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

// NewRecorder creates a new Recorder wrapping the given transport.
// If transport is nil, http.DefaultTransport is used.
//
// By default:
//   - async mode is enabled with a buffer size of 1024
//   - sample rate is 1.0 (record every request)
//   - route is "" (empty — caller should set via WithRoute)
//   - no sanitizer (tapes are stored as-is)
//   - errors during async writes are silently discarded
//
// The caller must call Close when done to flush pending recordings.
func NewRecorder(store Store, opts ...RecorderOption) *Recorder {
	r := &Recorder{
		transport:  http.DefaultTransport,
		store:      store,
		async:      true,
		sampleRate: 1.0,
		randFloat:  rand.Float64,
		bufSize:    1024,
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

	// Capture response body.
	var respBody []byte
	if resp.Body != nil {
		respBody, err = io.ReadAll(resp.Body)
		if err != nil {
			// Return the response with the body in an error state rather than
			// failing the entire call due to a recording issue.
			return resp, nil
		}
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
	}

	// Build the tape.
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

	tape := NewTape(r.route, recordedReq, recordedResp)

	// Apply sanitizer if set.
	if r.sanitizer != nil {
		tape = r.sanitizer.Sanitize(tape)
	}

	// Persist the tape.
	if r.async {
		select {
		case r.tapeCh <- tape:
			// sent
		default:
			// channel full — drop tape, call onError if set
			if r.onError != nil {
				r.onError(fmt.Errorf("httptape: recorder buffer full, tape dropped"))
			}
		}
	} else {
		saveErr := r.store.Save(req.Context(), tape)
		if saveErr != nil && r.onError != nil {
			r.onError(saveErr)
		}
	}

	return resp, nil
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
		close(r.tapeCh)
		<-r.done
	})
	return nil
}

// drain is the background goroutine that reads tapes from the channel and
// persists them to the store.
func (r *Recorder) drain() {
	defer close(r.done)
	for tape := range r.tapeCh {
		err := r.store.Save(context.Background(), tape)
		if err != nil && r.onError != nil {
			r.onError(err)
		}
	}
}
