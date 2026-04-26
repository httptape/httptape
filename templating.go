package httptape

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// templateExpr represents a parsed template expression extracted from a
// Mustache-style {{...}} placeholder. It stores the raw expression text
// and its byte offsets within the source.
type templateExpr struct {
	// raw is the expression text between {{ and }}, trimmed of whitespace.
	raw string
	// start is the byte offset of the opening "{{" in the source.
	start int
	// end is the byte offset just past the closing "}}" in the source.
	end int
}

// parsedExpr is a parsed template expression. It represents either an accessor
// (e.g., "request.path") or a helper call (e.g., "faker.email seed=user-42").
type parsedExpr struct {
	// name is the function/accessor name (e.g., "request.path", "now",
	// "faker.email", "counter", "pathParam.id").
	name string

	// args holds keyword arguments as key-value pairs.
	// Nil for accessor-style expressions (request.*, pathParam.*).
	// Example: {"seed": "user-42", "format": "unix"}.
	args map[string]string
}

// templateCtx holds the evaluation context for template resolution.
// It is constructed per-request by the Server before template resolution
// and passed through to all resolvers.
type templateCtx struct {
	// req is the incoming HTTP request.
	req *http.Request
	// reqBody is the cached request body bytes.
	reqBody []byte
	// pathParams holds captured path segments from PathPatternCriterion.
	// Nil if no path pattern was used for matching.
	pathParams map[string]string
	// tapeID is the matched tape's ID (used for auto-seed generation).
	tapeID string
	// counters is the server's counter state (shared, mutex-protected).
	counters *counterState
	// randSource provides randomness for non-deterministic helpers
	// (uuid, randomHex, randomInt). Injectable for testing.
	randSource io.Reader
}

// counterState manages named counters for the {{counter}} template helper.
// Thread-safe via a sync.Mutex.
type counterState struct {
	mu       sync.Mutex
	counters map[string]int64
}

// Next increments the named counter and returns the new value.
// The counter starts at 1 on first call. Wraps to 0 at math.MaxInt64.
func (cs *counterState) Next(name string) int64 {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cur := cs.counters[name]
	if cur == math.MaxInt64 {
		cs.counters[name] = 0
		return 0
	}
	cur++
	cs.counters[name] = cur
	return cur
}

// Reset resets the named counter to 0. If name is empty, all counters
// are reset.
func (cs *counterState) Reset(name string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if name == "" {
		cs.counters = make(map[string]int64)
	} else {
		delete(cs.counters, name)
	}
}

// parseExpr parses the inner text of a {{...}} expression into a parsedExpr.
// It splits the text into a function name and optional keyword arguments.
//
// Syntax:
//   - Accessors: "request.path", "pathParam.id"
//   - Helpers without args: "now", "uuid"
//   - Helpers with args: "now format=unix", "faker.email seed=user-42"
//
// Keyword argument values may contain nested {{...}} expressions which are
// resolved before the helper is invoked (see resolveArgs).
//
// Returns the parsed expression. If the input is empty, returns a parsedExpr
// with an empty name.
func parseExpr(raw string) parsedExpr {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return parsedExpr{}
	}

	// Split on first whitespace to get name and the rest.
	idx := strings.IndexAny(raw, " \t")
	if idx < 0 {
		return parsedExpr{name: raw}
	}

	name := raw[:idx]
	rest := strings.TrimSpace(raw[idx+1:])
	if rest == "" {
		return parsedExpr{name: name}
	}

	// Parse key=value pairs from rest.
	args := make(map[string]string)
	for rest != "" {
		eqIdx := strings.IndexByte(rest, '=')
		if eqIdx < 0 {
			break
		}
		key := strings.TrimSpace(rest[:eqIdx])
		rest = rest[eqIdx+1:]

		// Value runs until the next unquoted space or end-of-string.
		// But we need to account for nested {{...}} in values.
		val, remaining := parseArgValue(rest)
		args[key] = val
		rest = strings.TrimSpace(remaining)
	}

	return parsedExpr{name: name, args: args}
}

