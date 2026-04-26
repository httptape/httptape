package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

// TestProxyHelpExposesHealthFlags is a documentation regression: --help on
// the proxy command lists the new flags so operators discover them.
func TestProxyHelpExposesHealthFlags(t *testing.T) {
	// Capture stderr where flag.Usage writes.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origStderr := os.Stderr
	os.Stderr = w

	go func() {
		_ = run([]string{"proxy", "-h"})
		w.Close()
	}()

	br := bufio.NewReader(r)
	var sb strings.Builder
	for {
		line, err := br.ReadString('\n')
		sb.WriteString(line)
		if err != nil {
			break
		}
	}
	os.Stderr = origStderr

	got := sb.String()
	if !strings.Contains(got, "-health-endpoint") {
		t.Errorf("--health-endpoint missing from proxy help: %s", got)
	}
	if !strings.Contains(got, "-upstream-probe-interval") {
		t.Errorf("--upstream-probe-interval missing from proxy help: %s", got)
	}
}

// TestProxyUpstreamProbeIntervalRequiresHealthEndpoint validates the usage
// guard: --upstream-probe-interval without --health-endpoint is an error.
func TestProxyUpstreamProbeIntervalRequiresHealthEndpoint(t *testing.T) {
	tmpDir := t.TempDir()
	got := run([]string{"proxy",
		"--upstream", "http://example.com",
		"--fixtures", tmpDir,
		"--upstream-probe-interval", "1s",
	})
	if got != exitUsage {
		t.Errorf("got exit %d, want %d (usage)", got, exitUsage)
	}
}

// TestProxyHealthEndpointMounted starts the proxy CLI against a fake upstream
// with --health-endpoint set, then verifies /__httptape/health responds and
// /__httptape/health/stream emits an initial event.
func TestProxyHealthEndpointMounted(t *testing.T) {
	// Fake upstream the proxy will probe.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer upstream.Close()

	// Find a free port for the proxy listener.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	tmpDir := t.TempDir()
	args := []string{"proxy",
		"--upstream", upstream.URL,
		"--fixtures", tmpDir,
		"--port", itoa(port),
		"--health-endpoint",
		"--upstream-probe-interval", "50ms",
	}

	// run blocks on ListenAndServe; race a shutdown via SIGINT-like cancel by
	// closing the os.Stdin/os.Args isn't an option here, so we run in a
	// goroutine and just kill it after the assertions.
	done := make(chan int, 1)
	go func() {
		done <- run(args)
	}()

	base := "http://127.0.0.1:" + itoa(port)

	// Wait for the listener to come up.
	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for {
		resp, err := http.Get(base + "/__httptape/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
			lastErr = errors.New("non-200 from health endpoint")
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			t.Fatalf("health endpoint never came up: %v", lastErr)
		}
		time.Sleep(20 * time.Millisecond)
	}

	resp, err := http.Get(base + "/__httptape/health")
	if err != nil {
		t.Fatalf("GET health: %v", err)
	}
	defer resp.Body.Close()

	var snap httptape.HealthSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snap.UpstreamURL != upstream.URL {
		t.Errorf("UpstreamURL=%q, want %q", snap.UpstreamURL, upstream.URL)
	}
	if snap.ProbeIntervalMS != 50 {
		t.Errorf("ProbeIntervalMS=%d, want 50", snap.ProbeIntervalMS)
	}

	// SSE: read the initial event.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/__httptape/health/stream", nil)
	streamResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("subscribe SSE: %v", err)
	}
	defer streamResp.Body.Close()

	br := bufio.NewReader(streamResp.Body)
	gotEvent := atomic.Bool{}
	go func() {
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(strings.TrimSpace(line), "data:") {
				gotEvent.Store(true)
				return
			}
		}
	}()
	deadline = time.Now().Add(2 * time.Second)
	for !gotEvent.Load() {
		if time.Now().After(deadline) {
			t.Fatal("SSE initial event not received")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Trigger shutdown by sending SIGINT to ourselves.
	p, _ := os.FindProcess(os.Getpid())
	_ = p.Signal(os.Interrupt)

	select {
	case code := <-done:
		_ = code
	case <-time.After(5 * time.Second):
		t.Fatal("CLI did not exit after SIGINT")
	}
}

func TestParseSSETiming(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		errMsg  string // substring expected in the error message
	}{
		{name: "realtime", input: "realtime", wantErr: false},
		{name: "instant", input: "instant", wantErr: false},
		{name: "accelerated integer", input: "accelerated=5", wantErr: false},
		{name: "accelerated float", input: "accelerated=2.5", wantErr: false},
		{name: "accelerated fractional", input: "accelerated=0.1", wantErr: false},
		{name: "empty string", input: "", wantErr: true, errMsg: "valid modes are"},
		{name: "unknown mode", input: "turbo", wantErr: true, errMsg: "valid modes are"},
		{name: "accelerated missing factor", input: "accelerated=", wantErr: true, errMsg: "accelerated requires a factor"},
		{name: "accelerated non-numeric", input: "accelerated=fast", wantErr: true, errMsg: "not a valid number"},
		{name: "accelerated zero", input: "accelerated=0", wantErr: true, errMsg: "must be greater than 0"},
		{name: "accelerated negative", input: "accelerated=-1", wantErr: true, errMsg: "must be greater than 0"},
		{name: "accelerated no equals", input: "accelerated", wantErr: true, errMsg: "valid modes are"},
		{name: "case sensitive realtime", input: "Realtime", wantErr: true, errMsg: "valid modes are"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, err := parseSSETiming(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseSSETiming(%q) = %v, want error", tt.input, mode)
				}
				if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("parseSSETiming(%q) error = %q, want substring %q", tt.input, err.Error(), tt.errMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSSETiming(%q) unexpected error: %v", tt.input, err)
			}
			if mode == nil {
				t.Fatalf("parseSSETiming(%q) returned nil mode", tt.input)
			}
		})
	}
}

