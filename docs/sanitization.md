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

## Typed fakers

`FakeFields` auto-detects the faking strategy from each value's runtime type. When you need explicit control -- for example, to force a generic-looking string into the credit-card format, or to fake a numeric ID as a fixed-length digit string -- use the typed-faker API.

The contract is the `Faker` interface:

```go
type Faker interface {
    Fake(seed string, original any) any
}
```

Implementations take an HMAC seed and the original JSON value (already decoded by `encoding/json`, so strings are `string`, numbers are `float64`, booleans are `bool`, nulls are `nil`, objects are `map[string]any`, arrays are `[]any`). They must be deterministic: the same seed and original must always produce the same fake.

Typed fakers are wired into a pipeline with `FakeFieldsWith`, which takes a seed and a path-to-`Faker` map:

```go
sanitizer := httptape.NewPipeline(
    httptape.FakeFieldsWith("my-seed", map[string]httptape.Faker{
        "$.user.email":  httptape.EmailFaker{},
        "$.user.phone":  httptape.PhoneFaker{},
        "$.card.number": httptape.CreditCardFaker{},
        "$.card.cvv":    httptape.NumericFaker{Length: 3},
    }),
)
```

Path syntax is identical to `RedactBodyPaths` and `FakeFields`. Invalid JSON bodies, missing paths, and invalid path strings are silently skipped (no error). All twelve built-in fakers are constructed as struct literals; none has a `NewXFaker` constructor.

### Auto-detect vs. typed -- when to use each

| Use case | API |
|---|---|
| Mixed body, you trust the value-type heuristic, want minimum config | `FakeFields(seed, paths...)` |
| You need a specific format (credit card, fixed-length digits, pattern, prefix) | `FakeFieldsWith(seed, fields)` |
| You want to fully redact a leaf rather than fake it | `FakeFieldsWith(...)` with `RedactedFaker{}` |
| You want a constant value at a path regardless of input | `FakeFieldsWith(...)` with `FixedFaker{Value: ...}` |
| You need a custom format the built-ins do not cover | Implement `Faker` and pass it to `FakeFieldsWith` |

### Built-in fakers -- redaction-style

These fakers replace values without preserving any information from the original. Use them when the original content is sensitive enough that even a deterministic transform of it should not appear in fixtures.

#### RedactedFaker

Replaces strings with `"[REDACTED]"`, numbers with `0`, and booleans with `false`. Other types (nil, objects, arrays) pass through unchanged. Equivalent to the leaf behavior of `RedactBodyPaths`, but addressable through the typed-faker map so you can mix it with other fakers in a single `FakeFieldsWith` call.

```go
httptape.FakeFieldsWith("seed", map[string]httptape.Faker{
    "$.password": httptape.RedactedFaker{},
    "$.email":    httptape.EmailFaker{},
})
```

#### FixedFaker

Always returns its `Value` field, ignoring both seed and original. Useful for stamping a known sentinel into a field (e.g., `"status": "active"`) so downstream tests can assert against it.

```go
httptape.FixedFaker{Value: "active"}
httptape.FixedFaker{Value: float64(1)}
httptape.FixedFaker{Value: true}
```

`Value` is `any`, so the encoded JSON type follows Go's `encoding/json` rules.

### Built-in fakers -- generic deterministic

These fakers produce a deterministic value derived from `HMAC-SHA256(seed, original)`, with no PII shape. Use them when you need consistency across fixtures but do not need the output to look like any particular format.

#### HMACFaker

Mirrors the auto-detect default for generic strings and numbers. Strings become `"fake_<8-hex>"`; numbers become a positive integer in `[1, 2^31-1]`; booleans, nulls, objects, and arrays pass through unchanged.

```go
httptape.HMACFaker{}
// "abc"        -> "fake_a1b2c3d4"
// float64(42)  -> 1734567890
```

#### NumericFaker

