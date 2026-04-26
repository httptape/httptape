package httptape

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"
)

// maxEntrySize is the maximum allowed size for a single tar entry during import.
// This prevents denial-of-service from maliciously large bundles (zip-bomb style).
const maxEntrySize = 50 << 20 // 50 MB

// Manifest describes the contents and metadata of an exported bundle.
// It is serialized as the first entry (manifest.json) in the tar.gz archive.
type Manifest struct {
	// ExportedAt is the UTC timestamp when the bundle was created.
	ExportedAt time.Time `json:"exported_at"`

	// FixtureCount is the total number of fixture files in the bundle.
	FixtureCount int `json:"fixture_count"`

	// Routes is the deduplicated, sorted list of route labels present
	// in the exported fixtures.
	Routes []string `json:"routes"`

	// SanitizerConfig is an optional human-readable summary of the
	// sanitizer configuration that was active when the fixtures were
	// recorded. Empty string if unknown or not applicable.
	SanitizerConfig string `json:"sanitizer_config,omitempty"`
}

// ExportOption configures an ExportBundle call.
type ExportOption func(*exportConfig)

// exportConfig holds resolved options for ExportBundle.
type exportConfig struct {
	sanitizerConfig string
	routes          []string  // nil = no route filter
	methods         []string  // nil = no method filter; stored uppercase
	since           time.Time // zero = no time filter
}

// WithSanitizerConfig attaches a human-readable sanitizer configuration
// summary to the bundle manifest.
func WithSanitizerConfig(summary string) ExportOption {
	return func(cfg *exportConfig) {
		cfg.sanitizerConfig = summary
	}
}

// WithRoutes filters the export to include only tapes whose Route field
// matches one of the specified route names. If no routes are specified,
// this option is a no-op. Route matching is exact (case-sensitive).
func WithRoutes(routes ...string) ExportOption {
	return func(cfg *exportConfig) {
		cfg.routes = routes
	}
}

// WithMethods filters the export to include only tapes whose HTTP method
// matches one of the specified methods. Methods are compared case-insensitively
// (normalized to uppercase). If no methods are specified, this option is a no-op.
func WithMethods(methods ...string) ExportOption {
	return func(cfg *exportConfig) {
		normalized := make([]string, len(methods))
		for i, m := range methods {
			normalized[i] = strings.ToUpper(m)
		}
		cfg.methods = normalized
	}
}

// WithSince filters the export to include only tapes recorded at or after
// the given timestamp. The zero value of time.Time disables this filter.
func WithSince(t time.Time) ExportOption {
	return func(cfg *exportConfig) {
		cfg.since = t
	}
}

// ExportBundle exports all tapes from the given store as a tar.gz archive.
// The returned io.Reader streams the archive — it is not buffered entirely
// in memory. The caller must read the reader to completion or cancel the
// context to release resources.
//
// ExportBundle is safe for concurrent use — it is a stateless function.
// Concurrent safety of the underlying Store is the Store's responsibility.
//
// Bundle layout:
//
//	manifest.json          — bundle metadata (see Manifest type)
//	fixtures/<id>.json     — one file per tape, JSON-encoded
//
// The function uses Store.List with an empty filter to enumerate all tapes.
// Fixture files are named by tape ID and placed in a flat fixtures/ directory.
func ExportBundle(ctx context.Context, s Store, opts ...ExportOption) (io.Reader, error) {
	var cfg exportConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	tapes, err := s.List(ctx, Filter{})
	if err != nil {
		return nil, fmt.Errorf("httptape: export: %w", err)
	}

	tapes = filterTapes(tapes, cfg)

	pr, pw := io.Pipe()

	go func() {
		err := writeBundle(ctx, pw, tapes, cfg)
		pw.CloseWithError(err) // nil err means success
	}()

	return pr, nil
}

