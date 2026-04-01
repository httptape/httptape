package httptape

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
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
// query params, regex), use CompositeMatcher or DefaultMatcher.
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

// MatchCriterion evaluates how well a candidate Tape matches an incoming
// request for a single dimension (method, path, body, etc.).
//
// It returns a score:
//   - 0 means the candidate does not match on this dimension (eliminates it).
//   - A positive value means the candidate matches, with higher values
//     indicating a stronger/more specific match.
//
// Implementations must not modify the candidate tape. They may read and
// restore the request body (as MatchBodyHash does) but must leave the
// request otherwise unchanged.
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

// MatchPathRegex returns a MatchCriterion that matches the incoming request's
// URL path against a compiled regular expression, and also verifies that the
// candidate tape's stored URL path matches the same expression. This ensures
// that only tapes belonging to the same "path family" as the request are
// considered matches.
//
// The pattern is compiled once at construction time using regexp.Compile.
// If the pattern is invalid, MatchPathRegex returns a non-nil error and a
// nil MatchCriterion. Callers must check the error before using the criterion.
//
// Returns score 1 on match, 0 on mismatch.
//
// Usage: use MatchPathRegex as a replacement for MatchPath when regex matching
// is desired, not alongside it. If MatchPath is also present in the same
// CompositeMatcher, candidates that do not exact-match will be eliminated by
// MatchPath (score 0) regardless of the regex result.
//
// Example:
//
//	criterion, err := MatchPathRegex(`^/users/\d+/orders$`)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	matcher := NewCompositeMatcher(MatchMethod(), criterion)
func MatchPathRegex(pattern string) (MatchCriterion, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("httptape: invalid path regex %q: %w", pattern, err)
	}
	return func(req *http.Request, candidate Tape) int {
		if !re.MatchString(req.URL.Path) {
			return 0
		}
		parsed, err := url.Parse(candidate.Request.URL)
		if err != nil {
			return 0
		}
		if !re.MatchString(parsed.Path) {
			return 0
		}
		return 1
	}, nil
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

// MatchHeaders returns a MatchCriterion that requires the specified header to
// be present in both the incoming request and the candidate tape's recorded
// request, with an exact value match.
//
// The header name is canonicalized using http.CanonicalHeaderKey, making it
// case-insensitive per HTTP specification (RFC 7230 section 3.2). The header
// value comparison is exact and case-sensitive.
//
// If the header has multiple values in either the request or the tape, the
// criterion checks whether the specified value appears among them (any-of
// semantics). This handles the common case where a header may be set multiple
// times (e.g., multiple Accept values).
//
// To require multiple headers, add multiple MatchHeaders criteria to the
// CompositeMatcher. They are AND-ed together naturally: if any criterion
// returns 0, the candidate is eliminated.
//
// Returns score 3 on match, 0 on mismatch.
//
// Example:
//
//	matcher := NewCompositeMatcher(
//	    MatchMethod(),
//	    MatchPath(),
//	    MatchHeaders("Accept", "application/vnd.api.v2+json"),
//	    MatchHeaders("X-Feature-Flag", "new-checkout"),
//	)
func MatchHeaders(key, value string) MatchCriterion {
	canonicalKey := http.CanonicalHeaderKey(key)
	return func(req *http.Request, candidate Tape) int {
		if !headerContains(req.Header, canonicalKey, value) {
			return 0
		}
		if !headerContains(candidate.Request.Headers, canonicalKey, value) {
			return 0
		}
		return 3
	}
}