// parseArgValue extracts an argument value from the beginning of s.
// Values may contain nested {{...}} expressions. The value ends at the
// next whitespace that is not inside a {{...}} block.
// Returns the value and the remaining string after it.
func parseArgValue(s string) (string, string) {
	depth := 0
	for i := 0; i < len(s); i++ {
		if i+1 < len(s) && s[i] == '{' && s[i+1] == '{' {
			depth++
			i++ // skip second {
			continue
		}
		if i+1 < len(s) && s[i] == '}' && s[i+1] == '}' {
			depth--
			i++ // skip second }
			continue
		}
		if depth == 0 && (s[i] == ' ' || s[i] == '\t') {
			return s[:i], s[i:]
		}
	}
	return s, ""
}

// resolveArgs resolves nested {{...}} expressions in helper argument values.
// It uses the same evaluation context (request, pathParams, tape) as the
// top-level resolver. Returns the args map with all nested expressions resolved.
func resolveArgs(args map[string]string, ctx *templateCtx) map[string]string {
	if len(args) == 0 {
		return args
	}
	resolved := make(map[string]string, len(args))
	for k, v := range args {
		if strings.Contains(v, "{{") {
			src := []byte(v)
			exprs := scanTemplateExprs(src)
			if len(exprs) > 0 {
				var buf bytes.Buffer
				buf.Grow(len(v))
				prev := 0
				for _, expr := range exprs {
					buf.Write(src[prev:expr.start])
					val, ok := resolveExpr(expr.raw, ctx)
					if ok {
						buf.WriteString(val)
					}
					// If not ok, silently drop (nested args always lenient).
					prev = expr.end
				}
				buf.Write(src[prev:])
				resolved[k] = buf.String()
			} else {
				resolved[k] = v
			}
		} else {
			resolved[k] = v
		}
	}
	return resolved
}

// autoSeed generates a deterministic seed from the tape ID and request path.
// The seed is the first 16 hex characters of SHA-256(tapeID + ":" + path).
func autoSeed(tapeID, path string) string {
	h := sha256.Sum256([]byte(tapeID + ":" + path))
	return hex.EncodeToString(h[:8])
}

// findMatchingClose finds the closing }} for an opening {{ at position
// openIdx in src, accounting for nested {{...}} expressions.
// Returns the absolute byte offset of the first character of the matching }},
// or -1 if no matching close is found.
func findMatchingClose(src []byte, openIdx int) int {
	depth := 1
	pos := openIdx + 2
	for pos < len(src)-1 {
		if src[pos] == '{' && src[pos+1] == '{' {
			depth++
			pos += 2
			continue
		}
		if src[pos] == '}' && src[pos+1] == '}' {
			depth--
			if depth == 0 {
				return pos
			}
			pos += 2
			continue
		}
		pos++
	}
	return -1
}

// scanTemplateExprs scans src for all {{...}} expressions and returns them
// in order of appearance. Supports nested {{...}} inside expressions by
// using balanced delimiter tracking. Unclosed delimiters are left as literal
// text.
func scanTemplateExprs(src []byte) []templateExpr {
	var exprs []templateExpr
	pos := 0
	for pos < len(src) {
		openIdx := bytes.Index(src[pos:], []byte("{{"))
		if openIdx < 0 {
			break
		}
		openIdx += pos // absolute offset

		closeIdx := findMatchingClose(src, openIdx)
		if closeIdx < 0 {
			break // unclosed {{ -- treat rest as literal
		}

		raw := string(bytes.TrimSpace(src[openIdx+2 : closeIdx]))
		exprs = append(exprs, templateExpr{
			raw:   raw,
			start: openIdx,
			end:   closeIdx + 2, // past the "}}"
		})
		pos = closeIdx + 2
	}
	return exprs
}