// writeBundle writes the full tar.gz archive to w. Returns nil on success.
// Writers are closed explicitly on the success path with error checking.
// On error paths, the deferred close ensures resources are released (the
// caller closes the pipe writer which propagates the error to readers).
func writeBundle(ctx context.Context, w io.Writer, tapes []Tape, cfg exportConfig) (retErr error) {
	gw := gzip.NewWriter(w)
	tw := tar.NewWriter(gw)

	// Deferred close only runs on error paths. On the success path,
	// the explicit close below handles finalization with error checking.
	defer func() {
		if retErr != nil {
			tw.Close()
			gw.Close()
		}
	}()

	manifest := buildManifest(tapes, cfg)

	// Write manifest.json
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("httptape: export: marshal manifest: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("httptape: export: %w", err)
	}

	err = tw.WriteHeader(&tar.Header{
		Name:     "manifest.json",
		Mode:     0o644,
		Size:     int64(len(manifestJSON)),
		ModTime:  manifest.ExportedAt,
		Typeflag: tar.TypeReg,
	})
	if err != nil {
		return fmt.Errorf("httptape: export: write manifest header: %w", err)
	}

	if _, err := tw.Write(manifestJSON); err != nil {
		return fmt.Errorf("httptape: export: write manifest: %w", err)
	}

	// Write each fixture
	for _, tape := range tapes {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("httptape: export: %w", err)
		}

		tapeJSON, err := json.MarshalIndent(tape, "", "  ")
		if err != nil {
			return fmt.Errorf("httptape: export: marshal tape %s: %w", tape.ID, err)
		}

		err = tw.WriteHeader(&tar.Header{
			Name:     "fixtures/" + tape.ID + ".json",
			Mode:     0o644,
			Size:     int64(len(tapeJSON)),
			ModTime:  tape.RecordedAt,
			Typeflag: tar.TypeReg,
		})
		if err != nil {
			return fmt.Errorf("httptape: export: write header for tape %s: %w", tape.ID, err)
		}

		if _, err := tw.Write(tapeJSON); err != nil {
			return fmt.Errorf("httptape: export: write tape %s: %w", tape.ID, err)
		}
	}

	// Explicit close in correct order with error checking (success path).
	if err := tw.Close(); err != nil {
		return fmt.Errorf("httptape: export: close tar: %w", err)
	}
	if err := gw.Close(); err != nil {
		return fmt.Errorf("httptape: export: close gzip: %w", err)
	}

	return nil
}

// ImportBundle imports tapes from a tar.gz bundle into the given store.
// The bundle must have been produced by ExportBundle (see Manifest for the format).
//
// ImportBundle is safe for concurrent use — it is a stateless function.
// Concurrent safety of the underlying Store is the Store's responsibility.
//
// Merge strategy: fixtures in the bundle overwrite any existing fixtures with
// the same ID in the store. Fixtures already in the store whose IDs are not
// present in the bundle are left untouched.
//
// The entire bundle is validated before any fixtures are persisted. If the
// manifest is missing, malformed, or any fixture fails JSON unmarshalling,
// ImportBundle returns an error and the store is not modified.
//
// ImportBundle does not re-sanitize imported tapes. Bundles produced by
// ExportBundle contain already-sanitized data, so re-sanitization would
// corrupt deterministically faked values. If you import a bundle from an
// untrusted or hand-edited source, validate its contents externally before
// import -- ImportBundle stores tapes exactly as they appear in the bundle.
func ImportBundle(ctx context.Context, s Store, r io.Reader) error {
	gr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("httptape: import: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)

	var manifest *Manifest
	var tapes []Tape

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("httptape: import: %w", err)
		}

		if err := ctx.Err(); err != nil {
			return fmt.Errorf("httptape: import: %w", err)
		}

		if err := validateEntryName(hdr.Name); err != nil {
			return err
		}

		lr := io.LimitReader(tr, maxEntrySize)

		switch {
		case hdr.Name == "manifest.json":
			var m Manifest
			if err := json.NewDecoder(lr).Decode(&m); err != nil {
				return fmt.Errorf("httptape: import: invalid manifest: %w", err)
			}
			manifest = &m

		case isFixtureEntry(hdr.Name):
			var t Tape
			if err := json.NewDecoder(lr).Decode(&t); err != nil {
				return fmt.Errorf("httptape: import: invalid fixture %q: %w", hdr.Name, err)
			}
			if err := validateFixture(t); err != nil {
				return err
			}
			tapes = append(tapes, t)
		}
		// Unknown entries are silently skipped (forward compatibility).
	}

	// Phase 1 validation
	if manifest == nil {
		return fmt.Errorf("httptape: import: missing manifest.json")
	}
	if manifest.FixtureCount != len(tapes) {
		return fmt.Errorf("httptape: import: manifest declares %d fixtures but bundle contains %d",
			manifest.FixtureCount, len(tapes))
	}

	// Phase 2 persist
	for _, t := range tapes {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("httptape: import: %w", err)
		}
		if err := s.Save(ctx, t); err != nil {
			return fmt.Errorf("httptape: import: save tape %s: %w", t.ID, err)
		}
	}

	return nil
}