// headerContains reports whether the header map contains the specified
// canonical key with the specified value among its values.
func headerContains(h http.Header, canonicalKey, value string) bool {
	values := h[canonicalKey]
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

// MatchBodyFuzzy returns a MatchCriterion that compares specific fields in
// the JSON request body between the incoming request and the candidate tape.
// Only the fields at the specified paths are compared; all other fields are
// ignored. This is useful when request bodies contain volatile fields
// (timestamps, nonces, request IDs) that vary per invocation.
//
// Paths use the same JSONPath-like syntax as RedactBodyPaths and FakeFields:
//   - $.field             -- top-level field
//   - $.nested.field      -- nested field access
//   - $.array[*].field    -- field within each element of an array
//
// Matching semantics:
//   - Both bodies are unmarshaled as JSON. If either body is not valid JSON,
//     the criterion returns 0 (no match).
//   - For each specified path, the value is extracted from both the request
//     and the tape body. If a path does not exist in both bodies, it is
//     skipped (does not cause a mismatch).
//   - If a path exists in both bodies, the extracted values must be
//     deeply equal (compared via reflect.DeepEqual on the unmarshaled
//     any values). If any compared field differs, the criterion returns 0.
//   - If no paths are provided, or no paths match fields present in both
//     bodies, the criterion returns 0 (no match — nothing to compare means
//     no evidence of a match).
//   - If at least one path matched and all matched fields are equal, the
//     criterion returns its score.
//
// The request body is read fully, then restored (replaced with a new reader
// over the same bytes) so subsequent criteria can read it again.
//
// Invalid or unsupported paths are silently ignored (same as RedactBodyPaths).
//
// Returns score 6 on match, 0 on mismatch.
//
// Note: using both MatchBodyFuzzy and MatchBodyHash in the same
// CompositeMatcher is safe but semantically redundant. If MatchBodyHash
// passes (exact match), MatchBodyFuzzy will also pass. If MatchBodyHash
// fails, the candidate is already eliminated. Choose one or the other.
//
// Example:
//
//	matcher := NewCompositeMatcher(
//	    MatchMethod(),
//	    MatchPath(),
//	    MatchBodyFuzzy("$.action", "$.user.id", "$.items[*].sku"),
//	)
func MatchBodyFuzzy(paths ...string) MatchCriterion {
	// Parse all paths at construction time (reuses parsePath from sanitizer.go).
	var parsed []parsedPath
	for _, p := range paths {
		if pp, ok := parsePath(p); ok {
			parsed = append(parsed, pp)
		}
	}

	return func(req *http.Request, candidate Tape) int {
		if len(parsed) == 0 {
			return 0
		}

		// Read and restore the incoming request body.
		var reqBody []byte
		if req.Body != nil {
			bodyBytes, err := io.ReadAll(req.Body)
			if err != nil {
				return 0
			}
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			reqBody = bodyBytes
		}

		// Unmarshal both bodies.
		var reqData, tapeData any
		if err := json.Unmarshal(reqBody, &reqData); err != nil {
			return 0
		}
		if err := json.Unmarshal(candidate.Request.Body, &tapeData); err != nil {
			return 0
		}

		// Compare specified fields.
		matched := 0
		for _, p := range parsed {
			reqVal, reqOk := extractAtPath(reqData, p.segments)
			tapeVal, tapeOk := extractAtPath(tapeData, p.segments)

			if !reqOk || !tapeOk {
				// Path doesn't exist in one or both — skip, not a mismatch.
				continue
			}

			if !reflect.DeepEqual(reqVal, tapeVal) {
				return 0 // field exists in both but values differ — eliminate.
			}
			matched++
		}

		if matched == 0 {
			return 0 // no fields compared — no evidence of match.
		}
		return 6
	}
}

// extractAtPath traverses the JSON structure following the given segments
// and returns the value at the leaf. Returns (value, true) if the path
// exists, or (nil, false) if any segment is missing or the structure does
// not match (e.g., expected object but found array).
//
// For wildcard segments (array[*].field), it collects the matching values
// from all array elements into a []any slice and returns that.
func extractAtPath(data any, segments []segment) (any, bool) {
	if len(segments) == 0 {
		return data, true
	}

	seg := segments[0]
	rest := segments[1:]

	obj, ok := data.(map[string]any)
	if !ok {
		return nil, false
	}

	val, exists := obj[seg.key]
	if !exists {
		return nil, false
	}

	if seg.wildcard {
		arr, ok := val.([]any)
		if !ok {
			return nil, false
		}
		if len(rest) == 0 {
			// Wildcard at leaf: return the array itself.
			return arr, true
		}
		// Collect values from each element.
		collected := make([]any, 0, len(arr))
		for _, elem := range arr {
			v, ok := extractAtPath(elem, rest)
			if !ok {
				return nil, false // all-or-nothing for arrays
			}
			collected = append(collected, v)
		}
		return collected, true
	}

	return extractAtPath(val, rest)
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
