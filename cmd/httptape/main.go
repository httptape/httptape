// Package main implements the httptape CLI binary.
//
// httptape is a standalone command-line tool for HTTP traffic recording,
// sanitization, and replay. It is a thin wrapper over the httptape library.
//
// Commands:
//
//	httptape serve             — Replay recorded fixtures as a mock HTTP server
//	httptape record            — Proxy requests to upstream, record and sanitize responses
//	httptape proxy             — Forward to upstream with L1/L2 fallback-to-cache
//	httptape export            — Export fixtures to a tar.gz bundle
//	httptape import            — Import fixtures from a tar.gz bundle
//	httptape migrate-fixtures  — Migrate fixtures from v0.11 to v0.12 format
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/VibeWarden/httptape"
)

// usageError wraps an error to indicate a usage/flag-parsing problem (exit code 1)
// as opposed to a runtime error (exit code 2).
type usageError struct{ err error }

func (u *usageError) Error() string { return u.err.Error() }
func (u *usageError) Unwrap() error { return u.err }

const (
	exitOK      = 0
	exitUsage   = 1
	exitRuntime = 2
)

var logger = log.New(os.Stderr, "httptape: ", 0)

// repeatableFlag is a flag.Value that accumulates repeated flag uses into a slice.
type repeatableFlag []string

func (r *repeatableFlag) String() string { return strings.Join(*r, ", ") }
func (r *repeatableFlag) Set(value string) error {
	*r = append(*r, value)
	return nil
}

const usageText = `httptape — HTTP traffic recording, sanitization, and replay

Usage:
  httptape <command> [flags]

Commands:
  serve              Replay recorded fixtures as a mock HTTP server
  record             Proxy requests to upstream, record and sanitize responses
  proxy              Forward to upstream with L1/L2 fallback-to-cache
  export             Export fixtures to a tar.gz bundle
  import             Import fixtures from a tar.gz bundle
  migrate-fixtures   Migrate fixtures from v0.11 to v0.12 format

Run 'httptape <command> -h' for details on a specific command.
`

func main() {
	code := run(os.Args[1:])
	os.Exit(code)
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usageText)
		return exitOK
	}

	cmd := args[0]
	cmdArgs := args[1:]

	var err error
	switch cmd {
	case "-h", "--help", "help":
		fmt.Fprint(os.Stderr, usageText)
		return exitOK
	case "serve":
		err = runServe(cmdArgs)
	case "record":
		err = runRecord(cmdArgs)
	case "proxy":
		err = runProxy(cmdArgs)
	case "export":
		err = runExport(cmdArgs)
	case "import":
		err = runImport(cmdArgs)
	case "migrate-fixtures":
		err = runMigrateFixtures(cmdArgs)
	default:
		fmt.Fprintf(os.Stderr, "httptape: unknown command %q\n\n", cmd)
		fmt.Fprint(os.Stderr, usageText)
		return exitUsage
	}

	if err != nil {
		// flag.ErrHelp means -h was passed; flag already printed usage.
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		var ue *usageError
		if errors.As(err, &ue) {
			logger.Println(ue)
			return exitUsage
		}
		logger.Println(err)
		return exitRuntime
	}
	return exitOK
}

