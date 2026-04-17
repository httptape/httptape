package httptape

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
)

// DiffStatus categorizes the outcome of comparing a live API response against
// a recorded fixture.
type DiffStatus int

const (
	// DiffMatched indicates the live response matched the fixture with no
	// differences (within tolerance).
	DiffMatched DiffStatus = iota

	// DiffDrifted indicates the live response differs from the fixture.
	// The DiffResult will contain details about the specific differences.
	DiffDrifted

	// DiffNoFixture indicates no fixture was found for the given request.
	// This represents a coverage gap in the fixture set.
	DiffNoFixture
)

// FieldChangeKind categorizes a single field-level difference within a JSON
// body comparison.
type FieldChangeKind int

const (
	// FieldAdded indicates a field present in the live response but absent
	// in the fixture.
	FieldAdded FieldChangeKind = iota

	// FieldRemoved indicates a field present in the fixture but absent in
	// the live response.
	FieldRemoved

	// FieldChanged indicates a field present in both but with differing
	// values.
	FieldChanged
)

// StatusCodeDiff reports a status code difference between fixture and live
// response.
type StatusCodeDiff struct {
	// Old is the status code from the fixture.
	Old int `json:"old"`

	// New is the status code from the live response.
	New int `json:"new"`
}

// HeaderDiff reports a single response header difference.
type HeaderDiff struct {
	// Name is the canonical header name.
	Name string `json:"name"`

	// Kind describes the type of change: FieldAdded, FieldRemoved, or
	// FieldChanged.
	Kind FieldChangeKind `json:"kind"`

	// OldValues contains the header values from the fixture. Nil for added
	// headers.
	OldValues []string `json:"old_values,omitempty"`

	// NewValues contains the header values from the live response. Nil for
	// removed headers.
	NewValues []string `json:"new_values,omitempty"`
}

// BodyFieldDiff reports a single field-level difference in a JSON response
// body.
type BodyFieldDiff struct {
	// Path is the JSONPath-like path to the field (e.g., "$.user.email").
	Path string `json:"path"`

	// Kind describes the type of change: FieldAdded, FieldRemoved, or
	// FieldChanged.
	Kind FieldChangeKind `json:"kind"`

	// OldValue is the value from the fixture. Nil for added fields.
	OldValue any `json:"old_value,omitempty"`

	// NewValue is the value from the live response. Nil for removed fields.
	NewValue any `json:"new_value,omitempty"`
}

// DiffResult contains the comparison outcome for a single request.
type DiffResult struct {
	// RequestMethod is the HTTP method of the request that was diffed.
	RequestMethod string `json:"request_method"`

	// RequestURL is the URL of the request that was diffed.
	RequestURL string `json:"request_url"`

	// FixtureID is the ID of the matched fixture. Empty when Status is
	// DiffNoFixture.
	FixtureID string `json:"fixture_id,omitempty"`

	// Status categorizes the overall outcome.
	Status DiffStatus `json:"status"`

	// StatusCode reports the status code difference, if any. Nil when
	// status codes match or when Status is DiffNoFixture.
	StatusCode *StatusCodeDiff `json:"status_code,omitempty"`

	// Headers lists response header differences. Empty when headers match
	// or when Status is DiffNoFixture.
	Headers []HeaderDiff `json:"headers,omitempty"`

	// BodyFields lists JSON body field differences. Empty when bodies
	// match, when the body is non-JSON, or when Status is DiffNoFixture.
	BodyFields []BodyFieldDiff `json:"body_fields,omitempty"`

	// BodyChanged is true when a non-JSON body differs by hash. It is only
	// meaningful when the response body is not valid JSON.
	BodyChanged bool `json:"body_changed,omitempty"`

	// Error is set when the live request failed (transport error). The
	// result will have Status == DiffDrifted in this case.
	Error string `json:"error,omitempty"`
}

// StaleFixture identifies a fixture that was not matched by any request in
// the diff batch.
type StaleFixture struct {
	// ID is the tape's unique identifier.
	ID string `json:"id"`

	// Route is the tape's route label.
	Route string `json:"route"`

	// Method is the tape's recorded request method.
	Method string `json:"method"`

	// URL is the tape's recorded request URL.
	URL string `json:"url"`
}

// DiffReport is the aggregate output of a diff operation. It contains one
// DiffResult per input request plus a list of stale fixtures.
type DiffReport struct {
	// Results contains one entry per input request, in the same order as
	// the input slice.
	Results []DiffResult `json:"results"`

	// Stale lists fixtures in the store that were not matched by any input
	// request.
	Stale []StaleFixture `json:"stale,omitempty"`
}

