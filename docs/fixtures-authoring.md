# Fixture Authoring Guide

Hand-write Tape JSON files for static mocking without recording from a live API.

## When to author fixtures by hand

- The upstream API does not exist yet (contract-first development)
- You need specific edge cases (empty arrays, 204 No Content, error responses)
- You want deterministic test data without a recording step
- You are building a mock backend for frontend development (see [UI-First Dev](ui-first-dev.md))

## Tape JSON structure

Every fixture file is a single JSON object with these fields:

```json
{
  "id": "get-users-list",
  "route": "users-api",
  "recorded_at": "2025-01-15T10:00:00Z",
  "request": {
    "method": "GET",
    "url": "http://mock/api/users",
    "headers": {
      "Accept": ["application/json"]
    },
    "body": null,
    "body_hash": ""
  },
  "response": {
    "status_code": 200,
    "headers": {
      "Content-Type": ["application/json"]
    },
    "body": {"users": [{"id": 1, "name": "Alice"}]}
  },
  "metadata": {}
}
```

### Field reference

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `id` | string | Yes | Unique identifier. Used as the filename (`<id>.json`). Must not contain `/`, `\`, or `..`. |
| `route` | string | No | Logical grouping label (e.g., `"users-api"`). Used by `Filter.Route` and `RouteCriterion`. |
| `recorded_at` | string (RFC 3339) | No | UTC timestamp. Informational only -- not used for matching. |
| `request.method` | string | Yes | HTTP method (`GET`, `POST`, `PUT`, `DELETE`, `PATCH`, `HEAD`). |
| `request.url` | string | Yes | Full URL. The path component is used for matching (e.g., `http://mock/api/users`). |
| `request.headers` | object | No | Request headers. Each key maps to an array of strings. |
| `request.body` | varies | No | Request body. Shape depends on Content-Type (see below). `null` for bodiless requests. |
| `request.body_hash` | string | No | Hex-encoded SHA-256 hash of the original request body. Required for `BodyHashCriterion`. |
| `response.status_code` | int | Yes | HTTP status code (200, 201, 204, 404, 500, etc.). |
| `response.headers` | object | No | Response headers. Each key maps to an array of strings. |
| `response.body` | varies | No | Response body. Shape depends on Content-Type (see below). |
| `metadata` | object | No | Key-value pairs for delay/error simulation. Not used for matching. |

### Content-Type-driven body shape (v0.12+)

The `body` field's JSON representation depends on the `Content-Type` header:

| Content-Type | Body bytes | Body shape | `body_encoding` field |
|---|---|---|---|
| `application/json`, `+json` suffix | Valid JSON | Native JSON object/array | absent |
| `application/json`, `+json` suffix | Invalid JSON | Base64-encoded string | absent |
| `text/*`, `application/xml`, `application/javascript` | Valid UTF-8 | JSON string | absent |
| `text/*`, `application/xml`, `application/javascript` | Non-UTF-8 (Latin-1, Shift-JIS, …) | Base64-encoded string | `"base64"` |
| Binary (`image/*`, `application/octet-stream`, etc.) | Any | Base64-encoded string | absent |
| Missing or unknown | Any | Base64-encoded string | absent |
| Nil or empty body | — | `null` | absent |

This means JSON fixtures are human-readable: response bodies appear as native JSON objects, not opaque base64 strings. UTF-8 text bodies appear as plain JSON strings.

#### The `body_encoding` field

The `body_encoding` field is present only for non-UTF-8 text bodies (e.g., Latin-1, Shift-JIS, Windows-1252). When the field value is `"base64"`, unmarshal base64-decodes the `body` string regardless of the `Content-Type`. This marker disambiguates the non-UTF-8 text fallback from a legitimate UTF-8 text body that happens to look like a base64 string.

Hand-authored fixtures for UTF-8 text do not need this field. Omit `body_encoding` and write the body as a plain JSON string:

```json
"body": "Hello, world!"
```

For non-UTF-8 text that you need to fixture by hand, base64-encode the raw bytes and add the marker:

```json
"body": "Y2Fm6SBhdSBsYWl0",
"body_encoding": "base64"
```

Any value other than `"base64"` (including the legacy `"identity"` from v0.11) is ignored for JSON-string bodies.

**Migrating from v0.11:** Fixtures created with v0.11 used base64 encoding for all bodies and included a `body_encoding: "identity"` field. Use the migration tool to convert:

```bash
httptape migrate-fixtures --recursive ./fixtures
```

The migration tool reads each `.json` file, decodes any base64 bodies, removes the legacy `body_encoding` field, and writes the fixture in the new Content-Type-aware format. It is safe to run multiple times (idempotent). Note: the `body_encoding: "base64"` value introduced in v0.14.0 is meaningful and is preserved by the migration tool.

### URL format and matching

The `request.url` field stores a full URL, but the `DefaultMatcher` (used by the Server) only compares the **path** component. This means:

- `http://mock/api/users` and `https://production.example.com/api/users` match the same `GET /api/users` request
- Use `http://mock` as the host for hand-written fixtures -- it is a convention, not a requirement
- Query parameters are ignored by the default matcher. Use `QueryParamsCriterion{}` in a `CompositeMatcher` if you need them.

## Example fixtures

### GET returning JSON (200)

**File:** `fixtures/get-users.json`

```json
{
  "id": "get-users",
  "route": "users-api",
  "recorded_at": "2025-01-15T10:00:00Z",
  "request": {
    "method": "GET",
    "url": "http://mock/api/users",
    "headers": {},
    "body": null,
    "body_hash": ""
  },
  "response": {
    "status_code": 200,
    "headers": {
      "Content-Type": ["application/json"],
      "X-Total-Count": ["42"]
    },
    "body": {
      "users": [
        {"id": 1, "name": "Alice"},
        {"id": 2, "name": "Bob"}
      ]
    }
  }
}
```

The response body is native JSON -- no encoding needed.

### POST returning created resource (201)

**File:** `fixtures/create-user.json`

```json
{
  "id": "create-user",
  "route": "users-api",
  "recorded_at": "2025-01-15T10:00:00Z",
  "request": {
    "method": "POST",
    "url": "http://mock/api/users",
    "headers": {
      "Content-Type": ["application/json"]
    },
    "body": null,
    "body_hash": ""
  },
  "response": {
    "status_code": 201,
    "headers": {
      "Content-Type": ["application/json"],
      "Location": ["/api/users/3"]
    },
    "body": {
      "id": 3,
      "name": "Charlie",
      "created_at": "2025-01-15T10:00:00Z"
    }
  }
}
```

### DELETE returning 204 No Content

**File:** `fixtures/delete-user.json`

```json
{
  "id": "delete-user",
  "route": "users-api",
  "recorded_at": "2025-01-15T10:00:00Z",
  "request": {
    "method": "DELETE",
    "url": "http://mock/api/users/1",
    "headers": {},
    "body": null,
    "body_hash": ""
  },
  "response": {
    "status_code": 204,
    "headers": {},
    "body": null
  }
}
```

### GET with custom headers (paginated response)

**File:** `fixtures/get-users-page2.json`

```json
{
  "id": "get-users-page2",
  "route": "users-api",
  "recorded_at": "2025-01-15T10:00:00Z",
  "request": {
    "method": "GET",
    "url": "http://mock/api/users?page=2&per_page=10",
    "headers": {
      "Accept": ["application/json"],
      "Authorization": ["Bearer [REDACTED]"]
    },
    "body": null,
    "body_hash": ""
  },
  "response": {
    "status_code": 200,
    "headers": {
      "Content-Type": ["application/json"],
      "X-Total-Count": ["42"],
      "X-Page": ["2"],
      "X-Per-Page": ["10"],
      "Link": ["<http://mock/api/users?page=3&per_page=10>; rel=\"next\""]
    },
    "body": {
      "users": [{"id": 11, "name": "Karen"}]
    }
  }
}
```

### GET returning text (text/plain)

**File:** `fixtures/get-health.json`

```json
{
  "id": "get-health",
  "route": "health",
  "request": {
    "method": "GET",
    "url": "http://mock/health",
    "headers": {},
    "body": null,
    "body_hash": ""
  },
  "response": {
    "status_code": 200,
    "headers": {
      "Content-Type": ["text/plain"]
    },
    "body": "OK"
  }
}
```

Text bodies are stored as JSON strings -- no base64 encoding needed.

## Metadata: delay and error simulation

The `metadata` field holds per-fixture configuration that the Server reads at replay time. It is not used for matching.

### Simulating latency

Add a `delay` key with a Go duration string:

```json
{
  "id": "slow-endpoint",
  "request": {
    "method": "GET",
    "url": "http://mock/api/reports",
    "headers": {},
    "body": null,
    "body_hash": ""
  },
  "response": {
    "status_code": 200,
    "headers": {
      "Content-Type": ["application/json"]
    },
    "body": {"status": "complete"}
  },
  "metadata": {
    "delay": "2s"
  }
}
```

Supported duration formats: `100ms`, `1.5s`, `2s`, `500ms`. The server sleeps for the specified duration before writing the response. If the client disconnects during the delay, the server returns immediately.

The per-fixture delay overrides the global `WithDelay` server option.

### Simulating errors

Add an `error` key with a `status` code and optional `body`:

```json
{
  "id": "failing-endpoint",
  "request": {
    "method": "GET",
    "url": "http://mock/api/flaky",
    "headers": {},
    "body": null,
    "body_hash": ""
  },
  "response": {
    "status_code": 200,
    "headers": {
      "Content-Type": ["application/json"]
    },
    "body": {"ok": true}
  },
  "metadata": {
    "error": {
      "status": 503,
      "body": "Service Unavailable"
    }
  }
}
```

When the Server matches this fixture, it returns a `503` with the body `"Service Unavailable"` and sets the header `X-Httptape-Error: simulated`. The `response` section is ignored when `metadata.error` is present.

### Combining delay and error

