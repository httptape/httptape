package httptape

import (
	"net/http"
)

// Matcher selects a Tape from a list of candidates that best matches
// the incoming HTTP request. Implementations define the matching strategy
// (exact, fuzzy, regex, etc.).
type Matcher interface {
	// Match returns the best-matching Tape for the given request.
	// If no tape matches, it returns false as the second return value.
	// The candidates slice is never nil but may be empty.
	// Implementations must not modify the request or the candidate tapes.
	Match(req *http.Request, candidates []Tape) (Tape, bool)
}

// MatcherFunc is an adapter to allow the use of ordinary functions as Matchers.
type MatcherFunc func(req *http.Request, candidates []Tape) (Tape, bool)

// Match calls f(req, candidates).
func (f MatcherFunc) Match(req *http.Request, candidates []Tape) (Tape, bool) {
	return f(req, candidates)
}

// ExactMatcher is a simple Matcher that matches requests by HTTP method and
// URL path. It returns the first candidate whose method and URL path are
// equal to the incoming request's method and URL path.
//
// This is intentionally minimal. For advanced matching (headers, body,
// query params, regex), use the matchers from issue #30.
func ExactMatcher() Matcher {
	return MatcherFunc(func(req *http.Request, candidates []Tape) (Tape, bool) {
		for _, t := range candidates {
			if t.Request.Method == req.Method && t.Request.URL == req.URL.Path {
				return t, true
			}
		}
		return Tape{}, false
	})
}

// Server is an http.Handler that replays recorded HTTP interactions.
// It receives incoming requests, finds a matching Tape via a Matcher,
// and writes the recorded response. If no match is found, it returns
// a configurable fallback status code.
//
// Server is safe for concurrent use.
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
// If not set, ExactMatcher() is used.
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
//   - matcher is ExactMatcher() (matches by method + URL path)
//   - fallback status is 404 Not Found
//   - fallback body is "httptape: no matching tape found"
//   - no onNoMatch callback
//
// The store must not be nil. If it is, NewServer returns a Server that
// always responds with 500 Internal Server Error and a descriptive body.
func NewServer(store Store, opts ...ServerOption) *Server {
	s := &Server{
		store:          store,
		matcher:        ExactMatcher(),
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
	// Step 2: nil store check.
	if s.store == nil {
		http.Error(w, "httptape: server misconfigured (nil store)", http.StatusInternalServerError)
		return
	}

	// Step 3: retrieve all tapes from store.
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
	// 6a: copy response headers.
	for key, values := range tape.Response.Headers {
		w.Header()[key] = values
	}
	// 6b: write status code.
	w.WriteHeader(tape.Response.StatusCode)
	// 6c: write body.
	w.Write(tape.Response.Body) //nolint:errcheck // response write failure is not actionable
}