// parseSSETiming parses the --sse-timing flag value into an SSETimingMode.
// Accepted values: "realtime", "instant", "accelerated=<factor>".
// Returns an error (suitable for wrapping in *usageError) on invalid input.
func parseSSETiming(s string) (httptape.SSETimingMode, error) {
	switch {
	case s == "realtime":
		return httptape.SSETimingRealtime(), nil
	case s == "instant":
		return httptape.SSETimingInstant(), nil
	case strings.HasPrefix(s, "accelerated="):
		raw := strings.TrimPrefix(s, "accelerated=")
		if raw == "" {
			return nil, fmt.Errorf("invalid --sse-timing %q: accelerated requires a factor (e.g., accelerated=5). Valid modes: realtime, instant, accelerated=<factor>", s)
		}
		factor, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid --sse-timing %q: factor is not a valid number. Valid modes: realtime, instant, accelerated=<factor>", s)
		}
		if factor <= 0 {
			return nil, fmt.Errorf("invalid --sse-timing %q: factor must be greater than 0. Valid modes: realtime, instant, accelerated=<factor>", s)
		}
		// The CLI pre-validates factor > 0 above, so SSETimingAccelerated
		// cannot return an error here. Handle it defensively anyway.
		mode, modeErr := httptape.SSETimingAccelerated(factor)
		if modeErr != nil {
			return nil, fmt.Errorf("invalid --sse-timing %q: %w", s, modeErr)
		}
		return mode, nil
	default:
		return nil, fmt.Errorf("invalid --sse-timing %q: valid modes are realtime, instant, accelerated=<factor>", s)
	}
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("httptape serve", flag.ContinueOnError)
	fixtures := fs.String("fixtures", "", "Path to fixture directory (required)")
	port := fs.Int("port", 8081, "Listen port")
	fallbackStatus := fs.Int("fallback-status", 404, "HTTP status when no tape matches")
	configPath := fs.String("config", "", "Path to httptape config JSON (matcher and sanitization rules)")
	cors := fs.Bool("cors", false, "Enable CORS headers (Access-Control-Allow-Origin: *)")
	delay := fs.Duration("delay", 0, "Fixed delay before every response (e.g., 200ms, 1s)")
	errorRate := fs.Float64("error-rate", 0, "Fraction of requests that return 500 (0.0-1.0)")
	var replayHeaders repeatableFlag
	fs.Var(&replayHeaders, "replay-header", "Header to inject into responses (Key=Value, repeatable)")
	sseTiming := fs.String("sse-timing", "", "SSE replay timing mode: realtime, instant, accelerated=<factor>")
	synthesize := fs.Bool("synthesize", false, "Enable synthesis mode (exemplar tapes generate responses for unmatched URLs)")

	if err := fs.Parse(args); err != nil {
		return &usageError{err}
	}

	if *fixtures == "" {
		fs.Usage()
		return &usageError{fmt.Errorf("--fixtures is required")}
	}

	store, err := httptape.NewFileStore(httptape.WithDirectory(*fixtures))
	if err != nil {
		return fmt.Errorf("create store: %w", err)
	}

	var serverOpts []httptape.ServerOption
	serverOpts = append(serverOpts, httptape.WithFallbackStatus(*fallbackStatus))
	if *configPath != "" {
		cfg, err := httptape.LoadConfigFile(*configPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		matcher, err := cfg.BuildMatcher()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		serverOpts = append(serverOpts, httptape.WithMatcher(matcher))
	}
	if *sseTiming != "" {
		mode, err := parseSSETiming(*sseTiming)
		if err != nil {
			return &usageError{err}
		}
		serverOpts = append(serverOpts, httptape.WithSSETiming(mode))
	}
	if *cors {
		serverOpts = append(serverOpts, httptape.WithCORS())
	}
	if *delay > 0 {
		serverOpts = append(serverOpts, httptape.WithDelay(*delay))
	}
	if *errorRate > 0 {
		serverOpts = append(serverOpts, httptape.WithErrorRate(*errorRate))
	}
	for _, rh := range replayHeaders {
		eqIdx := strings.Index(rh, "=")
		if eqIdx < 1 {
			return &usageError{fmt.Errorf("invalid --replay-header %q: expected Key=Value", rh)}
		}
		serverOpts = append(serverOpts, httptape.WithReplayHeaders(rh[:eqIdx], rh[eqIdx+1:]))
	}
	if *synthesize {
		serverOpts = append(serverOpts, httptape.WithSynthesis())
	}

	// Startup validation: check all loaded tapes for structural validity.
	allTapes, err := store.List(context.Background(), httptape.Filter{})
	if err != nil {
		return fmt.Errorf("load tapes: %w", err)
	}
	for _, t := range allTapes {
		if err := httptape.ValidateTape(t); err != nil {
			return fmt.Errorf("invalid tape %s: %w", t.ID, err)
		}
	}

	// Synthesis logging.
	if *synthesize {
		exemplarCount := 0
		for _, t := range allTapes {
			if t.Exemplar {
				exemplarCount++
			}
		}
		logger.Printf("synthesis mode ENABLED -- %d exemplar tape(s) loaded", exemplarCount)
	} else {
		exemplarCount := 0
		for _, t := range allTapes {
			if t.Exemplar {
				exemplarCount++
			}
		}
		if exemplarCount > 0 {
			logger.Printf("WARNING: %d exemplar tape(s) found but synthesis is disabled (use --synthesize to enable)", exemplarCount)
		}
	}

	server, err := httptape.NewServer(store, serverOpts...)
	if err != nil {
		return fmt.Errorf("create server: %w", err)
	}

	addr := fmt.Sprintf(":%d", *port)
	httpServer := &http.Server{
		Addr:    addr,
		Handler: server,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		logger.Println("shutdown initiated")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			logger.Printf("graceful shutdown failed: %v, forcing close", err)
			httpServer.Close()
		}
	}()

	logger.Printf("serve mode: listening on %s, fixtures=%s", addr, *fixtures)
	if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
		return fmt.Errorf("server error: %w", err)
	}

	logger.Println("shutdown complete")
	return nil
}

