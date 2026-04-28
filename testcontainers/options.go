package httptape

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Option configures the httptape container. Use the With* functions to
// create options.
type Option func(*options)

// options holds the resolved configuration for RunContainer.
type options struct {
	image       string
	port        string
	mode        string
	fixturesDir string
	configFile  string
	configJSON  []byte
	configErr   error
	target      string
}

// validate checks option consistency and returns an error if the
// configuration is invalid.
func (o *options) validate() error {
	if o.configErr != nil {
		return fmt.Errorf("WithConfig: failed to marshal config: %w", o.configErr)
	}

	if o.configFile != "" && len(o.configJSON) > 0 {
		return errors.New("WithConfig and WithConfigFile are mutually exclusive")
	}

	if o.mode == ModeRecord && o.target == "" {
		return errors.New("record mode requires WithTarget to specify the upstream URL")
	}

	if o.mode == ModeServe && o.fixturesDir == "" {
		return errors.New("serve mode requires WithFixturesDir to specify the fixtures directory")
	}

	if o.mode != ModeServe && o.mode != ModeRecord {
		return fmt.Errorf("unknown mode %q: must be %q or %q", o.mode, ModeServe, ModeRecord)
	}

	return nil
}

// WithFixturesDir bind-mounts a host directory to /fixtures in the container.
// This is required for serve mode.
func WithFixturesDir(path string) Option {
	return func(o *options) {
		o.fixturesDir = path
	}
}

// WithConfig serialises cfg to JSON and makes it available inside the
// container at /config/config.json. It is mutually exclusive with
// [WithConfigFile].
//
// The cfg value must be JSON-serialisable. In practice, pass an
// httptape.Config from the main module.
func WithConfig(cfg any) Option {
	return func(o *options) {
		data, err := json.Marshal(cfg)
		if err != nil {
			o.configJSON = nil
			o.configErr = err
			return
		}
		o.configJSON = data
	}
}

// WithConfigFile bind-mounts a host JSON config file to
// /config/config.json in the container. It is mutually exclusive with
// [WithConfig].
func WithConfigFile(path string) Option {
	return func(o *options) {
		o.configFile = path
	}
}

// WithPort sets the container's exposed port. The value should include the
// protocol, e.g. "9090/tcp". Defaults to "8081/tcp".
func WithPort(port string) Option {
	return func(o *options) {
		o.port = port
	}
}

// WithImage overrides the Docker image reference. Defaults to
// "ghcr.io/httptape/httptape:latest".
func WithImage(image string) Option {
	return func(o *options) {
		o.image = image
	}
}

// WithMode sets the CLI subcommand: "serve" (default) or "record".
func WithMode(mode string) Option {
	return func(o *options) {
		o.mode = mode
	}
}

// WithTarget sets the upstream URL for record mode (maps to the --upstream
// CLI flag). This option is required when mode is "record".
func WithTarget(url string) Option {
	return func(o *options) {
		o.target = url
	}
}
