package httptape

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRedactedFaker_String(t *testing.T) {
	f := RedactedFaker{}
	got := f.Fake("seed", "secret-value")
	if got != Redacted {
		t.Errorf("got %q, want %q", got, Redacted)
	}
}

func TestRedactedFaker_Number(t *testing.T) {
	f := RedactedFaker{}
	got := f.Fake("seed", float64(42))
	if got != float64(0) {
		t.Errorf("got %v, want 0", got)
	}
}

func TestRedactedFaker_Bool(t *testing.T) {
	f := RedactedFaker{}
	got := f.Fake("seed", true)
	if got != false {
		t.Errorf("got %v, want false", got)
	}
}

func TestRedactedFaker_Nil(t *testing.T) {
	f := RedactedFaker{}
	got := f.Fake("seed", nil)
	if got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestFixedFaker(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{"string", "fixed-value"},
		{"number", float64(99)},
		{"bool", true},
		{"nil", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := FixedFaker{Value: tt.value}
			got := f.Fake("seed", "anything")
			if got != tt.value {
				t.Errorf("got %v, want %v", got, tt.value)
			}
		})
	}
}

func TestHMACFaker_String(t *testing.T) {
	f := HMACFaker{}
	got := f.Fake("seed", "hello")
	s, ok := got.(string)
	if !ok {
		t.Fatalf("expected string, got %T", got)
	}
	if !strings.HasPrefix(s, "fake_") {
		t.Errorf("got %q, want prefix \"fake_\"", s)
	}
}

func TestHMACFaker_Number(t *testing.T) {
	f := HMACFaker{}
	got := f.Fake("seed", float64(42))
	n, ok := got.(float64)
	if !ok {
		t.Fatalf("expected float64, got %T", got)
	}
	if n <= 0 {
		t.Errorf("got %v, want positive number", n)
	}
}

func TestHMACFaker_Deterministic(t *testing.T) {
	f := HMACFaker{}
	a := f.Fake("seed", "value")
	b := f.Fake("seed", "value")
	if a != b {
		t.Errorf("non-deterministic: %v != %v", a, b)
	}
}

func TestHMACFaker_DifferentSeed(t *testing.T) {
	f := HMACFaker{}
	a := f.Fake("seed1", "value")
	b := f.Fake("seed2", "value")
	if a == b {
		t.Errorf("different seeds produced same result: %v", a)
	}
}

func TestHMACFaker_Bool(t *testing.T) {
	f := HMACFaker{}
	got := f.Fake("seed", true)
	if got != true {
		t.Errorf("bool should be unchanged, got %v", got)
	}
}

func TestEmailFaker(t *testing.T) {
	f := EmailFaker{}
	got := f.Fake("seed", "alice@corp.com")
	s, ok := got.(string)
	if !ok {
		t.Fatalf("expected string, got %T", got)
	}
	if !strings.HasPrefix(s, "user_") || !strings.HasSuffix(s, "@example.com") {
		t.Errorf("got %q, want user_<hash>@example.com", s)
	}
}

func TestEmailFaker_Deterministic(t *testing.T) {
	f := EmailFaker{}
	a := f.Fake("seed", "alice@corp.com")
	b := f.Fake("seed", "alice@corp.com")
	if a != b {
		t.Errorf("non-deterministic: %v != %v", a, b)
	}
}

func TestEmailFaker_NonString(t *testing.T) {
	f := EmailFaker{}
	got := f.Fake("seed", float64(42))
	if got != float64(42) {
		t.Errorf("non-string should be unchanged, got %v", got)
	}
}

func TestPhoneFaker_PreservesFormat(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		format string // regex-like description of expected format
	}{
		{"US format", "+1 (555) 123-4567", "+# (###) ###-####"},
		{"simple", "1234567890", "##########"},
		{"dashes", "123-456-7890", "###-###-####"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := PhoneFaker{}
			got := f.Fake("seed", tt.input)
			s, ok := got.(string)
			if !ok {
				t.Fatalf("expected string, got %T", got)
			}
			if len(s) != len(tt.input) {
				t.Errorf("length mismatch: got %d, want %d", len(s), len(tt.input))
			}
			// Verify non-digit characters preserved.
			for i, c := range tt.input {
				if c < '0' || c > '9' {
					if rune(s[i]) != c {
						t.Errorf("format char at %d: got %c, want %c", i, s[i], c)
					}
				} else {
					if s[i] < '0' || s[i] > '9' {
						t.Errorf("expected digit at %d, got %c", i, s[i])
					}
				}
			}
		})
	}
}