// resolveExpr resolves a single template expression against the evaluation
// context. It returns the resolved string value and true, or ("", false)
// if the expression is unresolvable.
//
// Dispatches on the parsed expression name:
//   - request.*    -- request accessor
//   - pathParam.*  -- path parameter accessor
//   - now          -- current time helper
//   - uuid         -- random UUID helper
//   - randomHex    -- random hex string helper
//   - randomInt    -- random integer helper
//   - counter      -- monotonic counter helper
//   - faker.*      -- deterministic faker bridge
//   - other        -- unresolvable
func resolveExpr(expr string, ctx *templateCtx) (string, bool) {
	pe := parseExpr(expr)
	if pe.name == "" {
		return "", false
	}

	// Resolve nested {{...}} in arguments before dispatching.
	args := resolveArgs(pe.args, ctx)

	switch {
	case strings.HasPrefix(pe.name, "request."):
		return resolveRequestExpr(pe.name, ctx)

	case strings.HasPrefix(pe.name, "pathParam."):
		paramName := pe.name[len("pathParam."):]
		if ctx.pathParams == nil {
			return "", false
		}
		val, ok := ctx.pathParams[paramName]
		if !ok {
			return "", false
		}
		return val, true

	case pe.name == "now":
		return resolveNow(args), true

	case pe.name == "uuid":
		val, err := resolveUUID(ctx)
		if err != nil {
			return "", false
		}
		return val, true

	case pe.name == "randomHex":
		val, err := resolveRandomHex(args, ctx)
		if err != nil {
			return "", false
		}
		return val, true

	case pe.name == "randomInt":
		val, err := resolveRandomInt(args, ctx)
		if err != nil {
			return "", false
		}
		return val, true

	case pe.name == "counter":
		return resolveCounter(args, ctx), true

	case strings.HasPrefix(pe.name, "faker."):
		fakerType := pe.name[len("faker."):]
		return resolveFaker(fakerType, args, ctx)

	default:
		return "", false
	}
}

// resolveRequestExpr handles the request.* accessor namespace.
func resolveRequestExpr(name string, ctx *templateCtx) (string, bool) {
	rest := name[8:] // strip "request."
	switch {
	case rest == "method":
		return ctx.req.Method, true
	case rest == "path":
		return ctx.req.URL.Path, true
	case rest == "url":
		return ctx.req.URL.String(), true
	}

	// request.headers.<Name>
	if len(rest) > 8 && rest[:8] == "headers." {
		headerName := rest[8:]
		val := ctx.req.Header.Get(http.CanonicalHeaderKey(headerName))
		if val == "" {
			return "", false
		}
		return val, true
	}

	// request.query.<name>
	if len(rest) > 6 && rest[:6] == "query." {
		qName := rest[6:]
		val := ctx.req.URL.Query().Get(qName)
		if val == "" {
			return "", false
		}
		return val, true
	}

	// request.body.<json.path>
	if len(rest) > 5 && rest[:5] == "body." {
		jsonPath := rest[5:]
		return resolveBodyField(ctx.reqBody, jsonPath)
	}

	return "", false
}

// resolveNow returns the current UTC time formatted per the "format" argument.
// Supported formats: "rfc3339" (default), "iso" (alias for rfc3339),
// "unix" (seconds since epoch), "unixMillis" (ms since epoch).
// Custom Go time format strings are also accepted (e.g., "2006-01-02").
func resolveNow(args map[string]string) string {
	t := time.Now().UTC()
	format := args["format"]
	if format == "" {
		format = "rfc3339"
	}

	switch format {
	case "rfc3339", "iso":
		return t.Format(time.RFC3339)
	case "unix":
		return strconv.FormatInt(t.Unix(), 10)
	case "unixMillis":
		return strconv.FormatInt(t.UnixMilli(), 10)
	default:
		// Treat as a custom Go time format string.
		return t.Format(format)
	}
}

// resolveUUID generates a random UUID v4 string using ctx.randSource.
// Returns an error if the random source fails.
func resolveUUID(ctx *templateCtx) (string, error) {
	var buf [16]byte
	if _, err := io.ReadFull(ctx.randSource, buf[:]); err != nil {
		return "", fmt.Errorf("resolveUUID: crypto/rand read: %w", err)
	}
	// Set version 4 and variant RFC 4122.
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
}

