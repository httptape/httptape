# Declarative Configuration

Instead of building sanitization pipelines in Go code, httptape supports a JSON configuration format. This is useful for the [CLI](cli.md), [Docker](docker.md), and [Testcontainers](testcontainers.md) workflows where you want to define sanitization rules outside your Go code.

## Config format

```json
{
  "version": "1",
  "rules": [
    { "action": "redact_headers" },
    { "action": "redact_body", "paths": ["$.password", "$.ssn"] },
    { "action": "fake", "seed": "my-project-seed", "paths": ["$.user.email", "$.user.id"] }
  ]
}
```

### Version

Must be `"1"`. This field is required.

### Rules

An ordered array of sanitization rules. Rules are applied sequentially, matching the Pipeline's semantics. At least one rule is required.

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

Maps to `FakeFields()`. Replaces values with deterministic HMAC-based fakes.

```json
{ "action": "fake", "seed": "my-project-seed", "paths": ["$.user.email", "$.user.id", "$.user.name"] }
```

Both `seed` and `paths` are required and must be non-empty.

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
- Rules must be non-empty
- Each rule must have a known action (`redact_headers`, `redact_body`, `fake`)
- Action-specific required fields must be present
- All paths must use valid JSONPath-like syntax (`$.field`, `$.nested.field`, `$.array[*].field`)
- Fields irrelevant to an action are rejected (e.g., `paths` on `redact_headers`)
- Unknown JSON fields are rejected

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

## See also

- [Redaction](sanitization.md) -- programmatic redaction pipeline API
- [CLI](cli.md) -- using config files with the CLI
- [Docker](docker.md) -- mounting config files into containers