func TestPhoneFaker_Deterministic(t *testing.T) {
	f := PhoneFaker{}
	a := f.Fake("seed", "555-1234")
	b := f.Fake("seed", "555-1234")
	if a != b {
		t.Errorf("non-deterministic: %v != %v", a, b)
	}
}

func TestPhoneFaker_NonString(t *testing.T) {
	f := PhoneFaker{}
	got := f.Fake("seed", float64(42))
	if got != float64(42) {
		t.Errorf("non-string should be unchanged, got %v", got)
	}
}

func TestCreditCardFaker_Format(t *testing.T) {
	f := CreditCardFaker{}
	got := f.Fake("seed", "4532-1234-5678-9012")
	s, ok := got.(string)
	if !ok {
		t.Fatalf("expected string, got %T", got)
	}
	// Should be XXXX-XXXX-XXXX-XXXX format.
	parts := strings.Split(s, "-")
	if len(parts) != 4 {
		t.Fatalf("expected 4 parts, got %d: %q", len(parts), s)
	}
	for i, part := range parts {
		if len(part) != 4 {
			t.Errorf("part[%d] length = %d, want 4", i, len(part))
		}
	}
}

func TestCreditCardFaker_PreservesIssuerPrefix(t *testing.T) {
	f := CreditCardFaker{}
	got := f.Fake("seed", "4532-1234-5678-9012")
	s := got.(string)
	// First 4 chars should be the first 4 of the prefix "453212".
	digits := strings.ReplaceAll(s, "-", "")
	if !strings.HasPrefix(digits, "4532") {
		t.Errorf("expected issuer prefix 4532, got %q", digits[:4])
	}
}

func TestCreditCardFaker_ValidLuhn(t *testing.T) {
	f := CreditCardFaker{}
	inputs := []string{
		"4532-1234-5678-9012",
		"5500-0000-0000-0004",
		"3782-8224-6310-005",
	}
	for _, input := range inputs {
		got := f.Fake("seed", input)
		s := got.(string)
		digits := strings.ReplaceAll(s, "-", "")
		if !isValidLuhn(digits) {
			t.Errorf("invalid Luhn for input %q -> %q (digits: %s)", input, s, digits)
		}
	}
}

// isValidLuhn checks the Luhn checksum of a digit string.
func isValidLuhn(digits string) bool {
	sum := 0
	nDigits := len(digits)
	parity := nDigits % 2
	for i := 0; i < nDigits; i++ {
		d := int(digits[i] - '0')
		if i%2 == parity {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return sum%10 == 0
}

func TestCreditCardFaker_Deterministic(t *testing.T) {
	f := CreditCardFaker{}
	a := f.Fake("seed", "4532-1234-5678-9012")
	b := f.Fake("seed", "4532-1234-5678-9012")
	if a != b {
		t.Errorf("non-deterministic: %v != %v", a, b)
	}
}

func TestCreditCardFaker_NonString(t *testing.T) {
	f := CreditCardFaker{}
	got := f.Fake("seed", float64(42))
	if got != float64(42) {
		t.Errorf("non-string should be unchanged, got %v", got)
	}
}

func TestCreditCardFaker_ShortInput(t *testing.T) {
	// Fewer than 6 digits should fall back to "400000" prefix.
	f := CreditCardFaker{}
	got := f.Fake("seed", "12")
	s := got.(string)
	digits := strings.ReplaceAll(s, "-", "")
	if !strings.HasPrefix(digits, "4000") {
		t.Errorf("expected fallback prefix 4000, got %q", digits[:4])
	}
	if !isValidLuhn(digits) {
		t.Errorf("invalid Luhn for short input: %s", digits)
	}
}

func TestNumericFaker(t *testing.T) {
	tests := []struct {
		name   string
		length int
	}{
		{"3 digits", 3},
		{"10 digits", 10},
		{"40 digits (exceeds HMAC length)", 40},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NumericFaker{Length: tt.length}
			got := f.Fake("seed", "input")
			s, ok := got.(string)
			if !ok {
				t.Fatalf("expected string, got %T", got)
			}
			if len(s) != tt.length {
				t.Errorf("length = %d, want %d", len(s), tt.length)
			}
			for i, c := range s {
				if c < '0' || c > '9' {
					t.Errorf("non-digit at %d: %c", i, c)
				}
			}
		})
	}
}

