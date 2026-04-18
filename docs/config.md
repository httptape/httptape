# Declarative Configuration

Instead of building sanitization pipelines and matchers in Go code, httptape supports a JSON configuration format. This is useful for the [CLI](cli.md), [Docker](docker.md), and [Testcontainers](testcontainers.md) workflows where you want to define sanitization rules and matching criteria outside your Go code.

## Config format

```json
{
  "version": "1",
  "matcher": {
    "criteria": [
      { "type": "method" },
      { "type": "path" },
      { "type": "content_negotiation" }
    ]
  },
  "rules": [
    { "action": "redact_headers" },
    { "action": "redact_body", "paths": ["$.password", "$.ssn"] },
    { "action": "fake", "seed": "my-project-seed", "paths": ["$.user.email", "$.user.id"] }
  ]
}
```

### Version

Must be `"1"`. This field is required.

### Matcher (optional)

Declares the `CompositeMatcher` criteria the replay server uses to select recorded tapes. When omitted, `DefaultMatcher()` (method + path) is used. When present, replaces the default matcher entirely.

See [Matching](matching.md) for the scoring model and available criteria.

### Rules

An ordered array of sanitization rules. Rules are applied sequentially, matching the Pipeline's semantics. Rules may be empty (`[]`) when the config is used only for matcher composition.

## Matcher criteria

The `matcher.criteria` array declares which `Criterion` implementations to compose. Each entry has a `type` field (matching `Criterion.Name()`) and optional type-specific fields.

| Type | Fields | Maps to |
|------|--------|---------|
| `"method"` | (none) | `MethodCriterion{}` |
| `"path"` | (none) | `PathCriterion{}` |
| `"body_fuzzy"` | `paths` (required, valid JSONPath-like) | `NewBodyFuzzyCriterion(paths...)` |
| `"content_negotiation"` | (none) | `ContentNegotiationCriterion{}` |

**Example: method + path + content negotiation**

```json
{
  "version": "1",
  "matcher": {
    "criteria": [
      { "type": "method" },
      { "type": "path" },
      { "type": "content_negotiation" }
    ]
  },
  "rules": []
}
```

**Example: method + path + body fuzzy (for distinguishing POST requests by body)**

```json
{
  "version": "1",
  "matcher": {
    "criteria": [
      { "type": "method" },
      { "type": "path" },
      { "type": "body_fuzzy", "paths": ["$.action", "$.messages[*].role"] }
    ]
  },
  "rules": [
    { "action": "redact_headers" }
  ]
}
```

### BuildMatcher

```go
matcher := cfg.BuildMatcher()

srv := httptape.NewServer(store,
    httptape.WithMatcher(matcher),
)
```

`BuildMatcher` returns `nil` when no `matcher` section is present. Check for nil before passing to `WithMatcher`.

## Actions

### redact_headers

Maps to `RedactHeaders()`. Replaces header values with `"[REDACTED]"`.

```json
{ "action": "redact_headers" }
```

Optionally specify which headers to redact (default: `DefaultSensitiveHeaders`):

```json
{ "action": "redact_headers", "headers": ["Authorization", "X-Custom-Secret"] }
```

### redact_body

Maps to `RedactBodyPaths()`. Redacts specific fields in JSON bodies.

```json
{ "action": "redact_body", "paths": ["$.password", "$.credit_card.number", "$.tokens[*].secret"] }
```

The `paths` field is required and must be non-empty.

### fake

Replaces values with deterministic HMAC-based fakes. The `seed` field is always required. The faking strategy is selected by which other field you set:

- `paths` -- auto-detects the strategy from each value's runtime type (maps to `FakeFields`).
- `fields` -- selects an explicit faker per path (maps to `FakeFieldsWith`).

Exactly one of `paths` or `fields` must be set; specifying both is rejected.

#### Auto-detect form (paths)

```json
{ "action": "fake", "seed": "my-project-seed", "paths": ["$.user.email", "$.user.id", "$.user.name"] }
```

