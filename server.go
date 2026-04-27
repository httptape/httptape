package httptape

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	mathrand "math/rand"
	"net/http"
	"time"
)

// Server is an http.Handler that replays recorded HTTP interactions.
// It receives incoming requests, finds a matching Tape via a Matcher,
// and writes the recorded response. If no match is found, it returns
// a configurable fallback status code.
//
// Server is safe for concurrent use by multiple goroutines. All fields are
// immutable after construction.
type Server struct {
	store            Store
	matcher          Matcher
	fallbackStatus   int                                        // HTTP status when no tape matches
	fallbackBody     []byte                                     // response body when no tape matches
	onNoMatch        func(*http.Request)                        // optional callback when no tape matches
	cors             bool                                       // if true, add CORS headers to all responses
	delay            time.Duration                              // fixed delay before every response; zero means no delay
	errorRate        float64                                    // fraction of requests that return 500 (0.0-1.0)
	randFloat        func() float64                             // random number generator (injectable for testing)
	replayHeaders    map[string]string                          // headers injected into every replayed response
	templating       bool                                       // if true, resolve {{...}} in responses
	strictTemplating bool                                       // if true, unresolvable expressions produce 500
	sseTiming        SSETimingMode                              // controls SSE replay inter-event timing
	replayTiming     ResponseTimingMode                         // controls response elapsed-time replay delay
	sleepFunc        func(context.Context, time.Duration) error // injectable sleep for testing
	counters         *counterState                              // per-server counter state for {{counter}}
	randSource       io.Reader                                  // randomness source for template helpers
	synthesis        bool                                       // if true, exemplar tapes are consulted on miss
}

// ServerOption configures a Server.
type ServerOption func(*Server)

// WithMatcher sets the Matcher used to find tapes for incoming requests.
// If not set, DefaultMatcher() is used.
func WithMatcher(m Matcher) ServerOption {
	return func(s *Server) { s.matcher = m }
}

// WithFallbackStatus sets the HTTP status code returned when no tape matches
// the incoming request. Defaults to 404.
func WithFallbackStatus(code int) ServerOption {
	return func(s *Server) { s.fallbackStatus = code }
}

// WithFallbackBody sets the response body returned when no tape matches.
// Defaults to "httptape: no matching tape found".
func WithFallbackBody(body []byte) ServerOption {
	return func(s *Server) { s.fallbackBody = body }
}

// WithOnNoMatch sets a callback invoked when no tape matches an incoming
// request. The callback receives the unmatched request and is called before
// the fallback response is written. It must be safe for concurrent use.
func WithOnNoMatch(fn func(*http.Request)) ServerOption {
	return func(s *Server) { s.onNoMatch = fn }
}

// WithCORS enables CORS headers on all responses. When enabled, the
// server adds permissive CORS headers (Access-Control-Allow-Origin: *)
// and handles OPTIONS preflight requests automatically with 204 No Content.
//
// This is intended for local development where the frontend dev server
// (e.g., localhost:3000) calls the mock backend (e.g., localhost:3001).
// It is opt-in only.
func WithCORS() ServerOption {
	return func(s *Server) { s.cors = true }
}

// WithDelay adds a fixed delay before every response. The delay is
// applied after matching but before writing the response. If the
// request context is cancelled during the delay (e.g., client
// disconnects), ServeHTTP returns immediately without writing.
//
// A zero or negative duration is a no-op.
func WithDelay(d time.Duration) ServerOption {
	return func(s *Server) { s.delay = d }
}

// WithErrorRate causes a fraction of requests to return 500 Internal
// Server Error with an "X-Httptape-Error: simulated" header instead of
// the recorded response. The rate must be between 0.0 and 1.0 inclusive.
//
// A rate of 0.0 disables error simulation (default). A rate of 1.0
// causes all requests to fail.
//
// The rate is validated when NewServer is called. An out-of-range rate
// causes NewServer to return an error.
func WithErrorRate(rate float64) ServerOption {
	return func(s *Server) {
		s.errorRate = rate
	}
}