// resolveRandomHex generates a random hex string of the specified length.
// The "length" argument is required.
func resolveRandomHex(args map[string]string, ctx *templateCtx) (string, error) {
	lengthStr, ok := args["length"]
	if !ok || lengthStr == "" {
		return "", fmt.Errorf("randomHex requires \"length\" argument")
	}
	length, err := strconv.Atoi(lengthStr)
	if err != nil || length <= 0 {
		return "", fmt.Errorf("randomHex \"length\" must be a positive integer, got %q", lengthStr)
	}
	nBytes := (length + 1) / 2
	buf := make([]byte, nBytes)
	if _, err := io.ReadFull(ctx.randSource, buf); err != nil {
		return "", fmt.Errorf("randomHex: crypto/rand read: %w", err)
	}
	return hex.EncodeToString(buf)[:length], nil
}

// resolveRandomInt generates a random integer in [min, max].
func resolveRandomInt(args map[string]string, ctx *templateCtx) (string, error) {
	minVal := int64(0)
	maxVal := int64(100)
	var err error

	if s, ok := args["min"]; ok {
		minVal, err = strconv.ParseInt(s, 10, 64)
		if err != nil {
			return "", fmt.Errorf("randomInt \"min\" must be an integer, got %q", s)
		}
	}
	if s, ok := args["max"]; ok {
		maxVal, err = strconv.ParseInt(s, 10, 64)
		if err != nil {
			return "", fmt.Errorf("randomInt \"max\" must be an integer, got %q", s)
		}
	}
	if minVal > maxVal {
		return "", fmt.Errorf("randomInt min (%d) must be <= max (%d)", minVal, maxVal)
	}
	if minVal == maxVal {
		return strconv.FormatInt(minVal, 10), nil
	}

	// Use math/big arithmetic to avoid int64 overflow when the range
	// (maxVal - minVal + 1) exceeds math.MaxInt64. For example,
	// min=0, max=math.MaxInt64 would overflow int64 subtraction.
	hi := new(big.Int).SetInt64(maxVal)
	lo := new(big.Int).SetInt64(minVal)
	rangeSize := new(big.Int).Sub(hi, lo)
	rangeSize.Add(rangeSize, big.NewInt(1))

	n, err := rand.Int(ctx.randSource, rangeSize)
	if err != nil {
		return "", fmt.Errorf("randomInt: crypto/rand: %w", err)
	}
	result := new(big.Int).Add(n, lo).Int64()
	return strconv.FormatInt(result, 10), nil
}

// resolveCounter increments and returns a named counter.
func resolveCounter(args map[string]string, ctx *templateCtx) string {
	name := args["name"]
	if name == "" {
		name = "default"
	}
	if ctx.counters == nil {
		return "0"
	}
	val := ctx.counters.Next(name)
	return strconv.FormatInt(val, 10)
}

// resolveFaker dispatches to the appropriate Faker and returns its output.
// The fakerType is the suffix after "faker." (e.g., "email", "name").
// The seed is resolved from args or auto-generated.
func resolveFaker(fakerType string, args map[string]string, ctx *templateCtx) (string, bool) {
	var faker Faker

	switch fakerType {
	case "email":
		faker = EmailFaker{}
	case "name":
		faker = NameFaker{}
	case "phone":
		faker = PhoneFaker{}
	case "address":
		faker = AddressFaker{}
	case "creditCard":
		faker = CreditCardFaker{}
	case "hmac":
		faker = HMACFaker{}
	case "redacted":
		faker = RedactedFaker{}
	case "uuid":
		return resolveFakerUUID(args, ctx), true
	default:
		return "", false
	}

	seed := args["seed"]
	if seed == "" {
		seed = autoSeed(ctx.tapeID, ctx.req.URL.Path)
	}

	result := faker.Fake(seed, seed)
	return fmt.Sprintf("%v", result), true
}

