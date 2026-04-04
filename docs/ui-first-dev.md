# UI-First Development Guide

Use httptape as a mock backend for frontend development. Build your UI against fixture data without waiting for the real API to be ready, stable, or accessible.

## Why use httptape for frontend dev

- **No backend dependency.** Start building UI before the API exists (contract-first).
- **Deterministic data.** Same fixtures, same responses, every time.
- **Offline development.** No network access needed once fixtures are written.
- **Edge case testing.** Simulate errors, slow responses, and empty states with fixture metadata.
- **Team sharing.** Commit fixtures to version control. Every developer gets the same mock data.

## Workflow overview

There are two paths to get fixtures:

### Path A: Write fixtures by hand

Best when the API does not exist yet or you want full control over the mock data.

1. Define the API contract (endpoints, request/response shapes)
2. Author fixture JSON files -- see [Fixture Authoring Guide](fixtures-authoring.md)
3. Start httptape in serve mode
4. Point your frontend at httptape

### Path B: Record from a real or staging API

Best when the API already exists and you want realistic data.

1. Start httptape in record mode, pointing at the upstream API
2. Exercise the API endpoints your frontend needs (manually or via a script)
3. Stop the recorder -- fixtures are now on disk
4. Start httptape in serve mode
5. Point your frontend at httptape

Record mode with the CLI:

```bash
# Record from staging
httptape record \
  --upstream https://staging-api.example.com \
  --fixtures ./fixtures \
  --config sanitize.json \
  --port 3001

# Exercise your endpoints
curl http://localhost:3001/api/users
curl http://localhost:3001/api/users/1
curl -X POST http://localhost:3001/api/users -d '{"name":"Alice"}'

# Stop with Ctrl+C, then serve the recorded fixtures
httptape serve --fixtures ./fixtures --port 3001
```

## Docker Compose: frontend + httptape

A typical setup with a React/Vue/Svelte dev server calling httptape as the API backend:

```yaml
# docker-compose.yml
services:
  mock-api:
    image: ghcr.io/vibewarden/httptape:latest
    command: ["serve", "--fixtures", "/fixtures", "--port", "3001", "--cors"]
    ports:
      - "3001:3001"
    volumes:
      - ./fixtures:/fixtures:ro

  frontend:
    build:
      context: ./frontend
      dockerfile: Dockerfile.dev
    ports:
      - "3000:3000"
    environment:
      VITE_API_URL: http://localhost:3001
    depends_on:
      - mock-api
```

Start everything with:

```bash
docker compose up
```

Your frontend at `http://localhost:3000` calls the mock API at `http://localhost:3001`. Fixtures are read from the `./fixtures` directory on the host.

### Without Docker

Run httptape directly:

```bash
httptape serve --fixtures ./fixtures --port 3001 --cors
```

In another terminal, start your frontend dev server:

```bash
cd frontend && npm run dev
# Vite/Next/CRA dev server on localhost:3000
```

Configure your frontend to use `http://localhost:3001` as the API base URL.

## CORS setup

When the frontend dev server (e.g., `localhost:3000`) calls the mock backend (e.g., `localhost:3001`), the browser enforces the same-origin policy. httptape has built-in CORS support.

### CLI flag

```bash
httptape serve --fixtures ./fixtures --port 3001 --cors
```

### Go API

```go
srv := httptape.NewServer(store, httptape.WithCORS())
```

When CORS is enabled, the server:

- Adds `Access-Control-Allow-Origin: *` to all responses
- Adds `Access-Control-Allow-Methods: GET, POST, PUT, DELETE, PATCH, OPTIONS, HEAD`
- Adds `Access-Control-Allow-Headers: Content-Type, Authorization, X-Requested-With, Accept`
- Adds `Access-Control-Max-Age: 86400` (24 hours)
- Handles `OPTIONS` preflight requests automatically with `204 No Content`

This is a permissive CORS configuration intended for local development only.

## Hot-reload: edit fixtures, see changes immediately

`FileStore` reads fixture files from disk on every request. There is no in-memory cache. This means:

- Edit a fixture JSON file, save it, and the next request picks up the change
- Add a new fixture file to the directory, and it is immediately available
- Delete a fixture file, and the next request to that endpoint returns the fallback (404 by default)

No server restart is needed. This makes the edit-refresh cycle fast:

1. Frontend shows wrong data or you need a new endpoint
2. Edit or create a fixture file in `./fixtures/`
3. Refresh the browser
4. New response is served

### Example: adding an endpoint on the fly

Your frontend needs `GET /api/notifications`, which does not have a fixture yet. Create the file:

**File:** `fixtures/get-notifications.json`

```json
{
  "id": "get-notifications",
  "request": {
    "method": "GET",
    "url": "http://mock/api/notifications",
    "headers": {},
    "body": null,
    "body_hash": ""
  },
  "response": {
    "status_code": 200,
    "headers": {
      "Content-Type": ["application/json"]
    },
    "body": "W3siaWQiOjEsIm1lc3NhZ2UiOiJXZWxjb21lISIsInJlYWQiOmZhbHNlfV0="
  }
}
```

The body decodes to:

```json
[{"id":1,"message":"Welcome!","read":false}]
```

Save the file. Refresh the frontend. The notification badge appears.

## Latency simulation for loading states

Frontend loading states (spinners, skeletons, progress bars) are hard to test against a local mock that responds instantly. Use the `metadata.delay` field to slow specific endpoints:

```json
{
  "id": "slow-dashboard",
  "request": {
    "method": "GET",
    "url": "http://mock/api/dashboard",
    "headers": {},
    "body": null,
    "body_hash": ""
  },
  "response": {
    "status_code": 200,
    "headers": {
      "Content-Type": ["application/json"]
    },
    "body": "eyJ3aWRnZXRzIjpbXX0="
  },
  "metadata": {
    "delay": "2s"
  }
}
```

The server waits 2 seconds before sending the response. Your frontend loading spinner is now visible during development.

### Global delay

Apply a delay to every endpoint via the Go API:

```go
srv := httptape.NewServer(store,
    httptape.WithCORS(),
    httptape.WithDelay(500 * time.Millisecond),
)
```

Per-fixture delays in `metadata.delay` override the global delay for that specific endpoint.

### Supported duration values

| Value | Duration |
|-------|----------|
| `"100ms"` | 100 milliseconds |
| `"500ms"` | Half a second |
| `"1s"` | One second |
| `"1.5s"` | 1.5 seconds |
| `"2s"` | Two seconds |
| `"5s"` | Five seconds |

## Error simulation for error handling in UI

Test your error boundaries, retry logic, toast notifications, and fallback UI by simulating server errors.

### Per-fixture errors

Return a specific error for a specific endpoint:

```json
{
  "id": "payment-failure",
  "request": {
    "method": "POST",
    "url": "http://mock/api/payments",
    "headers": {},
    "body": null,
    "body_hash": ""
  },
  "response": {
    "status_code": 201,
    "headers": {
      "Content-Type": ["application/json"]
    },
    "body": "eyJpZCI6MX0="
  },
  "metadata": {
    "error": {
      "status": 422,
      "body": "{\"errors\":{\"card_number\":[\"is invalid\"]}}"
    }
  }
}
```

The server returns 422 with the validation error body. The `response` section is ignored when `metadata.error` is present. To switch back to the success response, remove the `metadata.error` block and save the file.

### Random error rate

Simulate flaky APIs where some percentage of requests fail with 500:

```go
srv := httptape.NewServer(store,
    httptape.WithCORS(),
    httptape.WithErrorRate(0.3), // 30% of requests return 500
)
```

Failed responses include the header `X-Httptape-Error: simulated` so you can distinguish simulated errors from real ones in your frontend code or browser dev tools.

### Common error scenarios for frontend testing

| Scenario | Fixture setup |
|----------|--------------|
| Validation error (422) | `metadata.error.status: 422`, body with field errors |
| Unauthorized (401) | `metadata.error.status: 401`, body with auth error message |
| Not found (404) | `metadata.error.status: 404` |
| Rate limited (429) | `metadata.error.status: 429`, body with retry-after info |
| Server error (500) | `metadata.error.status: 500` |
| Service unavailable (503) | `metadata.error.status: 503` |
| Gateway timeout (504) | `metadata.error.status: 504` combined with `metadata.delay: "30s"` |

### Toggle between success and error

A practical workflow for testing both happy and error paths:

1. Start with the fixture returning the success response (no `metadata.error`)
2. Test the happy path in the UI
3. Add `metadata.error` to the fixture file and save
4. Refresh the browser -- error UI is now shown
5. Remove `metadata.error` and save to go back to the happy path

No server restart needed. Hot-reload picks up changes immediately.

## Full example: e-commerce frontend

A complete fixture set for a simple e-commerce frontend:

```
fixtures/
  get-products.json          # GET /api/products -> 200, product list
  get-product-detail.json    # GET /api/products/1 -> 200, single product
  get-cart.json              # GET /api/cart -> 200, cart contents
  add-to-cart.json           # POST /api/cart/items -> 201, added item
  checkout.json              # POST /api/checkout -> 201, order confirmation
  checkout-error.json        # POST /api/checkout -> 422, validation error (via metadata.error)
  slow-search.json           # GET /api/search -> 200, delayed 1.5s (via metadata.delay)
```

Note: since the default matcher uses method + path, having both `checkout.json` and `checkout-error.json` match the same `POST /api/checkout` means the first file loaded wins. To toggle between them, use a single fixture and swap the `metadata.error` block in and out, or delete one file and keep the other.

Docker Compose for this setup:

```yaml
services:
  mock-api:
    image: ghcr.io/vibewarden/httptape:latest
    command: ["serve", "--fixtures", "/fixtures", "--port", "3001", "--cors"]
    ports:
      - "3001:3001"
    volumes:
      - ./fixtures:/fixtures:ro

  frontend:
    image: node:20-alpine
    working_dir: /app
    command: ["npm", "run", "dev"]
    ports:
      - "3000:3000"
    volumes:
      - ./frontend:/app
    environment:
      VITE_API_URL: http://localhost:3001
    depends_on:
      - mock-api
```

## Comparison with alternatives

### json-server

[json-server](https://github.com/typicode/json-server) generates REST endpoints from a single JSON file with a database-like structure.

| Aspect | httptape | json-server |
|--------|----------|-------------|
| Data model | One JSON file per endpoint (request + response pair) | Single `db.json` file with resource collections |
| CRUD support | Read-only replay (records exact responses) | Full CRUD with auto-generated routes |
| Response fidelity | Exact headers, status codes, body from fixtures | Generated responses with limited header control |
| Recording | Built-in -- record from a real API | None -- hand-author only |
| Sanitization | Built-in redaction and deterministic faking | None |
| Error simulation | Per-fixture `metadata.error` and global `WithErrorRate` | Custom middleware required |
| Latency simulation | Per-fixture `metadata.delay` and global `WithDelay` | `--delay` flag (global only) |
| Language | Go (single binary, Docker image) | Node.js |

**Choose httptape when** you need exact replay of recorded API interactions, per-endpoint error/delay simulation, or sanitized fixtures safe for version control.

**Choose json-server when** you need full CRUD semantics with auto-generated routes and a database-like data model.

### Mockoon

[Mockoon](https://mockoon.com/) is a desktop application and CLI for creating mock APIs with a GUI.

| Aspect | httptape | Mockoon |
|--------|----------|---------|
| Configuration | JSON fixture files (text editor) | GUI application or JSON environment files |
| Version control | Individual fixture files, easy diffs | Single large environment JSON file |
| Recording | Built-in proxy recording with sanitization | Built-in proxy recording |
| Error simulation | `metadata.error` per fixture, `WithErrorRate` global | Per-route rules in GUI |
| Latency simulation | `metadata.delay` per fixture, `WithDelay` global | Per-route latency in GUI |
| Go integration | Native -- embeddable in Go tests | None (separate process) |
| Docker | Minimal scratch image (~10 MB) | Node.js-based image |
| CORS | `--cors` flag | Built-in toggle |

**Choose httptape when** you want text-file-based fixtures, Git-friendly diffs, Go test integration, or sanitization.

**Choose Mockoon when** you prefer a GUI for designing mock APIs or need features like response templating and dynamic generation.

## See also

- [Fixture Authoring Guide](fixtures-authoring.md) -- JSON fixture format and field reference
- [CLI](cli.md) -- `serve` and `record` commands
- [Docker](docker.md) -- container setup and Docker Compose examples
- [Replay](replay.md) -- how the Server matches requests to fixtures
- [Matching](matching.md) -- customizing request-to-tape matching
