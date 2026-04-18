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
// The tape's URL (which is stored as a full URL string, e.g.,
// "https://example.com/path") is parsed to extract only the path component
// for comparison against the incoming request's URL.Path.
//
// This is intentionally minimal. For advanced matching (headers, body,
// query params, regex), use CompositeMatcher or DefaultMatcher.
func ExactMatcher() Matcher {
	return MatcherFunc(func(req *http.Request, candidates []Tape) (Tape, bool) {
		for _, t := range candidates {
			parsed, err := url.Parse(t.Request.URL)
			if err != nil {
				continue
			}
			if t.Request.Method == req.Method && parsed.Path == req.URL.Path {
				return t, true
			}
		}
		return Tape{}, false
	})
}

// Criterion evaluates how well a candidate Tape matches an incoming request
// for a single dimension (method, path, body, etc.).
type Criterion interface {
	// Score returns a match score:
	//   - 0 means the candidate does not match on this dimension (eliminates it).
	//   - A positive value means the candidate matches, with higher values
	//     indicating a stronger/more specific match.
	//
	// Implementations must not modify the candidate tape. They may read and
	// restore the request body but must leave the request otherwise unchanged.
	Score(req *http.Request, candidate Tape) int

	// Name returns a stable identifier for this criterion type.
	// Used for debugging, logging, and config-driven dispatch.
	// Built-in criteria use lowercase underscore-separated names
	// (e.g., "method", "path", "body_hash").
	Name() string
}

// CriterionFunc is an adapter to allow the use of ordinary functions as
// Criterion implementations. Its Name() method returns "custom".
type CriterionFunc func(req *http.Request, candidate Tape) int

// Score calls f(req, candidate).
func (f CriterionFunc) Score(req *http.Request, candidate Tape) int {
	return f(req, candidate)
}

// Name returns "custom" for ad-hoc functional criteria.
func (f CriterionFunc) Name() string {
	return "custom"
}

// MethodCriterion matches on HTTP method.
// Returns score 1 on match, 0 on mismatch.
type MethodCriterion struct{}

// Score returns 1 if the request method matches the candidate tape's method,
// 0 otherwise.
func (MethodCriterion) Score(req *http.Request, candidate Tape) int {
	if req.Method == candidate.Request.Method {
		return 1
	}
	return 0
}

// Name returns "method".
func (MethodCriterion) Name() string { return "method" }

// PathCriterion matches on URL path (exact).
// It compares the incoming request's URL.Path against the path component of
// the tape's stored URL. Returns score 2 on match, 0 on mismatch.
//
// The tape's URL is stored as a full URL string (e.g., "https://api.example.com/users?page=1").
// PathCriterion parses it with url.Parse and compares only the Path component.
// If the tape's URL cannot be parsed, the criterion returns 0.
type PathCriterion struct{}

// Score returns 2 if the request path matches the candidate tape's URL path,
// 0 otherwise.
func (PathCriterion) Score(req *http.Request, candidate Tape) int {
	parsed, err := url.Parse(candidate.Request.URL)
	if err != nil {
		return 0
	}
	if req.URL.Path == parsed.Path {
		return 2
	}
	return 0
}

// Name returns "path".
func (PathCriterion) Name() string { return "path" }

// PathRegexCriterion matches the incoming request's URL path against a compiled
// regular expression, and also verifies that the candidate tape's stored URL
// path matches the same expression. This ensures that only tapes belonging to
// the same "path family" as the request are considered matches.
//
// Returns score 1 on match, 0 on mismatch.
//
// Usage: use PathRegexCriterion as a replacement for PathCriterion when regex
// matching is desired, not alongside it. If PathCriterion is also present in
// the same CompositeMatcher, candidates that do not exact-match will be
// eliminated by PathCriterion (score 0) regardless of the regex result.
//
// Example:
//
//	criterion, err := NewPathRegexCriterion(`^/users/\d+/orders$`)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	matcher := NewCompositeMatcher(MethodCriterion{}, criterion)
type PathRegexCriterion struct {
	// Pattern is the original regex pattern string.
	Pattern string
	re      *regexp.Regexp
}