// WithReplayHeaders adds a header that will be injected into every replayed
// response. It is applied after tape matching and overrides any header with the
// same key that was present in the recorded tape. WithReplayHeaders may be
// called multiple times to set multiple headers.
//
// Common use cases include injecting authorization tokens, correlation IDs,
// or cache-control headers that differ between environments.
func WithReplayHeaders(key, value string) ServerOption {
	return func(s *Server) {
		if s.replayHeaders == nil {
			s.replayHeaders = make(map[string]string)
		}
		s.replayHeaders[key] = value
	}
}

// WithSSETiming sets the inter-event timing mode for SSE tape replay.
// Defaults to SSETimingRealtime (replay with original inter-event gaps).
//
// Modes:
//   - SSETimingRealtime(): replay with original timing
//   - SSETimingAccelerated(factor): divide gaps by factor (must be > 0)
//   - SSETimingInstant(): emit all events immediately, no delay
func WithSSETiming(mode SSETimingMode) ServerOption {
	return func(s *Server) { s.sseTiming = mode }
}

// WithSynthesis enables synthesis mode on the Server. When enabled, requests
// that don't match any exact-URL tape fall back to exemplar tapes -- tapes
// with Exemplar: true and a URLPattern. The exemplar's response body is
// rendered using template helpers (path params, fakers, etc.) to produce a
// unique, deterministic response.
//
// Synthesis is disabled by default. When disabled, exemplar tapes are loaded
// but never consulted, ensuring integration tests are not affected by
// exemplar tapes in the fixture directory.
//
// Requires that tapes are loaded from a store that includes exemplar fixtures.
// Exemplar tapes must pass validation (see ValidateExemplar).
func WithSynthesis() ServerOption {
	return func(s *Server) { s.synthesis = true }
}

// WithTemplating controls whether Mustache-style {{request.*}} template
// expressions in response bodies and headers are resolved against the
// incoming request at serve time. When enabled, template expressions are
// replaced with values from the request (method, path, headers, query
// params, body fields). When disabled, no scanning or replacement occurs
// and response bodies/headers are written exactly as stored.
//
// Templating is enabled by default. The {{...}} delimiter is vanishingly
// rare in real HTTP payloads, so enabling it by default is safe. Users who
// only replay recorded traffic (no {{ in fixtures) see zero behavioral
// change, as the fast path skips processing for bodies without "{{".
//
// SSE responses skip templating — events are streamed verbatim from the
// tape. See also WithStrictTemplating for error handling of unresolvable
// expressions.
func WithTemplating(enabled bool) ServerOption {
	return func(s *Server) { s.templating = enabled }
}

// WithStrictTemplating controls the error behavior when a template
// expression cannot be resolved. When strict is true, an unresolvable
// expression (e.g., referencing a missing header) causes the server to
// return HTTP 500 with an X-Httptape-Error: template header and a body
// describing which expression failed. When strict is false (the default),
// unresolvable expressions are silently replaced with an empty string.
//
// Strict mode is useful in tests where fixture authoring mistakes should
// be caught early. Lenient mode is useful for production-like tests where
// a missing value is acceptable.
//
// Strict mode has no effect when templating is disabled via
// WithTemplating(false).
func WithStrictTemplating(strict bool) ServerOption {
	return func(s *Server) { s.strictTemplating = strict }
}

// withRandFloat overrides the random number generator for testing.
// This is unexported -- only used in tests to make error simulation
// deterministic.
func withRandFloat(fn func() float64) ServerOption {
	return func(s *Server) { s.randFloat = fn }
}

// withRandSource overrides the randomness source for template helpers.
// This is unexported -- only used in tests to make UUID/randomHex/randomInt
// deterministic.
func withRandSource(r io.Reader) ServerOption {
	return func(s *Server) { s.randSource = r }
}

// ResponseTimingMode controls how the recorded response elapsed time is
// applied during replay. It is a sealed interface implemented by three
// unexported types, mirroring SSETimingMode.
type ResponseTimingMode interface {
	// responseDelay returns the duration to sleep before sending the
	// response, given the recorded elapsed time in milliseconds.
	// Returns zero for unknown elapsed time (elapsedMS <= 0).
	responseDelay(elapsedMS int64) time.Duration
	responseTimingMode() // seal
}

