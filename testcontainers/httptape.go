package httptape

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/docker/go-connections/nat"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	// DefaultImage is the default Docker image used for the httptape container.
	DefaultImage = "ghcr.io/httptape/httptape:latest"

	// DefaultPort is the default exposed port inside the container.
	DefaultPort = "8081/tcp"

	// ModeServe is the serve mode, which replays recorded fixtures.
	ModeServe = "serve"

	// ModeRecord is the record mode, which proxies to an upstream and records.
	ModeRecord = "record"
)

// Container wraps a running httptape Docker container. It embeds
// [testcontainers.Container] for advanced use cases and provides
// convenience methods for common operations.
type Container struct {
	testcontainers.Container
	port    string
	baseURL string
}

// BaseURL returns the mapped HTTP base URL for the container
// (e.g. "http://localhost:32789"). The URL is resolved once during
// container startup and cached for the lifetime of the Container.
func (c *Container) BaseURL() string {
	return c.baseURL
}

// Endpoint returns the host:port string for the mapped container port.
func (c *Container) Endpoint(ctx context.Context) (string, error) {
	return c.Container.Endpoint(ctx, c.port)
}

// RunContainer starts an httptape Docker container with the given options
// and returns a handle. The caller must call [Container.Terminate] to stop
// and remove the container when done.
//
// RunContainer validates option consistency before starting the container:
//   - [WithConfig] and [WithConfigFile] are mutually exclusive.
//   - [ModeRecord] requires [WithTarget] to specify the upstream URL.
//   - [ModeServe] requires [WithFixturesDir] to specify the fixtures directory.
func RunContainer(ctx context.Context, opts ...Option) (*Container, error) {
	o := options{
		image: DefaultImage,
		port:  DefaultPort,
		mode:  ModeServe,
	}
	for _, opt := range opts {
		opt(&o)
	}

	if err := o.validate(); err != nil {
		return nil, fmt.Errorf("httptape: invalid options: %w", err)
	}

	cmd := buildCmd(o)
	mounts, err := buildMounts(o)
	if err != nil {
		return nil, fmt.Errorf("httptape: failed to prepare mounts: %w", err)
	}

	req := testcontainers.ContainerRequest{
		Image:        o.image,
		Cmd:          cmd,
		ExposedPorts: []string{o.port},
		Mounts:       mounts,
		WaitingFor:   wait.ForListeningPort(nat.Port(o.port)),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, fmt.Errorf("httptape: failed to start container: %w", err)
	}

	endpoint, err := container.Endpoint(ctx, o.port)
	if err != nil {
		// Best effort: terminate the container if we can't resolve the endpoint.
		_ = container.Terminate(ctx)
		return nil, fmt.Errorf("httptape: failed to resolve endpoint: %w", err)
	}

	return &Container{
		Container: container,
		port:      o.port,
		baseURL:   "http://" + endpoint,
	}, nil
}

// buildCmd constructs the CLI command for the container based on the options.
func buildCmd(o options) []string {
	// Extract the port number from the port spec (e.g. "8081/tcp" -> "8081").
	portNum := extractPort(o.port)

	cmd := []string{o.mode}

	cmd = append(cmd, "--port", portNum)

	if o.fixturesDir != "" {
		cmd = append(cmd, "--fixtures", "/fixtures")
	}

	if o.target != "" {
		cmd = append(cmd, "--upstream", o.target)
	}

	if o.configFile != "" || len(o.configJSON) > 0 {
		cmd = append(cmd, "--config", "/config/config.json")
	}

	return cmd
}

// buildMounts creates the bind mounts for fixtures and config.
// When configJSON is set, it writes the JSON to a host temp file and
// bind-mounts it into the container at /config/config.json.
func buildMounts(o options) (testcontainers.ContainerMounts, error) {
	var mounts testcontainers.ContainerMounts

	if o.fixturesDir != "" {
		mounts = append(mounts, testcontainers.ContainerMount{
			Source: testcontainers.GenericBindMountSource{HostPath: o.fixturesDir},
			Target: "/fixtures",
		})
	}

	if o.configFile != "" {
		mounts = append(mounts, testcontainers.ContainerMount{
			Source: testcontainers.GenericBindMountSource{HostPath: o.configFile},
			Target: "/config/config.json",
		})
	}

	if len(o.configJSON) > 0 {
		tmpDir, err := os.MkdirTemp("", "httptape-config-*")
		if err != nil {
			return nil, fmt.Errorf("create temp dir for config: %w", err)
		}
		tmpFile := filepath.Join(tmpDir, "config.json")
		if err := os.WriteFile(tmpFile, o.configJSON, 0o644); err != nil {
			return nil, fmt.Errorf("write config to temp file: %w", err)
		}
		mounts = append(mounts, testcontainers.ContainerMount{
			Source: testcontainers.GenericBindMountSource{HostPath: tmpFile},
			Target: "/config/config.json",
		})
	}

	return mounts, nil
}

// extractPort returns the numeric port from a port spec like "8081/tcp".
func extractPort(port string) string {
	for i, c := range port {
		if c == '/' {
			return port[:i]
		}
	}
	return port
}