```json
{
  "metadata": {
    "delay": "3s",
    "error": {
      "status": 504,
      "body": "Gateway Timeout"
    }
  }
}
```

The error check runs before the delay, so in practice the error response is returned immediately (the delay applies only to successful responses).

## FileStore directory structure

`FileStore` stores all fixture files in a single flat directory. Each file is named `<id>.json`:

```
fixtures/
  get-users.json
  create-user.json
  delete-user.json
  get-users-page2.json
  slow-endpoint.json
  failing-endpoint.json
```

Rules:
- The filename is derived from the `id` field: `id + ".json"`
- IDs must not contain path separators (`/`, `\`) or traversal components (`..`)
- Only `.json` files are loaded -- other files are ignored
- There is no subdirectory nesting. Use the `route` field for logical grouping instead.
- The default directory is `fixtures/` relative to the working directory. Override with `WithDirectory`:

```go
store, err := httptape.NewFileStore(httptape.WithDirectory("./testdata/api-fixtures"))
```

## Tips for hand-authored fixtures

**Use descriptive IDs.** The ID is the filename, so `get-users` is easier to find than a UUID. Recorded tapes use UUIDs, but hand-authored ones can use any valid string.

**Keep the `route` consistent.** If you plan to filter fixtures by route (e.g., to run tests against a subset), use the same route string across related fixtures.

**Omit optional fields.** Fields like `body_hash`, `recorded_at`, and `metadata` can be omitted entirely:

```json
{
  "id": "minimal-fixture",
  "request": {
    "method": "GET",
    "url": "http://mock/api/health",
    "headers": {},
    "body": null,
    "body_hash": ""
  },
  "response": {
    "status_code": 200,
    "headers": {
      "Content-Type": ["application/json"]
    },
    "body": {"status": "ok"}
  }
}
```

**Write JSON bodies as native JSON.** Since v0.12, JSON response bodies are written as native JSON objects -- no base64 encoding needed. Just write the JSON inline in the `body` field.

**Validate your fixtures.** Load fixtures with `FileStore` and check for JSON parse errors:

```go
store, err := httptape.NewFileStore(httptape.WithDirectory("./fixtures"))
if err != nil {
    log.Fatal(err)
}
tapes, err := store.List(context.Background(), httptape.Filter{})
if err != nil {
    log.Fatal("fixture load error:", err)
}
fmt.Printf("Loaded %d fixtures\n", len(tapes))
```

## Reference: sanitization config

If your fixtures were recorded with sanitization enabled, the values in headers and body fields will already be redacted or faked. When authoring fixtures by hand, you can use the same redacted placeholders for consistency:

- Redacted header: `"[REDACTED]"`
- Redacted body field: `"[REDACTED]"`
- Faked field: deterministic HMAC-based value (varies by seed)

See [Declarative Configuration](config.md) for the config file format and [Sanitization](sanitization.md) for the programmatic API.

## Exemplar tapes (synthesis mode)

Exemplar tapes are hand-authored fixtures that serve as templates for URL
families. Instead of recording one tape per URL, you author a single exemplar
with a URL pattern and template expressions in the response body.

### Required fields

- `"exemplar": true` at the tape level.
- `"url_pattern"` on the request, using colon-prefixed named segments
  (e.g., `/users/:id`). Mutually exclusive with `"url"`.

### Template expressions in exemplar bodies

JSON response bodies support template expressions at string leaf positions:

```json
{
  "id": "{{pathParam.id | int}}",
  "name": "{{faker.name seed=user-{{pathParam.id}}}}",
  "active": "{{request.query.active | bool}}"
}
```

The `| int`, `| float`, and `| bool` coercion pipes convert the resolved
string to a native JSON type (number or boolean).

### Validation

Exemplar tapes are validated at load time. Common validation errors:

- `exemplar: true` without `url_pattern` -- error.
- `url_pattern` without `exemplar: true` -- error.
- Both `url` and `url_pattern` set -- error.
- SSE exemplar (has `sse_events`) -- error (not supported).

### Example

```json
{
  "id": "products-exemplar",
  "route": "",
  "recorded_at": "2026-01-01T00:00:00Z",
  "exemplar": true,
  "request": {
    "method": "GET",
    "url_pattern": "/products/:category/:id",
    "headers": {},
    "body": null,
    "body_hash": ""
  },
  "response": {
    "status_code": 200,
    "headers": { "Content-Type": ["application/json"] },
    "body": {
      "id": "{{pathParam.id | int}}",
      "category": "{{pathParam.category}}",
      "name": "{{faker.name seed=product-{{pathParam.id}}}}"
    }
  }
}
```

See [Synthesis Mode](synthesis.md) for the full guide.

## See also

- [Storage](storage.md) -- FileStore and MemoryStore details
- [Replay](replay.md) -- how the Server matches and serves fixtures
- [Matching](matching.md) -- customizing request-to-tape matching
- [Synthesis](synthesis.md) -- exemplar tapes and URL pattern matching
- [UI-First Dev](ui-first-dev.md) -- using hand-authored fixtures for frontend development
- [Config](config.md) -- sanitization configuration reference
