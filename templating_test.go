package httptape

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestCtx creates a templateCtx for testing with all fields populated.
func newTestCtx(req *http.Request, reqBody []byte) *templateCtx {
	return &templateCtx{
		req:        req,
		reqBody:    reqBody,
		pathParams: nil,
		tapeID:     "test-tape",
		counters:   &counterState{counters: make(map[string]int64)},
		randSource: rand.Reader,
	}
}

// simpleCtx creates a minimal templateCtx from a request.
func simpleCtx(req *http.Request) *templateCtx {
	return newTestCtx(req, readRequestBody(req))
}

func TestScanTemplateExprs(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []templateExpr
	}{
		{
			name: "no expressions",
			src:  "hello world",
			want: nil,
		},
		{
			name: "single expression",
			src:  "Hello {{request.method}}!",
			want: []templateExpr{
				{raw: "request.method", start: 6, end: 24},
			},
		},
		{
			name: "multiple expressions",
			src:  "{{request.method}} {{request.path}}",
			want: []templateExpr{
				{raw: "request.method", start: 0, end: 18},
				{raw: "request.path", start: 19, end: 35},
			},
		},
		{
			name: "expression with whitespace",
			src:  "{{ request.method }}",
			want: []templateExpr{
				{raw: "request.method", start: 0, end: 20},
			},
		},
		{
			name: "unclosed expression",
			src:  "hello {{request.method",
			want: nil,
		},
		{
			name: "empty expression",
			src:  "hello {{}} world",
			want: []templateExpr{
				{raw: "", start: 6, end: 10},
			},
		},
		{
			name: "nested braces not treated as nested",
			src:  "{{request.headers.X-Key}}",
			want: []templateExpr{
				{raw: "request.headers.X-Key", start: 0, end: 25},
			},
		},
		{
			name: "expression at end of string",
			src:  "prefix {{request.path}}",
			want: []templateExpr{
				{raw: "request.path", start: 7, end: 23},
			},
		},
		{
			name: "expression at start and end",
			src:  "{{request.method}} and {{request.path}}",
			want: []templateExpr{
				{raw: "request.method", start: 0, end: 18},
				{raw: "request.path", start: 23, end: 39},
			},
		},
		{
			name: "only opening braces",
			src:  "{{ no close",
			want: nil,
		},
		{
			name: "only closing braces",
			src:  "no open }}",
			want: nil,
		},
		{
			name: "nested expression",
			src:  "{{faker.name seed=user-{{pathParam.id}}}}",
			want: []templateExpr{
				{raw: "faker.name seed=user-{{pathParam.id}}", start: 0, end: 41},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scanTemplateExprs([]byte(tt.src))
			if len(got) != len(tt.want) {
				t.Fatalf("scanTemplateExprs(%q) returned %d expressions, want %d", tt.src, len(got), len(tt.want))
			}
			for i := range got {
				if got[i].raw != tt.want[i].raw {
					t.Errorf("expr[%d].raw = %q, want %q", i, got[i].raw, tt.want[i].raw)
				}
				if got[i].start != tt.want[i].start {
					t.Errorf("expr[%d].start = %d, want %d", i, got[i].start, tt.want[i].start)
				}
				if got[i].end != tt.want[i].end {
					t.Errorf("expr[%d].end = %d, want %d", i, got[i].end, tt.want[i].end)
				}
			}
		})
	}
}

func TestParseExpr(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantName string
		wantArgs map[string]string
	}{
		{
			name:     "accessor only",
			input:    "request.path",
			wantName: "request.path",
			wantArgs: nil,
		},
		{
			name:     "zero-arg helper",
			input:    "now",
			wantName: "now",
			wantArgs: nil,
		},
		{
			name:     "single-arg helper",
			input:    "now format=unix",
			wantName: "now",
			wantArgs: map[string]string{"format": "unix"},
		},
		{
			name:     "multi-arg helper",
			input:    "randomInt min=1 max=50",
			wantName: "randomInt",
			wantArgs: map[string]string{"min": "1", "max": "50"},
		},
		{
			name:     "faker with seed",
			input:    "faker.email seed=user-42",
			wantName: "faker.email",
			wantArgs: map[string]string{"seed": "user-42"},
		},
		{
			name:     "pathParam accessor",
			input:    "pathParam.id",
			wantName: "pathParam.id",
			wantArgs: nil,
		},
		{
			name:     "empty input",
			input:    "",
			wantName: "",
			wantArgs: nil,
		},
		{
			name:     "nested template in arg",
			input:    "faker.name seed=user-{{pathParam.id}}",
			wantName: "faker.name",
			wantArgs: map[string]string{"seed": "user-{{pathParam.id}}"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseExpr(tt.input)
			if got.name != tt.wantName {
				t.Errorf("parseExpr(%q).name = %q, want %q", tt.input, got.name, tt.wantName)
			}
			if tt.wantArgs == nil {
				if len(got.args) != 0 {
					t.Errorf("parseExpr(%q).args = %v, want nil/empty", tt.input, got.args)
				}
			} else {
				for k, wantV := range tt.wantArgs {
					if gotV, ok := got.args[k]; !ok || gotV != wantV {
						t.Errorf("parseExpr(%q).args[%q] = %q, want %q", tt.input, k, gotV, wantV)
					}
				}
				if len(got.args) != len(tt.wantArgs) {
					t.Errorf("parseExpr(%q).args has %d entries, want %d", tt.input, len(got.args), len(tt.wantArgs))
				}
			}
		})
	}
}

