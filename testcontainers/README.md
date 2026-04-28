# httptape/testcontainers

A [Testcontainers for Go](https://golang.testcontainers.org/) module that
spins up an **httptape** Docker container directly from `go test`.

## Quick start

```go
import (
    "context"
    "net/http"
    "testing"

    httptape "github.com/httptape/httptape/testcontainers"
)

func TestAPI(t *testing.T) {
    ctx := context.Background()
    ctr, err := httptape.RunContainer(ctx,
        httptape.WithFixturesDir("./testdata/fixtures"),
    )
    if err != nil {
        t.Fatal(err)
    }
    defer ctr.Terminate(ctx)

    resp, err := http.Get(ctr.BaseURL() + "/api/users")
    if err != nil {
        t.Fatal(err)
    }
    defer resp.Body.Close()
    // assert on resp...
}
```

## Options

| Function            | Description                                               |
|---------------------|-----------------------------------------------------------|
| `WithFixturesDir`   | Bind-mount a host directory to `/fixtures` in the container. Required for serve mode. |
| `WithConfigFile`    | Bind-mount a host JSON config file into the container.    |
| `WithConfig`        | Serialize a Go value to JSON and mount it as config.      |
| `WithMode`          | Set the mode: `"serve"` (default) or `"record"`.         |
| `WithTarget`        | Set the upstream URL for record mode.                     |
| `WithPort`          | Override the exposed port (default `"8081/tcp"`).         |
| `WithImage`         | Override the Docker image (default `ghcr.io/httptape/httptape:latest`). |

`WithConfig` and `WithConfigFile` are mutually exclusive.

## Modes

- **serve** (default) -- replays previously recorded fixtures.
- **record** -- proxies requests to an upstream and records exchanges. Requires `WithTarget`.

## Running integration tests

Integration tests that start a Docker container use the `dockertest` build tag:

```bash
go test -tags dockertest ./testcontainers/...
```

Unit tests (validation, command building, mount building) run without Docker:

```bash
go test ./testcontainers/...
```

## Separate module

This package lives in its own Go module (`testcontainers/go.mod`) so the
`testcontainers-go` dependency does not affect the main httptape module's
zero-dependency guarantee.
