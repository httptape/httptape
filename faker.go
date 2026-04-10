package httptape

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Faker is the interface for pluggable deterministic faking strategies.
// Implementations produce a fake value from a seed (HMAC key) and the
// original JSON value. The result must be deterministic: same seed and
// original must always produce the same fake.
type Faker interface {
	Fake(seed string, original any) any
}

// RedactedFaker replaces values with type-aware redacted placeholders:
// strings become "[REDACTED]", numbers become 0, booleans become false.
// Other types (nil, objects, arrays) are returned unchanged.
type RedactedFaker struct{}

// Fake implements Faker.
func (f RedactedFaker) Fake(_ string, original any) any {
	switch original.(type) {
	case string:
		return Redacted
	case float64:
		return float64(0)
	case bool:
		return false
	default:
		return original
	}
}

// FixedFaker always returns its Value field, regardless of the original.
type FixedFaker struct {
	Value any
}

// Fake implements Faker.
func (f FixedFaker) Fake(_ string, _ any) any {
	return f.Value
}

// HMACFaker replaces string values with "fake_<hash>", numbers with a
// deterministic positive integer, and booleans/nil unchanged. This mirrors
// the behavior of the existing fakeValue auto-detection for generic strings.
type HMACFaker struct{}

// Fake implements Faker.
func (f HMACFaker) Fake(seed string, original any) any {
	switch val := original.(type) {
	case string:
		h := computeHMAC(seed, val)
		return fakeString(h)
	case float64:
		h := computeHMAC(seed, strconv.FormatFloat(val, 'f', -1, 64))
		return fakeNumericID(h)
	default:
		return original
	}
}

// EmailFaker replaces string values with "user_<hash>@example.com".
// Non-string values are returned unchanged.
type EmailFaker struct{}

// Fake implements Faker.
func (f EmailFaker) Fake(seed string, original any) any {
	s, ok := original.(string)
	if !ok {
		return original
	}
	h := computeHMAC(seed, s)
	return fakeEmail(h)
}

// PhoneFaker replaces digits in a string with HMAC-derived digits while
// preserving the original formatting (dashes, spaces, parentheses, plus
// signs). Non-string values are returned unchanged.
type PhoneFaker struct{}

// Fake implements Faker.
func (f PhoneFaker) Fake(seed string, original any) any {
	s, ok := original.(string)
	if !ok {
		return original
	}
	h := computeHMAC(seed, s)
	hi := 0
	var buf strings.Builder
	buf.Grow(len(s))
	for _, c := range s {
		if c >= '0' && c <= '9' {
			if hi >= len(h) {
				// Chain: re-HMAC with current output.
				h = computeHMAC(seed, buf.String())
				hi = 0
			}
			digit := h[hi] % 10
			hi++
			buf.WriteByte('0' + digit)
		} else {
			buf.WriteRune(c)
		}
	}
	return buf.String()
}

// CreditCardFaker generates a fake credit card number that preserves the
// issuer prefix (first 6 digits), uses HMAC-derived digits for the middle,
// and appends a valid Luhn check digit. The result is formatted as
// "XXXX-XXXX-XXXX-XXXX". Non-string values are returned unchanged.
type CreditCardFaker struct{}

// Fake implements Faker.
func (f CreditCardFaker) Fake(seed string, original any) any {
	s, ok := original.(string)
	if !ok {
		return original
	}

	// Extract digits from original.
	var digits []byte
	for _, c := range s {
		if c >= '0' && c <= '9' {
			digits = append(digits, byte(c))
		}
	}

	// Use first 6 digits as issuer prefix; fall back to "400000".
	prefix := "400000"
	if len(digits) >= 6 {
		prefix = string(digits[:6])
	}

	// Generate 9 middle digits from HMAC.
	h := computeHMAC(seed, s)
	var mid strings.Builder
	for i := 0; i < 9; i++ {
		mid.WriteByte('0' + h[i]%10)
	}

	// 15 digits = prefix(6) + mid(9), then compute Luhn check digit.
	base := prefix + mid.String()
	check := luhnCheckDigit(base)

	full := base + string(rune('0'+check))

	// Format as XXXX-XXXX-XXXX-XXXX.
	return fmt.Sprintf("%s-%s-%s-%s", full[0:4], full[4:8], full[8:12], full[12:16])
}