// NewPathRegexCriterion compiles the pattern and returns a PathRegexCriterion.
// Returns an error if the pattern is not a valid regular expression.
func NewPathRegexCriterion(pattern string) (*PathRegexCriterion, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("httptape: invalid path regex %q: %w", pattern, err)
	}
	return &PathRegexCriterion{Pattern: pattern, re: re}, nil
}

// Score returns 1 if both the request path and the candidate tape's URL path
// match the compiled regex, 0 otherwise.
func (c *PathRegexCriterion) Score(req *http.Request, candidate Tape) int {
	if !c.re.MatchString(req.URL.Path) {
		return 0
	}
	parsed, err := url.Parse(candidate.Request.URL)
	if err != nil {
		return 0
	}
	if !c.re.MatchString(parsed.Path) {
		return 0
	}
	return 1
}

// Name returns "path_regex".
func (c *PathRegexCriterion) Name() string { return "path_regex" }

// RouteCriterion matches on the tape's Route field.
// Returns score 1 on match, 0 on mismatch.
// If Route is empty, the criterion always returns 1 (matches any tape).
type RouteCriterion struct {
	Route string
}

// Score returns 1 if the candidate tape's Route matches, 0 otherwise.
// If Route is empty, the criterion always returns 1.
func (c RouteCriterion) Score(_ *http.Request, candidate Tape) int {
	if c.Route == "" {
		return 1
	}
	if candidate.Route == c.Route {
		return 1
	}
	return 0
}

// Name returns "route".
func (c RouteCriterion) Name() string { return "route" }

// QueryParamsCriterion matches on query parameters (subset match).
// It requires all query parameters from the incoming request to be present
// in the tape's stored URL with the same values. Extra parameters in the
// tape are allowed.
// Returns score 4 on match, 0 on mismatch.
//
// If the incoming request has no query parameters, this criterion always
// returns 4 (vacuously true -- all zero params match).
//
// The tape's URL is parsed with url.Parse to extract query parameters.
// If parsing fails, the criterion returns 0.
type QueryParamsCriterion struct{}

// Score returns 4 if the request query parameters are a subset of the
// candidate tape's query parameters, 0 otherwise.
func (QueryParamsCriterion) Score(req *http.Request, candidate Tape) int {
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

// Name returns "query_params".
func (QueryParamsCriterion) Name() string { return "query_params" }

// HeadersCriterion matches a specific header key-value pair.
// It requires the specified header to be present in both the incoming request
// and the candidate tape's recorded request, with an exact value match.
//
// The header name is canonicalized using http.CanonicalHeaderKey at score time,
// making it case-insensitive per HTTP specification (RFC 7230 section 3.2).
// The header value comparison is exact and case-sensitive.
//
// If the header has multiple values in either the request or the tape, the
// criterion checks whether the specified value appears among them (any-of
// semantics).
//
// To require multiple headers, add multiple HeadersCriterion instances to the
// CompositeMatcher. They are AND-ed together naturally: if any criterion
// returns 0, the candidate is eliminated.
//
// Returns score 3 on match, 0 on mismatch.
//
// Example:
//
//	matcher := NewCompositeMatcher(
//	    MethodCriterion{},
//	    PathCriterion{},
//	    HeadersCriterion{Key: "Accept", Value: "application/vnd.api.v2+json"},
//	    HeadersCriterion{Key: "X-Feature-Flag", Value: "new-checkout"},
//	)
type HeadersCriterion struct {
	Key   string
	Value string
}

// Score returns 3 if both the request and the candidate tape contain the
// specified header key-value pair, 0 otherwise.
func (c HeadersCriterion) Score(req *http.Request, candidate Tape) int {
	canonicalKey := http.CanonicalHeaderKey(c.Key)
	if !headerContains(req.Header, canonicalKey, c.Value) {
		return 0
	}
	if !headerContains(candidate.Request.Headers, canonicalKey, c.Value) {
		return 0
	}
	return 3
}

// Name returns "headers".
func (c HeadersCriterion) Name() string { return "headers" }

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

// BodyHashCriterion matches on SHA-256 body hash.
// It requires the SHA-256 hash of the incoming request's body to match the
// tape's BodyHash field.
// Returns score 8 on match, 0 on mismatch.
//
// If the tape's BodyHash is empty (e.g., a GET request with no body), and
// the incoming request also has no body (or empty body), this is a match.
// If the tape has a BodyHash but the request has no body (or vice versa),
// this is a mismatch.
//
// The request body is read fully, then restored (replaced with a new reader
// over the same bytes) so subsequent handlers or criteria can read it again.
//
// Performance note: the body is re-read and the SHA-256 hash is recomputed
// for each candidate tape. For large candidate sets, pre-computing the hash
// once before the candidate loop would be more efficient. This is acceptable
// for v1 (see ADR-4) but should be optimized if matching performance becomes
// a bottleneck.
//
// TODO: pre-compute request body hash once before candidate iteration.
type BodyHashCriterion struct{}

// Score returns 8 if the request body hash matches the candidate tape's
// BodyHash, 0 otherwise.
func (BodyHashCriterion) Score(req *http.Request, candidate Tape) int {
	// Compute hash of incoming request body.
	// NOTE: This recomputes the hash per candidate. The body bytes are
	// cached in memory after the first read (via bytes.NewReader), but
	// the SHA-256 hash is recomputed each time. See TODO above.
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
		return 8 // both empty -- match
	}
	if candidate.Request.BodyHash == reqHash {
		return 8
	}
	return 0
}

