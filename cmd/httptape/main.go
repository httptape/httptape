// Package main implements the httptape CLI binary.
//
// httptape is a standalone command-line tool for HTTP traffic recording,
// sanitization, and replay. It is a thin wrapper over the httptape library.
//
// Commands:
//
//	httptape serve    — Replay recorded fixtures as a mock HTTP server
//	httptape record   — Proxy requests to upstream, record and sanitize responses
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

const usageText = `httptape — HTTP traffic recording, sanitization, and replay

Usage:
  httptape <command> [flags]

Commands:
  serve    Replay recorded fixtures as a mock HTTP server
  record   Proxy requests to upstream, record and sanitize responses
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

	server := httptape.NewServer(store, httptape.WithFallbackStatus(*fallbackStatus))

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

	var recorderOpts []httptape.RecorderOption
	recorderOpts = append(recorderOpts, httptape.WithAsync(true))
	recorderOpts = append(recorderOpts, httptape.WithOnError(func(err error) {
		logger.Printf("recorder error: %v", err)
	}))

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

	addr := fmt.Sprintf(":%d", *port)
	httpServer := &http.Server{
		Addr:    addr,
		Handler: proxy,
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
