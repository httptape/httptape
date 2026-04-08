# Redaction

Redaction is httptape's most distinctive feature. Sensitive data (secrets, PII, credentials) is redacted or replaced with deterministic fakes **before it touches disk**. This is the second R in httptape's **Record, Redact, Replay** pipeline.

The redaction pipeline is implemented in Go as a `Pipeline` of `SanitizeFunc` transformations (the Go types use "sanitize" terminology). Each function receives a `Tape` and returns a (possibly modified) copy. Functions are applied in order.

```go
type SanitizeFunc func(Tape) Tape

type Pipeline struct { /* ... */ }
func NewPipeline(funcs ...SanitizeFunc) *Pipeline
func (p *Pipeline) Sanitize(t Tape) Tape
```

The `Pipeline` implements the `Sanitizer` interface, which the `Recorder` accepts:

```go
type Sanitizer interface {
    Sanitize(Tape) Tape
}
```

## Building a redaction pipeline

```go
sanitizer := httptape.NewPipeline(
    httptape.RedactHeaders(),
    httptape.RedactBodyPaths("$.password", "$.ssn"),
    httptape.FakeFields("my-seed", "$.email", "$.user_id"),
)

rec := httptape.NewRecorder(store,
    httptape.WithSanitizer(sanitizer),
)
```

Functions are applied in order. In this example: headers are redacted first, then body fields are redacted, then remaining fields get deterministic fakes.

## RedactHeaders

Replaces header values with `"[REDACTED]"` in both request and response headers.

```go
// Redact the default sensitive headers:
httptape.RedactHeaders()
```

Default sensitive headers:
- `Authorization` -- bearer tokens, basic auth
- `Cookie` -- session tokens
- `Set-Cookie` -- server-set sessions
- `X-Api-Key` -- API key auth
- `Proxy-Authorization` -- proxy auth
- `X-Forwarded-For` -- client IPs (PII)

To redact specific headers:

```go
httptape.RedactHeaders("Authorization", "X-Custom-Secret", "X-Internal-Token")
```

Header matching is case-insensitive per the HTTP spec.

You can retrieve the default list programmatically:

```go
defaults := httptape.DefaultSensitiveHeaders()
// ["Authorization", "Cookie", "Set-Cookie", "X-Api-Key", "Proxy-Authorization", "X-Forwarded-For"]
```

## RedactBodyPaths

Redacts fields within JSON request and response bodies at specified paths.

```go
httptape.RedactBodyPaths("$.password", "$.user.ssn", "$.tokens[*].value")
```

### Path syntax

Paths use a JSONPath-like syntax:

| Pattern | Description |
|---------|-------------|
| `$.field` | Top-level field |
| `$.nested.field` | Nested field access |
| `$.array[*].field` | Field within each element of an array |

### Redaction behavior

Redacted values are type-aware:

| JSON type | Redacted value |
|-----------|---------------|
| string | `"[REDACTED]"` |
| number | `0` |
| boolean | `false` |
| null, object, array | Unchanged |

If the body is not valid JSON, it is left unchanged (no error). Missing paths are silently skipped.

### Example

Input body:
```json
{
  "username": "alice",
  "password": "s3cret",
  "profile": {
    "ssn": "123-45-6789"
  }
}
```

With `RedactBodyPaths("$.password", "$.profile.ssn")`:

```json
{
  "username": "alice",
  "password": "[REDACTED]",
  "profile": {
    "ssn": "[REDACTED]"
  }
}
```

## FakeFields

Replaces field values with deterministic fakes derived from HMAC-SHA256. The same seed and input value always produce the same fake output, preserving cross-fixture consistency.

```go
httptape.FakeFields("my-project-seed",
    "$.user.email",
    "$.user.id",
    "$.tokens[*].value",
)
```

### The seed parameter

The first argument is a project-level seed used as the HMAC key. Different seeds produce different fakes. The same seed and input always produce the same output.

Choose a seed that is unique to your project. It does not need to be secret -- it is used for determinism, not security.

### Faking strategies

The fake value depends on the detected type of the original value:

| Detected type | Fake format | Example |
|--------------|-------------|---------|
| Email (contains `@`) | `user_<hash>@example.com` | `user_a1b2c3d4@example.com` |
| UUID (8-4-4-4-12 hex) | Deterministic UUID v5 | `a1b2c3d4-e5f6-5789-abcd-0123456789ab` |
| Number (float64) | Positive integer [1, 2^31-1] | `1234567890` |
| Other string | `fake_<hash>` | `fake_a1b2c3d4` |
| Boolean, null, object, array | Unchanged | -- |

### Example

Input body:
```json
{
  "user": {
    "email": "alice@company.com",
    "id": "550e8400-e29b-41d4-a716-446655440000",
    "name": "Alice Smith"
  }
}
```

With `FakeFields("my-seed", "$.user.email", "$.user.id", "$.user.name")`:

```json
{
  "user": {
    "email": "user_7f3a2b1c@example.com",
    "id": "7f3a2b1c-4d5e-5f60-8a9b-c0d1e2f3a4b5",
    "name": "fake_7f3a2b1c"
  }
}
```

The key property: if `alice@company.com` appears in another fixture, it will be faked to the same value. This preserves relational consistency across your fixture set.

## Combining redaction and faking

Order matters. Typically, redact first (remove things that should be gone entirely), then fake (replace things that need consistent stand-in values):

```go
sanitizer := httptape.NewPipeline(
    // Step 1: Remove sensitive headers entirely
    httptape.RedactHeaders(),

    // Step 2: Redact body fields that should be blank
    httptape.RedactBodyPaths("$.password", "$.credit_card.number"),

    // Step 3: Replace PII with deterministic fakes
    httptape.FakeFields("my-seed",
        "$.user.email",
        "$.user.phone",
        "$.user.id",
    ),
)
```

## CLI and Docker

Redaction is available in all httptape modes (record, proxy) via a JSON config file:

```bash
# Record with redaction
httptape record --upstream https://api.example.com --fixtures ./mocks --config redact.json

# Proxy with redaction (applied to L2/disk cache only)
httptape proxy --upstream https://api.example.com --fixtures ./cache --config redact.json
```

See [CLI](cli.md) and [Docker](docker.md) for full usage.

## Declarative configuration

Instead of building pipelines in code, you can define redaction rules in a JSON config file. See [Config](config.md) for details.

```json
{
  "version": "1",
  "rules": [
    { "action": "redact_headers" },
    { "action": "redact_body", "paths": ["$.password"] },
    { "action": "fake", "seed": "my-seed", "paths": ["$.user.email"] }
  ]
}
```

## Custom sanitize functions

You can write your own `SanitizeFunc` and add it to the pipeline:

```go
func maskIPAddresses() httptape.SanitizeFunc {
    return func(t httptape.Tape) httptape.Tape {
        // Your custom transformation logic
        // Remember: do not mutate the input tape -- copy fields you modify
        return t
    }
}

sanitizer := httptape.NewPipeline(
    httptape.RedactHeaders(),
    maskIPAddresses(),
)
```

## See also

- [Config](config.md) -- declarative JSON configuration
- [Recording](recording.md) -- attaching the redaction pipeline to recorders
- [Proxy Mode](proxy.md) -- redaction in proxy mode (L2 writes only)
- [API Reference](api-reference.md) -- full type signatures