func TestSSETimingFlagIntegration(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name     string
		args     []string
		wantCode int
	}{
		{
			name:     "serve valid realtime",
			args:     []string{"serve", "--fixtures", tmpDir, "--sse-timing", "realtime", "-h"},
			wantCode: exitOK,
		},
		{
			name:     "serve valid instant",
			args:     []string{"serve", "--fixtures", tmpDir, "--sse-timing", "instant", "-h"},
			wantCode: exitOK,
		},
		{
			name:     "serve valid accelerated",
			args:     []string{"serve", "--fixtures", tmpDir, "--sse-timing", "accelerated=10", "-h"},
			wantCode: exitOK,
		},
		{
			name:     "serve invalid mode",
			args:     []string{"serve", "--fixtures", tmpDir, "--sse-timing", "bogus"},
			wantCode: exitUsage,
		},
		{
			name:     "serve accelerated zero",
			args:     []string{"serve", "--fixtures", tmpDir, "-sse-timing", "accelerated=0"},
			wantCode: exitUsage,
		},
		{
			name:     "serve accelerated missing factor",
			args:     []string{"serve", "--fixtures", tmpDir, "--sse-timing", "accelerated="},
			wantCode: exitUsage,
		},
		{
			name:     "proxy invalid mode",
			args:     []string{"proxy", "--upstream", "http://example.com", "--fixtures", tmpDir, "--sse-timing", "nope"},
			wantCode: exitUsage,
		},
		{
			name:     "proxy valid instant",
			args:     []string{"proxy", "--upstream", "http://example.com", "--fixtures", tmpDir, "--sse-timing", "instant", "-h"},
			wantCode: exitOK,
		},
		{
			name:     "serve no sse-timing flag",
			args:     []string{"serve", "--fixtures", tmpDir, "-h"},
			wantCode: exitOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := run(tt.args)
			if got != tt.wantCode {
				t.Errorf("run(%v) = %d, want %d", tt.args, got, tt.wantCode)
			}
		})
	}
}

