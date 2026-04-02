package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/VibeWarden/httptape"
)

func TestSubcommandDispatch(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{name: "serve recognized", args: []string{"serve", "--fixtures", t.TempDir()}, want: exitOK},
		{name: "record recognized missing upstream", args: []string{"record", "--fixtures", t.TempDir()}, want: exitUsage},
		{name: "export recognized", args: []string{"export", "--fixtures", t.TempDir()}, want: exitOK},
		{name: "import recognized no input", args: []string{"import", "--fixtures", t.TempDir()}, want: exitRuntime},
		{name: "unknown command", args: []string{"bogus"}, want: exitUsage},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// serve will start a server; skip actual run for serve
			if tt.args[0] == "serve" {
				// Just verify it doesn't return exitUsage for a valid invocation
				// by checking with -h instead.
				got := run([]string{"serve", "-h"})
				if got != exitOK {
					t.Errorf("serve -h: got exit %d, want %d", got, exitOK)
				}
				return
			}
			got := run(tt.args)
			if got != tt.want {
				t.Errorf("run(%v) = %d, want %d", tt.args, got, tt.want)
			}
		})
	}
}

func TestMissingRequiredFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{name: "serve without --fixtures", args: []string{"serve"}, want: exitUsage},
		{name: "record without --upstream", args: []string{"record", "--fixtures", t.TempDir()}, want: exitUsage},
		{name: "record without --fixtures", args: []string{"record", "--upstream", "http://example.com"}, want: exitUsage},
		{name: "record without both", args: []string{"record"}, want: exitUsage},
		{name: "export without --fixtures", args: []string{"export"}, want: exitUsage},
		{name: "import without --fixtures", args: []string{"import"}, want: exitUsage},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := run(tt.args)
			if got != tt.want {
				t.Errorf("run(%v) = %d, want %d", tt.args, got, tt.want)
			}
		})
	}
}

func TestHelpExitsZero(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "no args", args: nil},
		{name: "-h flag", args: []string{"-h"}},
		{name: "--help flag", args: []string{"--help"}},
		{name: "help command", args: []string{"help"}},
		{name: "serve -h", args: []string{"serve", "-h"}},
		{name: "record -h", args: []string{"record", "-h"}},
		{name: "export -h", args: []string{"export", "-h"}},
		{name: "import -h", args: []string{"import", "-h"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := run(tt.args)
			if got != exitOK {
				t.Errorf("run(%v) = %d, want %d", tt.args, got, exitOK)
			}
		})
	}
}

func TestUnknownSubcommandExitCode(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "unknown command", args: []string{"unknown"}},
		{name: "another unknown", args: []string{"frobnicate"}},
		{name: "typo of serve", args: []string{"sever"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := run(tt.args)
			if got != exitUsage {
				t.Errorf("run(%v) = %d, want %d", tt.args, got, exitUsage)
			}
		})
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Create source store and save a tape.
	srcDir := filepath.Join(tmpDir, "src")
	srcStore, err := httptape.NewFileStore(httptape.WithDirectory(srcDir))
	if err != nil {
		t.Fatalf("create src store: %v", err)
	}

	tape := httptape.NewTape("test-route", httptape.RecordedReq{
		Method:  "GET",
		URL:     "http://example.com/hello",
		Headers: http.Header{"Accept": {"application/json"}},
		Body:    []byte("request body"),
	}, httptape.RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{"Content-Type": {"application/json"}},
		Body:       []byte(`{"msg":"hi"}`),
	})

	if err := srcStore.Save(ctx, tape); err != nil {
		t.Fatalf("save tape: %v", err)
	}

	// Export via CLI to a temp file.
	bundlePath := filepath.Join(tmpDir, "bundle.tar.gz")
	code := run([]string{"export", "--fixtures", srcDir, "--output", bundlePath})
	if code != exitOK {
		t.Fatalf("export exited %d, want %d", code, exitOK)
	}

	if info, err := os.Stat(bundlePath); err != nil {
		t.Fatalf("bundle file not created: %v", err)
	} else if info.Size() == 0 {
		t.Fatal("bundle file is empty")
	}

	// Import via CLI into a new store.
	dstDir := filepath.Join(tmpDir, "dst")
	code = run([]string{"import", "--fixtures", dstDir, "--input", bundlePath})
	if code != exitOK {
		t.Fatalf("import exited %d, want %d", code, exitOK)
	}

	// Verify the tape exists in the destination store.
	dstStore, err := httptape.NewFileStore(httptape.WithDirectory(dstDir))
	if err != nil {
		t.Fatalf("create dst store: %v", err)
	}

	loaded, err := dstStore.Load(ctx, tape.ID)
	if err != nil {
		t.Fatalf("load tape from dst: %v", err)
	}

	if loaded.Route != tape.Route {
		t.Errorf("route = %q, want %q", loaded.Route, tape.Route)
	}
	if loaded.Request.Method != tape.Request.Method {
		t.Errorf("method = %q, want %q", loaded.Request.Method, tape.Request.Method)
	}
	if loaded.Request.URL != tape.Request.URL {
		t.Errorf("url = %q, want %q", loaded.Request.URL, tape.Request.URL)
	}
	if loaded.Response.StatusCode != tape.Response.StatusCode {
		t.Errorf("status = %d, want %d", loaded.Response.StatusCode, tape.Response.StatusCode)
	}
	if string(loaded.Response.Body) != string(tape.Response.Body) {
		t.Errorf("body = %q, want %q", loaded.Response.Body, tape.Response.Body)
	}
}
