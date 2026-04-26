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

// Config represents a declarative configuration for httptape.
// It can be loaded from JSON or constructed programmatically.
//
// The Version field must be "1". The Rules field contains an ordered list
// of sanitization rules that map to the existing Pipeline / SanitizeFunc API.
// The optional Matcher field declares which criteria the replay server's
// CompositeMatcher uses to select recorded tapes.
type Config struct {
	Version string         `json:"version"`
	Matcher *MatcherConfig `json:"matcher,omitempty"`
	Rules   []Rule         `json:"rules"`
}

// MatcherConfig declares the composition of matching criteria for the replay
// server. It maps to a CompositeMatcher constructed via Config.BuildMatcher.
type MatcherConfig struct {
	Criteria []CriterionConfig `json:"criteria"`
}

// CriterionConfig represents a single matching criterion in the declarative
// config. The Type field is the discriminator (matches Criterion.Name()).
// Type-specific fields are validated based on the Type value.
//
// Currently supported types:
//   - "method":              matches on HTTP method (no type-specific fields)
//   - "path":                matches on URL path (no type-specific fields)
//   - "body_fuzzy":          matches on specific JSON body fields (requires Paths)
//   - "content_negotiation": matches request Accept against response Content-Type (no type-specific fields)
//   - "path_pattern":        matches Express-style path patterns (requires Pattern)
type CriterionConfig struct {
	Type    string   `json:"type"`
	Paths   []string `json:"paths,omitempty"`
	Pattern string   `json:"pattern,omitempty"`
}

