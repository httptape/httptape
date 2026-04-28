// Package httptape provides a Testcontainers module for running an httptape
// Docker container in integration tests.
//
// This module wraps the ghcr.io/httptape/httptape Docker image and exposes
// a functional-options API consistent with the main httptape library. It
// enables Go developers to spin up an isolated httptape container directly
// from go test, without manual Docker orchestration.
//
// # Quick start
//
//	ctx := context.Background()
//	ctr, err := httptape.RunContainer(ctx,
//	    httptape.WithFixturesDir("./testdata/fixtures"),
//	)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer ctr.Terminate(ctx)
//
//	// Use ctr.BaseURL() as the HTTP base for your tests.
//	resp, err := http.Get(ctr.BaseURL() + "/api/users")
//
// # Modes
//
// The container supports two modes via [WithMode]:
//   - "serve" (default): replays previously recorded fixtures.
//   - "record": proxies requests to an upstream target and records them.
//     Record mode requires [WithTarget] to specify the upstream URL.
//
// # Build tags
//
// Integration tests in this package use the "dockertest" build tag. Run them
// with:
//
//	go test -tags dockertest ./testcontainers/...
//
// This module lives in a separate Go module (testcontainers/go.mod) so that
// its dependency on testcontainers-go does not affect the main httptape
// module's zero-dependency guarantee.
package httptape