// DiffOption configures the behavior of the Diff function.
type DiffOption func(*diffConfig)

// diffConfig holds all resolved options for a Diff invocation.
type diffConfig struct {
	matcher       Matcher
	sanitizer     Sanitizer
	ignorePaths   []parsedPath
	ignoreHeaders map[string]struct{}
}

// WithDiffMatcher sets the Matcher used to locate the fixture corresponding
// to each request. If not set, DefaultMatcher() is used.
func WithDiffMatcher(m Matcher) DiffOption {
	return func(c *diffConfig) {
		if m != nil {
			c.matcher = m
		}
	}
}

// WithDiffSanitizer sets the Sanitizer applied to live responses before
// comparison against fixtures. This must be the same sanitizer (same pipeline
// and seed) that was used when recording the fixtures. If not set, live
// responses are compared as-is.
func WithDiffSanitizer(s Sanitizer) DiffOption {
	return func(c *diffConfig) {
		c.sanitizer = s
	}
}

// WithIgnorePaths specifies JSONPath-like field paths to exclude from body
// comparison. This is used for volatile fields (timestamps, request IDs)
// that change on every request. Paths use the same syntax as RedactBodyPaths
// and FakeFields: $.field, $.nested.field, $.array[*].field.
//
// Invalid paths are silently ignored.
func WithIgnorePaths(paths ...string) DiffOption {
	return func(c *diffConfig) {
		for _, p := range paths {
			if pp, ok := parsePath(p); ok {
				c.ignorePaths = append(c.ignorePaths, pp)
			}
		}
	}
}

// WithIgnoreHeaders specifies response header names to exclude from header
// comparison. Header names are canonicalized (case-insensitive matching).
func WithIgnoreHeaders(names ...string) DiffOption {
	return func(c *diffConfig) {
		for _, n := range names {
			c.ignoreHeaders[http.CanonicalHeaderKey(n)] = struct{}{}
		}
	}
}

// Diff executes each request against the live API via the provided transport,
// finds the corresponding fixture in the store using the configured matcher,
// and produces a structured diff report comparing live responses against
// recorded fixtures.
//
// Requests are executed sequentially in the order provided. Users who want
// parallelism should partition requests across goroutines and call Diff
// separately for each partition.
//
// Transport errors (failure to reach the upstream) result in DiffDrifted
// status for that request, not a short-circuit of the entire operation.
//
// After all requests are processed, any fixtures in the store that were not
// matched by any request are reported as stale.
//
// The function returns an error only for infrastructure failures that prevent
// the operation from starting (e.g., store enumeration failure). Per-request
// transport errors are captured in each DiffResult.
func Diff(ctx context.Context, store Store, transport http.RoundTripper, requests []*http.Request, opts ...DiffOption) (DiffReport, error) {
	cfg := &diffConfig{
		matcher:       DefaultMatcher(),
		ignoreHeaders: make(map[string]struct{}),
	}
	for _, opt := range opts {
		opt(cfg)
	}

	// Load all fixtures from the store.
	allTapes, err := store.List(ctx, Filter{})
	if err != nil {
		return DiffReport{}, fmt.Errorf("httptape: diff list fixtures: %w", err)
	}

	// Track which fixture IDs are matched.
	matchedIDs := make(map[string]struct{}, len(allTapes))

	results := make([]DiffResult, 0, len(requests))

	for _, req := range requests {
		if err := ctx.Err(); err != nil {
			return DiffReport{}, fmt.Errorf("httptape: diff cancelled: %w", err)
		}

		result := diffOneRequest(ctx, cfg, transport, req, allTapes)
		if result.FixtureID != "" {
			matchedIDs[result.FixtureID] = struct{}{}
		}
		results = append(results, result)
	}

	// Identify stale fixtures.
	var stale []StaleFixture
	for _, t := range allTapes {
		if _, ok := matchedIDs[t.ID]; !ok {
			stale = append(stale, StaleFixture{
				ID:     t.ID,
				Route:  t.Route,
				Method: t.Request.Method,
				URL:    t.Request.URL,
			})
		}
	}

	return DiffReport{
		Results: results,
		Stale:   stale,
	}, nil
}