func TestServeWithConfigMatcher(t *testing.T) {
	tmpDir := t.TempDir()
	fixturesDir := filepath.Join(tmpDir, "fixtures")

	// Create a fixtures directory with two tapes at the same POST path
	// but different response bodies. We'll use body_fuzzy matching to
	// distinguish them by $.action field.
	store, err := httptape.NewFileStore(httptape.WithDirectory(fixturesDir))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	ctx := context.Background()

	tape1 := httptape.NewTape("test", httptape.RecordedReq{
		Method:  "POST",
		URL:     "http://example.com/api/do",
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    []byte(`{"action":"create","name":"widget"}`),
	}, httptape.RecordedResp{
		StatusCode: 201,
		Headers:    http.Header{"Content-Type": {"application/json"}},
		Body:       []byte(`{"result":"created"}`),
	})
	if err := store.Save(ctx, tape1); err != nil {
		t.Fatalf("save tape1: %v", err)
	}

	tape2 := httptape.NewTape("test", httptape.RecordedReq{
		Method:  "POST",
		URL:     "http://example.com/api/do",
		Headers: http.Header{"Content-Type": {"application/json"}},
		Body:    []byte(`{"action":"delete","name":"widget"}`),
	}, httptape.RecordedResp{
		StatusCode: 200,
		Headers:    http.Header{"Content-Type": {"application/json"}},
		Body:       []byte(`{"result":"deleted"}`),
	})
	if err := store.Save(ctx, tape2); err != nil {
		t.Fatalf("save tape2: %v", err)
	}

	// Write a config file with body_fuzzy matching on $.action.
	configPath := filepath.Join(tmpDir, "config.json")
	configContent := `{
		"version": "1",
		"matcher": {
			"criteria": [
				{"type": "method"},
				{"type": "path"},
				{"type": "body_fuzzy", "paths": ["$.action"]}
			]
		},
		"rules": []
	}`
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Find a free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	// Start the server in a goroutine.
	done := make(chan int, 1)
	go func() {
		done <- run([]string{
			"serve",
			"--fixtures", fixturesDir,
			"--port", itoa(port),
			"--config", configPath,
		})
	}()

	base := "http://127.0.0.1:" + itoa(port)

	// Wait for the server to start.
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never came up: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Send POST with action=create, expect "created" response.
	resp, err := http.Post(base+"/api/do", "application/json",
		strings.NewReader(`{"action":"create","name":"something"}`))
	if err != nil {
		t.Fatalf("POST create: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Errorf("create status = %d, want 201", resp.StatusCode)
	}
	if !strings.Contains(string(body), "created") {
		t.Errorf("create body = %q, want to contain %q", string(body), "created")
	}

	// Send POST with action=delete, expect "deleted" response.
	resp, err = http.Post(base+"/api/do", "application/json",
		strings.NewReader(`{"action":"delete","name":"something"}`))
	if err != nil {
		t.Fatalf("POST delete: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("delete status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "deleted") {
		t.Errorf("delete body = %q, want to contain %q", string(body), "deleted")
	}

	// Shutdown.
	p, _ := os.FindProcess(os.Getpid())
	_ = p.Signal(os.Interrupt)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("CLI did not exit after SIGINT")
	}
}

func TestServeWithInvalidConfig(t *testing.T) {
	tmpDir := t.TempDir()
	fixturesDir := filepath.Join(tmpDir, "fixtures")
	if err := os.MkdirAll(fixturesDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	configPath := filepath.Join(tmpDir, "bad-config.json")
	if err := os.WriteFile(configPath, []byte(`{"version":"1","rules":[],"matcher":{"criteria":[{"type":"bogus"}]}}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	got := run([]string{
		"serve",
		"--fixtures", fixturesDir,
		"--config", configPath,
	})
	if got != exitRuntime {
		t.Errorf("got exit %d, want %d (runtime error)", got, exitRuntime)
	}
}

func TestServeNoConfig(t *testing.T) {
	// serve with no --config flag should work fine (default behavior).
	got := run([]string{"serve", "-h"})
	if got != exitOK {
		t.Errorf("got exit %d, want %d", got, exitOK)
	}
}

func TestMigrateFixtures_Help(t *testing.T) {
	got := run([]string{"migrate-fixtures", "-h"})
	if got != exitOK {
		t.Errorf("got exit %d, want %d", got, exitOK)
	}
}

func TestMigrateFixtures_MissingDir(t *testing.T) {
	got := run([]string{"migrate-fixtures"})
	if got != exitUsage {
		t.Errorf("got exit %d, want %d", got, exitUsage)
	}
}

func TestMigrateFixtures_NonexistentDir(t *testing.T) {
	got := run([]string{"migrate-fixtures", "/nonexistent/path/xyz"})
	if got != exitRuntime {
		t.Errorf("got exit %d, want %d", got, exitRuntime)
	}
}

func TestMigrateFixtures_NotADirectory(t *testing.T) {
	f, err := os.CreateTemp("", "httptape-test-*.json")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	defer os.Remove(f.Name())
	f.Close()

	got := run([]string{"migrate-fixtures", f.Name()})
	if got != exitRuntime {
		t.Errorf("got exit %d, want %d", got, exitRuntime)
	}
}

func TestMigrateFixtures_MigratesLegacyTape(t *testing.T) {
	dir := t.TempDir()

	// Write a legacy fixture with base64-encoded JSON body and body_encoding field.
	legacy := `{
  "id": "test-001",
  "route": "api",
  "recorded_at": "2026-01-01T00:00:00Z",
  "request": {
    "method": "GET",
    "url": "http://example.com/api/users",
    "headers": {"Accept": ["application/json"]},
    "body": null,
    "body_hash": ""
  },
  "response": {
    "status_code": 200,
    "headers": {"Content-Type": ["application/json"]},
    "body": "eyJuYW1lIjoiYWxpY2UifQ==",
    "body_encoding": "base64"
  }
}`
	path := filepath.Join(dir, "tape.json")
	if err := os.WriteFile(path, []byte(legacy), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := run([]string{"migrate-fixtures", dir})
	if got != exitOK {
		t.Fatalf("got exit %d, want %d", got, exitOK)
	}

	// Read the migrated file.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// Verify body is now native JSON (not base64 string).
	if !strings.Contains(string(data), `"name"`) {
		t.Errorf("migrated fixture should contain native JSON body, got:\n%s", data)
	}
	// Verify body_encoding field is removed.
	if strings.Contains(string(data), "body_encoding") {
		t.Errorf("migrated fixture should not contain body_encoding, got:\n%s", data)
	}
	// Verify trailing newline.
	if data[len(data)-1] != '\n' {
		t.Error("migrated fixture should end with newline")
	}
}

func TestMigrateFixtures_SkipsNonTapeJSON(t *testing.T) {
	dir := t.TempDir()

	// Write a JSON file that is not a valid tape.
	notTape := `{"foo": "bar"}`
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(notTape), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := run([]string{"migrate-fixtures", dir})
	if got != exitOK {
		t.Fatalf("got exit %d, want %d", got, exitOK)
	}

	// Verify file is unchanged.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != notTape {
		t.Errorf("non-tape file should be unchanged, got:\n%s", data)
	}
}

func TestMigrateFixtures_SkipsNonJSONFiles(t *testing.T) {
	dir := t.TempDir()

	// Write a non-JSON file.
	path := filepath.Join(dir, "readme.txt")
	content := "not a json file"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := run([]string{"migrate-fixtures", dir})
	if got != exitOK {
		t.Fatalf("got exit %d, want %d", got, exitOK)
	}

	// Verify file is unchanged.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != content {
		t.Errorf("non-JSON file should be unchanged, got:\n%s", data)
	}
}

func TestMigrateFixtures_RecursiveFlag(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Write a legacy tape in a subdirectory.
	legacy := `{
  "id": "test-002",
  "route": "api",
  "recorded_at": "2026-01-01T00:00:00Z",
  "request": {
    "method": "POST",
    "url": "http://example.com/api/data",
    "headers": {"Content-Type": ["text/plain"]},
    "body": "aGVsbG8=",
    "body_hash": "abc123",
    "body_encoding": "base64"
  },
  "response": {
    "status_code": 200,
    "headers": {"Content-Type": ["text/plain"]},
    "body": "d29ybGQ=",
    "body_encoding": "base64"
  }
}`
	path := filepath.Join(subDir, "tape.json")
	if err := os.WriteFile(path, []byte(legacy), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Without --recursive, subdirectories are skipped.
	got := run([]string{"migrate-fixtures", dir})
	if got != exitOK {
		t.Fatalf("got exit %d, want %d", got, exitOK)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// File should still have body_encoding because it wasn't processed.
	if !strings.Contains(string(data), "body_encoding") {
		t.Error("without --recursive, subdirectory fixture should be unchanged")
	}

	// With --recursive, subdirectories are processed.
	got = run([]string{"migrate-fixtures", "--recursive", dir})
	if got != exitOK {
		t.Fatalf("got exit %d, want %d", got, exitOK)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(data), "body_encoding") {
		t.Errorf("with --recursive, fixture should be migrated (no body_encoding), got:\n%s", data)
	}

	// text/plain body should be a string in the migrated fixture.
	if !strings.Contains(string(data), `"hello"`) || !strings.Contains(string(data), `"world"`) {
		t.Errorf("text/plain bodies should be decoded as strings, got:\n%s", data)
	}
}

func TestMigrateFixtures_AlreadyMigratedTape(t *testing.T) {
	dir := t.TempDir()

	// Write a tape that is already in the new format (native JSON body).
	modern := `{
  "id": "test-003",
  "route": "api",
  "recorded_at": "2026-01-01T00:00:00Z",
  "request": {
    "method": "GET",
    "url": "http://example.com/api/health",
    "headers": {},
    "body": null,
    "body_hash": ""
  },
  "response": {
    "status_code": 200,
    "headers": {"Content-Type": ["application/json"]},
    "body": {"status": "ok"}
  }
}`
	path := filepath.Join(dir, "tape.json")
	if err := os.WriteFile(path, []byte(modern), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := run([]string{"migrate-fixtures", dir})
	if got != exitOK {
		t.Fatalf("got exit %d, want %d", got, exitOK)
	}

	// Read and verify the body is still native JSON.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), `"status"`) {
		t.Errorf("already-migrated fixture should still have native JSON body, got:\n%s", data)
	}
}

func TestServeWithSynthesize(t *testing.T) {
	// --synthesize flag should be accepted without error.
	got := run([]string{"serve", "--fixtures", t.TempDir(), "--synthesize", "-h"})
	if got != exitOK {
		t.Errorf("got exit %d, want %d for serve --synthesize -h", got, exitOK)
	}
}

func TestServeWithSynthesizeLogging(t *testing.T) {
	// Create a fixture directory with an exemplar tape.
	dir := t.TempDir()
	store, err := httptape.NewFileStore(httptape.WithDirectory(dir))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	exemplar := httptape.Tape{
		ID:       "exemplar-log-test",
		Exemplar: true,
		Request: httptape.RecordedReq{
			Method:     "GET",
			URLPattern: "/users/:id",
		},
		Response: httptape.RecordedResp{
			StatusCode: 200,
			Headers:    http.Header{"Content-Type": {"application/json"}},
			Body:       []byte(`{"id":"{{pathParam.id}}"}`),
		},
	}
	ctx := context.Background()
	if err := store.Save(ctx, exemplar); err != nil {
		t.Fatalf("save exemplar: %v", err)
	}

	// Just verify --synthesize flag is accepted (the -h flag triggers help and exits).
	got := run([]string{"serve", "--fixtures", dir, "--synthesize", "-h"})
	if got != exitOK {
		t.Errorf("got exit %d, want %d", got, exitOK)
	}
}

func TestServeWithInvalidExemplarTape(t *testing.T) {
	// Create a fixture with an invalid exemplar tape (Exemplar=true but no URLPattern).
	dir := t.TempDir()

	// Write a malformed exemplar tape directly as JSON (bypasses Store validation
	// to test the startup path's own ValidateTape call).
	tapeJSON := `{
		"id": "bad-exemplar",
		"route": "",
		"recorded_at": "2026-01-01T00:00:00Z",
		"exemplar": true,
		"request": {
			"method": "GET",
			"url": "",
			"headers": {},
			"body": null,
			"body_hash": ""
		},
		"response": {
			"status_code": 200,
			"headers": {},
			"body": null
		}
	}`
	tapePath := filepath.Join(dir, "bad-exemplar.json")
	if err := os.WriteFile(tapePath, []byte(tapeJSON), 0644); err != nil {
		t.Fatalf("write tape: %v", err)
	}

	// The serve command should fail at startup validation because the tape
	// has Exemplar=true but no URLPattern.
	got := run([]string{"serve", "--fixtures", dir})
	if got != exitRuntime {
		t.Errorf("got exit %d, want %d (startup validation should reject bad exemplar)", got, exitRuntime)
	}
}

func itoa(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	negative := i < 0
	if negative {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = digits[i%10]
		i /= 10
	}
	if negative {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