// responseTimingInstant replays responses immediately with no delay.
// This is the default and preserves pre-feature behavior.
type responseTimingInstant struct{}

func (responseTimingInstant) responseTimingMode() {}
func (responseTimingInstant) responseDelay(_ int64) time.Duration {
	return 0
}

// responseTimingRecorded replays responses with the original recorded
// elapsed time as a delay.
type responseTimingRecorded struct{}

func (responseTimingRecorded) responseTimingMode() {}
func (responseTimingRecorded) responseDelay(elapsedMS int64) time.Duration {
	if elapsedMS <= 0 {
		return 0
	}
	return time.Duration(elapsedMS) * time.Millisecond
}

// responseTimingAccelerated divides the recorded elapsed time by the
// given factor. Factor > 1 means slower than recorded; factor < 1 means
// faster than recorded.
type responseTimingAccelerated struct {
	factor float64
}

func (responseTimingAccelerated) responseTimingMode() {}
func (a responseTimingAccelerated) responseDelay(elapsedMS int64) time.Duration {
	if elapsedMS <= 0 {
		return 0
	}
	return time.Duration(float64(elapsedMS)*a.factor) * time.Millisecond
}

// ResponseTimingInstant returns a ResponseTimingMode that replays responses
// immediately with no delay. This is the default mode and preserves
// pre-feature behavior.
func ResponseTimingInstant() ResponseTimingMode {
	return responseTimingInstant{}
}

// ResponseTimingRecorded returns a ResponseTimingMode that replays responses
// with the original recorded elapsed time as a delay. Pre-feature fixtures
// (ElapsedMS == 0) are replayed instantly regardless of this setting.
func ResponseTimingRecorded() ResponseTimingMode {
	return responseTimingRecorded{}
}

// ResponseTimingAccelerated returns a ResponseTimingMode that scales the
// recorded elapsed time by the given factor. A factor > 1 makes responses
// slower than recorded; a factor < 1 makes them faster. Factor must be > 0;
// returns an error otherwise.
func ResponseTimingAccelerated(factor float64) (ResponseTimingMode, error) {
	if factor <= 0 {
		return nil, fmt.Errorf("httptape: ResponseTimingAccelerated factor must be > 0, got %g", factor)
	}
	return responseTimingAccelerated{factor: factor}, nil
}

// WithReplayTiming sets the response timing mode for replayed responses.
// Defaults to ResponseTimingInstant() (no delay, preserving pre-feature
// behavior).
//
// Timing composition: WithReplayTiming composes ADDITIVELY with
// WithDelay / metadata.delay. The existing effectiveDelay (user-authored
// "simulate slow API" delay) runs first, then the replay timing delay
// runs second, before the response is written. This means if WithDelay
// is 100ms and the recorded elapsed time is 200ms with
// ResponseTimingRecorded(), the total delay is 300ms. Pre-feature
// fixtures (ElapsedMS == 0) incur no replay timing delay regardless
// of mode.
func WithReplayTiming(mode ResponseTimingMode) ServerOption {
	return func(s *Server) { s.replayTiming = mode }
}

// withSleepFunc overrides the sleep function for testing. This is
// unexported -- only used in tests to avoid real sleeping.
func withSleepFunc(fn func(context.Context, time.Duration) error) ServerOption {
	return func(s *Server) { s.sleepFunc = fn }
}