func TestResolveExpr_RequestAccessors(t *testing.T) {
	reqBody := []byte(`{"user":{"email":"alice@example.com","age":30},"active":true,"score":99.5}`)
	req := httptest.NewRequest("POST", "/api/users?page=2&sort=name", bytes.NewReader(reqBody))
	req.Header.Set("X-Request-Id", "req-abc-123")
	req.Header.Set("Content-Type", "application/json")

	ctx := newTestCtx(req, reqBody)

	tests := []struct {
		name    string
		expr    string
		wantVal string
		wantOk  bool
	}{
		{"request.method", "request.method", "POST", true},
		{"request.path", "request.path", "/api/users", true},
		{"request.url", "request.url", "/api/users?page=2&sort=name", true},
		{"request.headers existing", "request.headers.X-Request-Id", "req-abc-123", true},
		{"request.headers case insensitive", "request.headers.x-request-id", "req-abc-123", true},
		{"request.headers missing", "request.headers.X-Missing", "", false},
		{"request.query existing", "request.query.page", "2", true},
		{"request.query missing", "request.query.missing", "", false},
		{"request.body string field", "request.body.user.email", "alice@example.com", true},
		{"request.body number field", "request.body.user.age", "30", true},
		{"request.body boolean field", "request.body.active", "true", true},
		{"request.body fractional number", "request.body.score", "99.5", true},
		{"request.body non-scalar (object)", "request.body.user", "", false},
		{"request.body missing field", "request.body.nonexistent", "", false},
		{"unknown namespace is unresolvable", "state.counter", "", false},
		{"unknown request sub-key", "request.unknown", "", false},
		{"empty expression", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotVal, gotOk := resolveExpr(tt.expr, ctx)
			if gotOk != tt.wantOk {
				t.Errorf("resolveExpr(%q) ok = %v, want %v", tt.expr, gotOk, tt.wantOk)
			}
			if gotVal != tt.wantVal {
				t.Errorf("resolveExpr(%q) = %q, want %q", tt.expr, gotVal, tt.wantVal)
			}
		})
	}
}

func TestResolveExpr_NilBody(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	ctx := newTestCtx(req, nil)
	val, ok := resolveExpr("request.body.field", ctx)
	if ok {
		t.Errorf("expected unresolvable for nil body, got ok=true, val=%q", val)
	}
}

