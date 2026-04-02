package httptape

import (
	"net/http"
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

// NewServer creates a new Server that replays tapes from the given store.
//
// By default:
//   - matcher is DefaultMatcher() (matches by method + URL path with scoring)
//   - fallback status is 404 Not Found
//   - fallback body is "httptape: no matching tape found"
//   - no onNoMatch callback
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
	return s
}

// ServeHTTP handles an incoming HTTP request by finding a matching tape
// and writing the recorded response. If no tape matches, it writes the
// configured fallback response.
//
// The method is safe for concurrent use.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Step 2: retrieve all tapes from store.
	tapes, err := s.store.List(r.Context(), Filter{})
	if err != nil {
		http.Error(w, "httptape: store error", http.StatusInternalServerError)
		return
	}

	// Step 4: find a matching tape.
	tape, ok := s.matcher.Match(r, tapes)

	// Step 5: no match.
	if !ok {
		if s.onNoMatch != nil {
			s.onNoMatch(r)
		}
		w.WriteHeader(s.fallbackStatus)
		w.Write(s.fallbackBody) //nolint:errcheck // response write failure is not actionable
		return
	}

	// Step 6: write the matched tape's response.
	// 6a: copy response headers (clone slices to prevent aliasing with tape data).
	for key, values := range tape.Response.Headers {
		w.Header()[key] = append([]string(nil), values...)
	}
	// 6b: remove Content-Length — the recorded value may be stale if the body
	// was modified by sanitization. Let net/http set it from the actual body.
	w.Header().Del("Content-Length")
	// 6c: write status code.
	w.WriteHeader(tape.Response.StatusCode)
	// 6d: write body.
	w.Write(tape.Response.Body) //nolint:errcheck // response write failure is not actionable
}