// defaultSleepFunc sleeps for the given duration, respecting context
// cancellation. Returns nil on successful sleep, or the context error
// if the context is cancelled during the sleep.
func defaultSleepFunc(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// NewServer creates a new Server that replays tapes from the given store.
//
// By default:
//   - matcher is DefaultMatcher() (matches by method + URL path with scoring)
//   - fallback status is 404 Not Found
//   - fallback body is "httptape: no matching tape found"
//   - no onNoMatch callback
//   - CORS disabled
//   - no delay
//   - no error simulation
//   - templating enabled (lenient mode)
//
// The store must not be nil. Passing a nil store is a programming error and
// will panic.
//
// Returns an error if any option values are invalid (e.g., error rate outside
// [0.0, 1.0]). All validation errors are accumulated and returned together.
func NewServer(store Store, opts ...ServerOption) (*Server, error) {
	if store == nil {
		panic("httptape: NewServer requires a non-nil Store")
	}

	s := &Server{
		store:          store,
		matcher:        DefaultMatcher(),
		fallbackStatus: http.StatusNotFound,
		fallbackBody:   []byte("httptape: no matching tape found"),
		templating:     true,
		sseTiming:      SSETimingRealtime(),
		replayTiming:   ResponseTimingInstant(),
		sleepFunc:      defaultSleepFunc,
	}
	for _, opt := range opts {
		opt(s)
	}

	// Validate after all options are applied.
	var errs []error
	if s.errorRate < 0 || s.errorRate > 1 {
		errs = append(errs, fmt.Errorf("httptape: WithErrorRate rate must be between 0.0 and 1.0, got %g", s.errorRate))
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	// Default random number generator for error simulation.
	if s.randFloat == nil {
		s.randFloat = mathrand.Float64
	}

	// Initialize counter state and random source for template helpers.
	if s.counters == nil {
		s.counters = &counterState{counters: make(map[string]int64)}
	}
	if s.randSource == nil {
		s.randSource = rand.Reader
	}

	return s, nil
}

// ServeHTTP handles an incoming HTTP request by finding a matching tape
// and writing the recorded response. If no tape matches, it writes the
// configured fallback response.
//
// When multiple features are enabled, they execute in this order:
//  1. CORS -- add CORS headers and handle OPTIONS preflight
//  2. Error simulation -- short-circuit with 500 before any work
//  3. Normal replay -- existing match-and-respond logic
//  4. Per-fixture error override -- after matching, before writing
//  5. Delay -- sleep before writing the real response
//  6. Templating -- resolve {{request.*}} expressions in body and headers
//  7. Replay header injection -- override/add headers from WithReplayHeaders
//  8. Write response
//
// Performance note: ServeHTTP calls Store.List with an empty filter on every
// request, resulting in an O(n) scan over all tapes. This is acceptable for
// v1 test usage with small fixture sets.
//
// The method is safe for concurrent use.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. CORS headers (if enabled).
	if s.cors {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods",
			"GET, POST, PUT, DELETE, PATCH, OPTIONS, HEAD")
		w.Header().Set("Access-Control-Allow-Headers",
			"Content-Type, Authorization, X-Requested-With, Accept")
		w.Header().Set("Access-Control-Max-Age", "86400")
		w.Header().Set("Access-Control-Expose-Headers",
			"X-Httptape-Source, X-Httptape-Error")

		// Handle OPTIONS preflight: return 204 with CORS headers, no body.
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}

	// 2. Random error simulation (if enabled).
	if s.errorRate > 0 && s.randFloat() < s.errorRate {
		w.Header().Set("X-Httptape-Error", "simulated")
		http.Error(w, "httptape: simulated error", http.StatusInternalServerError)
		return
	}

	// 3. Retrieve all tapes from store.
	tapes, err := s.store.List(r.Context(), Filter{})
	if err != nil {
		http.Error(w, "httptape: store error", http.StatusInternalServerError)
		return
	}

	// 4. Partition tapes into exact and exemplar sets.
	// When synthesis is disabled, exemplar tapes are explicitly filtered out
	// to guarantee they are inert regardless of matcher configuration.
	var exactTapes, exemplarTapes []Tape
	for _, t := range tapes {
		if t.Exemplar {
			exemplarTapes = append(exemplarTapes, t)
		} else {
			exactTapes = append(exactTapes, t)
		}
	}

	// 5. Phase 1 -- exact match.
	tape, ok := s.matcher.Match(r, exactTapes)

	// 6. Phase 2 -- exemplar fallback (only when synthesis is enabled and
	// phase 1 found no match).
	var pathParams map[string]string
	if !ok && s.synthesis && len(exemplarTapes) > 0 {
		tape, pathParams, ok = s.matchExemplar(r, exemplarTapes)
	}

	// 7. No match at all.
	if !ok {
		if s.onNoMatch != nil {
			s.onNoMatch(r)
		}
		w.WriteHeader(s.fallbackStatus)
		w.Write(s.fallbackBody) //nolint:errcheck // response write failure is not actionable
		return
	}

	// 8. Per-fixture error override.
	if tape.Metadata != nil {
		if raw, ok := tape.Metadata["error"]; ok {
			if errMap, ok := raw.(map[string]any); ok {
				status := http.StatusInternalServerError
				body := "httptape: fixture error"
				if s, ok := errMap["status"].(float64); ok {
					status = int(s)
				}
				if b, ok := errMap["body"].(string); ok {
					body = b
				}
				w.Header().Set("X-Httptape-Error", "simulated")
				http.Error(w, body, status)
				return
			}
		}
	}

	// 9. Delay (global or per-fixture override).
	effectiveDelay := s.delay
	if tape.Metadata != nil {
		if raw, ok := tape.Metadata["delay"]; ok {
			if ds, ok := raw.(string); ok {
				if parsed, err := time.ParseDuration(ds); err == nil {
					effectiveDelay = parsed
				}
			}
		}
	}
	if effectiveDelay > 0 {
		timer := time.NewTimer(effectiveDelay)
		defer timer.Stop()
		select {
		case <-timer.C:
			// delay elapsed, proceed to write response
		case <-r.Context().Done():
			// client disconnected during delay, bail out
			return
		}
	}

	// 9b. Replay timing delay (opt-in via WithReplayTiming). This composes
	// ADDITIVELY with the effectiveDelay above: WithDelay / metadata.delay
	// is a user-authored "simulate slow API" delay, while WithReplayTiming
	// is replay-fidelity. Both run sequentially so the total delay is their
	// sum. Pre-feature fixtures (ElapsedMS == 0) incur no replay timing
	// delay regardless of mode.
	if replayDelay := s.replayTiming.responseDelay(tape.Response.ElapsedMS); replayDelay > 0 {
		if err := s.sleepFunc(r.Context(), replayDelay); err != nil {
			// Context cancelled during replay timing delay.
			return
		}
	}

	// 10. Check if this is an SSE tape -- SSE responses stream events verbatim
	// (no templating, no replay-header injection on the body path).
	if tape.Response.IsSSE() {
		s.serveSSE(w, r, tape)
		return
	}

	// 11. Templating -- resolve {{...}} expressions in non-SSE response
	// body and headers.
	respBody := tape.Response.Body
	respHeaders := tape.Response.Headers
	if s.templating {
		reqBody := readRequestBody(r)

		// For non-exemplar tapes, extract path params from the matcher's
		// PathPatternCriterion if present (existing behavior from #196).
		if pathParams == nil {
			if cm, ok := s.matcher.(*CompositeMatcher); ok {
				for _, criterion := range cm.criteria {
					if ppc, ok := criterion.(*PathPatternCriterion); ok {
						pathParams = ppc.ExtractParams(r.URL.Path)
						break
					}
				}
			}
		}

		ctx := &templateCtx{
			req:        r,
			reqBody:    reqBody,
			pathParams: pathParams,
			tapeID:     tape.ID,
			counters:   s.counters,
			randSource: s.randSource,
		}

		// For exemplar tapes with JSON Content-Type, use JSON-aware resolution
		// with type coercion support.
		if tape.Exemplar {
			respBody, err = resolveExemplarBody(respBody, tape.Response.Headers, ctx, s.strictTemplating)
		} else {
			respBody, err = ResolveTemplateBody(respBody, ctx, s.strictTemplating)
		}
		if err != nil {
			w.Header().Set("X-Httptape-Error", "template")
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		respHeaders, err = ResolveTemplateHeaders(respHeaders, ctx, s.strictTemplating)
		if err != nil {
			w.Header().Set("X-Httptape-Error", "template")
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// 12. Write the matched tape's response.
	// 12a: copy response headers (clone slices to prevent aliasing with tape data).
	for key, values := range respHeaders {
		w.Header()[key] = append([]string(nil), values...)
	}
	// 12b: apply replay header overrides (if any).
	for key, value := range s.replayHeaders {
		w.Header().Set(key, value)
	}
	// 12c: remove Content-Length -- the recorded value may be stale if the body
	// was modified by sanitization or templating. Let net/http set it from the actual body.
	w.Header().Del("Content-Length")
	// 12d: write status code.
	w.WriteHeader(tape.Response.StatusCode)
	// 12e: write body.
	w.Write(respBody) //nolint:errcheck // response write failure is not actionable
}

// matchExemplar searches for the best-matching exemplar tape for the given
// request. For each exemplar, it builds a PathPatternCriterion from the
// tape's URLPattern, tests the incoming request path against it, and also
// runs the server's other configured criteria (method, headers, body_fuzzy,
// etc.) to ensure the exemplar matches all dimensions.
//
// Returns the best-matching tape, captured path parameters, and true, or
// (Tape{}, nil, false) if no exemplar matches.
func (s *Server) matchExemplar(r *http.Request, exemplars []Tape) (Tape, map[string]string, bool) {
	type match struct {
		tape      Tape
		params    map[string]string
		score     int
		declOrder int
	}

	var best *match

	// Extract non-path criteria from the server's matcher for secondary scoring.
	var otherCriteria []Criterion
	if cm, ok := s.matcher.(*CompositeMatcher); ok {
		for _, c := range cm.criteria {
			switch c.(type) {
			case PathCriterion, *PathCriterion, *PathPatternCriterion, *PathRegexCriterion:
				// Skip path-related criteria -- we use our own PathPatternCriterion.
			default:
				otherCriteria = append(otherCriteria, c)
			}
		}
	}

	for i, exemplar := range exemplars {
		ppc, err := NewPathPatternCriterion(exemplar.Request.URLPattern)
		if err != nil {
			continue // invalid pattern: skip silently
		}

		// Test if the request path matches the exemplar's pattern.
		params := ppc.ExtractParams(r.URL.Path)
		if params == nil && len(ppc.paramNames) > 0 {
			// Pattern has parameters but didn't match.
			if !ppc.re.MatchString(r.URL.Path) {
				continue
			}
		} else if params == nil {
			// Pattern has no parameters -- check exact regex match.
			if !ppc.re.MatchString(r.URL.Path) {
				continue
			}
			params = map[string]string{}
		}

		// PathPatternCriterion's Score returns a flat 3 on match. Same-depth
		// literal-vs-parameterized exemplars therefore tie and fall back to
		// declaration order (see follow-up issue for true specificity sort).
		pathScore := 3

		// Run other criteria against the exemplar. The exemplar must pass all.
		totalScore := pathScore
		eliminated := false
		for _, c := range otherCriteria {
			score := c.Score(r, exemplar)
			if score == 0 {
				eliminated = true
				break
			}
			totalScore += score
		}
		if eliminated {
			continue
		}

		if best == nil || totalScore > best.score || (totalScore == best.score && i < best.declOrder) {
			best = &match{
				tape:      exemplar,
				params:    params,
				score:     totalScore,
				declOrder: i,
			}
		}
	}

	if best == nil {
		return Tape{}, nil, false
	}
	return best.tape, best.params, true
}

// serveSSE handles SSE tape replay. It writes response headers, then
// streams events using the configured SSETimingMode.
func (s *Server) serveSSE(w http.ResponseWriter, r *http.Request, tape Tape) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "httptape: streaming not supported", http.StatusInternalServerError)
		return
	}

	// Copy tape response headers.
	for key, values := range tape.Response.Headers {
		w.Header()[key] = append([]string(nil), values...)
	}

	// Ensure SSE-required headers are set.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Apply replay header overrides (if any).
	for key, value := range s.replayHeaders {
		w.Header().Set(key, value)
	}

	// Remove Content-Length — SSE streams are chunked.
	w.Header().Del("Content-Length")

	// Write status code.
	w.WriteHeader(tape.Response.StatusCode)
	flusher.Flush()

	// Replay events with the configured timing.
	_ = replaySSEEvents(r.Context(), w, flusher, tape.Response.SSEEvents, s.sseTiming) //nolint:errcheck // SSE replay write failure is not actionable
}

// ResetCounter resets the named counter to 0. If name is empty, all counters
// are reset. This is useful in tests that need deterministic counter values
// across test cases.
func (s *Server) ResetCounter(name string) {
	s.counters.Reset(name)
}
