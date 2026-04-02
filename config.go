package httptape

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Supported config actions.
const (
	// ActionRedactHeaders maps to RedactHeaders.
	ActionRedactHeaders = "redact_headers"
	// ActionRedactBody maps to RedactBodyPaths.
	ActionRedactBody = "redact_body"
	// ActionFake maps to FakeFields.
	ActionFake = "fake"
)

// configVersion is the only supported config version.
const configVersion = "1"

// validActions is the set of recognized action strings.
var validActions = map[string]struct{}{
	ActionRedactHeaders: {},
	ActionRedactBody:    {},
	ActionFake:          {},
}

// Config represents a declarative sanitization configuration.
// It can be loaded from JSON or constructed programmatically.
//
// The Version field must be "1". The Rules field contains an ordered list
// of sanitization rules that map to the existing Pipeline / SanitizeFunc API.
type Config struct {
	Version string `json:"version"`
	Rules   []Rule `json:"rules"`
}

// Rule represents a single sanitization rule within a Config.
// The Action field determines which other fields are relevant:
//
//   - "redact_headers": Headers (optional; defaults to DefaultSensitiveHeaders)
//   - "redact_body":    Paths (required, non-empty)
//   - "fake":           Seed (required, non-empty) and Paths (required, non-empty)
type Rule struct {
	Action  string   `json:"action"`
	Headers []string `json:"headers,omitempty"`
	Paths   []string `json:"paths,omitempty"`
	Seed    string   `json:"seed,omitempty"`
}

// LoadConfig reads a JSON sanitization config from r, validates it, and
// returns a Config. It returns an error if the JSON is malformed, contains
// unknown fields, or fails validation.
func LoadConfig(r io.Reader) (*Config, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("config: read error: %w", err)
	}

	var cfg Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("config: invalid JSON: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// LoadConfigFile is a convenience wrapper that opens the file at path and
// calls LoadConfig.
func LoadConfigFile(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config: open file: %w", err)
	}
	defer f.Close()
	return LoadConfig(f)
}

// Validate checks c for structural and semantic errors. It returns nil if
// the config is valid, or an error describing all issues found.
//
// Validation checks include:
//   - Version must be "1"
//   - Rules must be non-empty
//   - Each rule must have a known action
//   - Action-specific required fields must be present
//   - All paths must be valid JSONPath-like syntax
func (c *Config) Validate() error {
	var errs []string

	if c.Version != configVersion {
		errs = append(errs, fmt.Sprintf("unsupported version %q (expected %q)", c.Version, configVersion))
	}

	if len(c.Rules) == 0 {
		errs = append(errs, "rules must be a non-empty array")
	}

	for i, rule := range c.Rules {
		prefix := fmt.Sprintf("rule[%d]", i)

		if _, ok := validActions[rule.Action]; !ok {
			errs = append(errs, fmt.Sprintf("%s: unknown action %q", prefix, rule.Action))
			continue
		}

		switch rule.Action {
		case ActionRedactHeaders:
			// Headers is optional; no required fields.
			// Warn about irrelevant fields.
			if len(rule.Paths) > 0 {
				errs = append(errs, fmt.Sprintf("%s: %q does not use \"paths\"", prefix, rule.Action))
			}
			if rule.Seed != "" {
				errs = append(errs, fmt.Sprintf("%s: %q does not use \"seed\"", prefix, rule.Action))
			}

		case ActionRedactBody:
			if len(rule.Paths) == 0 {
				errs = append(errs, fmt.Sprintf("%s: %q requires non-empty \"paths\"", prefix, rule.Action))
			}
			for _, p := range rule.Paths {
				if _, ok := parsePath(p); !ok {
					errs = append(errs, fmt.Sprintf("%s: %q invalid path syntax: %q", prefix, rule.Action, p))
				}
			}
			if len(rule.Headers) > 0 {
				errs = append(errs, fmt.Sprintf("%s: %q does not use \"headers\"", prefix, rule.Action))
			}
			if rule.Seed != "" {
				errs = append(errs, fmt.Sprintf("%s: %q does not use \"seed\"", prefix, rule.Action))
			}

		case ActionFake:
			if rule.Seed == "" {
				errs = append(errs, fmt.Sprintf("%s: %q requires non-empty \"seed\"", prefix, rule.Action))
			}
			if len(rule.Paths) == 0 {
				errs = append(errs, fmt.Sprintf("%s: %q requires non-empty \"paths\"", prefix, rule.Action))
			}
			for _, p := range rule.Paths {
				if _, ok := parsePath(p); !ok {
					errs = append(errs, fmt.Sprintf("%s: %q invalid path syntax: %q", prefix, rule.Action, p))
				}
			}
			if len(rule.Headers) > 0 {
				errs = append(errs, fmt.Sprintf("%s: %q does not use \"headers\"", prefix, rule.Action))
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("config validation: %s", strings.Join(errs, "; "))
	}
	return nil
}

// BuildPipeline converts the Config into a Pipeline by mapping each Rule
// to the corresponding SanitizeFunc. Rules are applied in order, matching
// the Pipeline's sequential semantics.
//
// BuildPipeline assumes the config has been validated (via LoadConfig or
// Validate). If called on an invalid config, behavior is undefined.
func (c *Config) BuildPipeline() *Pipeline {
	funcs := make([]SanitizeFunc, 0, len(c.Rules))

	for _, rule := range c.Rules {
		switch rule.Action {
		case ActionRedactHeaders:
			funcs = append(funcs, RedactHeaders(rule.Headers...))
		case ActionRedactBody:
			funcs = append(funcs, RedactBodyPaths(rule.Paths...))
		case ActionFake:
			funcs = append(funcs, FakeFields(rule.Seed, rule.Paths...))
		}
	}

	return NewPipeline(funcs...)
}

