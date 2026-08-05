package httptape

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNewTape(t *testing.T) {
	req := RecordedReq{
		Method: "GET",
		URL:    "http://example.com",
	}
	resp := RecordedResp{
		StatusCode: 200,
	}

	tape := NewTape("test-route", req, resp)

	if tape.ID == "" {
		t.Error("NewTape().ID is empty, want UUID")
	}
	if tape.Route != "test-route" {
		t.Errorf("NewTape().Route = %q, want %q", tape.Route, "test-route")
	}
	if tape.RecordedAt.IsZero() {
		t.Error("NewTape().RecordedAt is zero, want current time")
	}
	if tape.Request.Method != "GET" {
		t.Errorf("NewTape().Request.Method = %q, want %q", tape.Request.Method, "GET")
	}
	if tape.Response.StatusCode != 200 {
		t.Errorf("NewTape().Response.StatusCode = %d, want %d", tape.Response.StatusCode, 200)
	}
}

func TestNewTape_UniqueIDs(t *testing.T) {
	req := RecordedReq{Method: "GET", URL: "http://example.com"}
	resp := RecordedResp{StatusCode: 200}

	tape1 := NewTape("route", req, resp)
	tape2 := NewTape("route", req, resp)

	if tape1.ID == tape2.ID {
		t.Errorf("NewTape() produced duplicate IDs: %q", tape1.ID)
	}
}

func TestNewUUID_Format(t *testing.T) {
	uuid := newUUID()

	// UUID v4 format: xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx
	parts := strings.Split(uuid, "-")
	if len(parts) != 5 {
		t.Fatalf("UUID %q has %d parts, want 5", uuid, len(parts))
	}

	expectedLens := []int{8, 4, 4, 4, 12}
	for i, part := range parts {
		if len(part) != expectedLens[i] {
			t.Errorf("UUID part %d = %q (len %d), want len %d", i, part, len(part), expectedLens[i])
		}
	}

	// Version nibble must be '4'.
	if parts[2][0] != '4' {
		t.Errorf("UUID version nibble = %c, want '4'", parts[2][0])
	}

	// Variant nibble must be 8, 9, a, or b.
	variant := parts[3][0]
	if variant != '8' && variant != '9' && variant != 'a' && variant != 'b' {
		t.Errorf("UUID variant nibble = %c, want one of 8, 9, a, b", variant)
	}
}

