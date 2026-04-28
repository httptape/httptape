//go:build dockertest

package httptape_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	httptape "github.com/httptape/httptape/testcontainers"
)

func TestRunContainer_ServeMode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ctr, err := httptape.RunContainer(ctx,
		httptape.WithFixturesDir("./testdata/fixtures"),
	)
	if err != nil {
		t.Fatalf("RunContainer: %v", err)
	}
	defer func() {
		if err := ctr.Terminate(ctx); err != nil {
			t.Errorf("Terminate: %v", err)
		}
	}()

	baseURL := ctr.BaseURL()
	if baseURL == "" {
		t.Fatal("BaseURL returned empty string")
	}

	resp, err := http.Get(baseURL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	resp.Body.Close()

	// The server should respond (any status is fine — we just verify it's reachable).
	t.Logf("GET / returned status %d at %s", resp.StatusCode, baseURL)
}

func TestRunContainer_Endpoint(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ctr, err := httptape.RunContainer(ctx,
		httptape.WithFixturesDir("./testdata/fixtures"),
	)
	if err != nil {
		t.Fatalf("RunContainer: %v", err)
	}
	defer func() {
		if err := ctr.Terminate(ctx); err != nil {
			t.Errorf("Terminate: %v", err)
		}
	}()

	endpoint, err := ctr.Endpoint(ctx)
	if err != nil {
		t.Fatalf("Endpoint: %v", err)
	}
	if endpoint == "" {
		t.Fatal("Endpoint returned empty string")
	}
	t.Logf("Endpoint: %s", endpoint)
}

func TestRunContainer_WithConfigFile(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ctr, err := httptape.RunContainer(ctx,
		httptape.WithFixturesDir("./testdata/fixtures"),
		httptape.WithConfigFile("./testdata/config.json"),
	)
	if err != nil {
		t.Fatalf("RunContainer: %v", err)
	}
	defer func() {
		if err := ctr.Terminate(ctx); err != nil {
			t.Errorf("Terminate: %v", err)
		}
	}()

	t.Logf("Container started with config at %s", ctr.BaseURL())
}

func TestRunContainer_WithImage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ctr, err := httptape.RunContainer(ctx,
		httptape.WithFixturesDir("./testdata/fixtures"),
		httptape.WithImage("ghcr.io/httptape/httptape:latest"),
	)
	if err != nil {
		t.Fatalf("RunContainer: %v", err)
	}
	defer func() {
		if err := ctr.Terminate(ctx); err != nil {
			t.Errorf("Terminate: %v", err)
		}
	}()

	t.Logf("Container started with custom image at %s", ctr.BaseURL())
}