// Rule represents a single sanitization rule within a Config.
// The Action field determines which other fields are relevant:
//
//   - "redact_headers": Headers (optional; defaults to DefaultSensitiveHeaders)
//   - "redact_body":    Paths (required, non-empty)
//   - "fake":           Seed (required, non-empty) and either Paths or Fields (mutually exclusive)
//
// For "fake" rules, Fields maps JSONPath-like paths to faker specifications.
// A faker spec is either a string shorthand (e.g., "email", "phone") or an
// object with a "type" field and type-specific parameters.
type Rule struct {
	Action  string         `json:"action"`
	Headers []string       `json:"headers,omitempty"`
	Paths   []string       `json:"paths,omitempty"`
	Seed    string         `json:"seed,omitempty"`
	Fields  map[string]any `json:"fields,omitempty"`
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
//   - Rules must be non-empty (unless a matcher is configured)
//   - Each rule must have a known action
//   - Action-specific required fields must be present
//   - All paths must be valid JSONPath-like syntax
//   - If a matcher is configured, each criterion type must be recognized
//     and type-specific field requirements must be met
func (c *Config) Validate() error {
	var errs []string

	if c.Version != configVersion {
		errs = append(errs, fmt.Sprintf("unsupported version %q (expected %q)", c.Version, configVersion))
	}

	if len(c.Rules) == 0 && c.Matcher == nil {
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
			hasPaths := len(rule.Paths) > 0
			hasFields := len(rule.Fields) > 0
			if hasPaths && hasFields {
				errs = append(errs, fmt.Sprintf("%s: %q cannot use both \"paths\" and \"fields\"", prefix, rule.Action))
			}
			if !hasPaths && !hasFields {
				errs = append(errs, fmt.Sprintf("%s: %q requires non-empty \"paths\" or \"fields\"", prefix, rule.Action))
			}
			for _, p := range rule.Paths {
				if _, ok := parsePath(p); !ok {
					errs = append(errs, fmt.Sprintf("%s: %q invalid path syntax: %q", prefix, rule.Action, p))
				}
			}
			for path, spec := range rule.Fields {
				if _, ok := parsePath(path); !ok {
					errs = append(errs, fmt.Sprintf("%s: %q invalid path syntax: %q", prefix, rule.Action, path))
				}
				if _, err := parseFakerSpec(spec); err != nil {
					errs = append(errs, fmt.Sprintf("%s: %q field %q: %v", prefix, rule.Action, path, err))
				}
			}
			if len(rule.Headers) > 0 {
				errs = append(errs, fmt.Sprintf("%s: %q does not use \"headers\"", prefix, rule.Action))
			}
		}
	}

	// Validate matcher config if present.
	if c.Matcher != nil {
		if len(c.Matcher.Criteria) == 0 {
			errs = append(errs, "matcher.criteria must be a non-empty array")
		}
		for i, cc := range c.Matcher.Criteria {
			prefix := fmt.Sprintf("matcher.criteria[%d]", i)

			builder, ok := criterionBuilders[cc.Type]
			if !ok {
				errs = append(errs, fmt.Sprintf("%s: unknown type %q", prefix, cc.Type))
				continue
			}

			// Use the builder's validate function for type-specific checks.
			if vErr := builder.validate(cc); vErr != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", prefix, vErr))
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("config validation: %s", strings.Join(errs, "; "))
	}
	return nil
}

// criterionBuilder holds a factory function and a validation function for
// a single criterion type. The validate function performs shape checks
// (type-specific field requirements). The build function constructs the
// Criterion value.
type criterionBuilder struct {
	validate func(CriterionConfig) error
	build    func(CriterionConfig) (Criterion, error)
}

// criterionBuilders maps criterion type names to their builder definitions.
// Each entry corresponds to a supported Criterion.Name() value.
var criterionBuilders = map[string]criterionBuilder{
	"method":              {validate: validateMethodCriterion, build: buildMethodCriterion},
	"path":                {validate: validatePathCriterion, build: buildPathCriterion},
	"body_fuzzy":          {validate: validateBodyFuzzyCriterion, build: buildBodyFuzzyCriterion},
	"content_negotiation": {validate: validateContentNegotiationCriterion, build: buildContentNegotiationCriterion},
	"path_pattern":        {validate: validatePathPatternCriterion, build: buildPathPatternCriterion},
}

func validateMethodCriterion(cc CriterionConfig) error {
	if len(cc.Paths) > 0 {
		return fmt.Errorf("%q does not use \"paths\"", cc.Type)
	}
	if cc.Pattern != "" {
		return fmt.Errorf("%q does not use \"pattern\"", cc.Type)
	}
	return nil
}

func buildMethodCriterion(_ CriterionConfig) (Criterion, error) {
	return MethodCriterion{}, nil
}

func validatePathCriterion(cc CriterionConfig) error {
	if len(cc.Paths) > 0 {
		return fmt.Errorf("%q does not use \"paths\"", cc.Type)
	}
	if cc.Pattern != "" {
		return fmt.Errorf("%q does not use \"pattern\"", cc.Type)
	}
	return nil
}

func buildPathCriterion(_ CriterionConfig) (Criterion, error) {
	return PathCriterion{}, nil
}

func validateBodyFuzzyCriterion(cc CriterionConfig) error {
	if len(cc.Paths) == 0 {
		return fmt.Errorf("%q requires non-empty \"paths\"", cc.Type)
	}
	if cc.Pattern != "" {
		return fmt.Errorf("%q does not use \"pattern\"", cc.Type)
	}
	for _, p := range cc.Paths {
		if _, ok := parsePath(p); !ok {
			return fmt.Errorf("%q invalid path syntax: %q", cc.Type, p)
		}
	}
	return nil
}

func buildBodyFuzzyCriterion(cc CriterionConfig) (Criterion, error) {
	return NewBodyFuzzyCriterion(cc.Paths...), nil
}

func validateContentNegotiationCriterion(cc CriterionConfig) error {
	if len(cc.Paths) > 0 {
		return fmt.Errorf("%q does not use \"paths\"", cc.Type)
	}
	if cc.Pattern != "" {
		return fmt.Errorf("%q does not use \"pattern\"", cc.Type)
	}
	return nil
}

func validatePathPatternCriterion(cc CriterionConfig) error {
	if cc.Pattern == "" {
		return fmt.Errorf("%q requires non-empty \"pattern\"", cc.Type)
	}
	if len(cc.Paths) > 0 {
		return fmt.Errorf("%q does not use \"paths\"", cc.Type)
	}
	// Validate the pattern by attempting to compile it.
	_, err := NewPathPatternCriterion(cc.Pattern)
	if err != nil {
		return fmt.Errorf("%q invalid pattern: %w", cc.Type, err)
	}
	return nil
}

func buildPathPatternCriterion(cc CriterionConfig) (Criterion, error) {
	return NewPathPatternCriterion(cc.Pattern)
}

func buildContentNegotiationCriterion(_ CriterionConfig) (Criterion, error) {
	return ContentNegotiationCriterion{}, nil
}

// BuildMatcher constructs a Matcher from the config's matcher declaration.
// If no matcher is configured (Matcher field is nil or has no criteria),
// it returns DefaultMatcher().
//
// BuildMatcher validates criterion types and their fields. It returns an
// error if any criterion type is unknown or required fields are missing.
//
// BuildMatcher assumes the config has been validated via Validate(). However,
// it performs its own materialization-time checks (e.g., for criteria that
// require runtime validation beyond shape checks).
func (c *Config) BuildMatcher() (Matcher, error) {
	if c.Matcher == nil || len(c.Matcher.Criteria) == 0 {
		return DefaultMatcher(), nil
	}

	criteria := make([]Criterion, 0, len(c.Matcher.Criteria))
	for i, cc := range c.Matcher.Criteria {
		builder, ok := criterionBuilders[cc.Type]
		if !ok {
			return nil, fmt.Errorf("matcher: criteria[%d]: unknown type %q", i, cc.Type)
		}
		criterion, err := builder.build(cc)
		if err != nil {
			return nil, fmt.Errorf("matcher: criteria[%d]: %w", i, err)
		}
		criteria = append(criteria, criterion)
	}

	return NewCompositeMatcher(criteria...), nil
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
			if len(rule.Fields) > 0 {
				fakers := make(map[string]Faker, len(rule.Fields))
				for path, spec := range rule.Fields {
					f, _ := parseFakerSpec(spec) // already validated
					fakers[path] = f
				}
				funcs = append(funcs, FakeFieldsWith(rule.Seed, fakers))
			} else {
				funcs = append(funcs, FakeFields(rule.Seed, rule.Paths...))
			}
		}
	}

	return NewPipeline(funcs...)
}