func TestBodyHashFromBytes(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  string
	}{
		{
			name:  "nil input",
			input: nil,
			want:  "",
		},
		{
			name:  "empty input",
			input: []byte{},
			want:  "",
		},
		{
			name:  "non-empty input",
			input: []byte("hello world"),
			// SHA-256 of "hello world"
			want: "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BodyHashFromBytes(tt.input)
			if got != tt.want {
				t.Errorf("BodyHashFromBytes() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBodyHashFromBytes_Deterministic(t *testing.T) {
	input := []byte("deterministic test input")
	hash1 := BodyHashFromBytes(input)
	hash2 := BodyHashFromBytes(input)

	if hash1 != hash2 {
		t.Errorf("BodyHashFromBytes() not deterministic: %q != %q", hash1, hash2)
	}
}

// --- ADR-41: Body marshal/unmarshal tests ---

func TestRecordedReq_MarshalJSON_JSONBody(t *testing.T) {
	r := RecordedReq{
		Method:  "POST",
		URL:     "http://example.com/api",
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    []byte(`{"key":"value"}`),
	}

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	// The body should appear as native JSON, not a string.
	if !strings.Contains(string(data), `"body":{"key":"value"}`) {
		t.Errorf("expected native JSON body, got: %s", data)
	}
}

func TestRecordedReq_MarshalJSON_TextBody(t *testing.T) {
	r := RecordedReq{
		Method:  "POST",
		URL:     "http://example.com/api",
		Headers: http.Header{"Content-Type": {"text/plain"}},
		Body:    []byte("hello world"),
	}

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	if !strings.Contains(string(data), `"body":"hello world"`) {
		t.Errorf("expected text string body, got: %s", data)
	}
}

func TestRecordedReq_MarshalJSON_BinaryBody(t *testing.T) {
	r := RecordedReq{
		Method:  "GET",
		URL:     "http://example.com/image",
		Headers: http.Header{"Content-Type": {"image/png"}},
		Body:    []byte{0x89, 0x50, 0x4E, 0x47},
	}

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	// Should be base64 string "iVBORw==" for {0x89, 0x50, 0x4E, 0x47}.
	if !strings.Contains(string(data), `"body":"iVBORw=="`) {
		t.Errorf("expected base64 body, got: %s", data)
	}
}

func TestRecordedReq_MarshalJSON_NilBody(t *testing.T) {
	r := RecordedReq{
		Method:  "GET",
		URL:     "http://example.com/api",
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    nil,
	}

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	if !strings.Contains(string(data), `"body":null`) {
		t.Errorf("expected null body, got: %s", data)
	}
}

func TestRecordedReq_MarshalJSON_EmptyBody(t *testing.T) {
	r := RecordedReq{
		Method:  "GET",
		URL:     "http://example.com/api",
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    []byte{},
	}

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	if !strings.Contains(string(data), `"body":null`) {
		t.Errorf("expected null body for empty slice, got: %s", data)
	}
}

func TestRecordedReq_MarshalJSON_InvalidJSONWithJSONCT(t *testing.T) {
	r := RecordedReq{
		Method:  "POST",
		URL:     "http://example.com/api",
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    []byte("not valid json {{{"),
	}

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	// Should fall back to base64 since the body is not valid JSON.
	if strings.Contains(string(data), `"body":"not valid json`) {
		t.Errorf("expected base64 fallback, but got raw text: %s", data)
	}
	// Should still have a "body" field.
	if !strings.Contains(string(data), `"body":`) {
		t.Errorf("missing body field: %s", data)
	}
}

func TestRecordedReq_MarshalJSON_VendorJSON(t *testing.T) {
	r := RecordedReq{
		Method:  "POST",
		URL:     "http://example.com/api",
		Headers: http.Header{"Content-Type": {"application/vnd.api+json"}},
		Body:    []byte(`{"data":{"type":"users","id":"1"}}`),
	}

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	// Should emit native JSON for vendor +json types.
	if !strings.Contains(string(data), `"body":{"data":{"type":"users","id":"1"}}`) {
		t.Errorf("expected native JSON body for vendor +json, got: %s", data)
	}
}

func TestRecordedResp_MarshalJSON_JSONBody(t *testing.T) {
	r := RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{"Content-Type": {"application/json"}},
		Body:       []byte(`[1,2,3]`),
	}

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	if !strings.Contains(string(data), `"body":[1,2,3]`) {
		t.Errorf("expected native JSON array body, got: %s", data)
	}
}

func TestRecordedResp_MarshalJSON_NilBodyWithSSE(t *testing.T) {
	r := RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{"Content-Type": {"text/event-stream"}},
		Body:       nil,
		SSEEvents:  []SSEEvent{{Data: "hello"}},
	}

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	if !strings.Contains(string(data), `"body":null`) {
		t.Errorf("expected null body for SSE tape, got: %s", data)
	}
	if !strings.Contains(string(data), `"sse_events"`) {
		t.Errorf("expected sse_events field, got: %s", data)
	}
}

func TestRecordedReq_MarshalJSON_MissingCT(t *testing.T) {
	r := RecordedReq{
		Method: "POST",
		URL:    "http://example.com/api",
		Body:   []byte("some data"),
	}

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	// No Content-Type -> binary -> base64.
	if strings.Contains(string(data), `"body":"some data"`) {
		t.Errorf("expected base64, got raw text: %s", data)
	}
}

// --- Unmarshal tests ---

func TestRecordedReq_UnmarshalJSON_NativeJSON(t *testing.T) {
	input := `{
		"method": "POST",
		"url": "http://example.com/api",
		"headers": {"Content-Type": ["application/json"]},
		"body": {"key": "value"},
		"body_hash": ""
	}`

	var r RecordedReq
	if err := json.Unmarshal([]byte(input), &r); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	// Body should be compact JSON bytes.
	want := `{"key":"value"}`
	if string(r.Body) != want {
		t.Errorf("Body = %q, want %q", string(r.Body), want)
	}
}

func TestRecordedReq_UnmarshalJSON_StringText(t *testing.T) {
	input := `{
		"method": "POST",
		"url": "http://example.com/api",
		"headers": {"Content-Type": ["text/plain"]},
		"body": "hello world",
		"body_hash": ""
	}`

	var r RecordedReq
	if err := json.Unmarshal([]byte(input), &r); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if string(r.Body) != "hello world" {
		t.Errorf("Body = %q, want %q", string(r.Body), "hello world")
	}
}

func TestRecordedReq_UnmarshalJSON_Base64Binary(t *testing.T) {
	// AQID is base64 for []byte{1, 2, 3}
	input := `{
		"method": "GET",
		"url": "http://example.com/image",
		"headers": {"Content-Type": ["image/png"]},
		"body": "AQID",
		"body_hash": ""
	}`

	var r RecordedReq
	if err := json.Unmarshal([]byte(input), &r); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	want := []byte{1, 2, 3}
	if !bytes.Equal(r.Body, want) {
		t.Errorf("Body = %v, want %v", r.Body, want)
	}
}

func TestRecordedReq_UnmarshalJSON_Null(t *testing.T) {
	input := `{
		"method": "GET",
		"url": "http://example.com/api",
		"headers": {},
		"body": null,
		"body_hash": ""
	}`

	var r RecordedReq
	if err := json.Unmarshal([]byte(input), &r); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if r.Body != nil {
		t.Errorf("Body = %v, want nil", r.Body)
	}
}

func TestRecordedReq_UnmarshalJSON_LegacyBase64JSON(t *testing.T) {
	// eyJrIjoidiJ9 is base64 for {"k":"v"}
	input := `{
		"method": "POST",
		"url": "http://example.com/api",
		"headers": {"Content-Type": ["application/json"]},
		"body": "eyJrIjoidiJ9",
		"body_hash": ""
	}`

	var r RecordedReq
	if err := json.Unmarshal([]byte(input), &r); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	want := `{"k":"v"}`
	if string(r.Body) != want {
		t.Errorf("Body = %q, want %q", string(r.Body), want)
	}
}

func TestRecordedReq_UnmarshalJSON_LegacyBodyEncoding(t *testing.T) {
	// Legacy fixtures may have body_encoding field -- it should be silently ignored.
	input := `{
		"method": "POST",
		"url": "http://example.com/api",
		"headers": {"Content-Type": ["application/json"]},
		"body": {"ok": true},
		"body_hash": "",
		"body_encoding": "identity"
	}`

	var r RecordedReq
	if err := json.Unmarshal([]byte(input), &r); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	want := `{"ok":true}`
	if string(r.Body) != want {
		t.Errorf("Body = %q, want %q", string(r.Body), want)
	}
}

func TestRecordedReq_UnmarshalJSON_NativeJSONArray(t *testing.T) {
	input := `{
		"method": "POST",
		"url": "http://example.com/api",
		"headers": {"Content-Type": ["application/json"]},
		"body": [1, 2, 3],
		"body_hash": ""
	}`

	var r RecordedReq
	if err := json.Unmarshal([]byte(input), &r); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	want := `[1,2,3]`
	if string(r.Body) != want {
		t.Errorf("Body = %q, want %q", string(r.Body), want)
	}
}

// --- Round-trip tests ---

func TestRoundTrip_JSONBody(t *testing.T) {
	r := RecordedReq{
		Method:  "POST",
		URL:     "http://example.com/api",
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    []byte(`{"key":"value","nested":{"a":1}}`),
	}

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var r2 RecordedReq
	if err := json.Unmarshal(data, &r2); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if !bytes.Equal(r.Body, r2.Body) {
		t.Errorf("round-trip body mismatch:\n  original: %q\n  result:   %q", string(r.Body), string(r2.Body))
	}
}

func TestRoundTrip_TextBody(t *testing.T) {
	r := RecordedReq{
		Method:  "POST",
		URL:     "http://example.com/api",
		Headers: http.Header{"Content-Type": {"text/plain"}},
		Body:    []byte("hello world"),
	}

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var r2 RecordedReq
	if err := json.Unmarshal(data, &r2); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if !bytes.Equal(r.Body, r2.Body) {
		t.Errorf("round-trip body mismatch:\n  original: %q\n  result:   %q", string(r.Body), string(r2.Body))
	}
}

func TestRoundTrip_BinaryBody(t *testing.T) {
	r := RecordedReq{
		Method:  "GET",
		URL:     "http://example.com/image",
		Headers: http.Header{"Content-Type": {"image/png"}},
		Body:    []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A},
	}

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var r2 RecordedReq
	if err := json.Unmarshal(data, &r2); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if !bytes.Equal(r.Body, r2.Body) {
		t.Errorf("round-trip body mismatch:\n  original: %v\n  result:   %v", r.Body, r2.Body)
	}
}

