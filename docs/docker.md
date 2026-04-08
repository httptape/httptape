# Docker

httptape is available as a minimal Docker image built from scratch (no OS, no shell). The image contains only the httptape binary and CA certificates.

## Pull

```bash
docker pull ghcr.io/vibewarden/httptape:latest
```

## Serve mode

Replay recorded fixtures:

```bash
docker run --rm \
  -v ./fixtures:/fixtures:ro \
  -p 8081:8081 \
  ghcr.io/vibewarden/httptape:latest \
  serve --fixtures /fixtures --port 8081
```

The `--fixtures` flag inside the container always points to `/fixtures` (the mount target).

## Record mode

Proxy and record traffic from an upstream (with redaction):

```bash
docker run --rm \
  -v ./fixtures:/fixtures \
  -v ./redact.json:/config/config.json:ro \
  -p 8081:8081 \
  ghcr.io/vibewarden/httptape:latest \
  record --upstream https://api.example.com \
         --fixtures /fixtures \
         --config /config/config.json \
         --port 8081
```

Note: the fixtures volume is mounted read-write (no `:ro`) so the recorder can write fixture files.

## Proxy mode

Forward to upstream with automatic fallback to cached responses:

```bash
docker run --rm \
  -v ./cache:/fixtures \
  -v ./redact.json:/config/config.json:ro \
  -p 8081:8081 \
  ghcr.io/vibewarden/httptape:latest \
  proxy --upstream https://api.example.com \
        --fixtures /fixtures \
        --config /config/config.json \
        --port 8081 \
        --cors
```

The `--fixtures` volume stores the L2 (persistent, redacted) cache. The L1 (in-memory) cache is managed internally and is lost when the container stops. Add `--fallback-on-5xx` to also fall back on upstream 5xx responses.

See [Proxy Mode](proxy.md) for a full guide.

## Volumes

| Mount point | Purpose |
|-------------|---------|
| `/fixtures` | Fixture directory (read-write for record/proxy, read-only for serve) |
| `/config` | Configuration directory (read-only) |

Both are declared as `VOLUME` in the Dockerfile and pre-exist in the image.

## Image details

- **Base:** `scratch` (no OS, no shell, no package manager)
- **Size:** ~10 MB (static Go binary + CA certs)
- **User:** `65534` (nobody) -- runs as non-root
- **Exposed port:** `8081`
- **Entrypoint:** `httptape`

## Docker Compose

### Serve mode

```yaml
services:
  mock-api:
    image: ghcr.io/vibewarden/httptape:latest
    command: ["serve", "--fixtures", "/fixtures", "--port", "8081"]
    ports:
      - "8081:8081"
    volumes:
      - ./fixtures:/fixtures:ro

  my-app:
    build: .
    environment:
      API_URL: http://mock-api:8081
    depends_on:
      - mock-api
```

### Record mode

```yaml
services:
  recorder:
    image: ghcr.io/vibewarden/httptape:latest
    command:
      - record
      - --upstream
      - https://api.example.com
      - --fixtures
      - /fixtures
      - --config
      - /config/config.json
      - --port
      - "8081"
    ports:
      - "8081:8081"
    volumes:
      - ./fixtures:/fixtures
      - ./redact.json:/config/config.json:ro
```

### Proxy mode (frontend dev with fallback)

```yaml
services:
  api-proxy:
    image: ghcr.io/vibewarden/httptape:latest
    command:
      - proxy
      - --upstream
      - https://api.staging.example.com
      - --fixtures
      - /fixtures
      - --config
      - /config/config.json
      - --port
      - "3001"
      - --cors
    ports:
      - "3001:3001"
    volumes:
      - ./cache:/fixtures
      - ./redact.json:/config/config.json:ro

  frontend:
    build:
      context: ./frontend
    ports:
      - "3000:3000"
    environment:
      VITE_API_URL: http://localhost:3001
    depends_on:
      - api-proxy
```

### Record then export

```yaml
services:
  recorder:
    image: ghcr.io/vibewarden/httptape:latest
    command: ["record", "--upstream", "https://api.example.com", "--fixtures", "/fixtures", "--port", "8081"]
    volumes:
      - fixture-data:/fixtures

  exporter:
    image: ghcr.io/vibewarden/httptape:latest
    command: ["export", "--fixtures", "/fixtures", "--output", "/output/bundle.tar.gz"]
    volumes:
      - fixture-data:/fixtures:ro
      - ./output:/output
    depends_on:
      recorder:
        condition: service_completed_successfully

volumes:
  fixture-data:
```

## CI example

Use httptape as a service container in GitHub Actions:

```yaml
jobs:
  test:
    runs-on: ubuntu-latest
    services:
      mock-api:
        image: ghcr.io/vibewarden/httptape:latest
        options: >-
          -v ${{ github.workspace }}/fixtures:/fixtures:ro
        ports:
          - 8081:8081
        # Serve mode is implied by the entrypoint + command
        env: {}
    steps:
      - uses: actions/checkout@v4
      - run: go test ./... -count=1
        env:
          API_URL: http://localhost:8081
```

## Building locally

```bash
docker build -t httptape:local .
```

The Dockerfile uses a multi-stage build:
1. **Builder stage:** Compiles the Go binary with `CGO_ENABLED=0` for a static binary
2. **Final stage:** Copies the binary into a `scratch` image with CA certificates

## See also

- [CLI](cli.md) -- all commands and flags
- [Proxy Mode](proxy.md) -- proxy mode with L1/L2 caching
- [Config](config.md) -- redaction config file format
- [Testcontainers](testcontainers.md) -- programmatic Docker usage in Go tests