// validShorthands maps string shorthand names to zero-value faker constructors.
var validShorthands = map[string]func() Faker{
	"redacted":    func() Faker { return RedactedFaker{} },
	"hmac":        func() Faker { return HMACFaker{} },
	"email":       func() Faker { return EmailFaker{} },
	"phone":       func() Faker { return PhoneFaker{} },
	"credit_card": func() Faker { return CreditCardFaker{} },
	"address":     func() Faker { return AddressFaker{} },
	"name":        func() Faker { return NameFaker{} },
}

// parseFakerSpec parses a faker specification from a config value.
// The spec can be a string shorthand (e.g., "email") or an object with
// a "type" field and type-specific parameters.
func parseFakerSpec(spec any) (Faker, error) {
	switch v := spec.(type) {
	case string:
		ctor, ok := validShorthands[v]
		if !ok {
			return nil, fmt.Errorf("unknown faker shorthand %q", v)
		}
		return ctor(), nil

	case map[string]any:
		typeName, ok := v["type"].(string)
		if !ok || typeName == "" {
			return nil, fmt.Errorf("faker object requires a \"type\" string field")
		}

		// Check if it's a shorthand name used in object form (no extra params).
		if ctor, ok := validShorthands[typeName]; ok {
			return ctor(), nil
		}

		switch typeName {
		case "numeric":
			length, ok := v["length"].(float64)
			if !ok || length <= 0 {
				return nil, fmt.Errorf("\"numeric\" faker requires \"length\" > 0")
			}
			return NumericFaker{Length: int(length)}, nil

		case "date":
			format, _ := v["format"].(string)
			return DateFaker{Format: format}, nil

		case "pattern":
			pattern, ok := v["pattern"].(string)
			if !ok || pattern == "" {
				return nil, fmt.Errorf("\"pattern\" faker requires non-empty \"pattern\" field")
			}
			return PatternFaker{Pattern: pattern}, nil

		case "prefix":
			prefix, ok := v["prefix"].(string)
			if !ok || prefix == "" {
				return nil, fmt.Errorf("\"prefix\" faker requires non-empty \"prefix\" field")
			}
			return PrefixFaker{Prefix: prefix}, nil

		case "fixed":
			val, ok := v["value"]
			if !ok {
				return nil, fmt.Errorf("\"fixed\" faker requires a \"value\" field")
			}
			return FixedFaker{Value: val}, nil

		default:
			return nil, fmt.Errorf("unknown faker type %q", typeName)
		}

	default:
		return nil, fmt.Errorf("faker spec must be a string or object, got %T", spec)
	}
}