func TestRoundTrip_NilBody(t *testing.T) {
	r := RecordedReq{
		Method:  "GET",
		URL:     "http://example.com/api",
		Headers: http.Header{},
		Body:    nil,
	}

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var r2 RecordedReq
	if err := json.Unmarshal(data, &r2); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if r2.Body != nil {
		t.Errorf("round-trip nil body: got %v, want nil", r2.Body)
	}
}

func TestRoundTrip_Resp_JSONBody(t *testing.T) {
	r := RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{"Content-Type": {"application/json"}},
		Body:       []byte(`{"users":[{"id":1,"name":"Alice"}]}`),
	}

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var r2 RecordedResp
	if err := json.Unmarshal(data, &r2); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if !bytes.Equal(r.Body, r2.Body) {
		t.Errorf("round-trip body mismatch:\n  original: %q\n  result:   %q", string(r.Body), string(r2.Body))
	}
}

func TestRecordedReq_UnmarshalJSON_FormUrlencoded(t *testing.T) {
	input := `{
		"method": "POST",
		"url": "http://example.com/login",
		"headers": {"Content-Type": ["application/x-www-form-urlencoded"]},
		"body": "username=alice&password=secret",
		"body_hash": ""
	}`

	var r RecordedReq
	if err := json.Unmarshal([]byte(input), &r); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	want := "username=alice&password=secret"
	if string(r.Body) != want {
		t.Errorf("Body = %q, want %q", string(r.Body), want)
	}
}