// Name returns "body_hash".
func (BodyHashCriterion) Name() string { return "body_hash" }

// BodyFuzzyCriterion compares specific fields in the JSON request body between
// the incoming request and the candidate tape. Only the fields at the specified
// paths are compared; all other fields are ignored. This is useful when request
// bodies contain volatile fields (timestamps, nonces, request IDs) that vary
// per invocation.
//
// Paths use the same JSONPath-like syntax as RedactBodyPaths and FakeFields:
//   - $.field             -- top-level field
//   - $.nested.field      -- nested field access
//   - $.array[*].field    -- field within each element of an array
//
// Matching semantics:
//   - If both the incoming request body and the tape body are absent
//     (nil, empty, or not valid JSON), the criterion returns 1 (vacuous
//     match -- the body dimension is irrelevant for this request/tape
//     pair). If exactly one side is absent, the criterion returns 0.
//   - When both bodies are present (not absent per the rule above),
//     they are unmarshaled as JSON. Path extraction and comparison
//     proceeds on the unmarshaled values.
//   - For each specified path, the value is extracted from both the request
//     and the tape body. If a path does not exist in both bodies, it is
//     skipped (does not cause a mismatch).
//   - If a path exists in both bodies, the extracted values must be
//     deeply equal (compared via reflect.DeepEqual on the unmarshaled
//     any values). If any compared field differs, the criterion returns 0.
//   - If no paths are provided, or no paths match fields present in both
//     bodies, the criterion returns 0 (no match -- nothing to compare means
//     no evidence of a match).
//   - If at least one path matched and all matched fields are equal, the
//     criterion returns its score.
//
// The request body is read fully, then restored (replaced with a new reader
// over the same bytes) so subsequent criteria can read it again.
//
// Invalid or unsupported paths are silently ignored (same as RedactBodyPaths).
//
// Returns score 6 on match, 1 on vacuous match (both bodies absent),
// 0 on mismatch.
//
// Note: using both BodyFuzzyCriterion and BodyHashCriterion in the same
// CompositeMatcher is safe but semantically redundant. If BodyHashCriterion
// passes (exact match), BodyFuzzyCriterion will also pass. If BodyHashCriterion
// fails, the candidate is already eliminated. Choose one or the other.
//
// Example:
//
//	matcher := NewCompositeMatcher(
//	    MethodCriterion{},
//	    PathCriterion{},
//	    NewBodyFuzzyCriterion("$.action", "$.user.id", "$.items[*].sku"),
//	)
type BodyFuzzyCriterion struct {
	// Paths contains the JSONPath-like expressions to compare.
	Paths  []string
	parsed []parsedPath
}