Generates a string of `Length` HMAC-derived digits. Useful for fixed-width numeric IDs (CVVs, OTPs, account numbers) where digit-count matters.

```go
httptape.NumericFaker{Length: 3}   // CVV
httptape.NumericFaker{Length: 16}  // account number
```

If the input is not a string, it is returned unchanged. Output length always equals `Length`, even if `Length` exceeds 32 (the HMAC is re-chained).

#### PatternFaker

Fills a template where `#` becomes a digit, `?` becomes a lowercase letter, and any other character is copied literally.

```go
httptape.PatternFaker{Pattern: "###-##-####"}     // SSN-shaped
httptape.PatternFaker{Pattern: "??-#####"}         // 2 letters, dash, 5 digits
httptape.PatternFaker{Pattern: "ORDER-####-????"} // mixed literal + variable
```

If the input is not a string, it is returned unchanged.

#### PrefixFaker

Generates `"<Prefix><16-hex>"`. Useful when an upstream issues namespaced identifiers (e.g., `cust_*`, `order_*`) and downstream tests look at the prefix.

```go
httptape.PrefixFaker{Prefix: "cust_"}   // "cust_a1b2c3d4e5f60718"
httptape.PrefixFaker{Prefix: "order_"}  // "order_a1b2c3d4e5f60718"
```

If the input is not a string, it is returned unchanged.

#### DateFaker

Generates a date string formatted with `Format` (Go reference layout; defaults to `"2006-01-02"` when empty). The date is drawn deterministically from a ~100-year window starting at 2000-01-01.

```go
httptape.DateFaker{}                       // "2042-09-13"
httptape.DateFaker{Format: "2006-01-02"}   // "2042-09-13"
httptape.DateFaker{Format: time.RFC3339}   // "2042-09-13T00:00:00Z"
```

If the input is not a string, it is returned unchanged.

### Built-in fakers -- PII-shaped

These fakers preserve a recognizable shape (so downstream parsers do not break) while replacing the underlying content. They are the right choice for fields whose format matters to consumers (clients that validate emails, payment processors that check Luhn, address forms that expect a US zip).

#### EmailFaker

Replaces strings with `"user_<8-hex>@example.com"`. Non-string inputs pass through unchanged.

```go
httptape.EmailFaker{}
// "alice@corp.com" -> "user_a1b2c3d4@example.com"
```

#### PhoneFaker

Replaces digits in the input with HMAC-derived digits while preserving every non-digit character (spaces, dashes, parentheses, plus signs). Output length always equals input length.

```go
httptape.PhoneFaker{}
// "+1 (555) 123-4567" -> "+1 (937) 481-2056"
// "555-1234"          -> "938-1742"
```

If the input is not a string, it is returned unchanged.

#### CreditCardFaker

Generates a 16-digit number formatted as `XXXX-XXXX-XXXX-XXXX`. The first 6 digits (issuer prefix) are taken from the original; if the original has fewer than 6 digits, the prefix `400000` is used. The middle 9 digits are HMAC-derived; the last digit is a valid Luhn check digit.

```go
httptape.CreditCardFaker{}
// "4532-1234-5678-9012" -> "4532-12<derived>-<luhn>"
```

If the input is not a string, it is returned unchanged.

#### NameFaker

Picks a first name and a last name from internal fixed lists using two HMAC bytes. Output is `"<First> <Last>"`.

```go
httptape.NameFaker{}
// "Alice Johnson" -> "Olivia Martinez" (deterministic for that seed+input)
```

If the input is not a string, it is returned unchanged.

#### AddressFaker

Generates a US-style address: `"<number> <street> <suffix>, <city>, <ST> <zip>"`. House number is in `[1, 9999]`; zip is 5 digits; city, state, and street components are picked from internal fixed lists.

```go
httptape.AddressFaker{}
// "123 Main St, Anytown, CA 90210" -> "8421 Cedar Drive, Salem, NV 30418"
```

If the input is not a string, it is returned unchanged.