func runRecord(args []string) error {
	fs := flag.NewFlagSet("httptape record", flag.ContinueOnError)
	upstream := fs.String("upstream", "", "Upstream URL, e.g. https://api.example.com (required)")
	fixtures := fs.String("fixtures", "", "Path to fixture directory (required)")
	configPath := fs.String("config", "", "Path to sanitization config JSON")
	port := fs.Int("port", 8081, "Listen port")
	cors := fs.Bool("cors", false, "Enable CORS headers (Access-Control-Allow-Origin: *)")
	tlsCert := fs.String("tls-cert", "", "Path to PEM client certificate for mTLS")
	tlsKey := fs.String("tls-key", "", "Path to PEM client private key for mTLS")
	tlsCA := fs.String("tls-ca", "", "Path to PEM CA certificate(s) for upstream verification")
	tlsInsecure := fs.Bool("tls-insecure", false, "Skip TLS verification (dev only)")

	if err := fs.Parse(args); err != nil {
		return &usageError{err}
	}

	if *upstream == "" {
		fs.Usage()
		return &usageError{fmt.Errorf("--upstream is required")}
	}
	if *fixtures == "" {
		fs.Usage()
		return &usageError{fmt.Errorf("--fixtures is required")}
	}

	upstreamURL, err := url.Parse(*upstream)
	if err != nil {
		return fmt.Errorf("invalid upstream URL: %w", err)
	}
	if upstreamURL.Scheme == "" || upstreamURL.Host == "" {
		return fmt.Errorf("upstream URL must include scheme and host, got %q", *upstream)
	}

	store, err := httptape.NewFileStore(httptape.WithDirectory(*fixtures))
	if err != nil {
		return fmt.Errorf("create store: %w", err)
	}

	if *tlsInsecure {
		logger.Println("WARNING: --tls-insecure disables TLS verification. Do not use in production.")
	}

	tlsCfg, err := httptape.BuildTLSConfig(*tlsCert, *tlsKey, *tlsCA, *tlsInsecure)
	if err != nil {
		return &usageError{err}
	}

	var recorderOpts []httptape.RecorderOption
	recorderOpts = append(recorderOpts, httptape.WithAsync(true))
	recorderOpts = append(recorderOpts, httptape.WithOnError(func(err error) {
		logger.Printf("recorder error: %v", err)
	}))

	if tlsCfg != nil {
		recorderOpts = append(recorderOpts, httptape.WithRecorderTLSConfig(tlsCfg))
	}

	if *configPath != "" {
		cfg, err := httptape.LoadConfigFile(*configPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		pipeline := cfg.BuildPipeline()
		recorderOpts = append(recorderOpts, httptape.WithSanitizer(pipeline))
	}

	recorder := httptape.NewRecorder(store, recorderOpts...)

	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(upstreamURL)
			r.Out.Host = upstreamURL.Host
		},
		Transport: recorder,
	}

	var handler http.Handler = proxy
	if *cors {
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, OPTIONS, HEAD")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, Accept")
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.Header().Set("Access-Control-Expose-Headers", "X-Httptape-Source, X-Httptape-Error")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			proxy.ServeHTTP(w, r)
		})
	}

	addr := fmt.Sprintf(":%d", *port)
	httpServer := &http.Server{
		Addr:    addr,
		Handler: handler,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		logger.Println("shutdown initiated")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			logger.Printf("graceful shutdown failed: %v, forcing close", err)
			httpServer.Close()
		}
		if err := recorder.Close(); err != nil {
			logger.Printf("recorder close error: %v", err)
		} else {
			logger.Println("recorder flushed")
		}
	}()

	logger.Printf("record mode: listening on %s, upstream=%s, fixtures=%s", addr, *upstream, *fixtures)
	if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
		return fmt.Errorf("server error: %w", err)
	}

	logger.Println("shutdown complete")
	return nil
}