func TestRecordedReq_MarshalJSON_FormUrlencoded(t *testing.T) {
	r := RecordedReq{
		Method:  "POST",
		URL:     "http://example.com/login",
		Headers: http.Header{"Content-Type": {"application/x-www-form-urlencoded"}},
		Body:    []byte("username=alice&password=secret"),
	}

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	// Form data is classified as text, should be a JSON string.
	if !strings.Contains(string(data), `"body":"username=alice\u0026password=secret"`) &&
		!strings.Contains(string(data), `"body":"username=alice&password=secret"`) {
		t.Errorf("expected text string body for form data, got: %s", data)
	}
}

// --- Exemplar validation tests (ADR-43) ---

func TestValidateExemplar(t *testing.T) {
	tests := []struct {
		name    string
		tape    Tape
		wantErr string // empty means no error expected
	}{
		{
			name: "valid exemplar with url_pattern",
			tape: Tape{
				ID:       "t-1",
				Exemplar: true,
				Request: RecordedReq{
					Method:     "GET",
					URLPattern: "/users/:id",
				},
				Response: RecordedResp{StatusCode: 200},
			},
		},
		{
			name: "valid non-exemplar tape",
			tape: Tape{
				ID: "t-2",
				Request: RecordedReq{
					Method: "GET",
					URL:    "/users/1",
				},
				Response: RecordedResp{StatusCode: 200},
			},
		},
		{
			name: "exemplar missing url_pattern",
			tape: Tape{
				ID:       "t-3",
				Exemplar: true,
				Request: RecordedReq{
					Method: "GET",
				},
				Response: RecordedResp{StatusCode: 200},
			},
			wantErr: "url_pattern is required",
		},
		{
			name: "exemplar with both url and url_pattern",
			tape: Tape{
				ID:       "t-4",
				Exemplar: true,
				Request: RecordedReq{
					Method:     "GET",
					URL:        "/users/1",
					URLPattern: "/users/:id",
				},
				Response: RecordedResp{StatusCode: 200},
			},
			wantErr: "url and url_pattern are mutually exclusive",
		},
		{
			name: "url_pattern without exemplar flag",
			tape: Tape{
				ID: "t-5",
				Request: RecordedReq{
					Method:     "GET",
					URLPattern: "/users/:id",
				},
				Response: RecordedResp{StatusCode: 200},
			},
			wantErr: "url_pattern requires exemplar to be true",
		},
		{
			name: "SSE exemplar rejected",
			tape: Tape{
				ID:       "t-6",
				Exemplar: true,
				Request: RecordedReq{
					Method:     "GET",
					URLPattern: "/events/:id",
				},
				Response: RecordedResp{
					StatusCode: 200,
					SSEEvents:  []SSEEvent{{Data: "hello"}},
				},
			},
			wantErr: "SSE exemplars are not supported",
		},
		{
			name: "non-exemplar url and url_pattern both set",
			tape: Tape{
				ID: "t-7",
				Request: RecordedReq{
					Method:     "GET",
					URL:        "/users/1",
					URLPattern: "/users/:id",
				},
				Response: RecordedResp{StatusCode: 200},
			},
			wantErr: "url and url_pattern are mutually exclusive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateExemplar(tt.tape)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("ValidateExemplar() unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Errorf("ValidateExemplar() = nil, want error containing %q", tt.wantErr)
				return
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ValidateExemplar() error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidateTape_DelegatesToValidateExemplar(t *testing.T) {
	invalid := Tape{
		ID:       "t-bad",
		Exemplar: true,
		Request:  RecordedReq{Method: "GET"},
		Response: RecordedResp{StatusCode: 200},
	}
	err := ValidateTape(invalid)
	if err == nil {
		t.Error("ValidateTape() = nil, want error for exemplar missing url_pattern")
	}
}

func TestTape_MarshalJSON_Exemplar(t *testing.T) {
	tape := Tape{
		ID:       "exemplar-1",
		Route:    "api",
		Exemplar: true,
		Request: RecordedReq{
			Method:     "GET",
			URLPattern: "/users/:id",
			Headers:    http.Header{"Accept": {"application/json"}},
		},
		Response: RecordedResp{
			StatusCode: 200,
			Headers:    http.Header{"Content-Type": {"application/json"}},
			Body:       []byte(`{"id":"{{pathParam.id | int}}"}`),
		},
	}

	data, err := json.MarshalIndent(tape, "", "  ")
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	s := string(data)
	if !strings.Contains(s, `"exemplar": true`) {
		t.Errorf("marshal should contain exemplar: true, got:\n%s", s)
	}
	if !strings.Contains(s, `"url_pattern": "/users/:id"`) {
		t.Errorf("marshal should contain url_pattern, got:\n%s", s)
	}
}

func TestTape_UnmarshalJSON_Exemplar(t *testing.T) {
	input := `{
		"id": "exemplar-1",
		"route": "api",
		"recorded_at": "2026-01-01T00:00:00Z",
		"exemplar": true,
		"request": {
			"method": "GET",
			"url_pattern": "/users/:id",
			"headers": {},
			"body": null,
			"body_hash": ""
		},
		"response": {
			"status_code": 200,
			"headers": {"Content-Type": ["application/json"]},
			"body": {"id": "{{pathParam.id | int}}"}
		}
	}`

	var tape Tape
	if err := json.Unmarshal([]byte(input), &tape); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if !tape.Exemplar {
		t.Error("Exemplar should be true after unmarshal")
	}
	if tape.Request.URLPattern != "/users/:id" {
		t.Errorf("URLPattern = %q, want %q", tape.Request.URLPattern, "/users/:id")
	}
	if tape.Request.URL != "" {
		t.Errorf("URL = %q, want empty for exemplar tape", tape.Request.URL)
	}
}

func TestTape_UnmarshalJSON_NoExemplar(t *testing.T) {
	input := `{
		"id": "normal-1",
		"route": "api",
		"recorded_at": "2026-01-01T00:00:00Z",
		"request": {
			"method": "GET",
			"url": "http://example.com/users/1",
			"headers": {},
			"body": null,
			"body_hash": ""
		},
		"response": {
			"status_code": 200,
			"headers": {"Content-Type": ["application/json"]},
			"body": {"id": 1}
		}
	}`

	var tape Tape
	if err := json.Unmarshal([]byte(input), &tape); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if tape.Exemplar {
		t.Error("Exemplar should be false when field is absent")
	}
	if tape.Request.URLPattern != "" {
		t.Errorf("URLPattern = %q, want empty for normal tape", tape.Request.URLPattern)
	}
}

func TestTape_MarshalJSON_RoundTrip_Exemplar(t *testing.T) {
	original := Tape{
		ID:       "rt-1",
		Route:    "api",
		Exemplar: true,
		Request: RecordedReq{
			Method:     "GET",
			URLPattern: "/items/:category/:id",
			Headers:    http.Header{},
		},
		Response: RecordedResp{
			StatusCode: 200,
			Headers:    http.Header{"Content-Type": {"application/json"}},
			Body:       []byte(`{"category":"{{pathParam.category}}"}`),
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var roundTripped Tape
	if err := json.Unmarshal(data, &roundTripped); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if roundTripped.Exemplar != original.Exemplar {
		t.Errorf("Exemplar = %v, want %v", roundTripped.Exemplar, original.Exemplar)
	}
	if roundTripped.Request.URLPattern != original.Request.URLPattern {
		t.Errorf("URLPattern = %q, want %q", roundTripped.Request.URLPattern, original.Request.URLPattern)
	}
	if roundTripped.Request.URL != "" {
		t.Errorf("URL = %q, want empty after round-trip", roundTripped.Request.URL)
	}

	// Re-marshal and compare.
	data2, err := json.Marshal(roundTripped)
	if err != nil {
		t.Fatalf("re-marshal error: %v", err)
	}
	if string(data) != string(data2) {
		t.Errorf("round-trip mismatch:\n  first:  %s\n  second: %s", data, data2)
	}
}

// ---------------------------------------------------------------------------
// ElapsedMS serialization
// ---------------------------------------------------------------------------

func TestRecordedResp_MarshalJSON_ElapsedMSOmitempty(t *testing.T) {
	r := RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{"Content-Type": {"application/json"}},
		Body:       []byte(`{}`),
	}

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	if strings.Contains(string(data), "elapsed_ms") {
		t.Errorf("elapsed_ms should be omitted when zero, got: %s", data)
	}
}

func TestRecordedResp_MarshalJSON_ElapsedMSPresent(t *testing.T) {
	r := RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{"Content-Type": {"application/json"}},
		Body:       []byte(`{}`),
		ElapsedMS:  142,
	}

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	if !strings.Contains(string(data), `"elapsed_ms":142`) {
		t.Errorf("expected elapsed_ms:142 in output, got: %s", data)
	}
}

func TestRecordedResp_UnmarshalJSON_ElapsedMS(t *testing.T) {
	input := `{
		"status_code": 200,
		"headers": {"Content-Type": ["text/plain"]},
		"body": "hello",
		"elapsed_ms": 350
	}`

	var r RecordedResp
	if err := json.Unmarshal([]byte(input), &r); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if r.ElapsedMS != 350 {
		t.Errorf("ElapsedMS = %d, want 350", r.ElapsedMS)
	}
}

func TestRecordedResp_UnmarshalJSON_ElapsedMSAbsent(t *testing.T) {
	input := `{
		"status_code": 200,
		"headers": {"Content-Type": ["text/plain"]},
		"body": "hello"
	}`

	var r RecordedResp
	if err := json.Unmarshal([]byte(input), &r); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if r.ElapsedMS != 0 {
		t.Errorf("ElapsedMS = %d, want 0 for pre-feature fixture", r.ElapsedMS)
	}
}

func TestRecordedResp_JSON_RoundTrip_WithElapsedMS(t *testing.T) {
	original := RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{"Content-Type": {"application/json"}},
		Body:       []byte(`{"key":"value"}`),
		ElapsedMS:  500,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var roundTripped RecordedResp
	if err := json.Unmarshal(data, &roundTripped); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if roundTripped.ElapsedMS != original.ElapsedMS {
		t.Errorf("ElapsedMS = %d, want %d", roundTripped.ElapsedMS, original.ElapsedMS)
	}
}

// ---------------------------------------------------------------------------
// ADR-50: non-UTF-8 text body encoding tests
// ---------------------------------------------------------------------------

// TestRoundTrip_Latin1TextBody_SurvivesByteIdentically is the primary repro
// for #303: a Latin-1 text body must survive marshal → unmarshal byte-identically.
// encoding/json replaces invalid UTF-8 with U+FFFD when marshaling a string, so
// without the base64 fallback the body would be silently corrupted on persist.
func TestRoundTrip_Latin1TextBody_SurvivesByteIdentically(t *testing.T) {
	// "café au lait" in ISO-8859-1: 0xe9 is the Latin-1 code point for é.
	latin1Body := []byte("caf\xe9 au lait")
	headers := http.Header{"Content-Type": {"text/plain; charset=iso-8859-1"}}

	req := RecordedReq{
		Method:   "POST",
		URL:      "http://example.com/api",
		Headers:  headers,
		Body:     latin1Body,
		BodyHash: BodyHashFromBytes(latin1Body),
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	// Fixture must contain the body_encoding marker.
	if !strings.Contains(string(data), `"body_encoding":"base64"`) {
		t.Errorf("expected body_encoding:base64 in fixture, got: %s", data)
	}

	var req2 RecordedReq
	if err := json.Unmarshal(data, &req2); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if !bytes.Equal(req2.Body, latin1Body) {
		t.Errorf("body mismatch after round-trip:\n  want: %v\n  got:  %v", latin1Body, req2.Body)
	}
}

// TestRoundTrip_Latin1TextBody_Resp_SurvivesByteIdentically mirrors the repro
// on RecordedResp to ensure both marshal paths are covered.
func TestRoundTrip_Latin1TextBody_Resp_SurvivesByteIdentically(t *testing.T) {
	latin1Body := []byte("caf\xe9 au lait")
	headers := http.Header{"Content-Type": {"text/plain; charset=iso-8859-1"}}

	resp := RecordedResp{
		StatusCode: 200,
		Headers:    headers,
		Body:       latin1Body,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	if !strings.Contains(string(data), `"body_encoding":"base64"`) {
		t.Errorf("expected body_encoding:base64 in fixture, got: %s", data)
	}

	var resp2 RecordedResp
	if err := json.Unmarshal(data, &resp2); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if !bytes.Equal(resp2.Body, latin1Body) {
		t.Errorf("body mismatch after round-trip:\n  want: %v\n  got:  %v", latin1Body, resp2.Body)
	}
}

// TestBodyHash_MatchesStoredBody_ForNonUTF8Text asserts that after a round-trip
// the stored body_hash still matches the round-tripped body. This is the
// divergence guard for #303: before the fix, body_hash was computed over the
// original bytes but the stored body was U+FFFD-corrupted.
func TestBodyHash_MatchesStoredBody_ForNonUTF8Text(t *testing.T) {
	latin1Body := []byte("caf\xe9 au lait")
	headers := http.Header{"Content-Type": {"text/plain; charset=iso-8859-1"}}
	originalHash := BodyHashFromBytes(latin1Body)

	req := RecordedReq{
		Method:   "POST",
		URL:      "http://example.com",
		Headers:  headers,
		Body:     latin1Body,
		BodyHash: originalHash,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var req2 RecordedReq
	if err := json.Unmarshal(data, &req2); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	roundTripHash := BodyHashFromBytes(req2.Body)
	if roundTripHash != originalHash {
		t.Errorf("body_hash mismatch after round-trip: stored=%q computed-over-body=%q",
			originalHash, roundTripHash)
	}
}

// TestMarshalBody_TextCTShapes exercises marshalBody for the text Content-Type
// branch covering valid UTF-8 (no marker) and several non-UTF-8 encodings (marker set).
func TestMarshalBody_TextCTShapes(t *testing.T) {
	tests := []struct {
		name        string
		body        []byte
		ct          string
		wantEncoded bool   // true → base64 in JSON; false → plain string
		wantMarker  string // expected body_encoding value in emitted JSON ("" = absent)
	}{
		{
			name:        "valid UTF-8 text emits plain string without marker",
			body:        []byte("hello world"),
			ct:          "text/plain",
			wantEncoded: false,
			wantMarker:  "",
		},
		{
			name:        "Latin-1 byte emits base64 with base64 marker",
			body:        []byte("caf\xe9 au lait"),
			ct:          "text/plain; charset=iso-8859-1",
			wantEncoded: true,
			wantMarker:  "base64",
		},
		{
			name:        "lone 0xFF byte emits base64 with base64 marker",
			body:        []byte{0xFF},
			ct:          "text/plain",
			wantEncoded: true,
			wantMarker:  "base64",
		},
		{
			name:        "Shift-JIS sample emits base64 with base64 marker",
			body:        []byte{0x82, 0xb1, 0x82, 0xf1, 0x82, 0xc9, 0x82, 0xbf, 0x82, 0xcd}, // "こんにちは" in Shift-JIS
			ct:          "text/plain; charset=shift_jis",
			wantEncoded: true,
			wantMarker:  "base64",
		},
		{
			name:        "application/xml valid UTF-8 no marker",
			body:        []byte("<root/>"),
			ct:          "application/xml",
			wantEncoded: false,
			wantMarker:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := http.Header{"Content-Type": {tt.ct}}
			req := RecordedReq{Method: "POST", URL: "http://x", Headers: headers, Body: tt.body}
			data, err := json.Marshal(req)
			if err != nil {
				t.Fatalf("marshal error: %v", err)
			}
			s := string(data)

			hasMarker := strings.Contains(s, `"body_encoding":"base64"`)
			if tt.wantMarker == "base64" && !hasMarker {
				t.Errorf("expected body_encoding:base64 in output, got: %s", s)
			}
			if tt.wantMarker == "" && hasMarker {
				t.Errorf("unexpected body_encoding field in output, got: %s", s)
			}

			// Verify round-trip restores the original bytes.
			var req2 RecordedReq
			if err := json.Unmarshal(data, &req2); err != nil {
				t.Fatalf("unmarshal error: %v", err)
			}
			if !bytes.Equal(req2.Body, tt.body) {
				t.Errorf("body mismatch after round-trip:\n  want: %v\n  got:  %v", tt.body, req2.Body)
			}
		})
	}
}

// TestUnmarshalBody_Base64MarkerTakesPriorityOverContentType asserts that when
// body_encoding is "base64", the body is base64-decoded regardless of Content-Type.
func TestUnmarshalBody_Base64MarkerTakesPriorityOverContentType(t *testing.T) {
	// base64("café") with a text/plain Content-Type and the explicit marker.
	// Without the marker, a text CT would store this verbatim as "Y2Fmw6k=".
	latin1Body := []byte("caf\xe9 au lait")
	encoded := "Y2Fm6SBhdSBsYWl0" // base64.StdEncoding of latin1Body

	input := `{
		"method": "POST",
		"url": "http://example.com",
		"headers": {"Content-Type": ["text/plain; charset=iso-8859-1"]},
		"body": "` + encoded + `",
		"body_encoding": "base64",
		"body_hash": ""
	}`

	var req RecordedReq
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if !bytes.Equal(req.Body, latin1Body) {
		t.Errorf("Body = %v, want %v", req.Body, latin1Body)
	}
}

// TestUnmarshalBody_LegitTextThatLooksLikeBase64_ReadVerbatim guards the
// ambiguity that drove the marker decision: a legitimate UTF-8 text body that
// happens to be valid base64 must be read verbatim (not decoded) when no marker
// is present.
func TestUnmarshalBody_LegitTextThatLooksLikeBase64_ReadVerbatim(t *testing.T) {
	// "SGVsbG8=" is the base64 encoding of "Hello". As a text/plain body it
	// should be stored verbatim — the literal string "SGVsbG8=" — not decoded
	// to "Hello". This is the key regression guard for try-base64-first rejection.
	input := `{
		"method": "GET",
		"url": "http://example.com",
		"headers": {"Content-Type": ["text/plain"]},
		"body": "SGVsbG8=",
		"body_hash": ""
	}`

	var req RecordedReq
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	want := "SGVsbG8="
	if string(req.Body) != want {
		t.Errorf("Body = %q, want %q (must be verbatim, not base64-decoded)", string(req.Body), want)
	}
}

// TestUnmarshalBody_Base64MarkerWithBadBase64_Errors asserts that a body marked
// as base64 but containing invalid base64 data is a hard error (fail closed),
// not silent corruption.
func TestUnmarshalBody_Base64MarkerWithBadBase64_Errors(t *testing.T) {
	input := `{
		"method": "POST",
		"url": "http://example.com",
		"headers": {"Content-Type": ["text/plain"]},
		"body": "!!!not-valid-base64!!!",
		"body_encoding": "base64",
		"body_hash": ""
	}`

	var req RecordedReq
	err := json.Unmarshal([]byte(input), &req)
	if err == nil {
		t.Error("expected error for invalid base64 under body_encoding=base64, got nil")
	}
}

// TestUnmarshalBody_ExistingFixturesWithoutMarker_UnchangedBehavior is the
// backwards-compatibility regression: fixtures without body_encoding must
// unmarshal identically to before ADR-50.
func TestUnmarshalBody_ExistingFixturesWithoutMarker_UnchangedBehavior(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string // expected Body as string
	}{
		{
			name: "text/plain body without marker stays verbatim",
			input: `{
				"method": "GET",
				"url": "http://example.com",
				"headers": {"Content-Type": ["text/plain"]},
				"body": "hello world",
				"body_hash": ""
			}`,
			want: "hello world",
		},
		{
			name: "JSON body without marker stays native JSON",
			input: `{
				"method": "POST",
				"url": "http://example.com",
				"headers": {"Content-Type": ["application/json"]},
				"body": {"key": "value"},
				"body_hash": ""
			}`,
			want: `{"key":"value"}`,
		},
		{
			name: "legacy body_encoding identity is ignored (object body)",
			input: `{
				"method": "POST",
				"url": "http://example.com",
				"headers": {"Content-Type": ["application/json"]},
				"body": {"ok": true},
				"body_encoding": "identity",
				"body_hash": ""
			}`,
			want: `{"ok":true}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req RecordedReq
			if err := json.Unmarshal([]byte(tt.input), &req); err != nil {
				t.Fatalf("unmarshal error: %v", err)
			}
			if string(req.Body) != tt.want {
				t.Errorf("Body = %q, want %q", string(req.Body), tt.want)
			}
		})
	}
}

// TestMarshalBody_NoMarkerOnUTF8TextOrOtherCTs verifies that the body_encoding
// field is completely absent (omitempty) for all non-non-UTF-8-text cases,
// keeping existing fixture files byte-identical on disk.
func TestMarshalBody_NoMarkerOnUTF8TextOrOtherCTs(t *testing.T) {
	tests := []struct {
		name string
		req  RecordedReq
	}{
		{
			name: "UTF-8 text body",
			req: RecordedReq{
				Method:  "POST",
				URL:     "http://x",
				Headers: http.Header{"Content-Type": {"text/plain"}},
				Body:    []byte("hello"),
			},
		},
		{
			name: "JSON body",
			req: RecordedReq{
				Method:  "POST",
				URL:     "http://x",
				Headers: http.Header{"Content-Type": {"application/json"}},
				Body:    []byte(`{"a":1}`),
			},
		},
		{
			name: "binary body",
			req: RecordedReq{
				Method:  "GET",
				URL:     "http://x",
				Headers: http.Header{"Content-Type": {"image/png"}},
				Body:    []byte{0x89, 0x50, 0x4e, 0x47},
			},
		},
		{
			name: "nil body",
			req: RecordedReq{
				Method:  "GET",
				URL:     "http://x",
				Headers: http.Header{"Content-Type": {"application/json"}},
				Body:    nil,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.req)
			if err != nil {
				t.Fatalf("marshal error: %v", err)
			}
			if strings.Contains(string(data), "body_encoding") {
				t.Errorf("unexpected body_encoding field in output for %s: %s", tt.name, data)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// elapsedMS helper
// ---------------------------------------------------------------------------

func TestElapsedMS(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	nowFunc := func() time.Time {
		return start.Add(250 * time.Millisecond)
	}

	got := elapsedMS(start, nowFunc)
	if got != 250 {
		t.Errorf("elapsedMS = %d, want 250", got)
	}
}

func TestElapsedMS_Zero(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	nowFunc := func() time.Time { return start }

	got := elapsedMS(start, nowFunc)
	if got != 0 {
		t.Errorf("elapsedMS = %d, want 0", got)
	}
}
