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
    "body": "eyJ1c2VycyI6W3siaWQiOjEsIm5hbWUiOiJBbGljZSJ9XX0="
  },
  "metadata": {}
}
```

### Field reference

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `id` | string | Yes | Unique identifier. Used as the filename (`<id>.json`). Must not contain `/`, `\`, or `..`. |
| `route` | string | No | Logical grouping label (e.g., `"users-api"`). Used by `Filter.Route` and `MatchRoute`. |
| `recorded_at` | string (RFC 3339) | No | UTC timestamp. Informational only -- not used for matching. |
| `request.method` | string | Yes | HTTP method (`GET`, `POST`, `PUT`, `DELETE`, `PATCH`, `HEAD`). |
| `request.url` | string | Yes | Full URL. The path component is used for matching (e.g., `http://mock/api/users`). |
| `request.headers` | object | No | Request headers. Each key maps to an array of strings. |
| `request.body` | string/null | No | Base64-encoded request body, or `null` for bodiless requests. |
| `request.body_hash` | string | No | Hex-encoded SHA-256 hash of the original request body. Required for `MatchBodyHash`. |
| `request.body_encoding` | string | No | `"identity"` for UTF-8 text, `"base64"` for binary. Defaults to identity if omitted. |
| `response.status_code` | int | Yes | HTTP status code (200, 201, 204, 404, 500, etc.). |
| `response.headers` | object | No | Response headers. Each key maps to an array of strings. |
| `response.body` | string/null | No | Base64-encoded response body. |
| `response.body_encoding` | string | No | Same as request body encoding. |
| `metadata` | object | No | Key-value pairs for delay/error simulation. Not used for matching. |

### Body encoding

Go's `encoding/json` handles `[]byte` fields as base64 automatically. When authoring by hand:

- **JSON response bodies**: base64-encode the JSON string and set the body field
- **Text responses**: base64-encode the text
- **No body** (e.g., 204): set body to `null`

To base64-encode on the command line:

```bash
echo -n '{"users":[{"id":1,"name":"Alice"}]}' | base64
# eyJ1c2VycyI6W3siaWQiOjEsIm5hbWUiOiJBbGljZSJ9XX0=
```

### URL format and matching

The `request.url` field stores a full URL, but the `DefaultMatcher` (used by the Server) only compares the **path** component. This means:

- `http://mock/api/users` and `https://production.example.com/api/users` match the same `GET /api/users` request
- Use `http://mock` as the host for hand-written fixtures -- it is a convention, not a requirement
- Query parameters are ignored by the default matcher. Use `MatchQueryParams` in a `CompositeMatcher` if you need them.

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
    "body": "eyJ1c2VycyI6W3siaWQiOjEsIm5hbWUiOiJBbGljZSJ9LHsiaWQiOjIsIm5hbWUiOiJCb2IifV19"
  }
}
```

The response body decodes to:

```json
{"users":[{"id":1,"name":"Alice"},{"id":2,"name":"Bob"}]}
```

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
    "body": "eyJpZCI6MywibmFtZSI6IkNoYXJsaWUiLCJjcmVhdGVkX2F0IjoiMjAyNS0wMS0xNVQxMDowMDowMFoifQ=="
  }
}
```

The response body decodes to:

```json
{"id":3,"name":"Charlie","created_at":"2025-01-15T10:00:00Z"}
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
    "body": "eyJ1c2VycyI6W3siaWQiOjExLCJuYW1lIjoiS2FyZW4ifV19"
  }
}
```

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
    "body": "eyJzdGF0dXMiOiJjb21wbGV0ZSJ9"
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
    "body": "eyJvayI6dHJ1ZX0="
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

**Omit optional fields.** Fields like `body_hash`, `body_encoding`, `recorded_at`, and `metadata` can be omitted entirely:

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
    "body": "eyJzdGF0dXMiOiJvayJ9"
  }
}
```

**Base64 helper script.** Create a shell alias for encoding response bodies:

```bash
alias b64='python3 -c "import sys,base64; print(base64.b64encode(sys.stdin.buffer.read()).decode())"'

# Usage:
echo -n '{"status":"ok"}' | b64
# eyJzdGF0dXMiOiJvayJ9
```

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

## See also

- [Storage](storage.md) -- FileStore and MemoryStore details
- [Replay](replay.md) -- how the Server matches and serves fixtures
- [Matching](matching.md) -- customizing request-to-tape matching
- [UI-First Dev](ui-first-dev.md) -- using hand-authored fixtures for frontend development
- [Config](config.md) -- sanitization configuration reference