// isFixtureEntry reports whether the tar entry name matches the fixture pattern.
func isFixtureEntry(name string) bool {
	return strings.HasPrefix(name, "fixtures/") && strings.HasSuffix(name, ".json")
}

// validateEntryName checks that a tar entry name does not contain path-traversal
// patterns. This is defense in depth: ImportBundle deserializes entries into
// in-memory Tape objects (not to the filesystem), and Store.Save implementations
// have their own path validation. However, since bundle parsing sits at the trust
// boundary for untrusted input, we reject suspicious names early to prevent any
// future code path from being exploitable.
func validateEntryName(name string) error {
	// Reject absolute paths (leading /).
	if strings.HasPrefix(name, "/") {
		return fmt.Errorf("httptape: import: tar entry %q has absolute path", name)
	}

	// Reject backslashes (Windows-style separators have no place in tar archives).
	if strings.ContainsRune(name, '\\') {
		return fmt.Errorf("httptape: import: tar entry %q contains backslash", name)
	}

	// Reject path-traversal: clean the path and check for ".." components.
	// We use path.Clean (not filepath.Clean) because tar paths use forward
	// slashes regardless of OS.
	cleaned := path.Clean(name)
	for _, segment := range strings.Split(cleaned, "/") {
		if segment == ".." {
			return fmt.Errorf("httptape: import: tar entry %q contains path traversal", name)
		}
	}

	return nil
}

// validateFixture checks that a tape has the minimum required fields for
// matching and replay.
func validateFixture(t Tape) error {
	if t.ID == "" {
		return fmt.Errorf("httptape: import: fixture has empty ID")
	}
	if t.Request.Method == "" {
		return fmt.Errorf("httptape: import: fixture %s has empty request method", t.ID)
	}
	if t.Request.URL == "" {
		return fmt.Errorf("httptape: import: fixture %s has empty request URL", t.ID)
	}
	return nil
}

// filterTapes returns the subset of tapes matching the export filters.
// All non-nil/non-zero filters are AND-ed: a tape must pass every active
// filter to be included. If no filters are set, all tapes are returned.
func filterTapes(tapes []Tape, cfg exportConfig) []Tape {
	if len(cfg.routes) == 0 && len(cfg.methods) == 0 && cfg.since.IsZero() {
		return tapes
	}

	result := make([]Tape, 0, len(tapes))
	for _, t := range tapes {
		if !matchesRouteFilter(t, cfg.routes) {
			continue
		}
		if !matchesMethodFilter(t, cfg.methods) {
			continue
		}
		if !cfg.since.IsZero() && t.RecordedAt.Before(cfg.since) {
			continue
		}
		result = append(result, t)
	}
	return result
}

// matchesRouteFilter returns true if the tape's route matches any of the
// specified routes, or if routes is empty (no filter).
func matchesRouteFilter(t Tape, routes []string) bool {
	if len(routes) == 0 {
		return true
	}
	for _, r := range routes {
		if t.Route == r {
			return true
		}
	}
	return false
}

// matchesMethodFilter returns true if the tape's HTTP method matches any of
// the specified methods, or if methods is empty (no filter).
// Methods in the slice are expected to already be uppercase.
func matchesMethodFilter(t Tape, methods []string) bool {
	if len(methods) == 0 {
		return true
	}
	m := strings.ToUpper(t.Request.Method)
	for _, allowed := range methods {
		if m == allowed {
			return true
		}
	}
	return false
}

// buildManifest constructs a Manifest from the given tapes and export configuration.
func buildManifest(tapes []Tape, cfg exportConfig) Manifest {
	routeSet := make(map[string]struct{})
	for _, t := range tapes {
		if t.Route != "" {
			routeSet[t.Route] = struct{}{}
		}
	}
	routes := make([]string, 0, len(routeSet))
	for r := range routeSet {
		routes = append(routes, r)
	}
	sort.Strings(routes)

	return Manifest{
		ExportedAt:      time.Now().UTC(),
		FixtureCount:    len(tapes),
		Routes:          routes,
		SanitizerConfig: cfg.sanitizerConfig,
	}
}