func TestNumericFaker_Deterministic(t *testing.T) {
	f := NumericFaker{Length: 5}
	a := f.Fake("seed", "value")
	b := f.Fake("seed", "value")
	if a != b {
		t.Errorf("non-deterministic: %v != %v", a, b)
	}
}

func TestNumericFaker_NonString(t *testing.T) {
	f := NumericFaker{Length: 5}
	got := f.Fake("seed", float64(42))
	if got != float64(42) {
		t.Errorf("non-string should be unchanged, got %v", got)
	}
}

func TestDateFaker(t *testing.T) {
	f := DateFaker{Format: "2006-01-02"}
	got := f.Fake("seed", "2023-01-15")
	s, ok := got.(string)
	if !ok {
		t.Fatalf("expected string, got %T", got)
	}
	_, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Errorf("invalid date format: %q, err: %v", s, err)
	}
}

func TestDateFaker_DefaultFormat(t *testing.T) {
	f := DateFaker{}
	got := f.Fake("seed", "some date")
	s := got.(string)
	_, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Errorf("invalid date with default format: %q, err: %v", s, err)
	}
}

func TestDateFaker_Deterministic(t *testing.T) {
	f := DateFaker{Format: "2006-01-02"}
	a := f.Fake("seed", "2023-01-15")
	b := f.Fake("seed", "2023-01-15")
	if a != b {
		t.Errorf("non-deterministic: %v != %v", a, b)
	}
}

func TestDateFaker_NonString(t *testing.T) {
	f := DateFaker{Format: "2006-01-02"}
	got := f.Fake("seed", float64(42))
	if got != float64(42) {
		t.Errorf("non-string should be unchanged, got %v", got)
	}
}

func TestPatternFaker(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		check   func(string) bool
	}{
		{
			"SSN",
			"###-##-####",
			func(s string) bool {
				if len(s) != 11 {
					return false
				}
				return s[3] == '-' && s[6] == '-'
			},
		},
		{
			"alphanumeric",
			"??-###",
			func(s string) bool {
				if len(s) != 6 {
					return false
				}
				return s[0] >= 'a' && s[0] <= 'z' &&
					s[1] >= 'a' && s[1] <= 'z' &&
					s[2] == '-' &&
					s[3] >= '0' && s[3] <= '9'
			},
		},
		{
			"literal only",
			"ABC-123",
			func(s string) bool { return s == "ABC-123" },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := PatternFaker{Pattern: tt.pattern}
			got := f.Fake("seed", "input")
			s, ok := got.(string)
			if !ok {
				t.Fatalf("expected string, got %T", got)
			}
			if !tt.check(s) {
				t.Errorf("pattern %q produced invalid output: %q", tt.pattern, s)
			}
		})
	}
}

func TestPatternFaker_Deterministic(t *testing.T) {
	f := PatternFaker{Pattern: "###-##-####"}
	a := f.Fake("seed", "value")
	b := f.Fake("seed", "value")
	if a != b {
		t.Errorf("non-deterministic: %v != %v", a, b)
	}
}

func TestPatternFaker_NonString(t *testing.T) {
	f := PatternFaker{Pattern: "###"}
	got := f.Fake("seed", float64(42))
	if got != float64(42) {
		t.Errorf("non-string should be unchanged, got %v", got)
	}
}

func TestPrefixFaker(t *testing.T) {
	f := PrefixFaker{Prefix: "cust_"}
	got := f.Fake("seed", "original-id")
	s, ok := got.(string)
	if !ok {
		t.Fatalf("expected string, got %T", got)
	}
	if !strings.HasPrefix(s, "cust_") {
		t.Errorf("got %q, want prefix \"cust_\"", s)
	}
	// Prefix + 16 hex chars.
	if len(s) != 5+16 {
		t.Errorf("length = %d, want %d", len(s), 5+16)
	}
}

