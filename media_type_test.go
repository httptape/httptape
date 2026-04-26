package httptape

import (
	"testing"
)

func TestParseMediaType(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantType    string
		wantSubtype string
		wantSuffix  string
		wantQ       float64
		wantErr     bool
	}{
		{
			name:        "application/json",
			input:       "application/json",
			wantType:    "application",
			wantSubtype: "json",
			wantQ:       1.0,
		},
		{
			name:        "text/plain",
			input:       "text/plain",
			wantType:    "text",
			wantSubtype: "plain",
			wantQ:       1.0,
		},
		{
			name:        "image/png",
			input:       "image/png",
			wantType:    "image",
			wantSubtype: "png",
			wantQ:       1.0,
		},
		{
			name:        "with charset parameter",
			input:       "text/plain; charset=utf-8",
			wantType:    "text",
			wantSubtype: "plain",
			wantQ:       1.0,
		},
		{
			name:        "json with charset",
			input:       "application/json; charset=utf-8",
			wantType:    "application",
			wantSubtype: "json",
			wantQ:       1.0,
		},
		{
			name:        "q-value",
			input:       "text/html;q=0.9",
			wantType:    "text",
			wantSubtype: "html",
			wantQ:       0.9,
		},
		{
			name:        "q-value zero",
			input:       "application/xml;q=0",
			wantType:    "application",
			wantSubtype: "xml",
			wantQ:       0.0,
		},
		{
			name:        "vendor json suffix",
			input:       "application/vnd.api+json",
			wantType:    "application",
			wantSubtype: "vnd.api+json",
			wantSuffix:  "json",
			wantQ:       1.0,
		},
		{
			name:        "github vendor json",
			input:       "application/vnd.github.v3+json",
			wantType:    "application",
			wantSubtype: "vnd.github.v3+json",
			wantSuffix:  "json",
			wantQ:       1.0,
		},
		{
			name:        "xml suffix",
			input:       "application/atom+xml",
			wantType:    "application",
			wantSubtype: "atom+xml",
			wantSuffix:  "xml",
			wantQ:       1.0,
		},
		{
			name:        "full wildcard",
			input:       "*/*",
			wantType:    "*",
			wantSubtype: "*",
			wantQ:       1.0,
		},
		{
			name:        "subtype wildcard",
			input:       "application/*",
			wantType:    "application",
			wantSubtype: "*",
			wantQ:       1.0,
		},
		{
			name:        "text wildcard",
			input:       "text/*",
			wantType:    "text",
			wantSubtype: "*",
			wantQ:       1.0,
		},
		{
			name:        "with q-value and charset",
			input:       "application/json; charset=utf-8; q=0.9",
			wantType:    "application",
			wantSubtype: "json",
			wantQ:       0.9,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:    "whitespace only",
			input:   "   ",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mt, err := ParseMediaType(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseMediaType(%q) error = nil, want error", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseMediaType(%q) unexpected error: %v", tt.input, err)
			}
			if mt.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", mt.Type, tt.wantType)
			}
			if mt.Subtype != tt.wantSubtype {
				t.Errorf("Subtype = %q, want %q", mt.Subtype, tt.wantSubtype)
			}
			if tt.wantSuffix != "" && mt.Suffix != tt.wantSuffix {
				t.Errorf("Suffix = %q, want %q", mt.Suffix, tt.wantSuffix)
			}
			if mt.QValue != tt.wantQ {
				t.Errorf("QValue = %f, want %f", mt.QValue, tt.wantQ)
			}
		})
	}
}

func TestParseMediaType_ParamsExcludeQ(t *testing.T) {
	mt, err := ParseMediaType("text/plain; charset=utf-8; q=0.5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := mt.Params["q"]; ok {
		t.Error("Params should not contain 'q' -- it should be in QValue")
	}
	if mt.Params["charset"] != "utf-8" {
		t.Errorf("Params[charset] = %q, want %q", mt.Params["charset"], "utf-8")
	}
	if mt.QValue != 0.5 {
		t.Errorf("QValue = %f, want 0.5", mt.QValue)
	}
}

