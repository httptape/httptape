package httptape

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestLoadConfig_Valid(t *testing.T) {
	input := `{
		"version": "1",
		"rules": [
			{"action": "redact_headers", "headers": ["Authorization", "Cookie"]},
			{"action": "redact_headers"},
			{"action": "redact_body", "paths": ["$.password", "$.user.ssn"]},
			{"action": "fake", "seed": "my-seed", "paths": ["$.email", "$.user_id"]}
		]
	}`

	cfg, err := LoadConfig(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Version != "1" {
		t.Errorf("version = %q, want %q", cfg.Version, "1")
	}
	if len(cfg.Rules) != 4 {
		t.Fatalf("len(rules) = %d, want 4", len(cfg.Rules))
	}

	// Verify rule actions.
	wantActions := []string{"redact_headers", "redact_headers", "redact_body", "fake"}
	for i, want := range wantActions {
		if cfg.Rules[i].Action != want {
			t.Errorf("rules[%d].action = %q, want %q", i, cfg.Rules[i].Action, want)
		}
	}

	// Verify redact_headers with explicit headers.
	if len(cfg.Rules[0].Headers) != 2 {
		t.Errorf("rules[0].headers len = %d, want 2", len(cfg.Rules[0].Headers))
	}

	// Verify redact_headers with no headers (defaults).
	if len(cfg.Rules[1].Headers) != 0 {
		t.Errorf("rules[1].headers len = %d, want 0", len(cfg.Rules[1].Headers))
	}

	// Verify fake seed.
	if cfg.Rules[3].Seed != "my-seed" {
		t.Errorf("rules[3].seed = %q, want %q", cfg.Rules[3].Seed, "my-seed")
	}
}

func TestLoadConfig_ValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{
			name:    "wrong version",
			input:   `{"version": "2", "rules": [{"action": "redact_headers"}]}`,
			wantErr: `unsupported version "2"`,
		},
		{
			name:    "missing version",
			input:   `{"version": "", "rules": [{"action": "redact_headers"}]}`,
			wantErr: `unsupported version ""`,
		},
		{
			name:    "empty rules",
			input:   `{"version": "1", "rules": []}`,
			wantErr: "rules must be a non-empty array",
		},
		{
			name:    "unknown action",
			input:   `{"version": "1", "rules": [{"action": "redact"}]}`,
			wantErr: `unknown action "redact"`,
		},
		{
			name:    "redact_body missing paths",
			input:   `{"version": "1", "rules": [{"action": "redact_body"}]}`,
			wantErr: `"redact_body" requires non-empty "paths"`,
		},
		{
			name:    "redact_body invalid path",
			input:   `{"version": "1", "rules": [{"action": "redact_body", "paths": ["invalid"]}]}`,
			wantErr: `invalid path syntax: "invalid"`,
		},
		{
			name:    "fake missing seed",
			input:   `{"version": "1", "rules": [{"action": "fake", "paths": ["$.email"]}]}`,
			wantErr: `"fake" requires non-empty "seed"`,
		},
		{
			name:    "fake missing paths",
			input:   `{"version": "1", "rules": [{"action": "fake", "seed": "s"}]}`,
			wantErr: `"fake" requires non-empty "paths"`,
		},
		{
			name:    "redact_headers with irrelevant paths",
			input:   `{"version": "1", "rules": [{"action": "redact_headers", "paths": ["$.x"]}]}`,
			wantErr: `does not use "paths"`,
		},
		{
			name:    "redact_body with irrelevant headers",
			input:   `{"version": "1", "rules": [{"action": "redact_body", "paths": ["$.x"], "headers": ["Auth"]}]}`,
			wantErr: `does not use "headers"`,
		},
		{
			name:    "fake with irrelevant headers",
			input:   `{"version": "1", "rules": [{"action": "fake", "seed": "s", "paths": ["$.x"], "headers": ["Auth"]}]}`,
			wantErr: `does not use "headers"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadConfig(strings.NewReader(tt.input))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestLoadConfig_MalformedJSON(t *testing.T) {
	_, err := LoadConfig(strings.NewReader(`{invalid`))
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if !strings.Contains(err.Error(), "invalid JSON") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "invalid JSON")
	}
}

func TestLoadConfig_UnknownFields(t *testing.T) {
	input := `{"version": "1", "rules": [{"action": "redact_headers"}], "extra": true}`
	_, err := LoadConfig(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
	if !strings.Contains(err.Error(), "invalid JSON") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "invalid JSON")
	}
}

func TestLoadConfigFile_NotFound(t *testing.T) {
	_, err := LoadConfigFile("/nonexistent/path/config.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "open file") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "open file")
	}
}

func TestLoadConfigFile_Valid(t *testing.T) {
	// Write a temp file.
	dir := t.TempDir()
	path := dir + "/config.json"
	content := `{"version": "1", "rules": [{"action": "redact_headers"}]}`
	if err := writeTestFile(path, content); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	cfg, err := LoadConfigFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Rules) != 1 {
		t.Errorf("len(rules) = %d, want 1", len(cfg.Rules))
	}
}

func TestConfig_BuildPipeline(t *testing.T) {
	cfg := &Config{
		Version: "1",
		Rules: []Rule{
			{Action: ActionRedactHeaders, Headers: []string{"Authorization"}},
			{Action: ActionRedactBody, Paths: []string{"$.password"}},
			{Action: ActionFake, Seed: "test-seed", Paths: []string{"$.email"}},
		},
	}

	pipeline := cfg.BuildPipeline()
	if pipeline == nil {
		t.Fatal("BuildPipeline returned nil")
	}

	// Verify the pipeline has the correct number of functions.
	if len(pipeline.funcs) != 3 {
		t.Errorf("pipeline.funcs len = %d, want 3", len(pipeline.funcs))
	}
}

func TestConfig_BuildPipeline_RedactHeadersDefault(t *testing.T) {
	// When no headers specified, should use DefaultSensitiveHeaders.
	cfg := &Config{
		Version: "1",
		Rules:   []Rule{{Action: ActionRedactHeaders}},
	}

	pipeline := cfg.BuildPipeline()

	tape := Tape{
		Request: RecordedReq{
			Headers: http.Header{
				"Authorization": []string{"Bearer token"},
				"Content-Type":  []string{"application/json"},
			},
		},
		Response: RecordedResp{
			Headers: http.Header{
				"Set-Cookie": []string{"session=abc"},
			},
		},
	}

	result := pipeline.Sanitize(tape)

	if got := result.Request.Headers.Get("Authorization"); got != Redacted {
		t.Errorf("Authorization = %q, want %q", got, Redacted)
	}
	if got := result.Request.Headers.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}
	if got := result.Response.Headers.Get("Set-Cookie"); got != Redacted {
		t.Errorf("Set-Cookie = %q, want %q", got, Redacted)
	}
}

func TestConfig_BuildPipeline_RoundTrip(t *testing.T) {
	// Build equivalent pipelines from Go API and from config, verify same output.
	goPipeline := NewPipeline(
		RedactHeaders("Authorization"),
		RedactBodyPaths("$.password"),
		FakeFields("seed123", "$.email"),
	)

	cfg := &Config{
		Version: "1",
		Rules: []Rule{
			{Action: ActionRedactHeaders, Headers: []string{"Authorization"}},
			{Action: ActionRedactBody, Paths: []string{"$.password"}},
			{Action: ActionFake, Seed: "seed123", Paths: []string{"$.email"}},
		},
	}
	configPipeline := cfg.BuildPipeline()

	tape := Tape{
		Request: RecordedReq{
			Headers: http.Header{
				"Authorization": []string{"Bearer secret"},
				"Content-Type":  []string{"application/json"},
			},
			Body: []byte(`{"password":"s3cret","email":"alice@corp.com","name":"Alice"}`),
		},
		Response: RecordedResp{
			StatusCode: 200,
			Headers: http.Header{
				"Content-Type": []string{"application/json"},
			},
			Body: []byte(`{"password":"s3cret","email":"alice@corp.com","status":"ok"}`),
		},
	}

	goResult := goPipeline.Sanitize(tape)
	cfgResult := configPipeline.Sanitize(tape)

	// Compare headers.
	if goResult.Request.Headers.Get("Authorization") != cfgResult.Request.Headers.Get("Authorization") {
		t.Error("Authorization header mismatch between Go API and config pipeline")
	}
	if goResult.Request.Headers.Get("Content-Type") != cfgResult.Request.Headers.Get("Content-Type") {
		t.Error("Content-Type header mismatch")
	}

	// Compare bodies.
	if string(goResult.Request.Body) != string(cfgResult.Request.Body) {
		t.Errorf("request body mismatch:\n  go:  %s\n  cfg: %s",
			goResult.Request.Body, cfgResult.Request.Body)
	}
	if string(goResult.Response.Body) != string(cfgResult.Response.Body) {
		t.Errorf("response body mismatch:\n  go:  %s\n  cfg: %s",
			goResult.Response.Body, cfgResult.Response.Body)
	}
}

func TestConfig_Validate_MultipleErrors(t *testing.T) {
	cfg := &Config{
		Version: "2",
		Rules: []Rule{
			{Action: "unknown"},
			{Action: "redact_body"},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	errMsg := err.Error()
	// Should contain both version error and rule errors.
	if !strings.Contains(errMsg, `unsupported version "2"`) {
		t.Errorf("missing version error in: %s", errMsg)
	}
	if !strings.Contains(errMsg, `unknown action "unknown"`) {
		t.Errorf("missing unknown action error in: %s", errMsg)
	}
	if !strings.Contains(errMsg, `requires non-empty "paths"`) {
		t.Errorf("missing paths error in: %s", errMsg)
	}
}

func TestConfig_Validate_PathSyntax(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"valid simple", "$.field", false},
		{"valid nested", "$.a.b.c", false},
		{"valid wildcard", "$.items[*].name", false},
		{"missing dollar prefix", "field", true},
		{"empty after dollar", "$.", true},
		{"array index", "$.items[0].name", true},
		{"double dot", "$.a..b", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Version: "1",
				Rules:   []Rule{{Action: ActionRedactBody, Paths: []string{tt.path}}},
			}
			err := cfg.Validate()
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestConfig_Programmatic(t *testing.T) {
	// Verify that configs can be built programmatically and validated.
	cfg := Config{
		Version: "1",
		Rules: []Rule{
			{Action: ActionRedactHeaders},
			{Action: ActionRedactBody, Paths: []string{"$.secret"}},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}

	pipeline := cfg.BuildPipeline()
	if pipeline == nil {
		t.Fatal("BuildPipeline returned nil")
	}
	if len(pipeline.funcs) != 2 {
		t.Errorf("pipeline.funcs len = %d, want 2", len(pipeline.funcs))
	}
}

func TestLoadConfig_FieldsMode(t *testing.T) {
	input := `{
		"version": "1",
		"rules": [
			{
				"action": "fake",
				"seed": "my-seed",
				"fields": {
					"$.email": "email",
					"$.phone": "phone",
					"$.card": "credit_card",
					"$.addr": "address",
					"$.token": "hmac",
					"$.secret": "redacted",
					"$.cvv": {"type": "numeric", "length": 3},
					"$.ssn": {"type": "pattern", "pattern": "###-##-####"},
					"$.dob": {"type": "date", "format": "2006-01-02"},
					"$.ref": {"type": "prefix", "prefix": "ref_"},
					"$.status": {"type": "fixed", "value": "active"}
				}
			}
		]
	}`

	cfg, err := LoadConfig(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.Rules) != 1 {
		t.Fatalf("len(rules) = %d, want 1", len(cfg.Rules))
	}
	if cfg.Rules[0].Action != ActionFake {
		t.Errorf("action = %q, want %q", cfg.Rules[0].Action, ActionFake)
	}
	if len(cfg.Rules[0].Fields) != 11 {
		t.Errorf("len(fields) = %d, want 11", len(cfg.Rules[0].Fields))
	}
}

func TestLoadConfig_FieldsValidation(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{
			name:    "paths and fields mutually exclusive",
			input:   `{"version":"1","rules":[{"action":"fake","seed":"s","paths":["$.x"],"fields":{"$.y":"email"}}]}`,
			wantErr: "cannot use both",
		},
		{
			name:    "neither paths nor fields",
			input:   `{"version":"1","rules":[{"action":"fake","seed":"s"}]}`,
			wantErr: "requires non-empty",
		},
		{
			name:    "invalid path in fields key",
			input:   `{"version":"1","rules":[{"action":"fake","seed":"s","fields":{"badpath":"email"}}]}`,
			wantErr: "invalid path syntax",
		},
		{
			name:    "unknown faker shorthand",
			input:   `{"version":"1","rules":[{"action":"fake","seed":"s","fields":{"$.x":"unknown_faker"}}]}`,
			wantErr: "unknown faker shorthand",
		},
		{
			name:    "numeric missing length",
			input:   `{"version":"1","rules":[{"action":"fake","seed":"s","fields":{"$.x":{"type":"numeric"}}}]}`,
			wantErr: "requires \"length\" > 0",
		},
		{
			name:    "pattern missing pattern",
			input:   `{"version":"1","rules":[{"action":"fake","seed":"s","fields":{"$.x":{"type":"pattern"}}}]}`,
			wantErr: "requires non-empty \"pattern\"",
		},
		{
			name:    "prefix missing prefix",
			input:   `{"version":"1","rules":[{"action":"fake","seed":"s","fields":{"$.x":{"type":"prefix"}}}]}`,
			wantErr: "requires non-empty \"prefix\"",
		},
		{
			name:    "fixed missing value",
			input:   `{"version":"1","rules":[{"action":"fake","seed":"s","fields":{"$.x":{"type":"fixed"}}}]}`,
			wantErr: "requires a \"value\"",
		},
		{
			name:    "unknown faker type in object",
			input:   `{"version":"1","rules":[{"action":"fake","seed":"s","fields":{"$.x":{"type":"bogus"}}}]}`,
			wantErr: "unknown faker type",
		},
		{
			name:    "invalid spec type",
			input:   `{"version":"1","rules":[{"action":"fake","seed":"s","fields":{"$.x":42}}]}`,
			wantErr: "must be a string or object",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadConfig(strings.NewReader(tt.input))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestConfig_BuildPipeline_Fields(t *testing.T) {
	cfg := &Config{
		Version: "1",
		Rules: []Rule{
			{
				Action: ActionFake,
				Seed:   "test-seed",
				Fields: map[string]any{
					"$.email": "email",
					"$.phone": "phone",
				},
			},
		},
	}

	pipeline := cfg.BuildPipeline()
	if pipeline == nil {
		t.Fatal("BuildPipeline returned nil")
	}
	if len(pipeline.funcs) != 1 {
		t.Errorf("pipeline.funcs len = %d, want 1", len(pipeline.funcs))
	}

	// Apply to a tape and verify transformation.
	tape := Tape{
		Request: RecordedReq{
			Body:    []byte(`{"email":"alice@corp.com","phone":"555-1234","name":"Alice"}`),
			Headers: make(map[string][]string),
		},
		Response: RecordedResp{
			Body:    []byte(`{"email":"bob@corp.com","phone":"555-5678"}`),
			Headers: make(map[string][]string),
		},
	}

	result := pipeline.Sanitize(tape)

	// Check request body.
	var reqData map[string]any
	if err := json.Unmarshal(result.Request.Body, &reqData); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	email := reqData["email"].(string)
	if !strings.HasSuffix(email, "@example.com") {
		t.Errorf("email = %q, want *@example.com", email)
	}
	if reqData["name"] != "Alice" {
		t.Errorf("name = %v, want \"Alice\"", reqData["name"])
	}
}

func TestParseFakerSpec_ObjectShorthand(t *testing.T) {
	// Object form with a shorthand type name (no extra params).
	spec := map[string]any{"type": "email"}
	f, err := parseFakerSpec(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := f.(EmailFaker); !ok {
		t.Errorf("expected EmailFaker, got %T", f)
	}
}

func TestParseFakerSpec_AllShorthands(t *testing.T) {
	shorthands := []string{"redacted", "hmac", "email", "phone", "credit_card", "address"}
	for _, s := range shorthands {
		t.Run(s, func(t *testing.T) {
			f, err := parseFakerSpec(s)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if f == nil {
				t.Fatal("got nil faker")
			}
		})
	}
}

func TestParseFakerSpec_DateDefaultFormat(t *testing.T) {
	spec := map[string]any{"type": "date"}
	f, err := parseFakerSpec(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	df, ok := f.(DateFaker)
	if !ok {
		t.Fatalf("expected DateFaker, got %T", f)
	}
	// Format can be empty; DateFaker defaults to "2006-01-02" internally.
	if df.Format != "" {
		t.Errorf("format = %q, want empty (default)", df.Format)
	}
}

// ---------- Matcher config tests ----------

func TestLoadConfig_MatcherValid(t *testing.T) {
	input := `{
		"version": "1",
		"matcher": {
			"criteria": [
				{"type": "method"},
				{"type": "path"},
				{"type": "body_fuzzy", "paths": ["$.messages[*].role"]}
			]
		},
		"rules": [
			{"action": "redact_headers"}
		]
	}`

	cfg, err := LoadConfig(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Matcher == nil {
		t.Fatal("Matcher is nil, want non-nil")
	}
	if len(cfg.Matcher.Criteria) != 3 {
		t.Fatalf("len(criteria) = %d, want 3", len(cfg.Matcher.Criteria))
	}
	if cfg.Matcher.Criteria[0].Type != "method" {
		t.Errorf("criteria[0].type = %q, want %q", cfg.Matcher.Criteria[0].Type, "method")
	}
	if cfg.Matcher.Criteria[1].Type != "path" {
		t.Errorf("criteria[1].type = %q, want %q", cfg.Matcher.Criteria[1].Type, "path")
	}
	if cfg.Matcher.Criteria[2].Type != "body_fuzzy" {
		t.Errorf("criteria[2].type = %q, want %q", cfg.Matcher.Criteria[2].Type, "body_fuzzy")
	}
	if len(cfg.Matcher.Criteria[2].Paths) != 1 || cfg.Matcher.Criteria[2].Paths[0] != "$.messages[*].role" {
		t.Errorf("criteria[2].paths = %v, want [$.messages[*].role]", cfg.Matcher.Criteria[2].Paths)
	}
}

func TestLoadConfig_MatcherValidation(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{
			name:    "unknown criterion type",
			input:   `{"version":"1","matcher":{"criteria":[{"type":"bogus"}]},"rules":[{"action":"redact_headers"}]}`,
			wantErr: `unknown type "bogus"`,
		},
		{
			name:    "body_fuzzy missing paths",
			input:   `{"version":"1","matcher":{"criteria":[{"type":"body_fuzzy"}]},"rules":[{"action":"redact_headers"}]}`,
			wantErr: `"body_fuzzy" requires non-empty "paths"`,
		},
		{
			name:    "method with paths",
			input:   `{"version":"1","matcher":{"criteria":[{"type":"method","paths":["$.x"]}]},"rules":[{"action":"redact_headers"}]}`,
			wantErr: `"method" does not use "paths"`,
		},
		{
			name:    "path with paths",
			input:   `{"version":"1","matcher":{"criteria":[{"type":"path","paths":["$.x"]}]},"rules":[{"action":"redact_headers"}]}`,
			wantErr: `"path" does not use "paths"`,
		},
		{
			name:    "content_negotiation with paths",
			input:   `{"version":"1","matcher":{"criteria":[{"type":"content_negotiation","paths":["$.x"]}]},"rules":[{"action":"redact_headers"}]}`,
			wantErr: `"content_negotiation" does not use "paths"`,
		},
		{
			name:    "body_fuzzy invalid path syntax",
			input:   `{"version":"1","matcher":{"criteria":[{"type":"body_fuzzy","paths":["invalid"]}]},"rules":[{"action":"redact_headers"}]}`,
			wantErr: `invalid path syntax: "invalid"`,
		},
		{
			name:    "empty criteria array",
			input:   `{"version":"1","matcher":{"criteria":[]},"rules":[{"action":"redact_headers"}]}`,
			wantErr: "matcher.criteria must be a non-empty array",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadConfig(strings.NewReader(tt.input))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestLoadConfig_EmptyRulesWithMatcher(t *testing.T) {
	input := `{
		"version": "1",
		"matcher": {
			"criteria": [
				{"type": "method"},
				{"type": "path"}
			]
		},
		"rules": []
	}`

	cfg, err := LoadConfig(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Matcher == nil {
		t.Fatal("Matcher is nil")
	}
	if len(cfg.Rules) != 0 {
		t.Errorf("len(rules) = %d, want 0", len(cfg.Rules))
	}
}

func TestLoadConfig_EmptyRulesNoMatcher(t *testing.T) {
	input := `{"version": "1", "rules": []}`
	_, err := LoadConfig(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected error for empty rules without matcher, got nil")
	}
	if !strings.Contains(err.Error(), "rules must be a non-empty array") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "rules must be a non-empty array")
	}
}

func TestConfig_BuildMatcher_NoMatcher(t *testing.T) {
	cfg := &Config{
		Version: "1",
		Rules:   []Rule{{Action: ActionRedactHeaders}},
	}

	matcher, err := cfg.BuildMatcher()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should return DefaultMatcher equivalent (CompositeMatcher with method + path).
	cm, ok := matcher.(*CompositeMatcher)
	if !ok {
		t.Fatalf("expected *CompositeMatcher, got %T", matcher)
	}
	if len(cm.criteria) != 2 {
		t.Fatalf("criteria count = %d, want 2", len(cm.criteria))
	}
	if cm.criteria[0].Name() != "method" {
		t.Errorf("criteria[0].Name() = %q, want %q", cm.criteria[0].Name(), "method")
	}
	if cm.criteria[1].Name() != "path" {
		t.Errorf("criteria[1].Name() = %q, want %q", cm.criteria[1].Name(), "path")
	}
}

func TestConfig_BuildMatcher_Composed(t *testing.T) {
	cfg := &Config{
		Version: "1",
		Matcher: &MatcherConfig{
			Criteria: []CriterionConfig{
				{Type: "method"},
				{Type: "path"},
				{Type: "body_fuzzy", Paths: []string{"$.messages[*].role", "$.model"}},
			},
		},
		Rules: []Rule{},
	}

	matcher, err := cfg.BuildMatcher()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cm, ok := matcher.(*CompositeMatcher)
	if !ok {
		t.Fatalf("expected *CompositeMatcher, got %T", matcher)
	}
	if len(cm.criteria) != 3 {
		t.Fatalf("criteria count = %d, want 3", len(cm.criteria))
	}

	wantNames := []string{"method", "path", "body_fuzzy"}
	for i, want := range wantNames {
		if cm.criteria[i].Name() != want {
			t.Errorf("criteria[%d].Name() = %q, want %q", i, cm.criteria[i].Name(), want)
		}
	}

	// Verify body_fuzzy criterion has the correct paths.
	bfc, ok := cm.criteria[2].(*BodyFuzzyCriterion)
	if !ok {
		t.Fatalf("expected *BodyFuzzyCriterion, got %T", cm.criteria[2])
	}
	if len(bfc.Paths) != 2 {
		t.Fatalf("body_fuzzy paths count = %d, want 2", len(bfc.Paths))
	}
	if bfc.Paths[0] != "$.messages[*].role" {
		t.Errorf("body_fuzzy paths[0] = %q, want %q", bfc.Paths[0], "$.messages[*].role")
	}
	if bfc.Paths[1] != "$.model" {
		t.Errorf("body_fuzzy paths[1] = %q, want %q", bfc.Paths[1], "$.model")
	}
}

func TestConfig_BuildMatcher_UnknownType(t *testing.T) {
	cfg := &Config{
		Version: "1",
		Matcher: &MatcherConfig{
			Criteria: []CriterionConfig{
				{Type: "unknown_criterion"},
			},
		},
		Rules: []Rule{},
	}

	_, err := cfg.BuildMatcher()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `unknown type "unknown_criterion"`) {
		t.Errorf("error = %q, want to contain %q", err.Error(), `unknown type "unknown_criterion"`)
	}
}

func TestConfig_BuildMatcher_EmptyCriteria(t *testing.T) {
	cfg := &Config{
		Version: "1",
		Matcher: &MatcherConfig{
			Criteria: []CriterionConfig{},
		},
		Rules: []Rule{{Action: ActionRedactHeaders}},
	}

	// Empty criteria returns DefaultMatcher.
	matcher, err := cfg.BuildMatcher()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cm, ok := matcher.(*CompositeMatcher)
	if !ok {
		t.Fatalf("expected *CompositeMatcher, got %T", matcher)
	}
	if len(cm.criteria) != 2 {
		t.Fatalf("criteria count = %d, want 2 (default)", len(cm.criteria))
	}
}

func TestLoadConfig_BackwardCompat_SanitizationOnly(t *testing.T) {
	// Existing sanitization-only configs (no matcher) continue to work.
	input := `{
		"version": "1",
		"rules": [
			{"action": "redact_headers", "headers": ["Authorization"]},
			{"action": "redact_body", "paths": ["$.password"]},
			{"action": "fake", "seed": "seed", "paths": ["$.email"]}
		]
	}`

	cfg, err := LoadConfig(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Matcher != nil {
		t.Error("Matcher should be nil for sanitization-only config")
	}

	// BuildMatcher returns default.
	matcher, err := cfg.BuildMatcher()
	if err != nil {
		t.Fatalf("BuildMatcher error: %v", err)
	}
	cm, ok := matcher.(*CompositeMatcher)
	if !ok {
		t.Fatalf("expected *CompositeMatcher, got %T", matcher)
	}
	if len(cm.criteria) != 2 {
		t.Errorf("criteria count = %d, want 2 (default)", len(cm.criteria))
	}

	// BuildPipeline still works.
	pipeline := cfg.BuildPipeline()
	if pipeline == nil {
		t.Fatal("BuildPipeline returned nil")
	}
	if len(pipeline.funcs) != 3 {
		t.Errorf("pipeline.funcs len = %d, want 3", len(pipeline.funcs))
	}
}

func TestLoadConfig_MatcherWithRules(t *testing.T) {
	// Config with both matcher and sanitization rules works correctly.
	input := `{
		"version": "1",
		"matcher": {
			"criteria": [
				{"type": "method"},
				{"type": "path"},
				{"type": "body_fuzzy", "paths": ["$.action"]}
			]
		},
		"rules": [
			{"action": "redact_headers"}
		]
	}`

	cfg, err := LoadConfig(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Matcher == nil {
		t.Fatal("Matcher is nil")
	}
	if len(cfg.Rules) != 1 {
		t.Errorf("len(rules) = %d, want 1", len(cfg.Rules))
	}

	// Both BuildMatcher and BuildPipeline work.
	matcher, err := cfg.BuildMatcher()
	if err != nil {
		t.Fatalf("BuildMatcher error: %v", err)
	}
	cm, ok := matcher.(*CompositeMatcher)
	if !ok {
		t.Fatalf("expected *CompositeMatcher, got %T", matcher)
	}
	if len(cm.criteria) != 3 {
		t.Errorf("criteria count = %d, want 3", len(cm.criteria))
	}

	pipeline := cfg.BuildPipeline()
	if len(pipeline.funcs) != 1 {
		t.Errorf("pipeline.funcs len = %d, want 1", len(pipeline.funcs))
	}
}

func TestLoadConfig_ContentNegotiationCriterion(t *testing.T) {
	input := `{
		"version": "1",
		"matcher": {
			"criteria": [
				{"type": "method"},
				{"type": "path"},
				{"type": "content_negotiation"}
			]
		},
		"rules": []
	}`

	cfg, err := LoadConfig(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Matcher == nil {
		t.Fatal("Matcher is nil")
	}
	if len(cfg.Matcher.Criteria) != 3 {
		t.Fatalf("len(criteria) = %d, want 3", len(cfg.Matcher.Criteria))
	}
	if cfg.Matcher.Criteria[2].Type != "content_negotiation" {
		t.Errorf("criteria[2].type = %q, want %q", cfg.Matcher.Criteria[2].Type, "content_negotiation")
	}

	matcher, err := cfg.BuildMatcher()
	if err != nil {
		t.Fatalf("BuildMatcher error: %v", err)
	}

	cm, ok := matcher.(*CompositeMatcher)
	if !ok {
		t.Fatalf("expected *CompositeMatcher, got %T", matcher)
	}
	if len(cm.criteria) != 3 {
		t.Fatalf("criteria count = %d, want 3", len(cm.criteria))
	}

	wantNames := []string{"method", "path", "content_negotiation"}
	for i, want := range wantNames {
		if cm.criteria[i].Name() != want {
			t.Errorf("criteria[%d].Name() = %q, want %q", i, cm.criteria[i].Name(), want)
		}
	}
}

func TestLoadConfig_PathPatternCriterion(t *testing.T) {
	input := `{
		"version": "1",
		"matcher": {
			"criteria": [
				{"type": "method"},
				{"type": "path_pattern", "pattern": "/users/:id"}
			]
		},
		"rules": []
	}`

	cfg, err := LoadConfig(strings.NewReader(input))
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if len(cfg.Matcher.Criteria) != 2 {
		t.Fatalf("criteria count = %d, want 2", len(cfg.Matcher.Criteria))
	}
	if cfg.Matcher.Criteria[1].Type != "path_pattern" {
		t.Errorf("type = %q, want %q", cfg.Matcher.Criteria[1].Type, "path_pattern")
	}
	if cfg.Matcher.Criteria[1].Pattern != "/users/:id" {
		t.Errorf("pattern = %q, want %q", cfg.Matcher.Criteria[1].Pattern, "/users/:id")
	}
}

func TestConfig_BuildMatcher_PathPattern(t *testing.T) {
	cfg := &Config{
		Version: "1",
		Matcher: &MatcherConfig{
			Criteria: []CriterionConfig{
				{Type: "method"},
				{Type: "path_pattern", Pattern: "/users/:id"},
			},
		},
	}

	matcher, err := cfg.BuildMatcher()
	if err != nil {
		t.Fatalf("BuildMatcher failed: %v", err)
	}

	cm, ok := matcher.(*CompositeMatcher)
	if !ok {
		t.Fatalf("expected *CompositeMatcher, got %T", matcher)
	}
	if len(cm.criteria) != 2 {
		t.Fatalf("criteria count = %d, want 2", len(cm.criteria))
	}
	if cm.criteria[1].Name() != "path_pattern" {
		t.Errorf("criteria[1].Name() = %q, want %q", cm.criteria[1].Name(), "path_pattern")
	}

	// Verify it matches patterned paths.
	tape := Tape{Request: RecordedReq{Method: "GET", URL: "/users/42"}}
	req := httptest.NewRequest("GET", "/users/99", nil)
	matched, ok := matcher.Match(req, []Tape{tape})
	if !ok {
		t.Fatal("expected match")
	}
	if matched.Request.URL != "/users/42" {
		t.Errorf("matched tape URL = %q, want %q", matched.Request.URL, "/users/42")
	}
}

func TestLoadConfig_PathPatternValidationErrors(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name: "missing pattern",
			input: `{
				"version": "1",
				"matcher": {"criteria": [{"type": "path_pattern"}]},
				"rules": []
			}`,
		},
		{
			name: "paths on path_pattern",
			input: `{
				"version": "1",
				"matcher": {"criteria": [{"type": "path_pattern", "pattern": "/users/:id", "paths": ["$.x"]}]},
				"rules": []
			}`,
		},
		{
			name: "pattern on method",
			input: `{
				"version": "1",
				"matcher": {"criteria": [{"type": "method", "pattern": "/users/:id"}]},
				"rules": []
			}`,
		},
		{
			name: "pattern on path",
			input: `{
				"version": "1",
				"matcher": {"criteria": [{"type": "path", "pattern": "/users/:id"}]},
				"rules": []
			}`,
		},
		{
			name: "pattern on content_negotiation",
			input: `{
				"version": "1",
				"matcher": {"criteria": [{"type": "content_negotiation", "pattern": "/x"}]},
				"rules": []
			}`,
		},
		{
			name: "pattern on body_fuzzy",
			input: `{
				"version": "1",
				"matcher": {"criteria": [{"type": "body_fuzzy", "paths": ["$.x"], "pattern": "/x"}]},
				"rules": []
			}`,
		},
		{
			name: "invalid pattern (no leading slash)",
			input: `{
				"version": "1",
				"matcher": {"criteria": [{"type": "path_pattern", "pattern": "users/:id"}]},
				"rules": []
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadConfig(strings.NewReader(tt.input))
			if err == nil {
				t.Error("expected validation error, got nil")
			}
		})
	}
}

// writeTestFile is a helper that writes content to the given path.
func writeTestFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