// resolveFakerUUID generates a deterministic UUID from the seed using
// HMAC-SHA256, with version 4 and RFC 4122 variant bits set.
func resolveFakerUUID(args map[string]string, ctx *templateCtx) string {
	seed := args["seed"]
	if seed == "" {
		seed = autoSeed(ctx.tapeID, ctx.req.URL.Path)
	}

	h := computeHMAC(seed, seed)
	var buf [16]byte
	copy(buf[:], h[:16])
	buf[6] = (buf[6] & 0x0f) | 0x40 // version 4
	buf[8] = (buf[8] & 0x3f) | 0x80 // variant RFC 4122
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}

// resolveBodyField extracts a scalar value from a JSON body using dot-notation.
// The dotPath is prepended with "$." and parsed via parsePath from sanitizer.go,
// then extracted via extractAtPath from matcher.go.
//
// Only scalar values (string, number, bool) are converted to strings. Objects
// and arrays return ("", false) as they cannot be meaningfully interpolated.
func resolveBodyField(body []byte, dotPath string) (string, bool) {
	if len(body) == 0 {
		return "", false
	}

	pp, ok := parsePath("$." + dotPath)
	if !ok {
		return "", false
	}

	var data any
	if err := json.Unmarshal(body, &data); err != nil {
		return "", false
	}

	val, ok := extractAtPath(data, pp.segments)
	if !ok {
		return "", false
	}

	return scalarToString(val)
}

// scalarToString converts a JSON scalar value to its string representation.
// Returns ("", false) for non-scalar types (nil, objects, arrays).
func scalarToString(v any) (string, bool) {
	switch val := v.(type) {
	case string:
		return val, true
	case float64:
		// Use strconv for clean formatting (no trailing zeros).
		if val == float64(int64(val)) {
			return strconv.FormatInt(int64(val), 10), true
		}
		return strconv.FormatFloat(val, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(val), true
	default:
		// nil, map[string]any, []any -- not scalar.
		return "", false
	}
}

// resolveTemplateJSON resolves template expressions in a JSON body by
// walking the deserialized JSON tree. String values containing {{...}} are
// resolved; non-string values are left unchanged. Returns the re-serialized
// JSON bytes.
//
// This function ensures that resolved values are properly JSON-escaped,
// which byte-level string substitution cannot guarantee.
func resolveTemplateJSON(body []byte, ctx *templateCtx, strict bool) ([]byte, error) {
	var data any
	if err := json.Unmarshal(body, &data); err != nil {
		// Not valid JSON -- fall back to byte-level substitution.
		return resolveTemplateBytes(body, ctx, strict)
	}

	var walkErr error
	data = walkJSON(data, ctx, strict, &walkErr)
	if walkErr != nil {
		return nil, walkErr
	}

	result, err := json.Marshal(data)
	if err != nil {
		return body, nil
	}
	return result, nil
}

// walkJSON recursively walks a JSON tree and resolves template expressions
// in string values. Non-string values are returned as-is.
func walkJSON(data any, ctx *templateCtx, strict bool, errOut *error) any {
	if *errOut != nil {
		return data
	}

	switch v := data.(type) {
	case map[string]any:
		for key, val := range v {
			v[key] = walkJSON(val, ctx, strict, errOut)
		}
		return v
	case []any:
		for i, val := range v {
			v[i] = walkJSON(val, ctx, strict, errOut)
		}
		return v
	case string:
		if !strings.Contains(v, "{{") {
			return v
		}
		resolved, err := resolveTemplateStringCtx(v, ctx, strict)
		if err != nil {
			*errOut = err
			return v
		}
		return resolved
	default:
		return data
	}
}

// ResolveTemplateBody resolves all {{...}} template expressions in body
// against the evaluation context. It returns the resolved body bytes.
//
// If strict is true, any unresolvable expression causes an error describing
// which expression failed. If strict is false (lenient mode), unresolvable
// expressions are replaced with an empty string.
//
// If body contains no "{{" sequence, it is returned unchanged with zero
// allocations (fast path).
//
// JSON bodies (detected by leading { or [) use JSON-aware resolution to
// ensure proper escaping of resolved values.
func ResolveTemplateBody(body []byte, ctx *templateCtx, strict bool) ([]byte, error) {
	// Fast path: no template delimiters at all.
	if !bytes.Contains(body, []byte("{{")) {
		return body, nil
	}

	// Detect JSON body and use JSON-aware resolution.
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		return resolveTemplateJSON(body, ctx, strict)
	}

	return resolveTemplateBytes(body, ctx, strict)
}