func TestParseAccept(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantLen    int
		wantFirst  string // type/subtype of first entry
		wantFirstQ float64
	}{
		{
			name:       "single type",
			input:      "application/json",
			wantLen:    1,
			wantFirst:  "application/json",
			wantFirstQ: 1.0,
		},
		{
			name:       "multiple types sorted by q",
			input:      "text/html;q=0.9, application/json, */*;q=0.1",
			wantLen:    3,
			wantFirst:  "application/json",
			wantFirstQ: 1.0,
		},
		{
			name:       "empty string",
			input:      "",
			wantLen:    1,
			wantFirst:  "*/*",
			wantFirstQ: 1.0,
		},
		{
			name:       "all malformed",
			input:      "///invalid, , ",
			wantLen:    1,
			wantFirst:  "*/*",
			wantFirstQ: 1.0,
		},
		{
			name:       "mixed valid and malformed",
			input:      "application/json, ///invalid, text/html;q=0.5",
			wantLen:    2,
			wantFirst:  "application/json",
			wantFirstQ: 1.0,
		},
		{
			name:       "same q-value sorted by specificity",
			input:      "*/*;q=0.5, application/*;q=0.5, application/json;q=0.5",
			wantLen:    3,
			wantFirst:  "application/json",
			wantFirstQ: 0.5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseAccept(tt.input)
			if len(result) != tt.wantLen {
				t.Fatalf("ParseAccept(%q) returned %d entries, want %d", tt.input, len(result), tt.wantLen)
			}
			first := result[0].Type + "/" + result[0].Subtype
			if first != tt.wantFirst {
				t.Errorf("first entry = %q, want %q", first, tt.wantFirst)
			}
			if result[0].QValue != tt.wantFirstQ {
				t.Errorf("first q-value = %f, want %f", result[0].QValue, tt.wantFirstQ)
			}
		})
	}
}

func TestParseAccept_Ordering(t *testing.T) {
	result := ParseAccept("text/html;q=0.9, application/json, */*;q=0.1")
	if len(result) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(result))
	}
	// Should be: application/json (q=1.0), text/html (q=0.9), */* (q=0.1)
	expected := []struct {
		typ, subtype string
		q            float64
	}{
		{"application", "json", 1.0},
		{"text", "html", 0.9},
		{"*", "*", 0.1},
	}
	for i, e := range expected {
		if result[i].Type != e.typ || result[i].Subtype != e.subtype {
			t.Errorf("entry[%d] = %s/%s, want %s/%s", i, result[i].Type, result[i].Subtype, e.typ, e.subtype)
		}
		if result[i].QValue != e.q {
			t.Errorf("entry[%d] q = %f, want %f", i, result[i].QValue, e.q)
		}
	}
}