func TestResolveNow(t *testing.T) {
	tests := []struct {
		name   string
		args   map[string]string
		verify func(t *testing.T, result string)
	}{
		{
			name: "default rfc3339",
			args: nil,
			verify: func(t *testing.T, result string) {
				_, err := time.Parse(time.RFC3339, result)
				if err != nil {
					t.Errorf("expected RFC3339 format, got %q: %v", result, err)
				}
			},
		},
		{
			name: "unix format",
			args: map[string]string{"format": "unix"},
			verify: func(t *testing.T, result string) {
				val, err := strconv.ParseInt(result, 10, 64)
				if err != nil {
					t.Errorf("expected unix timestamp, got %q: %v", result, err)
				}
				now := time.Now().Unix()
				if val < now-2 || val > now+2 {
					t.Errorf("unix timestamp %d is too far from now %d", val, now)
				}
			},
		},
		{
			name: "iso format alias",
			args: map[string]string{"format": "iso"},
			verify: func(t *testing.T, result string) {
				_, err := time.Parse(time.RFC3339, result)
				if err != nil {
					t.Errorf("expected RFC3339 format, got %q: %v", result, err)
				}
			},
		},
		{
			name: "unixMillis format",
			args: map[string]string{"format": "unixMillis"},
			verify: func(t *testing.T, result string) {
				val, err := strconv.ParseInt(result, 10, 64)
				if err != nil {
					t.Errorf("expected unix millis, got %q: %v", result, err)
				}
				now := time.Now().UnixMilli()
				if val < now-2000 || val > now+2000 {
					t.Errorf("unixMillis %d is too far from now %d", val, now)
				}
			},
		},
		{
			name: "custom Go format",
			args: map[string]string{"format": "2006-01-02"},
			verify: func(t *testing.T, result string) {
				_, err := time.Parse("2006-01-02", result)
				if err != nil {
					t.Errorf("expected date format, got %q: %v", result, err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := resolveNow(tt.args)
			tt.verify(t, result)
		})
	}
}

func TestResolveUUID(t *testing.T) {
	// Use deterministic random source.
	src := bytes.NewReader(make([]byte, 16))
	ctx := &templateCtx{randSource: src}

	result, err := resolveUUID(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify UUID format: 8-4-4-4-12 hex chars separated by dashes.
	if len(result) != 36 {
		t.Fatalf("UUID length = %d, want 36", len(result))
	}
	parts := strings.Split(result, "-")
	if len(parts) != 5 {
		t.Fatalf("UUID has %d parts, want 5", len(parts))
	}
	wantLens := []int{8, 4, 4, 4, 12}
	for i, part := range parts {
		if len(part) != wantLens[i] {
			t.Errorf("part[%d] length = %d, want %d", i, len(part), wantLens[i])
		}
	}

	// Verify version 4 (character index 14 should be '4').
	if result[14] != '4' {
		t.Errorf("UUID version = %c, want '4'", result[14])
	}
	// Verify variant (character index 19 should be 8, 9, a, or b).
	variant := result[19]
	if variant != '8' && variant != '9' && variant != 'a' && variant != 'b' {
		t.Errorf("UUID variant = %c, want 8/9/a/b", variant)
	}
}

func TestResolveRandomHex(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]string
		wantLen int
		wantErr bool
	}{
		{
			name:    "length 16",
			args:    map[string]string{"length": "16"},
			wantLen: 16,
		},
		{
			name:    "length 1",
			args:    map[string]string{"length": "1"},
			wantLen: 1,
		},
		{
			name:    "missing length",
			args:    nil,
			wantErr: true,
		},
		{
			name:    "non-numeric length",
			args:    map[string]string{"length": "abc"},
			wantErr: true,
		},
		{
			name:    "zero length",
			args:    map[string]string{"length": "0"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &templateCtx{randSource: rand.Reader}
			result, err := resolveRandomHex(tt.args, ctx)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(result) != tt.wantLen {
				t.Errorf("length = %d, want %d", len(result), tt.wantLen)
			}
			// Verify all chars are valid hex.
			for _, c := range result {
				if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
					t.Errorf("invalid hex char %c in %q", c, result)
				}
			}
		})
	}
}