// resolveTemplateBytes performs byte-level template substitution on body.
func resolveTemplateBytes(body []byte, ctx *templateCtx, strict bool) ([]byte, error) {
	exprs := scanTemplateExprs(body)
	if len(exprs) == 0 {
		return body, nil
	}

	var buf bytes.Buffer
	buf.Grow(len(body))
	prev := 0

	for _, expr := range exprs {
		buf.Write(body[prev:expr.start])

		resolved, ok := resolveExpr(expr.raw, ctx)
		if !ok {
			if strict {
				return nil, fmt.Errorf("httptape: unresolvable template expression: {{%s}}", expr.raw)
			}
			// Lenient: replace with empty string (write nothing).
		} else {
			buf.WriteString(resolved)
		}
		prev = expr.end
	}
	buf.Write(body[prev:])

	return buf.Bytes(), nil
}

// ResolveTemplateHeaders resolves all {{...}} template expressions in
// response header values against the evaluation context. It returns a new
// http.Header map with resolved values.
//
// If strict is true, any unresolvable expression causes an error. If strict
// is false (lenient mode), unresolvable expressions are replaced with an
// empty string.
//
// Headers that contain no "{{" sequences are copied as-is (fast path per
// header value).
//
// For callers outside the package, use [ResolveTemplateHeadersSimple] which
// constructs the evaluation context from an *http.Request.
func ResolveTemplateHeaders(h http.Header, ctx *templateCtx, strict bool) (http.Header, error) {
	if h == nil {
		return nil, nil
	}

	result := make(http.Header, len(h))

	for key, values := range h {
		resolved := make([]string, len(values))
		for i, v := range values {
			rv, err := resolveTemplateStringCtx(v, ctx, strict)
			if err != nil {
				return nil, err
			}
			resolved[i] = rv
		}
		result[key] = resolved
	}

	return result, nil
}

// resolveTemplateStringCtx resolves all {{...}} expressions in a single string
// using the template context.
func resolveTemplateStringCtx(s string, ctx *templateCtx, strict bool) (string, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}

	src := []byte(s)
	exprs := scanTemplateExprs(src)
	if len(exprs) == 0 {
		return s, nil
	}

	var buf bytes.Buffer
	buf.Grow(len(s))
	prev := 0

	for _, expr := range exprs {
		buf.Write(src[prev:expr.start])

		resolved, ok := resolveExpr(expr.raw, ctx)
		if !ok {
			if strict {
				return "", fmt.Errorf("httptape: unresolvable template expression: {{%s}}", expr.raw)
			}
			// Lenient: empty string.
		} else {
			buf.WriteString(resolved)
		}
		prev = expr.end
	}
	buf.Write(src[prev:])

	return buf.String(), nil
}

// ResolveTemplateBodySimple is a backward-compatible convenience wrapper that
// resolves templates using only request data (no path params, no counters,
// no faker). Equivalent to the pre-#196 ResolveTemplateBody behavior.
//
// See also [ResolveTemplateHeadersSimple] for the header equivalent.
func ResolveTemplateBodySimple(body []byte, r *http.Request, strict bool) ([]byte, error) {
	if !bytes.Contains(body, []byte("{{")) {
		return body, nil
	}

	reqBody := readRequestBody(r)
	ctx := &templateCtx{
		req:        r,
		reqBody:    reqBody,
		randSource: rand.Reader,
	}
	return ResolveTemplateBody(body, ctx, strict)
}