func TestPrefixFaker_Deterministic(t *testing.T) {
	f := PrefixFaker{Prefix: "id_"}
	a := f.Fake("seed", "value")
	b := f.Fake("seed", "value")
	if a != b {
		t.Errorf("non-deterministic: %v != %v", a, b)
	}
}

func TestPrefixFaker_NonString(t *testing.T) {
	f := PrefixFaker{Prefix: "x_"}
	got := f.Fake("seed", float64(42))
	if got != float64(42) {
		t.Errorf("non-string should be unchanged, got %v", got)
	}
}

func TestAddressFaker(t *testing.T) {
	f := AddressFaker{}
	got := f.Fake("seed", "123 Main St, Anytown, CA 90210")
	s, ok := got.(string)
	if !ok {
		t.Fatalf("expected string, got %T", got)
	}
	// Should contain a comma (city separator).
	if !strings.Contains(s, ",") {
		t.Errorf("address missing comma: %q", s)
	}
	// Should have a 5-digit zip.
	parts := strings.Split(s, " ")
	zip := parts[len(parts)-1]
	if len(zip) != 5 {
		t.Errorf("zip length = %d, want 5: %q", len(zip), zip)
	}
	for _, c := range zip {
		if c < '0' || c > '9' {
			t.Errorf("non-digit in zip: %c", c)
		}
	}
}

func TestAddressFaker_Deterministic(t *testing.T) {
	f := AddressFaker{}
	a := f.Fake("seed", "some address")
	b := f.Fake("seed", "some address")
	if a != b {
		t.Errorf("non-deterministic: %v != %v", a, b)
	}
}

func TestAddressFaker_NonString(t *testing.T) {
	f := AddressFaker{}
	got := f.Fake("seed", float64(42))
	if got != float64(42) {
		t.Errorf("non-string should be unchanged, got %v", got)
	}
}

func TestLuhnCheckDigit(t *testing.T) {
	// Known card numbers with valid Luhn checksums.
	tests := []struct {
		base string
		want byte
	}{
		// Visa test card: 4111111111111111 -> base=411111111111111, check=1
		{"411111111111111", 1},
		// Mastercard test: 5500000000000004 -> base=550000000000000, check=4
		{"550000000000000", 4},
	}
	for _, tt := range tests {
		got := luhnCheckDigit(tt.base)
		if got != tt.want {
			t.Errorf("luhnCheckDigit(%q) = %d, want %d", tt.base, got, tt.want)
		}
	}
}

func TestFakeFieldsWith_Basic(t *testing.T) {
	body := []byte(`{"email":"alice@corp.com","phone":"555-1234","name":"Alice"}`)
	tape := Tape{
		Request: RecordedReq{
			Method:  "POST",
			URL:     "https://example.com/api",
			Body:    body,
			Headers: make(map[string][]string),
		},
		Response: RecordedResp{
			StatusCode: 200,
			Body:       body,
			Headers:    make(map[string][]string),
		},
	}

	fn := FakeFieldsWith("test-seed", map[string]Faker{
		"$.email": EmailFaker{},
		"$.phone": PhoneFaker{},
	})

	result := fn(tape)

	// Parse result bodies.
	var reqData map[string]any
	if err := json.Unmarshal(result.Request.Body, &reqData); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}

	email, ok := reqData["email"].(string)
	if !ok {
		t.Fatal("email not a string")
	}
	if !strings.HasSuffix(email, "@example.com") {
		t.Errorf("email = %q, want *@example.com", email)
	}

	phone, ok := reqData["phone"].(string)
	if !ok {
		t.Fatal("phone not a string")
	}
	if len(phone) != len("555-1234") {
		t.Errorf("phone length = %d, want %d", len(phone), len("555-1234"))
	}
	if phone[3] != '-' {
		t.Errorf("phone format broken: %q", phone)
	}

	// Name should be unchanged.
	name, ok := reqData["name"].(string)
	if !ok || name != "Alice" {
		t.Errorf("name = %q, want \"Alice\"", name)
	}
}