func runProxy(args []string) error {
	fs := flag.NewFlagSet("httptape proxy", flag.ContinueOnError)
	upstream := fs.String("upstream", "", "Upstream URL, e.g. https://api.example.com (required)")
	fixtures := fs.String("fixtures", "", "Path to fixture directory for L2 cache (required)")
	configPath := fs.String("config", "", "Path to sanitization config JSON (applied to L2 writes only)")
	port := fs.Int("port", 8081, "Listen port")
	cors := fs.Bool("cors", false, "Enable CORS headers (Access-Control-Allow-Origin: *)")
	fallbackOn5xx := fs.Bool("fallback-on-5xx", false, "Also fall back on 5xx responses from upstream")
	tlsCert := fs.String("tls-cert", "", "Path to PEM client certificate for mTLS")
	tlsKey := fs.String("tls-key", "", "Path to PEM client private key for mTLS")
	tlsCA := fs.String("tls-ca", "", "Path to PEM CA certificate(s) for upstream verification")
	tlsInsecure := fs.Bool("tls-insecure", false, "Skip TLS verification (dev only)")
	healthEndpoint := fs.Bool("health-endpoint", false,
		"Mount /__httptape/health (JSON snapshot) and /__httptape/health/stream (SSE).")
	upstreamProbeInterval := fs.Duration("upstream-probe-interval", 0,
		"Active upstream probe cadence. 0 = disabled. When --health-endpoint is set "+
			"and this is unset, defaults to 2s.")
	sseTiming := fs.String("sse-timing", "", "SSE replay timing mode: realtime, instant, accelerated=<factor>")

	if err := fs.Parse(args); err != nil {
		return &usageError{err}
	}

	if *upstream == "" {
		fs.Usage()
		return &usageError{fmt.Errorf("--upstream is required")}
	}
	if *fixtures == "" {
		fs.Usage()
		return &usageError{fmt.Errorf("--fixtures is required")}
	}

	upstreamURL, err := url.Parse(*upstream)
	if err != nil {
		return fmt.Errorf("invalid upstream URL: %w", err)
	}
	if upstreamURL.Scheme == "" || upstreamURL.Host == "" {
		return fmt.Errorf("upstream URL must include scheme and host, got %q", *upstream)
	}

	l1 := httptape.NewMemoryStore()
	l2, err := httptape.NewFileStore(httptape.WithDirectory(*fixtures))
	if err != nil {
		return fmt.Errorf("create L2 store: %w", err)
	}

	if *tlsInsecure {
		logger.Println("WARNING: --tls-insecure disables TLS verification. Do not use in production.")
	}

	tlsCfg, err := httptape.BuildTLSConfig(*tlsCert, *tlsKey, *tlsCA, *tlsInsecure)
	if err != nil {
		return &usageError{err}
	}

	var proxyOpts []httptape.ProxyOption
	proxyOpts = append(proxyOpts, httptape.WithProxyOnError(func(err error) {
		logger.Printf("proxy error: %v", err)
	}))

	if tlsCfg != nil {
		proxyOpts = append(proxyOpts, httptape.WithProxyTLSConfig(tlsCfg))
	}

	if *configPath != "" {
		cfg, err := httptape.LoadConfigFile(*configPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		pipeline := cfg.BuildPipeline()
		proxyOpts = append(proxyOpts, httptape.WithProxySanitizer(pipeline))
	}

	if *fallbackOn5xx {
		proxyOpts = append(proxyOpts, httptape.WithProxyFallbackOn(func(err error, resp *http.Response) bool {
			if err != nil {
				return true
			}
			return resp != nil && resp.StatusCode >= 500
		}))
	}

	if *sseTiming != "" {
		mode, err := parseSSETiming(*sseTiming)
		if err != nil {
			return &usageError{err}
		}
		proxyOpts = append(proxyOpts, httptape.WithProxySSETiming(mode))
	}

	if *healthEndpoint {
		interval := *upstreamProbeInterval
		if interval == 0 {
			interval = 2 * time.Second
		}
		proxyOpts = append(proxyOpts,
			httptape.WithProxyUpstreamURL(*upstream),
			httptape.WithProxyHealthEndpoint(),
			httptape.WithProxyProbeInterval(interval),
		)
	} else if *upstreamProbeInterval > 0 {
		return &usageError{fmt.Errorf("--upstream-probe-interval requires --health-endpoint")}
	}

	tapeProxy, err := httptape.NewProxy(l1, l2, proxyOpts...)
	if err != nil {
		return fmt.Errorf("create proxy: %w", err)
	}

	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(upstreamURL)
			r.Out.Host = upstreamURL.Host
		},
		Transport: tapeProxy,
	}

	// Compose the listener mux: mount the health surface (if enabled) under
	// /__httptape/ and forward everything else to the reverse proxy.
	var handler http.Handler = rp
	if hh := tapeProxy.HealthHandler(); hh != nil {
		mux := http.NewServeMux()
		mux.Handle("/__httptape/", hh)
		mux.Handle("/", rp)
		handler = mux
	}
	if *cors {
		inner := handler
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, OPTIONS, HEAD")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, Accept")
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.Header().Set("Access-Control-Expose-Headers", "X-Httptape-Source, X-Httptape-Error")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			inner.ServeHTTP(w, r)
		})
	}

	addr := fmt.Sprintf(":%d", *port)
	httpServer := &http.Server{
		Addr:    addr,
		Handler: handler,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start background workers (currently: the active probe loop). No-op when
	// the health endpoint is disabled.
	tapeProxy.Start()
	defer func() {
		if err := tapeProxy.Close(); err != nil {
			logger.Printf("proxy close error: %v", err)
		}
	}()

	go func() {
		<-ctx.Done()
		logger.Println("shutdown initiated")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			logger.Printf("graceful shutdown failed: %v, forcing close", err)
			httpServer.Close()
		}
		if err := tapeProxy.Close(); err != nil {
			logger.Printf("proxy close error: %v", err)
		}
	}()

	logger.Printf("proxy mode: listening on %s, upstream=%s, fixtures=%s", addr, *upstream, *fixtures)
	if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
		return fmt.Errorf("server error: %w", err)
	}

	logger.Println("shutdown complete")
	return nil
}