// ResolveTemplateHeadersSimple is a convenience wrapper that resolves
// templates in header values using only request data (no path params,
// no counters, no faker). It constructs the unexported evaluation context
// internally, making it callable from outside the package.
//
// See also [ResolveTemplateBodySimple] for the body equivalent.
func ResolveTemplateHeadersSimple(headers http.Header, r *http.Request, strict bool) (http.Header, error) {
	if headers == nil {
		return nil, nil
	}
	reqBody := readRequestBody(r)
	ctx := &templateCtx{
		req:        r,
		reqBody:    reqBody,
		randSource: rand.Reader,
	}
	return ResolveTemplateHeaders(headers, ctx, strict)
}

// readRequestBody reads the full request body and restores it so the body
// remains readable by downstream handlers. Returns nil if the body is nil
// or empty.
func readRequestBody(r *http.Request) []byte {
	if r.Body == nil {
		return nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil
	}
	// Restore the body for any downstream readers.
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body
}

// parseCoercion splits a template expression into the expression body and
// an optional type coercion. Returns the expression without the coercion
// pipe, the coercion type (or ""), and whether a coercion was found.
//
// Examples:
//
//	"pathParam.id | int"          -> ("pathParam.id", "int", true)
//	"pathParam.id"                -> ("pathParam.id", "", false)
//	"faker.name seed=foo"         -> ("faker.name seed=foo", "", false)
//	"request.query.page | float"  -> ("request.query.page", "float", true)
func parseCoercion(raw string) (expr string, coercion string, ok bool) {
	// Look for " | type" at the end. Coercion pipes are always preceded and
	// followed by whitespace.
	idx := strings.LastIndex(raw, " | ")
	if idx < 0 {
		return raw, "", false
	}

	// The coercion type is the token after the last " | ".
	candidate := strings.TrimSpace(raw[idx+3:])
	switch candidate {
	case "int", "float", "bool":
		return strings.TrimSpace(raw[:idx]), candidate, true
	default:
		// Not a recognized coercion -- treat the entire string as the expression.
		return raw, "", false
	}
}

// coerceValue converts a string value to the specified type. Supported
// coercions: "int" (float64 with integer value), "float" (float64),
// "bool" (bool). Returns the typed value or an error if conversion fails.
func coerceValue(s string, coercion string) (any, error) {
	switch coercion {
	case "int":
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return nil, fmt.Errorf("httptape: coercion | int: cannot parse %q as number: %w", s, err)
		}
		// Truncate to integer, emit as float64 (JSON number).
		return math.Trunc(f), nil
	case "float":
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return nil, fmt.Errorf("httptape: coercion | float: cannot parse %q as number: %w", s, err)
		}
		return f, nil
	case "bool":
		b, err := strconv.ParseBool(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("httptape: coercion | bool: cannot parse %q as boolean: %w", s, err)
		}
		return b, nil
	default:
		return s, nil
	}
}

// resolveExemplarBody resolves template expressions in an exemplar tape's
// response body. The dispatch is based on the response Content-Type:
//   - JSON: unmarshal, walk with coercion-aware resolution, re-marshal.
//   - Text: byte-level template substitution (existing ResolveTemplateBody).
//   - Binary: no resolution.
func resolveExemplarBody(body []byte, headers http.Header, ctx *templateCtx, strict bool) ([]byte, error) {
	if len(body) == 0 || !bytes.Contains(body, []byte("{{")) {
		return body, nil
	}

	ct := ""
	if headers != nil {
		ct = headers.Get("Content-Type")
	}

	mt, parseErr := ParseMediaType(ct)
	if parseErr == nil && IsJSON(mt) {
		return resolveTemplateJSONWithCoercion(body, ctx, strict)
	}
	if parseErr == nil && IsBinary(mt) {
		// Binary: no template resolution.
		return body, nil
	}
	// Text or unknown: use standard byte-level resolution.
	return resolveTemplateBytes(body, ctx, strict)
}