// luhnCheckDigit computes the Luhn check digit for the given string of
// digits. It returns a value 0-9.
func luhnCheckDigit(digits string) byte {
	sum := 0
	// Process from rightmost digit, doubling every second digit.
	for i := len(digits) - 1; i >= 0; i-- {
		d := int(digits[i] - '0')
		// Digits at odd positions from the right (0-indexed from right: 0,1,2,...)
		// Position 0 is rightmost. We double positions 0, 2, 4, ... (even from right).
		pos := len(digits) - 1 - i // position from right
		if pos%2 == 0 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return byte((10 - sum%10) % 10)
}

// NumericFaker generates a string of N HMAC-derived digits.
// Non-string values are returned unchanged.
type NumericFaker struct {
	Length int
}

// Fake implements Faker.
func (f NumericFaker) Fake(seed string, original any) any {
	s, ok := original.(string)
	if !ok {
		return original
	}
	h := computeHMAC(seed, s)
	var buf strings.Builder
	buf.Grow(f.Length)
	hi := 0
	for i := 0; i < f.Length; i++ {
		if hi >= len(h) {
			h = computeHMAC(seed, buf.String())
			hi = 0
		}
		buf.WriteByte('0' + h[hi]%10)
		hi++
	}
	return buf.String()
}

// DateFaker generates a deterministic date string from HMAC in the given
// Go time format (e.g., "2006-01-02"). Non-string values are returned
// unchanged.
type DateFaker struct {
	Format string
}

// Fake implements Faker.
func (f DateFaker) Fake(seed string, original any) any {
	s, ok := original.(string)
	if !ok {
		return original
	}
	h := computeHMAC(seed, s)
	// Use first 4 bytes as days offset from epoch (2000-01-01).
	n := uint32(h[0])<<24 | uint32(h[1])<<16 | uint32(h[2])<<8 | uint32(h[3])
	// Restrict to ~100 years range: mod 36525 days ≈ 100 years.
	days := int(n % 36525)
	epoch := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	t := epoch.AddDate(0, 0, days)
	format := f.Format
	if format == "" {
		format = "2006-01-02"
	}
	return t.Format(format)
}

// PatternFaker fills a pattern string where '#' is replaced with a digit
// and '?' is replaced with a lowercase letter, both derived from HMAC.
// All other characters are copied literally. Non-string values are returned
// unchanged.
type PatternFaker struct {
	Pattern string
}

// Fake implements Faker.
func (f PatternFaker) Fake(seed string, original any) any {
	s, ok := original.(string)
	if !ok {
		return original
	}
	h := computeHMAC(seed, s)
	hi := 0
	var buf strings.Builder
	buf.Grow(len(f.Pattern))
	for _, c := range f.Pattern {
		switch c {
		case '#':
			if hi >= len(h) {
				h = computeHMAC(seed, buf.String())
				hi = 0
			}
			buf.WriteByte('0' + h[hi]%10)
			hi++
		case '?':
			if hi >= len(h) {
				h = computeHMAC(seed, buf.String())
				hi = 0
			}
			buf.WriteByte('a' + h[hi]%26)
			hi++
		default:
			buf.WriteRune(c)
		}
	}
	return buf.String()
}

// PrefixFaker generates a string of the form "<prefix><hex_hash>".
// Non-string values are returned unchanged.
type PrefixFaker struct {
	Prefix string
}

// Fake implements Faker.
func (f PrefixFaker) Fake(seed string, original any) any {
	s, ok := original.(string)
	if !ok {
		return original
	}
	h := computeHMAC(seed, s)
	return f.Prefix + hex.EncodeToString(h[:8])
}

// NameFaker generates a deterministic full name from HMAC.
// Picks a first name and last name from fixed lists using HMAC bytes.
// Non-string values are returned unchanged.
type NameFaker struct{}

// Fake implements Faker.
func (f NameFaker) Fake(seed string, original any) any {
	s, ok := original.(string)
	if !ok {
		return original
	}
	h := computeHMAC(seed, s)
	first := firstNames[h[0]%byte(len(firstNames))]
	last := lastNames[h[1]%byte(len(lastNames))]
	return first + " " + last
}

// firstNames is a fixed list of first names for NameFaker.
var firstNames = []string{
	"James", "Emma", "Liam", "Olivia", "Noah",
	"Ava", "Lucas", "Sophia", "Mason", "Isabella",
	"Ethan", "Mia", "Logan", "Charlotte", "Aiden",
	"Amelia", "Jackson", "Harper", "Sebastian", "Evelyn",
}

// lastNames is a fixed list of last names for NameFaker.
var lastNames = []string{
	"Smith", "Johnson", "Williams", "Brown", "Jones",
	"Garcia", "Miller", "Davis", "Rodriguez", "Martinez",
	"Anderson", "Taylor", "Thomas", "Moore", "Jackson",
	"Martin", "Lee", "Thompson", "White", "Harris",
}

// streetNames is a fixed list of street names for AddressFaker.
var streetNames = []string{
	"Oak", "Maple", "Cedar", "Elm", "Pine",
	"Birch", "Walnut", "Cherry", "Willow", "Spruce",
}

// streetSuffixes is a fixed list of street suffixes for AddressFaker.
var streetSuffixes = []string{
	"Street", "Avenue", "Boulevard", "Drive", "Lane",
	"Court", "Place", "Road", "Way", "Circle",
}

// cityNames is a fixed list of city names for AddressFaker.
var cityNames = []string{
	"Springfield", "Riverside", "Fairview", "Madison", "Georgetown",
	"Clinton", "Arlington", "Salem", "Franklin", "Bristol",
}

// stateAbbrs is a fixed list of US state abbreviations for AddressFaker.
var stateAbbrs = []string{
	"AL", "AK", "AZ", "AR", "CA", "CO", "CT", "DE", "FL", "GA",
	"HI", "ID", "IL", "IN", "IA", "KS", "KY", "LA", "ME", "MD",
	"MA", "MI", "MN", "MS", "MO", "MT", "NE", "NV", "NH", "NJ",
	"NM", "NY", "NC", "ND", "OH", "OK", "OR", "PA", "RI", "SC",
	"SD", "TN", "TX", "UT", "VT", "VA", "WA", "WV", "WI", "WY",
}

// AddressFaker generates a deterministic US-style address from HMAC.
// Format: "<number> <street> <suffix>, <city>, <state> <zip>".
// Non-string values are returned unchanged.
type AddressFaker struct{}

// Fake implements Faker.
func (f AddressFaker) Fake(seed string, original any) any {
	s, ok := original.(string)
	if !ok {
		return original
	}
	h := computeHMAC(seed, s)

	// House number: bytes[0:2] as uint16, mod 9999 + 1.
	num := (uint16(h[0])<<8 | uint16(h[1])) % 9999
	num++ // 1-9999

	street := streetNames[h[2]%byte(len(streetNames))]
	suffix := streetSuffixes[h[3]%byte(len(streetSuffixes))]
	city := cityNames[h[4]%byte(len(cityNames))]
	state := stateAbbrs[h[5]%byte(len(stateAbbrs))]

	// Zip: 5 digits from bytes[6:11].
	var zip strings.Builder
	for i := 6; i < 11; i++ {
		zip.WriteByte('0' + h[i]%10)
	}

	return fmt.Sprintf("%d %s %s, %s, %s %s", num, street, suffix, city, state, zip.String())
}

// FakeFieldsWith returns a SanitizeFunc that replaces field values in JSON
// request and response bodies with deterministic fakes using explicitly
// assigned Faker implementations per path.
//
// The seed is a project-level secret used as the HMAC key. The fields map
// associates JSONPath-like paths with specific Faker implementations.
//
// This gives callers explicit control over the faking strategy for each
// field, unlike FakeFields which auto-detects the strategy from the value.
//
// Paths use the same JSONPath-like syntax as RedactBodyPaths:
//   - $.field             -- top-level field
//   - $.nested.field      -- nested field access
//   - $.array[*].field    -- field within each element of an array
//
// If the body is not valid JSON, it is left unchanged (no error).
// If a path does not match any field in the body, it is silently skipped.
// Invalid or unsupported paths are silently ignored.
//
// The returned function does not mutate the input Tape -- it copies the
// body byte slices before modification.
//
// Example:
//
//	sanitizer := NewPipeline(
//	    RedactHeaders(),
//	    FakeFieldsWith("my-seed", map[string]Faker{
//	        "$.user.email": EmailFaker{},
//	        "$.user.phone": PhoneFaker{},
//	        "$.card.number": CreditCardFaker{},
//	        "$.card.cvv": NumericFaker{Length: 3},
//	    }),
//	)
func FakeFieldsWith(seed string, fields map[string]Faker) SanitizeFunc {
	// Parse all paths at construction time.
	var pfs []pathFaker
	for p, f := range fields {
		if pp, ok := parsePath(p); ok {
			pfs = append(pfs, pathFaker{path: pp, faker: f})
		}
	}

	return func(t Tape) Tape {
		newReqBody := fakeBodyFieldsWith(t.Request.Body, pfs, seed)
		if !bytes.Equal(newReqBody, t.Request.Body) {
			t.Request.BodyHash = BodyHashFromBytes(newReqBody)
		}
		t.Request.Body = newReqBody
		// Note: BodyHash is updated for the request but not for the response
		// because RecordedResp does not have a BodyHash field. If a BodyHash
		// field is ever added to RecordedResp, it must be updated here too.
		t.Response.Body = fakeBodyFieldsWith(t.Response.Body, pfs, seed)
		return t
	}
}

// pathFaker pairs a parsed path with its Faker. It is used internally by
// FakeFieldsWith.
type pathFaker struct {
	path  parsedPath
	faker Faker
}

// fakeBodyFieldsWith unmarshals the body as JSON, applies all path-based
// faking with explicit Faker implementations, and re-marshals the result.
// If the body is nil, empty, or not valid JSON, it is returned unchanged.
func fakeBodyFieldsWith(body []byte, pfs []pathFaker, seed string) []byte {
	if len(body) == 0 {
		return body
	}

	var data any
	if err := json.Unmarshal(body, &data); err != nil {
		return body
	}

	for _, pf := range pfs {
		fakeAtPathWith(data, pf.path.segments, seed, pf.faker)
	}

	result, err := json.Marshal(data)
	if err != nil {
		return body
	}
	return result
}

// fakeAtPathWith recursively traverses the JSON structure following the
// given segments and replaces the leaf value using the provided Faker.
// It modifies the data in-place (caller must ensure data is a fresh
// copy from json.Unmarshal).
func fakeAtPathWith(data any, segments []segment, seed string, faker Faker) {
	if len(segments) == 0 {
		return
	}

	seg := segments[0]
	rest := segments[1:]

	obj, ok := data.(map[string]any)
	if !ok {
		return
	}

	val, exists := obj[seg.key]
	if !exists {
		return
	}

	if seg.wildcard {
		arr, ok := val.([]any)
		if !ok {
			return
		}
		if len(rest) == 0 {
			// Wildcard at leaf targets array elements (containers) -- skip.
			return
		}
		for _, elem := range arr {
			fakeAtPathWith(elem, rest, seed, faker)
		}
		return
	}

	// Not a wildcard segment.
	if len(rest) == 0 {
		// Leaf: apply the Faker.
		obj[seg.key] = faker.Fake(seed, val)
		return
	}

	// Intermediate: recurse deeper.
	fakeAtPathWith(val, rest, seed, faker)
}
