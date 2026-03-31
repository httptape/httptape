package httptape

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
)

// MatchCriterion evaluates how well a candidate Tape matches an incoming
// request for a single dimension (method, path, body, etc.).
//
// It returns a score:
//   - 0 means the candidate does not match on this dimension (eliminates it).
//   - A positive value means the candidate matches, with higher values
//     indicating a stronger/more specific match.
//
// Implementations must not modify the request or the candidate tape.
type MatchCriterion func(req *http.Request, candidate Tape) int

// MatchMethod returns a MatchCriterion that requires the HTTP method to match.
// Returns score 1 on match, 0 on mismatch.
func MatchMethod() MatchCriterion {
	return func(req *http.Request, candidate Tape) int {
		if req.Method == candidate.Request.Method {
			return 1
		}
		return 0
	}
}

// MatchPath returns a MatchCriterion that requires the URL path to match.
// It compares the incoming request's URL.Path against the path component of
// the tape's stored URL. Returns score 2 on match, 0 on mismatch.
//
// The tape's URL is stored as a full URL string (e.g., "https://api.example.com/users?page=1").
// MatchPath parses it with url.Parse and compares only the Path component.
// If the tape's URL cannot be parsed, the criterion returns 0.
func MatchPath() MatchCriterion {
	return func(req *http.Request, candidate Tape) int {
		parsed, err := url.Parse(candidate.Request.URL)
		if err != nil {
			return 0
		}
		if req.URL.Path == parsed.Path {
			return 2
		}
		return 0
	}
}

// MatchRoute returns a MatchCriterion that requires the tape's Route field
// to equal a specific value. Returns score 1 on match, 0 on mismatch.
// If route is empty, the criterion always returns 1 (matches any tape).
func MatchRoute(route string) MatchCriterion {
	return func(_ *http.Request, candidate Tape) int {
		if route == "" {
			return 1
		}
		if candidate.Route == route {
			return 1
		}
		return 0
	}
}

// MatchQueryParams returns a MatchCriterion that requires all query parameters
// from the incoming request to be present in the tape's stored URL with the
// same values. Extra parameters in the tape are allowed (subset match).
// Returns score 4 on match, 0 on mismatch.
//
// If the incoming request has no query parameters, this criterion always
// returns 4 (vacuously true — all zero params match).
//
// The tape's URL is parsed with url.Parse to extract query parameters.
// If parsing fails, the criterion returns 0.
func MatchQueryParams() MatchCriterion {
	return func(req *http.Request, candidate Tape) int {
		reqParams := req.URL.Query()
		if len(reqParams) == 0 {
			return 4
		}

		parsed, err := url.Parse(candidate.Request.URL)
		if err != nil {
			return 0
		}
		tapeParams := parsed.Query()

		for key, reqValues := range reqParams {
			tapeValues, ok := tapeParams[key]
			if !ok {
				return 0
			}
			if !stringSlicesEqual(reqValues, tapeValues) {
				return 0
			}
		}
		return 4
	}
}

// MatchBodyHash returns a MatchCriterion that requires the SHA-256 hash of
// the incoming request's body to match the tape's BodyHash field.
// Returns score 8 on match, 0 on mismatch.
//
// If the tape's BodyHash is empty (e.g., a GET request with no body), and
// the incoming request also has no body (or empty body), this is a match.
// If the tape has a BodyHash but the request has no body (or vice versa),
// this is a mismatch.
//
// The request body is read fully, then restored (replaced with a new reader
// over the same bytes) so subsequent handlers or criteria can read it again.
func MatchBodyHash() MatchCriterion {
	return func(req *http.Request, candidate Tape) int {
		// Compute hash of incoming request body.
		var reqHash string
		if req.Body != nil {
			bodyBytes, err := io.ReadAll(req.Body)
			if err != nil {
				return 0
			}
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			reqHash = BodyHashFromBytes(bodyBytes)
		}

		// Compare with tape's stored hash.
		if candidate.Request.BodyHash == "" && reqHash == "" {
			return 8 // both empty — match
		}
		if candidate.Request.BodyHash == reqHash {
			return 8
		}
		return 0
	}
}

// CompositeMatcher evaluates a list of MatchCriterion functions against
// candidate tapes and returns the highest-scoring match. If all criteria
// return a positive score for a candidate, the candidate's total score is
// the sum of all criterion scores. If any criterion returns 0 for a
// candidate, that candidate is eliminated.
//
// If no candidates survive all criteria, CompositeMatcher returns (Tape{}, false).
// If multiple candidates have the same highest score, the first one in the
// candidates slice wins (stable ordering).
type CompositeMatcher struct {
	criteria []MatchCriterion
}

// NewCompositeMatcher creates a CompositeMatcher with the given criteria.
// At least one criterion must be provided. If no criteria are given,
// the matcher matches nothing (returns false for all requests).
func NewCompositeMatcher(criteria ...MatchCriterion) *CompositeMatcher {
	return &CompositeMatcher{criteria: criteria}
}

// Match implements the Matcher interface.
func (m *CompositeMatcher) Match(req *http.Request, candidates []Tape) (Tape, bool) {
	bestScore := 0
	bestIdx := -1

	for i, tape := range candidates {
		total := 0
		eliminated := false
		for _, criterion := range m.criteria {
			score := criterion(req, tape)
			if score == 0 {
				eliminated = true
				break
			}
			total += score
		}
		if eliminated {
			continue
		}
		if total > bestScore {
			bestScore = total
			bestIdx = i
		}
	}

	if bestIdx < 0 {
		return Tape{}, false
	}
	return candidates[bestIdx], true
}

// DefaultMatcher returns a Matcher that matches on HTTP method and URL path.
// This covers the most common use case (exact method + path matching) and is
// the recommended default for the Server.
//
// It is equivalent to NewCompositeMatcher(MatchMethod(), MatchPath()).
func DefaultMatcher() *CompositeMatcher {
	return NewCompositeMatcher(MatchMethod(), MatchPath())
}

// stringSlicesEqual reports whether two string slices contain the same elements
// in the same order.
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