// resolveTemplateJSONWithCoercion resolves template expressions in a JSON
// body, supporting type coercion via the | int, | float, | bool pipe syntax.
// When a string value is a single template expression with a coercion suffix,
// the resolved string value is converted to the specified type (producing a
// JSON number or boolean instead of a string).
func resolveTemplateJSONWithCoercion(body []byte, ctx *templateCtx, strict bool) ([]byte, error) {
	var data any
	if err := json.Unmarshal(body, &data); err != nil {
		// Not valid JSON -- fall back to byte-level substitution.
		return resolveTemplateBytes(body, ctx, strict)
	}

	var walkErr error
	data = walkJSONWithCoercion(data, ctx, strict, &walkErr)
	if walkErr != nil {
		return nil, walkErr
	}

	result, err := json.Marshal(data)
	if err != nil {
		return body, nil
	}
	return result, nil
}

// walkJSONWithCoercion recursively walks a JSON tree and resolves template
// expressions in string values, with support for type coercion. When a string
// value is entirely a single {{...}} expression with a | int/float/bool pipe,
// the resolved value's type is changed from string to the target type.
func walkJSONWithCoercion(data any, ctx *templateCtx, strict bool, errOut *error) any {
	if *errOut != nil {
		return data
	}

	switch v := data.(type) {
	case map[string]any:
		result := make(map[string]any, len(v))
		for key, val := range v {
			result[key] = walkJSONWithCoercion(val, ctx, strict, errOut)
		}
		return result
	case []any:
		result := make([]any, len(v))
		for i, val := range v {
			result[i] = walkJSONWithCoercion(val, ctx, strict, errOut)
		}
		return result
	case string:
		if !strings.Contains(v, "{{") {
			return v
		}
		return resolveStringWithCoercion(v, ctx, strict, errOut)
	default:
		return data
	}
}

// resolveStringWithCoercion resolves template expressions in a string value
// within a JSON context. If the entire string is a single template expression
// with a type coercion pipe (e.g., "{{pathParam.id | int}}"), the result is
// a typed value (float64 or bool). Otherwise, the result is a string.
func resolveStringWithCoercion(s string, ctx *templateCtx, strict bool, errOut *error) any {
	src := []byte(s)
	exprs := scanTemplateExprs(src)
	if len(exprs) == 0 {
		return s
	}

	// Check if the entire string is a single template expression (possibly with coercion).
	if len(exprs) == 1 && exprs[0].start == 0 && exprs[0].end == len(src) {
		rawExpr := exprs[0].raw
		exprBody, coercion, hasCoercion := parseCoercion(rawExpr)

		resolved, ok := resolveExpr(exprBody, ctx)
		if !ok {
			if strict {
				*errOut = fmt.Errorf("httptape: unresolvable template expression: {{%s}}", rawExpr)
				return s
			}
			return ""
		}

		if hasCoercion {
			typed, err := coerceValue(resolved, coercion)
			if err != nil {
				if strict {
					*errOut = err
					return s
				}
				// Lenient: return the uncoerced string value.
				return resolved
			}
			return typed
		}
		return resolved
	}

	// Mixed content: multiple expressions or expressions with surrounding text.
	// Resolve all expressions and concatenate as a string.
	// Coercion in mixed content is not supported.
	var buf bytes.Buffer
	buf.Grow(len(s))
	prev := 0

	for _, expr := range exprs {
		buf.Write(src[prev:expr.start])
		rawExpr := expr.raw

		// Strip coercion if present (mixed content ignores it).
		exprBody, _, _ := parseCoercion(rawExpr)
		resolved, ok := resolveExpr(exprBody, ctx)
		if !ok {
			if strict {
				*errOut = fmt.Errorf("httptape: unresolvable template expression: {{%s}}", rawExpr)
				return s
			}
			// Lenient: empty string.
		} else {
			buf.WriteString(resolved)
		}
		prev = expr.end
	}
	buf.Write(src[prev:])
	return buf.String()
}
