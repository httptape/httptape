# Template Helpers

httptape supports Mustache-style `{{...}}` template expressions in response bodies and headers. These expressions are resolved at serve time against the incoming request and server state.

## Request accessors

These accessors are available in all template contexts:

| Expression | Resolves to |
|---|---|
| `{{request.method}}` | HTTP method (GET, POST, etc.) |
| `{{request.path}}` | URL path |
| `{{request.url}}` | Full URL including query string |
| `{{request.headers.NAME}}` | Request header value (case-insensitive) |
| `{{request.query.NAME}}` | Query parameter value |
| `{{request.body.PATH}}` | JSON body field (dot-separated path) |

## Path parameter accessors

When using `PathPatternCriterion` with Express-style patterns (e.g., `/users/:id`), captured path segments are available via `{{pathParam.NAME}}`:

```json
{
  "id": "{{pathParam.id}}",
  "name": "User {{pathParam.id}}"
}
```

See [Matching](matching.md) for details on `PathPatternCriterion`.

## Helper functions

Helpers are invoked by name with optional keyword arguments:

### `{{now}}`

Returns the current UTC time.

| Argument | Default | Description |
|---|---|---|
| `format` | `rfc3339` | Output format: `rfc3339`, `iso`, `unix`, `unixMillis`, or a custom Go time format |

Examples:
- `{{now}}` -- `2026-04-23T14:30:00Z`
- `{{now format=unix}}` -- `1745416200`
- `{{now format=2006-01-02}}` -- `2026-04-23`

### `{{uuid}}`

Generates a random UUID v4. Non-deterministic (different on each call). For deterministic UUIDs, use `{{faker.uuid seed=...}}`.

### `{{randomHex}}`

Generates a random hex string.

| Argument | Required | Description |
|---|---|---|
| `length` | Yes | Number of hex characters |

Example: `{{randomHex length=16}}` -- `a1b2c3d4e5f6a7b8`

### `{{randomInt}}`

Generates a random integer in a range.

| Argument | Default | Description |
|---|---|---|
| `min` | `0` | Inclusive minimum |
| `max` | `100` | Inclusive maximum |

Example: `{{randomInt min=1 max=1000}}` -- `42`

### `{{counter}}`

Returns a monotonically increasing integer. Counters are per-server instance and persist across requests. They start at 1 and increment on each call.

| Argument | Default | Description |
|---|---|---|
| `name` | `default` | Counter name (independent counters) |

Example:
- First request: `{{counter}}` -- `1`
- Second request: `{{counter}}` -- `2`
- `{{counter name=orders}}` -- independent counter

Reset counters programmatically:
```go
srv.ResetCounter("orders") // reset named counter
srv.ResetCounter("")       // reset all counters
```

### `{{faker.*}}`

Deterministic fake data generation using HMAC-SHA256. Same seed always produces the same output.

| Expression | Output |
|---|---|
| `{{faker.email}}` | `user_a1b2c3d4@example.com` |
| `{{faker.name}}` | `James Smith` |
| `{{faker.phone}}` | Digit-replaced phone number |
| `{{faker.address}}` | US-style address |
| `{{faker.creditCard}}` | Valid credit card number |
| `{{faker.hmac}}` | `fake_a1b2c3d4` |
| `{{faker.redacted}}` | `[REDACTED]` |
| `{{faker.uuid}}` | Deterministic UUID |

| Argument | Default | Description |
|---|---|---|
| `seed` | auto-generated | HMAC seed for deterministic output |

**Auto-seed**: when `seed=` is omitted, a deterministic seed is derived from `SHA-256(tapeID + ":" + request.URL.Path)`. This means:
- Different request paths produce different fakes
- Different tapes for the same path produce different fakes
- The same tape + path always produces the same fake

**Explicit seed**: use `seed=` for full control:
```
{{faker.email seed=user-42}}
```

## Nested template evaluation

Helper arguments can contain `{{...}}` expressions that are resolved before the helper runs:

```
{{faker.name seed=user-{{pathParam.id}}}}
```

For a request to `/users/42`, this resolves to `{{faker.name seed=user-42}}`, then the faker produces a deterministic name for seed `user-42`.

This enables per-entity consistency: every request for `/users/42` gets the same fake name, while `/users/43` gets a different one.

## JSON-aware resolution

When the response body is JSON (detected by leading `{` or `[`), template resolution uses JSON-aware processing:

1. The body is parsed as a JSON tree
2. Template expressions in string values are resolved
3. Non-string values (numbers, booleans, null) are preserved
4. Resolved values are properly JSON-escaped
5. The tree is re-serialized

This prevents resolved values with special characters (quotes, backslashes) from breaking JSON syntax.

## Strict vs lenient mode

- **Lenient** (default): unresolvable expressions are replaced with empty string
- **Strict**: unresolvable expressions return HTTP 500 with `X-Httptape-Error: template`

```go
srv, _ := httptape.NewServer(store,
    httptape.WithStrictTemplating(true),
)
```

## Disabling templating

```go
srv, _ := httptape.NewServer(store,
    httptape.WithTemplating(false),
)
```

When disabled, response bodies and headers are written exactly as stored. SSE responses always skip templating.

## Backward compatibility

The `ResolveTemplateBodySimple` function provides backward-compatible template resolution using only request data (no path params, counters, or faker):

```go
resolved, err := httptape.ResolveTemplateBodySimple(body, req, strict)
```
