// Package main implements the httptape CLI binary.
//
// httptape is a standalone command-line tool for HTTP traffic recording,
// sanitization, and replay. It is a thin wrapper over the httptape library.
//
// Commands:
//
//	httptape serve    — Replay recorded fixtures as a mock HTTP server
//	httptape record   — Proxy requests to upstream, record and sanitize responses
//	httptape proxy    — Forward to upstream with L1/L2 fallback-to-cache
//	httptape export   — Export fixtures to a tar.gz bundle
//	httptape import   — Import fixtures from a tar.gz bundle
package main

import (
	"context"
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
  serve    Replay recorded fixtures as a mock HTTP server
  record   Proxy requests to upstream, record and sanitize responses
  proxy    Forward to upstream with L1/L2 fallback-to-cache
  export   Export fixtures to a tar.gz bundle
  import   Import fixtures from a tar.gz bundle

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

func runServe(args []string) error {
	fs := flag.NewFlagSet("httptape serve", flag.ContinueOnError)
	fixtures := fs.String("fixtures", "", "Path to fixture directory (required)")
	port := fs.Int("port", 8081, "Listen port")
	fallbackStatus := fs.Int("fallback-status", 404, "HTTP status when no tape matches")
	_ = fs.String("config", "", "Path to sanitization config JSON (accepted but not used by serve)")
	cors := fs.Bool("cors", false, "Enable CORS headers (Access-Control-Allow-Origin: *)")
	delay := fs.Duration("delay", 0, "Fixed delay before every response (e.g., 200ms, 1s)")
	errorRate := fs.Float64("error-rate", 0, "Fraction of requests that return 500 (0.0-1.0)")
	var replayHeaders repeatableFlag
	fs.Var(&replayHeaders, "replay-header", "Header to inject into responses (Key=Value, repeatable)")

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

	server := httptape.NewServer(store, serverOpts...)

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

	tapeProxy := httptape.NewProxy(l1, l2, proxyOpts...)

	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(upstreamURL)
			r.Out.Host = upstreamURL.Host
		},
		Transport: tapeProxy,
	}

	var handler http.Handler = rp
	if *cors {
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, OPTIONS, HEAD")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, Accept")
			w.Header().Set("Access-Control-Max-Age", "86400")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			rp.ServeHTTP(w, r)
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