### Custom fakers

Anything that satisfies the `Faker` interface works. A typical use case is wrapping an existing data source (a list of canonical fake company names, a generator of valid IBANs, etc.) so the recorded fixtures are coherent with the rest of your test data.

```go
import (
    "crypto/hmac"
    "crypto/sha256"
)

type CompanyFaker struct {
    Names []string
}

func (f CompanyFaker) Fake(seed string, original any) any {
    s, ok := original.(string)
    if !ok || len(f.Names) == 0 {
        return original
    }
    // Deterministic pick using the HMAC of seed||s.
    mac := hmac.New(sha256.New, []byte(seed))
    mac.Write([]byte(s))
    h := mac.Sum(nil)
    idx := int(h[0]) % len(f.Names)
    return f.Names[idx]
}

sanitizer := httptape.NewPipeline(
    httptape.FakeFieldsWith("my-seed", map[string]httptape.Faker{
        "$.employer": CompanyFaker{Names: []string{"Acme", "Globex", "Initech"}},
    }),
)
```

Make sure your implementation is deterministic (same seed + original always produces the same output) and does not mutate `original` -- httptape passes the value pulled out of `json.Unmarshal` directly.

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

The `fake` action also accepts a `fields` map that selects a typed faker per path -- the JSON-config equivalent of `FakeFieldsWith`. See [Config](config.md#typed-fake-fields) for syntax and the full list of shorthands.

## SSE event redaction

For SSE (Server-Sent Events) responses, httptape provides two `SanitizeFunc` constructors that operate on individual event payloads. Each event's `Data` field is treated as an independent JSON body, so the same path syntax applies as `RedactBodyPaths` and `FakeFields`.

These functions are no-ops for non-SSE tapes, so they compose safely in a pipeline that handles both regular and SSE responses.

### RedactSSEEventData

Redacts fields within each SSE event's JSON data:

```go
httptape.RedactSSEEventData("$.choices[*].delta.content", "$.usage.prompt_tokens")
```

Example: an LLM streaming response where each event looks like:

```json
{"id":"chatcmpl-1","choices":[{"delta":{"content":"The user's SSN is 123-45-6789"}}]}
```

After `RedactSSEEventData("$.choices[*].delta.content")`:

```json
{"id":"chatcmpl-1","choices":[{"delta":{"content":"[REDACTED]"}}]}
```

Each event is redacted independently. Non-JSON event data (e.g., `[DONE]`) is left unchanged.

### FakeSSEEventData

Replaces fields within each SSE event's JSON data with deterministic fakes:

```go
httptape.FakeSSEEventData("my-seed", "$.user.email", "$.user.name")
```

This uses the same HMAC-SHA256 faking strategy as `FakeFields`. The same seed and input always produce the same fake, so cross-event consistency is preserved.

### Complete pipeline for LLM streaming

A typical pipeline for recording LLM API traffic with streaming redaction:

```go
sanitizer := httptape.NewPipeline(
    // Step 1: Redact auth headers.
    httptape.RedactHeaders("Authorization", "X-Api-Key"),

    // Step 2: Redact sensitive fields in regular (non-SSE) response bodies.
    httptape.RedactBodyPaths("$.api_key"),

    // Step 3: Redact PII from SSE event payloads.
    httptape.RedactSSEEventData("$.choices[*].delta.content"),

    // Step 4: Fake user identifiers in SSE events with deterministic values.
    httptape.FakeSSEEventData("my-seed", "$.user.email", "$.user.id"),
)

rec := httptape.NewRecorder(store, httptape.WithSanitizer(sanitizer))
```

The order is: headers first, regular body paths, SSE event redaction, SSE event faking. SSE-specific functions are no-ops for non-SSE tapes, and `RedactBodyPaths`/`FakeFields` are no-ops for SSE tapes (since SSE tapes have nil `Body`), so all functions coexist safely in one pipeline.

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
