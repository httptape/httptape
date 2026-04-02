package httptape

import (
	"net/http"
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

// writeTestFile is a helper that writes content to the given path.
func writeTestFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