// NewBodyFuzzyCriterion creates a BodyFuzzyCriterion with the given paths.
// Invalid paths are silently skipped (same behavior as the previous MatchBodyFuzzy).
func NewBodyFuzzyCriterion(paths ...string) *BodyFuzzyCriterion {
	var parsed []parsedPath
	for _, p := range paths {
		if pp, ok := parsePath(p); ok {
			parsed = append(parsed, pp)
		}
	}
	return &BodyFuzzyCriterion{Paths: paths, parsed: parsed}
}

// Score returns 6 if all compared fields match, 1 on vacuous match (both
// bodies absent), 0 on mismatch.
func (c *BodyFuzzyCriterion) Score(req *http.Request, candidate Tape) int {
	if len(c.parsed) == 0 {
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

	// Determine whether each side is "absent" (nil, empty, or not valid JSON).
	var reqData, tapeData any
	reqAbsent := len(reqBody) == 0 || json.Unmarshal(reqBody, &reqData) != nil
	tapeAbsent := len(candidate.Request.Body) == 0 || json.Unmarshal(candidate.Request.Body, &tapeData) != nil

	// Vacuous-true: both bodies absent -- return minimum positive score.
	if reqAbsent && tapeAbsent {
		return 1
	}
	// Asymmetric: one absent, one present -- mismatch.
	if reqAbsent || tapeAbsent {
		return 0
	}

	// Compare specified fields.
	matched := 0
	for _, p := range c.parsed {
		reqVal, reqOk := extractAtPath(reqData, p.segments)
		tapeVal, tapeOk := extractAtPath(tapeData, p.segments)

		if !reqOk || !tapeOk {
			// Path doesn't exist in one or both -- skip, not a mismatch.
			continue
		}

		if !reflect.DeepEqual(reqVal, tapeVal) {
			return 0 // field exists in both but values differ -- eliminate.
		}
		matched++
	}

	if matched == 0 {
		return 0 // no fields compared -- no evidence of match.
	}
	return 6
}

// Name returns "body_fuzzy".
func (c *BodyFuzzyCriterion) Name() string { return "body_fuzzy" }

// CompositeMatcher evaluates a list of Criterion implementations against
// candidate tapes and returns the highest-scoring match. If all criteria
// return a positive score for a candidate, the candidate's total score is
// the sum of all criterion scores. If any criterion returns 0 for a
// candidate, that candidate is eliminated.
//
// Score weight table for built-in criteria:
//
//	MethodCriterion:      1
//	PathCriterion:        2
//	RouteCriterion:       1
//	PathRegexCriterion:   1
//	HeadersCriterion:     3
//	QueryParamsCriterion: 4
//	BodyFuzzyCriterion:   6
//	BodyHashCriterion:    8
//
// The original design (ADR-4) used powers-of-two-ish weights so each
// higher-specificity criterion dominates all lower ones combined. Later
// additions (HeadersCriterion=3, BodyFuzzyCriterion=6) create a gap where
// QueryParamsCriterion(4) + HeadersCriterion(3) = 7 > BodyFuzzyCriterion(6).
// This is acceptable for practical use -- body-fuzzy matches are typically
// used without query-param matching -- but the strict dominance property no
// longer holds perfectly.
//
// If no candidates survive all criteria, CompositeMatcher returns (Tape{}, false).
// If multiple candidates have the same highest score, the first one in the
// candidates slice wins (stable ordering).
//
// CompositeMatcher is safe for concurrent use -- immutable after construction.
type CompositeMatcher struct {
	criteria []Criterion
}

// NewCompositeMatcher creates a CompositeMatcher with the given criteria.
// At least one criterion must be provided. If no criteria are given,
// the matcher matches nothing (returns false for all requests).
func NewCompositeMatcher(criteria ...Criterion) *CompositeMatcher {
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
			score := criterion.Score(req, tape)
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
// It is equivalent to NewCompositeMatcher(MethodCriterion{}, PathCriterion{}).
func DefaultMatcher() *CompositeMatcher {
	return NewCompositeMatcher(MethodCriterion{}, PathCriterion{})
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