func TestIsJSON(t *testing.T) {
	tests := []struct {
		name  string
		input MediaType
		want  bool
	}{
		{"application/json", MediaType{Type: "application", Subtype: "json"}, true},
		{"vendor +json", MediaType{Type: "application", Subtype: "vnd.api+json", Suffix: "json"}, true},
		{"ld+json", MediaType{Type: "application", Subtype: "ld+json", Suffix: "json"}, true},
		{"problem+json", MediaType{Type: "application", Subtype: "problem+json", Suffix: "json"}, true},
		{"text/plain", MediaType{Type: "text", Subtype: "plain"}, false},
		{"image/png", MediaType{Type: "image", Subtype: "png"}, false},
		{"application/xml", MediaType{Type: "application", Subtype: "xml"}, false},
		{"application/octet-stream", MediaType{Type: "application", Subtype: "octet-stream"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsJSON(tt.input)
			if got != tt.want {
				t.Errorf("IsJSON(%+v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsText(t *testing.T) {
	tests := []struct {
		name  string
		input MediaType
		want  bool
	}{
		{"text/plain", MediaType{Type: "text", Subtype: "plain"}, true},
		{"text/html", MediaType{Type: "text", Subtype: "html"}, true},
		{"text/csv", MediaType{Type: "text", Subtype: "csv"}, true},
		{"application/xml", MediaType{Type: "application", Subtype: "xml"}, true},
		{"application/javascript", MediaType{Type: "application", Subtype: "javascript"}, true},
		{"application/x-www-form-urlencoded", MediaType{Type: "application", Subtype: "x-www-form-urlencoded"}, true},
		{"atom+xml suffix", MediaType{Type: "application", Subtype: "atom+xml", Suffix: "xml"}, true},
		{"application/json", MediaType{Type: "application", Subtype: "json"}, false},
		{"vendor +json", MediaType{Type: "application", Subtype: "vnd.api+json", Suffix: "json"}, false},
		{"image/png", MediaType{Type: "image", Subtype: "png"}, false},
		{"application/octet-stream", MediaType{Type: "application", Subtype: "octet-stream"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsText(tt.input)
			if got != tt.want {
				t.Errorf("IsText(%+v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsBinary(t *testing.T) {
	tests := []struct {
		name  string
		input MediaType
		want  bool
	}{
		{"image/png", MediaType{Type: "image", Subtype: "png"}, true},
		{"application/octet-stream", MediaType{Type: "application", Subtype: "octet-stream"}, true},
		{"application/protobuf", MediaType{Type: "application", Subtype: "protobuf"}, true},
		{"audio/mpeg", MediaType{Type: "audio", Subtype: "mpeg"}, true},
		{"video/mp4", MediaType{Type: "video", Subtype: "mp4"}, true},
		{"empty type", MediaType{}, true},
		{"text/plain", MediaType{Type: "text", Subtype: "plain"}, false},
		{"application/json", MediaType{Type: "application", Subtype: "json"}, false},
		{"application/xml", MediaType{Type: "application", Subtype: "xml"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsBinary(tt.input)
			if got != tt.want {
				t.Errorf("IsBinary(%+v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestMatchesMediaRange(t *testing.T) {
	tests := []struct {
		name        string
		accept      MediaType
		contentType MediaType
		want        bool
	}{
		{
			name:        "exact match",
			accept:      MediaType{Type: "application", Subtype: "json"},
			contentType: MediaType{Type: "application", Subtype: "json"},
			want:        true,
		},
		{
			name:        "subtype wildcard",
			accept:      MediaType{Type: "application", Subtype: "*"},
			contentType: MediaType{Type: "application", Subtype: "json"},
			want:        true,
		},
		{
			name:        "full wildcard",
			accept:      MediaType{Type: "*", Subtype: "*"},
			contentType: MediaType{Type: "application", Subtype: "json"},
			want:        true,
		},
		{
			name:        "full wildcard matches binary",
			accept:      MediaType{Type: "*", Subtype: "*"},
			contentType: MediaType{Type: "image", Subtype: "png"},
			want:        true,
		},
		{
			name:        "no vendor fallback",
			accept:      MediaType{Type: "application", Subtype: "json"},
			contentType: MediaType{Type: "application", Subtype: "vnd.api+json", Suffix: "json"},
			want:        false,
		},
		{
			name:        "text mismatch",
			accept:      MediaType{Type: "text", Subtype: "plain"},
			contentType: MediaType{Type: "text", Subtype: "html"},
			want:        false,
		},
		{
			name:        "text wildcard matches html",
			accept:      MediaType{Type: "text", Subtype: "*"},
			contentType: MediaType{Type: "text", Subtype: "html"},
			want:        true,
		},
		{
			name:        "type mismatch",
			accept:      MediaType{Type: "text", Subtype: "plain"},
			contentType: MediaType{Type: "application", Subtype: "json"},
			want:        false,
		},
		{
			name:        "exact vendor match",
			accept:      MediaType{Type: "application", Subtype: "vnd.api+json"},
			contentType: MediaType{Type: "application", Subtype: "vnd.api+json"},
			want:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchesMediaRange(tt.accept, tt.contentType)
			if got != tt.want {
				t.Errorf("MatchesMediaRange(%v, %v) = %v, want %v", tt.accept, tt.contentType, got, tt.want)
			}
		})
	}
}

func TestMediaTypeError_FormatsInputAndReason(t *testing.T) {
	err := &mediaTypeError{input: "bad/type", reason: "missing subtype"}
	got := err.Error()
	want := `httptape: invalid media type "bad/type": missing subtype`
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestSpecificity(t *testing.T) {
	tests := []struct {
		name  string
		input MediaType
		want  int
	}{
		{"exact", MediaType{Type: "application", Subtype: "json"}, 3},
		{"subtype wildcard", MediaType{Type: "application", Subtype: "*"}, 2},
		{"full wildcard", MediaType{Type: "*", Subtype: "*"}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Specificity(tt.input)
			if got != tt.want {
				t.Errorf("Specificity(%+v) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}