Auto-detect picks `EmailFaker`-equivalent output for strings containing `@`, UUID-shaped output for UUID strings, a positive integer for numbers, and `"fake_<hex>"` for other strings. See [Redaction](sanitization.md#fakefields) for the full table.

#### Typed fake fields

When you need a specific format -- credit card, fixed-length digits, a sentinel value -- use the `fields` map. Each entry maps a JSONPath-like path to a faker spec.

```json
{
  "action": "fake",
  "seed": "my-project-seed",
  "fields": {
    "$.user.email":   "email",
    "$.user.phone":   "phone",
    "$.card.number":  "credit_card",
    "$.card.cvv":     { "type": "numeric", "length": 3 },
    "$.user.dob":     { "type": "date", "format": "2006-01-02" },
    "$.order.status": { "type": "fixed", "value": "active" }
  }
}
```

A faker spec is either a **string shorthand** or an **object** with a `type` field.

##### String shorthands

These names construct the corresponding zero-value Faker:

| Shorthand | Faker | Output |
|---|---|---|
| `"redacted"` | `RedactedFaker{}` | strings -> `"[REDACTED]"`, numbers -> `0`, bools -> `false` |
| `"hmac"` | `HMACFaker{}` | strings -> `"fake_<hex>"`, numbers -> positive int |
| `"email"` | `EmailFaker{}` | `"user_<hex>@example.com"` |
| `"phone"` | `PhoneFaker{}` | digits replaced, format preserved |
| `"credit_card"` | `CreditCardFaker{}` | `XXXX-XXXX-XXXX-XXXX`, prefix preserved, valid Luhn |
| `"name"` | `NameFaker{}` | `"<First> <Last>"` from internal lists |
| `"address"` | `AddressFaker{}` | `"<num> <street> <suffix>, <city>, <ST> <zip>"` |

A shorthand may also be written in object form (`{ "type": "email" }`) -- handy when an editor's JSON schema completion prefers objects, or when you want to keep all entries visually uniform.

##### Object-form fakers

Five fakers take parameters and must be written as objects:

| `type` | Required fields | Maps to |
|---|---|---|
| `"numeric"` | `length` (number > 0) | `NumericFaker{Length: ...}` |
| `"date"` | (optional) `format` (Go layout string; defaults to `"2006-01-02"`) | `DateFaker{Format: ...}` |
| `"pattern"` | `pattern` (non-empty string) | `PatternFaker{Pattern: ...}` -- `#` -> digit, `?` -> letter |
| `"prefix"` | `prefix` (non-empty string) | `PrefixFaker{Prefix: ...}` -> `"<Prefix><16-hex>"` |
| `"fixed"` | `value` (any JSON value) | `FixedFaker{Value: ...}` -- always returns `value` |

Examples:

```json
{ "type": "numeric", "length": 16 }
{ "type": "date", "format": "2006-01-02T15:04:05Z07:00" }
{ "type": "pattern", "pattern": "###-##-####" }
{ "type": "prefix", "prefix": "cust_" }
{ "type": "fixed", "value": true }
```

See [Redaction -> Typed fakers](sanitization.md#typed-fakers) for output examples and prose descriptions of each faker.

## Loading config in Go

### From a reader

```go
f, _ := os.Open("sanitize.json")
cfg, err := httptape.LoadConfig(f)
if err != nil {
    log.Fatal(err) // JSON parse error or validation error
}
```

### From a file path

```go
cfg, err := httptape.LoadConfigFile("sanitize.json")
if err != nil {
    log.Fatal(err)
}
```

### Building the pipeline

```go
pipeline := cfg.BuildPipeline()

rec := httptape.NewRecorder(store,
    httptape.WithSanitizer(pipeline),
)
```

## Validation

`LoadConfig` and `LoadConfigFile` validate the config automatically. You can also validate manually:

```go
cfg := &httptape.Config{
    Version: "1",
    Rules: []httptape.Rule{
        {Action: "redact_headers"},
    },
}
err := cfg.Validate()
```

Validation checks:
- Version must be `"1"`
- Rules may be empty only when a `matcher` section is present (the config is matcher-only)
- Each rule must have a known action (`redact_headers`, `redact_body`, `fake`)
- Action-specific required fields must be present
- All paths must use valid JSONPath-like syntax (`$.field`, `$.nested.field`, `$.array[*].field`)
- Fields irrelevant to an action are rejected (e.g., `paths` on `redact_headers`)
- Unknown JSON fields are rejected

Additional rules for `fake`:
- `seed` must be present and non-empty.
- Exactly one of `paths` or `fields` must be set; setting both is rejected, setting neither is rejected.
- Each key in `fields` must be a valid JSONPath-like path.
- Each value in `fields` must be either a known string shorthand or an object with a known `type`.
- Object-form fakers must include their required parameters (`numeric.length`, `pattern.pattern`, `prefix.prefix`, `fixed.value`); `date.format` is optional.
- Anything else (numbers, arrays, nulls) as a `fields` value is rejected.

Additional rules for `matcher`:
- `criteria` must be a non-empty array.
- Each criterion must have a known `type` (`method`, `path`, `body_fuzzy`, `content_negotiation`).
- `body_fuzzy` requires a non-empty `paths` array with valid JSONPath-like paths.
- `method`, `path`, and `content_negotiation` do not accept `paths`; specifying them is rejected.
- Unknown criterion fields are rejected.

## JSON Schema

A JSON Schema is available at [`config.schema.json`](https://github.com/VibeWarden/httptape/blob/main/config.schema.json) for IDE autocompletion and CI validation.

Reference it in your config file:

```json
{
  "$schema": "https://raw.githubusercontent.com/VibeWarden/httptape/main/config.schema.json",
  "version": "1",
  "rules": [...]
}
```

## Complete example

```json
{
  "version": "1",
  "rules": [
    {
      "action": "redact_headers",
      "headers": ["Authorization", "Cookie", "X-Api-Key"]
    },
    {
      "action": "redact_body",
      "paths": [
        "$.password",
        "$.credit_card.number",
        "$.credit_card.cvv"
      ]
    },
    {
      "action": "fake",
      "seed": "my-project-2024",
      "paths": [
        "$.user.email",
        "$.user.phone",
        "$.user.id",
        "$.orders[*].customer_id"
      ]
    }
  ]
}
```

## Typed-faker examples

### Email shorthand

The simplest typed-faker config: route a single field through `EmailFaker` so it always lands as `user_<hex>@example.com` regardless of what the upstream actually sends.

```json
{
  "version": "1",
  "rules": [
    {
      "action": "fake",
      "seed": "my-project-2024",
      "fields": {
        "$.user.email": "email"
      }
    }
  ]
}
```

### Numeric object form

Force a fixed-width numeric ID (CVV, OTP, account number) into a deterministic three-digit string. Auto-detect would treat the field as a generic string and produce `"fake_<hex>"`, which would not pass downstream length validation.

```json
{
  "version": "1",
  "rules": [
    {
      "action": "fake",
      "seed": "my-project-2024",
      "fields": {
        "$.payment.cvv": { "type": "numeric", "length": 3 }
      }
    }
  ]
}
```

### Credit card shorthand

Generate a Luhn-valid card number that preserves the issuer prefix from the original. Combine with `redact_body` to also clear out an unrelated CVV in the same pipeline.

```json
{
  "version": "1",
  "rules": [
    {
      "action": "redact_body",
      "paths": ["$.payment.cvv"]
    },
    {
      "action": "fake",
      "seed": "my-project-2024",
      "fields": {
        "$.payment.card_number": "credit_card"
      }
    }
  ]
}
```

## See also

- [Redaction](sanitization.md) -- programmatic redaction pipeline API
- [Redaction -> Typed fakers](sanitization.md#typed-fakers) -- prose descriptions of every built-in faker
- [API Reference -> Faker interface](api-reference.md#faker-interface) -- full Go signatures
- [CLI](cli.md) -- using config files with the CLI
- [Docker](docker.md) -- mounting config files into containers