// diffOneRequest handles the diff logic for a single request: execute against
// the live API, find the fixture, apply sanitization, and compare.
func diffOneRequest(ctx context.Context, cfg *diffConfig, transport http.RoundTripper, req *http.Request, allTapes []Tape) DiffResult {
	result := DiffResult{
		RequestMethod: req.Method,
		RequestURL:    req.URL.String(),
	}

	// Capture request body so it can be re-read for matching and transport.
	var reqBodyBytes []byte
	if req.Body != nil {
		var err error
		reqBodyBytes, err = io.ReadAll(req.Body)
		if err != nil {
			result.Status = DiffDrifted
			result.Error = fmt.Sprintf("read request body: %s", err)
			return result
		}
		req.Body = io.NopCloser(bytes.NewReader(reqBodyBytes))
	}

	// Find fixture match.
	fixture, ok := cfg.matcher.Match(req, allTapes)
	if !ok {
		result.Status = DiffNoFixture
		return result
	}
	result.FixtureID = fixture.ID

	// Restore request body for transport.
	req.Body = io.NopCloser(bytes.NewReader(reqBodyBytes))

	// Execute the live request.
	resp, err := transport.RoundTrip(req.WithContext(ctx))
	if err != nil {
		result.Status = DiffDrifted
		result.Error = fmt.Sprintf("transport: %s", err)
		return result
	}
	defer resp.Body.Close()

	// Read live response body.
	liveBody, err := io.ReadAll(resp.Body)
	if err != nil {
		result.Status = DiffDrifted
		result.Error = fmt.Sprintf("read response body: %s", err)
		return result
	}

	// Build a temporary tape from the live request+response and apply
	// sanitizer if configured. Only the response portion is used for
	// comparison, but sanitizers operate on full Tapes.
	liveResp := RecordedResp{
		StatusCode: resp.StatusCode,
		Headers:    resp.Header.Clone(),
		Body:       liveBody,
	}
	if cfg.sanitizer != nil {
		liveReq := RecordedReq{
			Method:   req.Method,
			URL:      req.URL.String(),
			Headers:  req.Header.Clone(),
			Body:     reqBodyBytes,
			BodyHash: BodyHashFromBytes(reqBodyBytes),
		}
		tmpTape := Tape{
			Request:  liveReq,
			Response: liveResp,
		}
		tmpTape = cfg.sanitizer.Sanitize(tmpTape)
		liveResp = tmpTape.Response
	}

	// Compare status codes.
	if fixture.Response.StatusCode != liveResp.StatusCode {
		result.StatusCode = &StatusCodeDiff{
			Old: fixture.Response.StatusCode,
			New: liveResp.StatusCode,
		}
	}

	// Compare headers.
	result.Headers = diffHeaders(fixture.Response.Headers, liveResp.Headers, cfg.ignoreHeaders)

	// Compare bodies.
	fixtureIsJSON := isJSONBody(fixture.Response.Body)
	liveIsJSON := isJSONBody(liveResp.Body)

	if fixtureIsJSON && liveIsJSON {
		result.BodyFields = diffJSONBodies(fixture.Response.Body, liveResp.Body, cfg.ignorePaths)
	} else {
		// Non-JSON: compare by hash.
		fixtureHash := bodyHash(fixture.Response.Body)
		liveHash := bodyHash(liveResp.Body)
		if fixtureHash != liveHash {
			result.BodyChanged = true
		}
	}

	// Determine overall status.
	if result.StatusCode != nil || len(result.Headers) > 0 || len(result.BodyFields) > 0 || result.BodyChanged {
		result.Status = DiffDrifted
	} else {
		result.Status = DiffMatched
	}

	return result
}

// isJSONBody checks whether the given body bytes are valid JSON.
func isJSONBody(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var v any
	return json.Unmarshal(body, &v) == nil
}