func TestFakeFieldsWith_NestedAndWildcard(t *testing.T) {
	body := []byte(`{"users":[{"email":"a@b.com"},{"email":"c@d.com"}],"meta":{"key":"val"}}`)
	tape := Tape{
		Request: RecordedReq{
			Body:    body,
			Headers: make(map[string][]string),
		},
		Response: RecordedResp{
			Body:    body,
			Headers: make(map[string][]string),
		},
	}

	fn := FakeFieldsWith("seed", map[string]Faker{
		"$.users[*].email": EmailFaker{},
		"$.meta.key":       RedactedFaker{},
	})

	result := fn(tape)

	var data map[string]any
	if err := json.Unmarshal(result.Request.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	users := data["users"].([]any)
	for i, u := range users {
		m := u.(map[string]any)
		email := m["email"].(string)
		if !strings.HasSuffix(email, "@example.com") {
			t.Errorf("users[%d].email = %q, want *@example.com", i, email)
		}
	}

	meta := data["meta"].(map[string]any)
	if meta["key"] != Redacted {
		t.Errorf("meta.key = %v, want %q", meta["key"], Redacted)
	}
}

func TestFakeFieldsWith_InvalidJSON(t *testing.T) {
	body := []byte("not json")
	tape := Tape{
		Request: RecordedReq{
			Body:    body,
			Headers: make(map[string][]string),
		},
		Response: RecordedResp{
			Body:    body,
			Headers: make(map[string][]string),
		},
	}

	fn := FakeFieldsWith("seed", map[string]Faker{
		"$.email": EmailFaker{},
	})

	result := fn(tape)
	if string(result.Request.Body) != "not json" {
		t.Errorf("non-JSON body should be unchanged, got %q", result.Request.Body)
	}
}

func TestFakeFieldsWith_EmptyBody(t *testing.T) {
	tape := Tape{
		Request: RecordedReq{
			Headers: make(map[string][]string),
		},
		Response: RecordedResp{
			Headers: make(map[string][]string),
		},
	}

	fn := FakeFieldsWith("seed", map[string]Faker{
		"$.email": EmailFaker{},
	})

	result := fn(tape)
	if result.Request.Body != nil {
		t.Errorf("nil body should remain nil, got %v", result.Request.Body)
	}
}

func TestFakeFieldsWith_Deterministic(t *testing.T) {
	body := []byte(`{"email":"test@test.com"}`)
	tape := Tape{
		Request:  RecordedReq{Body: body, Headers: make(map[string][]string)},
		Response: RecordedResp{Body: body, Headers: make(map[string][]string)},
	}

	fn := FakeFieldsWith("seed", map[string]Faker{
		"$.email": EmailFaker{},
	})

	r1 := fn(tape)
	r2 := fn(tape)
	if string(r1.Request.Body) != string(r2.Request.Body) {
		t.Errorf("non-deterministic:\n  first:  %s\n  second: %s", r1.Request.Body, r2.Request.Body)
	}
}

func TestNameFaker(t *testing.T) {
	f := NameFaker{}

	// Deterministic: same input = same output.
	r1 := f.Fake("seed", "Alice Johnson")
	r2 := f.Fake("seed", "Alice Johnson")
	if r1 != r2 {
		t.Errorf("not deterministic: %v vs %v", r1, r2)
	}

	// Produces a "First Last" format.
	name, ok := r1.(string)
	if !ok {
		t.Fatalf("expected string, got %T", r1)
	}
	parts := strings.Fields(name)
	if len(parts) != 2 {
		t.Errorf("expected 'First Last', got %q", name)
	}

	// Different input = different output.
	r3 := f.Fake("seed", "Bob Smith")
	if r1 == r3 {
		t.Errorf("different inputs produced same name: %v", r1)
	}

	// Different seed = different output.
	r4 := f.Fake("other-seed", "Alice Johnson")
	if r1 == r4 {
		t.Errorf("different seeds produced same name: %v", r1)
	}

	// Non-string passthrough.
	if f.Fake("seed", 42.0) != 42.0 {
		t.Error("non-string should pass through")
	}
}