func runExport(args []string) error {
	fs := flag.NewFlagSet("httptape export", flag.ContinueOnError)
	fixtures := fs.String("fixtures", "", "Path to fixture directory (required)")
	output := fs.String("output", "", "Output file path (default: stdout)")
	routes := fs.String("routes", "", "Comma-separated route filter")
	methods := fs.String("methods", "", "Comma-separated HTTP method filter")
	since := fs.String("since", "", "RFC 3339 timestamp filter")

	if err := fs.Parse(args); err != nil {
		return &usageError{err}
	}

	if *fixtures == "" {
		fs.Usage()
		return &usageError{fmt.Errorf("--fixtures is required")}
	}

	store, err := httptape.NewFileStore(httptape.WithDirectory(*fixtures))
	if err != nil {
		return fmt.Errorf("create store: %w", err)
	}

	var opts []httptape.ExportOption

	if *routes != "" {
		parts := strings.Split(*routes, ",")
		trimmed := make([]string, 0, len(parts))
		for _, p := range parts {
			if s := strings.TrimSpace(p); s != "" {
				trimmed = append(trimmed, s)
			}
		}
		if len(trimmed) > 0 {
			opts = append(opts, httptape.WithRoutes(trimmed...))
		}
	}

	if *methods != "" {
		parts := strings.Split(*methods, ",")
		trimmed := make([]string, 0, len(parts))
		for _, p := range parts {
			if s := strings.TrimSpace(p); s != "" {
				trimmed = append(trimmed, s)
			}
		}
		if len(trimmed) > 0 {
			opts = append(opts, httptape.WithMethods(trimmed...))
		}
	}

	if *since != "" {
		t, err := time.Parse(time.RFC3339, *since)
		if err != nil {
			return fmt.Errorf("invalid --since value: %w", err)
		}
		opts = append(opts, httptape.WithSince(t))
	}

	ctx := context.Background()
	reader, err := httptape.ExportBundle(ctx, store, opts...)
	if err != nil {
		return fmt.Errorf("export: %w", err)
	}

	var w io.Writer = os.Stdout
	var outputFile *os.File
	if *output != "" {
		f, err := os.Create(*output)
		if err != nil {
			return fmt.Errorf("create output file: %w", err)
		}
		defer func() {
			// Close error captured below; defer is a safety net.
			f.Close()
		}()
		outputFile = f
		w = f
	}

	if _, err := io.Copy(w, reader); err != nil {
		return fmt.Errorf("write export: %w", err)
	}

	if outputFile != nil {
		if err := outputFile.Close(); err != nil {
			return fmt.Errorf("close output file: %w", err)
		}
	}

	return nil
}