// bodyHash computes a hex-encoded SHA-256 hash of the given bytes.
// Returns empty string for nil/empty input.
func bodyHash(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// diffHeaders compares fixture and live response headers, returning a list
// of differences. Headers in the ignore set are excluded from comparison.
func diffHeaders(fixture, live http.Header, ignore map[string]struct{}) []HeaderDiff {
	var diffs []HeaderDiff

	// Collect all header names from both sides.
	allNames := make(map[string]struct{})
	for name := range fixture {
		allNames[http.CanonicalHeaderKey(name)] = struct{}{}
	}
	for name := range live {
		allNames[http.CanonicalHeaderKey(name)] = struct{}{}
	}

	// Sort for deterministic output.
	sortedNames := make([]string, 0, len(allNames))
	for name := range allNames {
		sortedNames = append(sortedNames, name)
	}
	sort.Strings(sortedNames)

	for _, name := range sortedNames {
		if _, ok := ignore[name]; ok {
			continue
		}

		fixtureVals := fixture[name]
		liveVals := live[name]

		if fixtureVals == nil && liveVals != nil {
			diffs = append(diffs, HeaderDiff{
				Name:      name,
				Kind:      FieldAdded,
				NewValues: copyStringSlice(liveVals),
			})
		} else if fixtureVals != nil && liveVals == nil {
			diffs = append(diffs, HeaderDiff{
				Name:      name,
				Kind:      FieldRemoved,
				OldValues: copyStringSlice(fixtureVals),
			})
		} else if !stringSlicesEqual(fixtureVals, liveVals) {
			diffs = append(diffs, HeaderDiff{
				Name:      name,
				Kind:      FieldChanged,
				OldValues: copyStringSlice(fixtureVals),
				NewValues: copyStringSlice(liveVals),
			})
		}
	}

	return diffs
}

// copyStringSlice returns a copy of the string slice.
func copyStringSlice(s []string) []string {
	if s == nil {
		return nil
	}
	cp := make([]string, len(s))
	copy(cp, s)
	return cp
}

// diffJSONBodies performs a recursive comparison of two JSON bodies and returns
// field-level differences. Fields at ignored paths are excluded.
func diffJSONBodies(fixtureBody, liveBody []byte, ignorePaths []parsedPath) []BodyFieldDiff {
	var fixtureData, liveData any
	if err := json.Unmarshal(fixtureBody, &fixtureData); err != nil {
		return nil
	}
	if err := json.Unmarshal(liveBody, &liveData); err != nil {
		return nil
	}

	var diffs []BodyFieldDiff
	compareJSON(fixtureData, liveData, nil, ignorePaths, &diffs)
	return diffs
}

// pathSegment represents one step in a concrete traversal path. It is used
// to build up the current path during JSON comparison and to format the
// path for display in BodyFieldDiff results.
type pathSegment struct {
	// key is the object field name. Empty for array index segments.
	key string
	// index is the array index. Only valid when isIndex is true.
	index int
	// isIndex is true when this segment represents an array index rather
	// than an object key.
	isIndex bool
}

// formatPath converts a slice of pathSegments into a JSONPath-like string
// for display (e.g., "$.user.addresses.0.city").
func formatPath(segs []pathSegment) string {
	var b bytes.Buffer
	b.WriteByte('$')
	for _, seg := range segs {
		b.WriteByte('.')
		if seg.isIndex {
			b.WriteString(strconv.Itoa(seg.index))
		} else {
			b.WriteString(seg.key)
		}
	}
	return b.String()
}

// isIgnored checks whether the current concrete path matches any of the
// ignore path patterns. The matching handles wildcards: a pattern segment
// with wildcard=true matches any array-index concrete segment, while a
// non-wildcard pattern segment must match the concrete key exactly.
func isIgnored(concretePath []pathSegment, ignorePaths []parsedPath) bool {
	for _, ip := range ignorePaths {
		if matchesIgnorePath(concretePath, ip.segments) {
			return true
		}
	}
	return false
}

// matchesIgnorePath checks whether a concrete path matches an ignore
// pattern. A concrete path like [key:"items", index:0, key:"id"] matches a
// pattern like [key:"items" wildcard:true, key:"id"].
//
// The matching is done by consuming both slices in lockstep:
//   - A pattern segment with wildcard=true matches the pattern key against the
//     concrete key AND then expects the NEXT concrete segment to be an array
//     index (which it consumes).
//   - A non-wildcard pattern segment must match the concrete key exactly.
func matchesIgnorePath(concrete []pathSegment, pattern []segment) bool {
	ci := 0
	pi := 0

	for pi < len(pattern) && ci < len(concrete) {
		ps := pattern[pi]
		cs := concrete[ci]

		if ps.wildcard {
			// Wildcard pattern segment: the concrete path should have the key
			// matching ps.key, followed by an array index.
			if cs.isIndex {
				// If we're at an array index, this doesn't match the key part.
				return false
			}
			if cs.key != ps.key {
				return false
			}
			ci++
			// Now consume the array index.
			if ci >= len(concrete) {
				return false
			}
			if !concrete[ci].isIndex {
				return false
			}
			ci++
			pi++
		} else {
			// Non-wildcard: concrete must be a key segment matching exactly.
			if cs.isIndex {
				return false
			}
			if cs.key != ps.key {
				return false
			}
			ci++
			pi++
		}
	}

	return ci == len(concrete) && pi == len(pattern)
}

// compareJSON recursively walks two JSON values and appends field differences
// to diffs. The path parameter tracks the current traversal path as a slice
// of pathSegments.
func compareJSON(fixture, live any, path []pathSegment, ignorePaths []parsedPath, diffs *[]BodyFieldDiff) {
	if len(path) > 0 && isIgnored(path, ignorePaths) {
		return
	}

	fixtureMap, fixtureIsMap := fixture.(map[string]any)
	liveMap, liveIsMap := live.(map[string]any)

	// Both are objects: compare field by field.
	if fixtureIsMap && liveIsMap {
		compareObjects(fixtureMap, liveMap, path, ignorePaths, diffs)
		return
	}

	fixtureArr, fixtureIsArr := fixture.([]any)
	liveArr, liveIsArr := live.([]any)

	// Both are arrays: compare element by element.
	if fixtureIsArr && liveIsArr {
		compareArrays(fixtureArr, liveArr, path, ignorePaths, diffs)
		return
	}

	// Scalar or type-mismatch: compare directly.
	if !diffJSONEqual(fixture, live) {
		*diffs = append(*diffs, BodyFieldDiff{
			Path:     formatPath(path),
			Kind:     FieldChanged,
			OldValue: fixture,
			NewValue: live,
		})
	}
}

// compareObjects compares two JSON objects field by field.
func compareObjects(fixture, live map[string]any, path []pathSegment, ignorePaths []parsedPath, diffs *[]BodyFieldDiff) {
	// Collect all keys from both maps.
	allKeys := make(map[string]struct{})
	for k := range fixture {
		allKeys[k] = struct{}{}
	}
	for k := range live {
		allKeys[k] = struct{}{}
	}

	// Sort for deterministic output.
	sortedKeys := make([]string, 0, len(allKeys))
	for k := range allKeys {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)

	for _, key := range sortedKeys {
		childPath := append(append([]pathSegment{}, path...), pathSegment{key: key})

		if isIgnored(childPath, ignorePaths) {
			continue
		}

		fixtureVal, inFixture := fixture[key]
		liveVal, inLive := live[key]

		if inFixture && !inLive {
			*diffs = append(*diffs, BodyFieldDiff{
				Path:     formatPath(childPath),
				Kind:     FieldRemoved,
				OldValue: fixtureVal,
			})
		} else if !inFixture && inLive {
			*diffs = append(*diffs, BodyFieldDiff{
				Path:     formatPath(childPath),
				Kind:     FieldAdded,
				NewValue: liveVal,
			})
		} else {
			compareJSON(fixtureVal, liveVal, childPath, ignorePaths, diffs)
		}
	}
}

// compareArrays compares two JSON arrays element by element.
func compareArrays(fixture, live []any, path []pathSegment, ignorePaths []parsedPath, diffs *[]BodyFieldDiff) {
	maxLen := len(fixture)
	if len(live) > maxLen {
		maxLen = len(live)
	}

	for i := 0; i < maxLen; i++ {
		childPath := append(append([]pathSegment{}, path...), pathSegment{index: i, isIndex: true})

		if isIgnored(childPath, ignorePaths) {
			continue
		}

		if i >= len(fixture) {
			*diffs = append(*diffs, BodyFieldDiff{
				Path:     formatPath(childPath),
				Kind:     FieldAdded,
				NewValue: live[i],
			})
		} else if i >= len(live) {
			*diffs = append(*diffs, BodyFieldDiff{
				Path:     formatPath(childPath),
				Kind:     FieldRemoved,
				OldValue: fixture[i],
			})
		} else {
			compareJSON(fixture[i], live[i], childPath, ignorePaths, diffs)
		}
	}
}

// diffJSONEqual performs a deep equality comparison of two JSON values.
// It handles the standard JSON types: nil, bool, float64, string,
// map[string]any, and []any.
func diffJSONEqual(a, b any) bool {
	// Handle nil comparison.
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}

	switch av := a.(type) {
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case float64:
		bv, ok := b.(float64)
		return ok && av == bv
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			bVal, exists := bv[k]
			if !exists || !diffJSONEqual(v, bVal) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !diffJSONEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	default:
		return false
	}
}
