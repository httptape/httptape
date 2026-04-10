package httptape

import (
	"math/rand"
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
	store          Store
	matcher        Matcher
	fallbackStatus int              // HTTP status when no tape matches
	fallbackBody   []byte           // response body when no tape matches
	onNoMatch      func(*http.Request) // optional callback when no tape matches
	cors           bool             // if true, add CORS headers to all responses
	delay          time.Duration    // fixed delay before every response; zero means no delay
	errorRate      float64          // fraction of requests that return 500 (0.0-1.0)
	randFloat      func() float64   // random number generator (injectable for testing)
	replayHeaders  map[string]string // headers injected into every replayed response
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
// Panics if rate is outside [0.0, 1.0]. This is a programming error,
// following the constructor-guard convention.
func WithErrorRate(rate float64) ServerOption {
	return func(s *Server) {
		if rate < 0 || rate > 1 {
			panic("httptape: WithErrorRate rate must be between 0.0 and 1.0")
		}
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

// withRandFloat overrides the random number generator for testing.
// This is unexported -- only used in tests to make error simulation
// deterministic.
func withRandFloat(fn func() float64) ServerOption {
	return func(s *Server) { s.randFloat = fn }
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
//
// The store must not be nil. Passing a nil store is a programming error and
// will panic.
func NewServer(store Store, opts ...ServerOption) *Server {
	if store == nil {
		panic("httptape: NewServer requires a non-nil Store")
	}

	s := &Server{
		store:          store,
		matcher:        DefaultMatcher(),
		fallbackStatus: http.StatusNotFound,
		fallbackBody:   []byte("httptape: no matching tape found"),
	}
	for _, opt := range opts {
		opt(s)
	}

	// Default random number generator for error simulation.
	if s.randFloat == nil {
		s.randFloat = rand.Float64
	}

	return s
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
//  6. Replay header injection -- override/add headers from WithReplayHeaders
//  7. Write response
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

	// 4. Find a matching tape.
	tape, ok := s.matcher.Match(r, tapes)

	// 5. No match.
	if !ok {
		if s.onNoMatch != nil {
			s.onNoMatch(r)
		}
		w.WriteHeader(s.fallbackStatus)
		w.Write(s.fallbackBody) //nolint:errcheck // response write failure is not actionable
		return
	}

	// 6. Per-fixture error override.
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

	// 7. Delay (global or per-fixture override).
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

	// 8. Write the matched tape's response.
	// 8a: copy response headers (clone slices to prevent aliasing with tape data).
	for key, values := range tape.Response.Headers {
		w.Header()[key] = append([]string(nil), values...)
	}
	// 8b: apply replay header overrides (if any).
	for key, value := range s.replayHeaders {
		w.Header().Set(key, value)
	}
	// 8c: remove Content-Length — the recorded value may be stale if the body
	// was modified by sanitization. Let net/http set it from the actual body.
	w.Header().Del("Content-Length")
	// 8d: write status code.
	w.WriteHeader(tape.Response.StatusCode)
	// 8e: write body.
	w.Write(tape.Response.Body) //nolint:errcheck // response write failure is not actionable
}
