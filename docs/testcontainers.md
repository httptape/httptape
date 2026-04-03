# Testcontainers

httptape provides a Go [Testcontainers](https://golang.testcontainers.org/) module for running httptape in Docker containers during integration tests. This is useful when you want an isolated mock server without managing container lifecycle manually.

## Install

```bash
go get github.com/VibeWarden/httptape/testcontainers
```

This module has external dependencies (testcontainers-go, Docker client libraries) unlike the core httptape library which is stdlib-only.

## Import

```go
import httptapecontainer "github.com/VibeWarden/httptape/testcontainers"
```

The package name is `httptape` (under the `testcontainers` directory), so most users alias it to avoid collision with the main `httptape` package.

## Serve mode

Start a container that replays recorded fixtures:

```go
func TestWithContainer(t *testing.T) {
    ctx := context.Background()

    container, err := httptapecontainer.RunContainer(ctx,
        httptapecontainer.WithFixturesDir("./testdata/fixtures"),
    )
    if err != nil {
        t.Fatal(err)
    }
    defer container.Terminate(ctx)

    // Use the container's URL
    baseURL := container.BaseURL() // e.g., "http://localhost:32789"

    resp, err := http.Get(baseURL + "/users/octocat")
    if err != nil {
        t.Fatal(err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != 200 {
        t.Errorf("expected 200, got %d", resp.StatusCode)
    }
}
```

## Record mode

Start a container that proxies to an upstream and records:

```go
container, err := httptapecontainer.RunContainer(ctx,
    httptapecontainer.WithMode(httptapecontainer.ModeRecord),
    httptapecontainer.WithTarget("https://api.example.com"),
    httptapecontainer.WithFixturesDir("./testdata/fixtures"),
)
```

## Options

### WithFixturesDir

```go
httptapecontainer.WithFixturesDir("./testdata/fixtures")
```

Bind-mounts a host directory to `/fixtures` in the container. Required for serve mode.

### WithMode

```go
httptapecontainer.WithMode(httptapecontainer.ModeServe)   // default
httptapecontainer.WithMode(httptapecontainer.ModeRecord)
```

Sets the CLI subcommand. `ModeServe` replays fixtures; `ModeRecord` proxies to an upstream and records.

### WithTarget

```go
httptapecontainer.WithTarget("https://api.example.com")
```

Sets the upstream URL for record mode (maps to `--upstream`). Required when mode is `ModeRecord`.

### WithConfig

```go
cfg := httptape.Config{
    Version: "1",
    Rules: []httptape.Rule{
        {Action: "redact_headers"},
    },
}
httptapecontainer.WithConfig(cfg)
```

Serializes a config struct to JSON and makes it available inside the container at `/config/config.json`. The config value must be JSON-serializable. Mutually exclusive with `WithConfigFile`.

### WithConfigFile

```go
httptapecontainer.WithConfigFile("./sanitize.json")
```

Bind-mounts a host JSON config file to `/config/config.json`. Mutually exclusive with `WithConfig`.

### WithImage

```go
httptapecontainer.WithImage("ghcr.io/vibewarden/httptape:v1.0.0")
```

Overrides the Docker image. Defaults to `ghcr.io/vibewarden/httptape:latest`.

### WithPort

```go
httptapecontainer.WithPort("9090/tcp")
```

Sets the container's exposed port. Defaults to `8081/tcp`.

## Container methods

### BaseURL

```go
url := container.BaseURL() // "http://localhost:32789"
```

Returns the mapped HTTP base URL. Resolved once during startup and cached.

### Endpoint

```go
endpoint, err := container.Endpoint(ctx) // "localhost:32789"
```

Returns the `host:port` string for the mapped container port.

### Terminate

```go
container.Terminate(ctx)
```

Stops and removes the container. Always call this in cleanup (typically via `defer`).

## Validation

`RunContainer` validates options before starting the container:

- `WithConfig` and `WithConfigFile` are mutually exclusive
- Record mode requires `WithTarget`
- Serve mode requires `WithFixturesDir`
- Mode must be `"serve"` or `"record"`

Invalid options return an error without starting a container.

## Full example with sanitization

```go
func TestRecordWithSanitization(t *testing.T) {
    ctx := context.Background()

    container, err := httptapecontainer.RunContainer(ctx,
        httptapecontainer.WithMode(httptapecontainer.ModeRecord),
        httptapecontainer.WithTarget("https://api.example.com"),
        httptapecontainer.WithFixturesDir("./testdata/fixtures"),
        httptapecontainer.WithConfig(httptape.Config{
            Version: "1",
            Rules: []httptape.Rule{
                {Action: "redact_headers"},
                {Action: "fake", Seed: "test-seed", Paths: []string{"$.user.email"}},
            },
        }),
    )
    if err != nil {
        t.Fatal(err)
    }
    defer container.Terminate(ctx)

    // Requests through the container are proxied, recorded, and sanitized
    client := &http.Client{}
    resp, err := client.Get(container.BaseURL() + "/users/42")
    if err != nil {
        t.Fatal(err)
    }
    defer resp.Body.Close()

    // Fixtures in ./testdata/fixtures/ now contain sanitized recordings
}
```

## See also

- [Docker](docker.md) -- manual Docker usage and compose files
- [Config](config.md) -- sanitization config format
- [CLI](cli.md) -- the commands that run inside the container