func runImport(args []string) error {
	fs := flag.NewFlagSet("httptape import", flag.ContinueOnError)
	fixtures := fs.String("fixtures", "", "Path to fixture directory (required)")
	input := fs.String("input", "", "Input file path (default: stdin)")

	if err := fs.Parse(args); err != nil {
		return &usageError{err}
	}

	if *fixtures == "" {
		fs.Usage()
		return &usageError{fmt.Errorf("--fixtures is required")}
	}

	store, err := httptape.NewFileStore(httptape.WithDirectory(*fixtures))
	if err != nil {
		return fmt.Errorf("create store: %w", err)
	}

	var r io.Reader = os.Stdin
	if *input != "" {
		f, err := os.Open(*input)
		if err != nil {
			return fmt.Errorf("open input file: %w", err)
		}
		defer f.Close()
		r = f
	}

	ctx := context.Background()
	if err := httptape.ImportBundle(ctx, store, r); err != nil {
		return fmt.Errorf("import: %w", err)
	}

	return nil
}

// legacyTapeEnvelope is used during migration to detect and decode the legacy
// body_encoding field from v0.11 fixtures. The new format determines body
// representation from Content-Type, so body_encoding is removed on migration.
type legacyTapeEnvelope struct {
	ID         string          `json:"id"`
	Route      string          `json:"route"`
	RecordedAt json.RawMessage `json:"recorded_at"`
	Request    json.RawMessage `json:"request"`
	Response   json.RawMessage `json:"response"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
}

// legacyEndpoint extracts body_encoding from a legacy request or response JSON.
type legacyEndpoint struct {
	BodyEncoding string `json:"body_encoding"`
}

// migrateLegacyFixture reads a v0.11 fixture (which may have body_encoding
// fields) and returns the re-serialized v0.12 content. It handles the case
// where bodies were base64-encoded regardless of Content-Type in the old format.
func migrateLegacyFixture(data []byte) ([]byte, error) {
	// First, check if this has legacy body_encoding fields that need
	// special handling. Parse the raw envelope to inspect sub-objects.
	var env legacyTapeEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("parse envelope: %w", err)
	}

	// Check for legacy body_encoding in request and response.
	hasLegacy := false
	var reqLE, respLE legacyEndpoint
	if len(env.Request) > 0 {
		// Unmarshal errors are intentionally ignored here: if the request
		// sub-object does not parse into legacyEndpoint, it simply means
		// there is no legacy body_encoding field to migrate. We fall
		// through to the hasLegacy=false path and skip the legacy
		// pre-processing, letting the standard Tape unmarshal handle it.
		_ = json.Unmarshal(env.Request, &reqLE)
		if reqLE.BodyEncoding != "" {
			hasLegacy = true
		}
	}
	if len(env.Response) > 0 {
		// Same rationale as the request unmarshal above: errors mean
		// "no legacy body_encoding present", not "corrupt data". Corrupt
		// data is caught later by the full Tape unmarshal below.
		_ = json.Unmarshal(env.Response, &respLE)
		if respLE.BodyEncoding != "" {
			hasLegacy = true
		}
	}

	if hasLegacy {
		// For legacy fixtures with body_encoding=base64, we need to pre-decode
		// the body before the new UnmarshalJSON runs, because the new code uses
		// Content-Type to decide how to interpret string bodies, but legacy
		// fixtures always stored as base64 regardless of Content-Type.
		data = removeLegacyBodyEncodingAndDecodeBody(data, reqLE.BodyEncoding, respLE.BodyEncoding)
	}

	// Now unmarshal with the new Tape type (custom UnmarshalJSON handles
	// the body based on Content-Type).
	var tape httptape.Tape
	if err := json.Unmarshal(data, &tape); err != nil {
		return nil, fmt.Errorf("unmarshal tape: %w", err)
	}

	if tape.ID == "" || tape.Request.Method == "" {
		return nil, fmt.Errorf("not a valid tape (missing id or method)")
	}

	out, err := json.MarshalIndent(tape, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal tape: %w", err)
	}
	return append(out, '\n'), nil
}

// removeLegacyBodyEncodingAndDecodeBody modifies the raw JSON to:
//  1. Remove body_encoding fields
//  2. For base64-encoded bodies with non-JSON Content-Types, decode the body
//     and replace it with the appropriate representation so the new
//     UnmarshalJSON interprets it correctly.
func removeLegacyBodyEncodingAndDecodeBody(data []byte, reqEncoding, respEncoding string) []byte {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return data
	}

	if req, ok := raw["request"]; ok {
		raw["request"] = fixLegacyEndpoint(req, reqEncoding)
	}
	if resp, ok := raw["response"]; ok {
		raw["response"] = fixLegacyEndpoint(resp, respEncoding)
	}

	out, err := json.Marshal(raw)
	if err != nil {
		return data
	}
	return out
}

// fixLegacyEndpoint removes body_encoding from an endpoint JSON object and,
// when the legacy encoding is "base64" and the Content-Type is textual,
// decodes the base64 body to a plain string so the new format handles it
// correctly.
func fixLegacyEndpoint(raw json.RawMessage, encoding string) json.RawMessage {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return raw
	}

	// Remove the legacy field.
	delete(fields, "body_encoding")

	if encoding != "base64" {
		out, err := json.Marshal(fields)
		if err != nil {
			return raw
		}
		return out
	}

	// Get the body value.
	bodyRaw, ok := fields["body"]
	if !ok || len(bodyRaw) == 0 || string(bodyRaw) == "null" {
		out, err := json.Marshal(fields)
		if err != nil {
			return raw
		}
		return out
	}

	// Body should be a JSON string containing base64-encoded data.
	var b64 string
	if err := json.Unmarshal(bodyRaw, &b64); err != nil {
		// Not a string; leave as-is.
		out, _ := json.Marshal(fields)
		return out
	}

	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		// Invalid base64; leave as-is.
		out, _ := json.Marshal(fields)
		return out
	}

	// Determine Content-Type to decide how to represent the decoded body.
	ct := extractContentType(fields)
	mt, parseErr := httptape.ParseMediaType(ct)

	if parseErr == nil && httptape.IsJSON(mt) {
		// JSON: write as native JSON value.
		if json.Valid(decoded) {
			fields["body"] = json.RawMessage(decoded)
		} else {
			// Invalid JSON bytes despite JSON CT: re-encode as base64 string.
			reEncoded, _ := json.Marshal(b64)
			fields["body"] = json.RawMessage(reEncoded)
		}
	} else if parseErr == nil && httptape.IsText(mt) {
		// Text: write as JSON string.
		encoded, _ := json.Marshal(string(decoded))
		fields["body"] = json.RawMessage(encoded)
	} else {
		// Binary or unknown: keep as base64 string (the new format's convention).
		// Already a base64 string, so leave bodyRaw as-is.
	}

	out, _ := json.Marshal(fields)
	return out
}

// extractContentType reads the Content-Type from a headers field within a
// request or response JSON object.
func extractContentType(fields map[string]json.RawMessage) string {
	headersRaw, ok := fields["headers"]
	if !ok {
		return ""
	}
	var headers map[string][]string
	if err := json.Unmarshal(headersRaw, &headers); err != nil {
		return ""
	}
	// http.Header uses canonical form, but JSON fixtures may vary.
	for k, v := range headers {
		if strings.EqualFold(k, "content-type") && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

func runMigrateFixtures(args []string) error {
	fs := flag.NewFlagSet("httptape migrate-fixtures", flag.ContinueOnError)
	recursive := fs.Bool("recursive", false, "Recurse into subdirectories")

	if err := fs.Parse(args); err != nil {
		return &usageError{err}
	}

	dir := fs.Arg(0)
	if dir == "" {
		fs.Usage()
		return &usageError{fmt.Errorf("<dir> argument is required")}
	}

	// Resolve to absolute path for consistent logging.
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	info, err := os.Stat(absDir)
	if err != nil {
		return fmt.Errorf("stat directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", absDir)
	}

	var migrated, skipped, errored int

	walkFn := func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			logger.Printf("walk error: %s: %v", path, walkErr)
			errored++
			return nil // continue walking
		}

		if d.IsDir() {
			// If not recursive, skip subdirectories (but not the root).
			if !*recursive && path != absDir {
				return filepath.SkipDir
			}
			return nil
		}

		if filepath.Ext(path) != ".json" {
			return nil
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			logger.Printf("skip (read error): %s: %v", path, readErr)
			errored++
			return nil
		}

		out, migrateErr := migrateLegacyFixture(data)
		if migrateErr != nil {
			logger.Printf("skip (not a tape): %s", path)
			skipped++
			return nil
		}

		if writeErr := os.WriteFile(path, out, 0644); writeErr != nil {
			logger.Printf("error (write): %s: %v", path, writeErr)
			errored++
			return nil
		}

		migrated++
		return nil
	}

	if walkErr := filepath.WalkDir(absDir, walkFn); walkErr != nil {
		return fmt.Errorf("walk directory: %w", walkErr)
	}

	logger.Printf("migrate-fixtures: %d migrated, %d skipped, %d errors", migrated, skipped, errored)

	if errored > 0 {
		return fmt.Errorf("%d files had errors during migration", errored)
	}
	return nil
}