func TestResolveRandomInt(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]string
		wantMin int64
		wantMax int64
		wantErr bool
	}{
		{
			name:    "default range",
			args:    nil,
			wantMin: 0,
			wantMax: 100,
		},
		{
			name:    "custom range",
			args:    map[string]string{"min": "10", "max": "20"},
			wantMin: 10,
			wantMax: 20,
		},
		{
			name:    "non-numeric min",
			args:    map[string]string{"min": "abc"},
			wantErr: true,
		},
		{
			name:    "non-numeric max",
			args:    map[string]string{"max": "abc"},
			wantErr: true,
		},
		{
			name:    "min greater than max",
			args:    map[string]string{"min": "50", "max": "10"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &templateCtx{randSource: rand.Reader}
			result, err := resolveRandomInt(tt.args, ctx)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			val, err := strconv.ParseInt(result, 10, 64)
			if err != nil {
				t.Fatalf("cannot parse result %q as int: %v", result, err)
			}
			if val < tt.wantMin || val > tt.wantMax {
				t.Errorf("result %d outside [%d, %d]", val, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestResolveRandomInt_LargeRanges(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]string
		wantMin int64
		wantMax int64
	}{
		{
			name:    "min=0 max=MaxInt64",
			args:    map[string]string{"min": "0", "max": strconv.FormatInt(math.MaxInt64, 10)},
			wantMin: 0,
			wantMax: math.MaxInt64,
		},
		{
			name:    "min=MinInt64 max=MaxInt64 (full int64 range)",
			args:    map[string]string{"min": strconv.FormatInt(math.MinInt64, 10), "max": strconv.FormatInt(math.MaxInt64, 10)},
			wantMin: math.MinInt64,
			wantMax: math.MaxInt64,
		},
		{
			name:    "min=max returns that value",
			args:    map[string]string{"min": "5", "max": "5"},
			wantMin: 5,
			wantMax: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &templateCtx{randSource: rand.Reader}
			result, err := resolveRandomInt(tt.args, ctx)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			val, err := strconv.ParseInt(result, 10, 64)
			if err != nil {
				t.Fatalf("cannot parse result %q as int: %v", result, err)
			}
			if val < tt.wantMin || val > tt.wantMax {
				t.Errorf("result %d outside [%d, %d]", val, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestResolveCounter(t *testing.T) {
	t.Run("sequential increment", func(t *testing.T) {
		cs := &counterState{counters: make(map[string]int64)}
		ctx := &templateCtx{counters: cs}

		for i := int64(1); i <= 5; i++ {
			result := resolveCounter(nil, ctx)
			want := strconv.FormatInt(i, 10)
			if result != want {
				t.Errorf("counter call %d = %q, want %q", i, result, want)
			}
		}
	})

	t.Run("named counters are independent", func(t *testing.T) {
		cs := &counterState{counters: make(map[string]int64)}
		ctx := &templateCtx{counters: cs}

		r1 := resolveCounter(map[string]string{"name": "a"}, ctx)
		r2 := resolveCounter(map[string]string{"name": "b"}, ctx)
		r3 := resolveCounter(map[string]string{"name": "a"}, ctx)

		if r1 != "1" || r2 != "1" || r3 != "2" {
			t.Errorf("got a=%s, b=%s, a=%s; want 1, 1, 2", r1, r2, r3)
		}
	})

	t.Run("nil counters returns 0", func(t *testing.T) {
		ctx := &templateCtx{counters: nil}
		result := resolveCounter(nil, ctx)
		if result != "0" {
			t.Errorf("got %q, want %q", result, "0")
		}
	})
}

func TestCounterState_WrapAtMaxInt64(t *testing.T) {
	cs := &counterState{counters: make(map[string]int64)}
	cs.counters["test"] = math.MaxInt64 - 1

	v1 := cs.Next("test")
	if v1 != math.MaxInt64 {
		t.Errorf("expected MaxInt64, got %d", v1)
	}

	v2 := cs.Next("test")
	if v2 != 0 {
		t.Errorf("expected 0 after wrap, got %d", v2)
	}
}

func TestCounterState_Reset(t *testing.T) {
	cs := &counterState{counters: make(map[string]int64)}
	cs.Next("a")
	cs.Next("a")
	cs.Next("b")

	// Reset named counter.
	cs.Reset("a")
	v := cs.Next("a")
	if v != 1 {
		t.Errorf("after reset(a), Next(a) = %d, want 1", v)
	}

	// Reset all.
	cs.Reset("")
	va := cs.Next("a")
	vb := cs.Next("b")
	if va != 1 || vb != 1 {
		t.Errorf("after reset all: a=%d, b=%d; want 1, 1", va, vb)
	}
}

func TestCounterState_Concurrent(t *testing.T) {
	cs := &counterState{counters: make(map[string]int64)}
	n := 1000
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			cs.Next("default")
		}()
	}
	wg.Wait()

	got := cs.counters["default"]
	if got != int64(n) {
		t.Errorf("after %d concurrent increments, counter = %d", n, got)
	}
}

func TestResolveFaker_Email(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	ctx := newTestCtx(req, nil)

	result, ok := resolveFaker("email", map[string]string{"seed": "test-seed"}, ctx)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !strings.Contains(result, "@example.com") {
		t.Errorf("expected email format, got %q", result)
	}

	// Deterministic: same seed -> same output.
	result2, _ := resolveFaker("email", map[string]string{"seed": "test-seed"}, ctx)
	if result != result2 {
		t.Errorf("same seed produced different results: %q vs %q", result, result2)
	}

	// Different seed -> different output.
	result3, _ := resolveFaker("email", map[string]string{"seed": "other-seed"}, ctx)
	if result == result3 {
		t.Errorf("different seeds produced same result: %q", result)
	}
}

func TestResolveFaker_Name(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	ctx := newTestCtx(req, nil)

	result, ok := resolveFaker("name", map[string]string{"seed": "test-seed"}, ctx)
	if !ok {
		t.Fatal("expected ok=true")
	}
	parts := strings.Fields(result)
	if len(parts) != 2 {
		t.Errorf("expected first+last name, got %q", result)
	}
}

func TestResolveFaker_Phone(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	ctx := newTestCtx(req, nil)

	result, ok := resolveFaker("phone", map[string]string{"seed": "test-seed"}, ctx)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if result == "" {
		t.Error("expected non-empty phone result")
	}
}

func TestResolveFaker_Address(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	ctx := newTestCtx(req, nil)

	result, ok := resolveFaker("address", map[string]string{"seed": "test-seed"}, ctx)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !strings.Contains(result, ",") {
		t.Errorf("expected address format, got %q", result)
	}
}

func TestResolveFaker_AutoSeed(t *testing.T) {
	req1 := httptest.NewRequest("GET", "/users/1", nil)
	ctx1 := newTestCtx(req1, nil)

	req2 := httptest.NewRequest("GET", "/users/2", nil)
	ctx2 := newTestCtx(req2, nil)

	result1, _ := resolveFaker("email", nil, ctx1)
	result2, _ := resolveFaker("email", nil, ctx2)

	// Different paths -> different output.
	if result1 == result2 {
		t.Errorf("different paths produced same faker output: %q", result1)
	}

	// Same path again -> same output.
	result1b, _ := resolveFaker("email", nil, ctx1)
	if result1 != result1b {
		t.Errorf("same path produced different faker output: %q vs %q", result1, result1b)
	}
}

func TestResolveFaker_Unknown(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	ctx := newTestCtx(req, nil)

	_, ok := resolveFaker("doesnotexist", nil, ctx)
	if ok {
		t.Error("expected ok=false for unknown faker type")
	}
}

func TestResolveFaker_UUID(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	ctx := newTestCtx(req, nil)

	result, ok := resolveFaker("uuid", map[string]string{"seed": "test"}, ctx)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if len(result) != 36 || strings.Count(result, "-") != 4 {
		t.Errorf("expected UUID format, got %q", result)
	}

	// Deterministic.
	result2, _ := resolveFaker("uuid", map[string]string{"seed": "test"}, ctx)
	if result != result2 {
		t.Errorf("same seed produced different UUIDs: %q vs %q", result, result2)
	}
}

func TestResolvePathParam(t *testing.T) {
	tests := []struct {
		name       string
		expr       string
		pathParams map[string]string
		wantVal    string
		wantOk     bool
	}{
		{
			name:       "existing param",
			expr:       "pathParam.id",
			pathParams: map[string]string{"id": "42"},
			wantVal:    "42",
			wantOk:     true,
		},
		{
			name:       "missing param",
			expr:       "pathParam.name",
			pathParams: map[string]string{"id": "42"},
			wantVal:    "",
			wantOk:     false,
		},
		{
			name:    "nil pathParams",
			expr:    "pathParam.id",
			wantVal: "",
			wantOk:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			ctx := newTestCtx(req, nil)
			ctx.pathParams = tt.pathParams

			gotVal, gotOk := resolveExpr(tt.expr, ctx)
			if gotOk != tt.wantOk {
				t.Errorf("resolveExpr(%q) ok = %v, want %v", tt.expr, gotOk, tt.wantOk)
			}
			if gotVal != tt.wantVal {
				t.Errorf("resolveExpr(%q) = %q, want %q", tt.expr, gotVal, tt.wantVal)
			}
		})
	}
}

func TestResolveArgs_Nested(t *testing.T) {
	req := httptest.NewRequest("GET", "/users/42", nil)
	ctx := newTestCtx(req, nil)
	ctx.pathParams = map[string]string{"id": "42"}

	args := map[string]string{"seed": "user-{{pathParam.id}}"}
	resolved := resolveArgs(args, ctx)

	if resolved["seed"] != "user-42" {
		t.Errorf("resolved seed = %q, want %q", resolved["seed"], "user-42")
	}
}

func TestAutoSeed(t *testing.T) {
	s1 := autoSeed("tape1", "/users/1")
	s2 := autoSeed("tape1", "/users/2")
	s3 := autoSeed("tape2", "/users/1")
	s4 := autoSeed("tape1", "/users/1")

	if s1 == s2 {
		t.Error("different paths should produce different seeds")
	}
	if s1 == s3 {
		t.Error("different tape IDs should produce different seeds")
	}
	if s1 != s4 {
		t.Error("same tape+path should produce same seed")
	}
	if len(s1) != 16 {
		t.Errorf("seed length = %d, want 16", len(s1))
	}
}

func TestResolveTemplateBody_BackwardCompat(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		method  string
		path    string
		headers http.Header
		reqBody string
		strict  bool
		want    string
		wantErr bool
	}{
		{
			name:   "no templates",
			body:   `{"id": "pay_123"}`,
			method: "GET",
			path:   "/test",
			want:   `{"id": "pay_123"}`, // fast path: no {{ means no processing
		},
		{
			name:   "method substitution",
			body:   `{"method": "{{request.method}}"}`,
			method: "POST",
			path:   "/test",
			want:   `{"method":"POST"}`,
		},
		{
			name:   "path substitution non-JSON",
			body:   `path={{request.path}}`,
			method: "GET",
			path:   "/users/42",
			want:   `path=/users/42`,
		},
		{
			name:   "url substitution non-JSON",
			body:   `url={{request.url}}`,
			method: "GET",
			path:   "/users?page=1",
			want:   `url=/users?page=1`,
		},
		{
			name:    "header substitution",
			body:    `key={{request.headers.X-Request-Id}}`,
			method:  "GET",
			path:    "/test",
			headers: http.Header{"X-Request-Id": {"req-123"}},
			want:    `key=req-123`,
		},
		{
			name:   "query substitution non-JSON",
			body:   `page={{request.query.page}}`,
			method: "GET",
			path:   "/test?page=5",
			want:   `page=5`,
		},
		{
			name:    "body field substitution",
			body:    `echo={{request.body.user.email}}`,
			method:  "POST",
			path:    "/test",
			reqBody: `{"user":{"email":"bob@example.com"}}`,
			want:    `echo=bob@example.com`,
		},
		{
			name:   "lenient unresolvable replaces with empty",
			body:   `val={{request.headers.Missing}}`,
			method: "GET",
			path:   "/test",
			strict: false,
			want:   `val=`,
		},
		{
			name:    "strict unresolvable returns error",
			body:    `val={{request.headers.Missing}}`,
			method:  "GET",
			path:    "/test",
			strict:  true,
			wantErr: true,
		},
		{
			name:   "unknown namespace in lenient mode resolves to empty",
			body:   `count={{state.counter}}`,
			method: "GET",
			path:   "/test",
			want:   `count=`,
		},
		{
			name:   "nil body returns nil",
			body:   "",
			method: "GET",
			path:   "/test",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var bodyReader *strings.Reader
			if tt.reqBody != "" {
				bodyReader = strings.NewReader(tt.reqBody)
			}
			var req *http.Request
			if bodyReader != nil {
				req = httptest.NewRequest(tt.method, tt.path, bodyReader)
			} else {
				req = httptest.NewRequest(tt.method, tt.path, nil)
			}
			for k, vs := range tt.headers {
				for _, v := range vs {
					req.Header.Add(k, v)
				}
			}

			var bodyBytes []byte
			if tt.body != "" {
				bodyBytes = []byte(tt.body)
			}

			ctx := simpleCtx(req)
			got, err := ResolveTemplateBody(bodyBytes, ctx, tt.strict)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("got %q, want %q", string(got), tt.want)
			}
		})
	}
}

func TestResolveTemplateBody_NilBody(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	ctx := simpleCtx(req)
	got, err := ResolveTemplateBody(nil, ctx, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %q", string(got))
	}
}

func TestResolveTemplateBody_FastPath_NoDelimiters(t *testing.T) {
	body := []byte(`{"no":"templates","here":true}`)
	req := httptest.NewRequest("GET", "/test", nil)
	ctx := simpleCtx(req)
	got, err := ResolveTemplateBody(body, ctx, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Fast path should return the exact same slice.
	if &got[0] != &body[0] {
		t.Error("fast path should return the same slice, got a copy")
	}
}

func TestResolveTemplateJSON(t *testing.T) {
	t.Run("string values resolved", func(t *testing.T) {
		body := []byte(`{"name":"{{request.method}}","count":42}`)
		req := httptest.NewRequest("POST", "/test", nil)
		ctx := simpleCtx(req)

		got, err := resolveTemplateJSON(body, ctx, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should resolve the template and preserve the number.
		if !strings.Contains(string(got), `"POST"`) {
			t.Errorf("expected POST in result, got %q", string(got))
		}
		if !strings.Contains(string(got), "42") {
			t.Errorf("expected 42 preserved, got %q", string(got))
		}
	})

	t.Run("nested objects resolved", func(t *testing.T) {
		body := []byte(`{"outer":{"inner":"{{request.path}}"}}`)
		req := httptest.NewRequest("GET", "/deep", nil)
		ctx := simpleCtx(req)

		got, err := resolveTemplateJSON(body, ctx, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(string(got), `"/deep"`) {
			t.Errorf("expected /deep in result, got %q", string(got))
		}
	})

	t.Run("arrays resolved", func(t *testing.T) {
		body := []byte(`["{{request.method}}","static"]`)
		req := httptest.NewRequest("PUT", "/test", nil)
		ctx := simpleCtx(req)

		got, err := resolveTemplateJSON(body, ctx, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(string(got), `"PUT"`) {
			t.Errorf("expected PUT in result, got %q", string(got))
		}
		if !strings.Contains(string(got), `"static"`) {
			t.Errorf("expected static preserved, got %q", string(got))
		}
	})

	t.Run("special chars properly escaped", func(t *testing.T) {
		body := []byte(`{"val":"{{request.headers.X-Data}}"}`)
		req := httptest.NewRequest("GET", "/test", nil)
		req.Header.Set("X-Data", `value with "quotes" and \backslash`)
		ctx := simpleCtx(req)

		got, err := resolveTemplateJSON(body, ctx, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// The result should be valid JSON.
		var parsed map[string]any
		if err := json.Unmarshal(got, &parsed); err != nil {
			t.Fatalf("result is not valid JSON: %v\nraw: %s", err, string(got))
		}
	})

	t.Run("strict mode error propagated", func(t *testing.T) {
		body := []byte(`{"val":"{{request.headers.Missing}}"}`)
		req := httptest.NewRequest("GET", "/test", nil)
		ctx := simpleCtx(req)

		_, err := resolveTemplateJSON(body, ctx, true)
		if err == nil {
			t.Fatal("expected error in strict mode")
		}
	})
}

func TestResolveTemplateHeaders_WithCtx(t *testing.T) {
	tests := []struct {
		name       string
		headers    http.Header
		method     string
		path       string
		reqHeaders http.Header
		strict     bool
		want       http.Header
		wantErr    bool
	}{
		{
			name:    "no templates in headers",
			headers: http.Header{"Content-Type": {"application/json"}},
			method:  "GET",
			path:    "/test",
			want:    http.Header{"Content-Type": {"application/json"}},
		},
		{
			name:       "header with template",
			headers:    http.Header{"X-Echo": {"{{request.headers.X-Request-Id}}"}},
			method:     "GET",
			path:       "/test",
			reqHeaders: http.Header{"X-Request-Id": {"id-42"}},
			want:       http.Header{"X-Echo": {"id-42"}},
		},
		{
			name:    "header with method template",
			headers: http.Header{"X-Method": {"{{request.method}}"}},
			method:  "DELETE",
			path:    "/test",
			want:    http.Header{"X-Method": {"DELETE"}},
		},
		{
			name:   "nil headers returns nil",
			method: "GET",
			path:   "/test",
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			for k, vs := range tt.reqHeaders {
				for _, v := range vs {
					req.Header.Add(k, v)
				}
			}
			ctx := simpleCtx(req)

			got, err := ResolveTemplateHeaders(tt.headers, ctx, tt.strict)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.want == nil {
				if got != nil {
					t.Fatalf("expected nil, got %v", got)
				}
				return
			}

			for key, wantVals := range tt.want {
				gotVals := got[key]
				if len(gotVals) != len(wantVals) {
					t.Errorf("header %s: got %d values, want %d", key, len(gotVals), len(wantVals))
					continue
				}
				for i := range wantVals {
					if gotVals[i] != wantVals[i] {
						t.Errorf("header %s[%d] = %q, want %q", key, i, gotVals[i], wantVals[i])
					}
				}
			}
		})
	}
}

func TestResolveTemplateBodySimple(t *testing.T) {
	req := httptest.NewRequest("POST", "/test?page=1", strings.NewReader(`{"key":"value"}`))
	req.Header.Set("X-Id", "abc")

	body := []byte(`method={{request.method}} query={{request.query.page}} header={{request.headers.X-Id}}`)
	got, err := ResolveTemplateBodySimple(body, req, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "method=POST query=1 header=abc"
	if string(got) != want {
		t.Errorf("got %q, want %q", string(got), want)
	}
}

func TestResolveTemplateHeadersSimple(t *testing.T) {
	req := httptest.NewRequest("DELETE", "/items/99?force=true", nil)
	req.Header.Set("X-Trace-Id", "trace-abc")

	headers := http.Header{
		"X-Method": {"{{request.method}}"},
		"X-Echo":   {"{{request.headers.X-Trace-Id}}"},
		"X-Static": {"no-template"},
	}
	got, err := ResolveTemplateHeadersSimple(headers, req, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v := got.Get("X-Method"); v != "DELETE" {
		t.Errorf("X-Method = %q, want %q", v, "DELETE")
	}
	if v := got.Get("X-Echo"); v != "trace-abc" {
		t.Errorf("X-Echo = %q, want %q", v, "trace-abc")
	}
	if v := got.Get("X-Static"); v != "no-template" {
		t.Errorf("X-Static = %q, want %q", v, "no-template")
	}
}

func TestResolveTemplateHeadersSimple_Nil(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	got, err := ResolveTemplateHeadersSimple(nil, req, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestScalarToString(t *testing.T) {
	tests := []struct {
		name   string
		input  any
		want   string
		wantOk bool
	}{
		{"string", "hello", "hello", true},
		{"integer float", float64(42), "42", true},
		{"fractional float", float64(3.14), "3.14", true},
		{"negative integer", float64(-7), "-7", true},
		{"zero", float64(0), "0", true},
		{"true", true, "true", true},
		{"false", false, "false", true},
		{"nil", nil, "", false},
		{"map", map[string]any{"k": "v"}, "", false},
		{"slice", []any{1, 2}, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := scalarToString(tt.input)
			if ok != tt.wantOk {
				t.Errorf("scalarToString(%v) ok = %v, want %v", tt.input, ok, tt.wantOk)
			}
			if got != tt.want {
				t.Errorf("scalarToString(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestResolveBodyField(t *testing.T) {
	body := []byte(`{"user":{"email":"a@b.com","name":"Alice"},"count":3}`)

	tests := []struct {
		name   string
		path   string
		want   string
		wantOk bool
	}{
		{"top level string", "count", "3", true},
		{"nested string", "user.email", "a@b.com", true},
		{"nested name", "user.name", "Alice", true},
		{"missing field", "user.phone", "", false},
		{"non-scalar object", "user", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := resolveBodyField(body, tt.path)
			if ok != tt.wantOk {
				t.Errorf("resolveBodyField(%q) ok = %v, want %v", tt.path, ok, tt.wantOk)
			}
			if got != tt.want {
				t.Errorf("resolveBodyField(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestResolveBodyField_EmptyBody(t *testing.T) {
	got, ok := resolveBodyField(nil, "field")
	if ok {
		t.Errorf("expected false for nil body, got true with value %q", got)
	}

	got2, ok2 := resolveBodyField([]byte{}, "field")
	if ok2 {
		t.Errorf("expected false for empty body, got true with value %q", got2)
	}
}

func TestResolveBodyField_InvalidJSON(t *testing.T) {
	got, ok := resolveBodyField([]byte("not-json"), "field")
	if ok {
		t.Errorf("expected false for invalid JSON, got true with value %q", got)
	}
}

func TestResolveBodyField_InvalidPath(t *testing.T) {
	got, ok := resolveBodyField([]byte(`{"a":"b"}`), "")
	if ok {
		t.Errorf("expected false for empty path, got true with value %q", got)
	}
}

func TestReadRequestBody(t *testing.T) {
	t.Run("truly nil body", func(t *testing.T) {
		req := &http.Request{Body: nil}
		got := readRequestBody(req)
		if got != nil {
			t.Errorf("expected nil for truly nil body, got %q", string(got))
		}
	})

	t.Run("httptest nil body", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		got := readRequestBody(req)
		if len(got) != 0 {
			t.Errorf("expected empty, got %q", string(got))
		}
	})

	t.Run("non-nil body", func(t *testing.T) {
		original := "hello body"
		req := httptest.NewRequest("POST", "/test", strings.NewReader(original))
		got := readRequestBody(req)
		if string(got) != original {
			t.Errorf("got %q, want %q", string(got), original)
		}
	})
}

func TestFindMatchingClose(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		openIdx int
		want    int
	}{
		{
			name:    "simple close",
			src:     "{{hello}}",
			openIdx: 0,
			want:    7,
		},
		{
			name:    "nested",
			src:     "{{outer {{inner}}}}",
			openIdx: 0,
			want:    17,
		},
		{
			name:    "no close",
			src:     "{{unclosed",
			openIdx: 0,
			want:    -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findMatchingClose([]byte(tt.src), tt.openIdx)
			if got != tt.want {
				t.Errorf("findMatchingClose(%q, %d) = %d, want %d", tt.src, tt.openIdx, got, tt.want)
			}
		})
	}
}
