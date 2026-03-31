package httptape

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"
)

// saveTestTapes saves all tapes to the store using context.Background().
func saveTestTapes(t *testing.T, s Store, tapes ...Tape) {
	t.Helper()
	ctx := context.Background()
	for _, tape := range tapes {
		if err := s.Save(ctx, tape); err != nil {
			t.Fatalf("failed to save tape %s: %v", tape.ID, err)
		}
	}
}

// readBundle fully reads an export reader and returns parsed tar entries keyed by name.
func readBundle(t *testing.T, r io.Reader) map[string][]byte {
	t.Helper()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("failed to read bundle: %v", err)
	}

	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("failed to create gzip reader: %v", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	entries := make(map[string][]byte)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("failed to read tar entry: %v", err)
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("failed to read tar entry content %s: %v", hdr.Name, err)
		}
		entries[hdr.Name] = content
	}
	return entries
}

func makeBundleTape(id, route, method, url string) Tape {
	return Tape{
		ID:         id,
		Route:      route,
		RecordedAt: time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
		Request: RecordedReq{
			Method:  method,
			URL:     url,
			Headers: http.Header{"Content-Type": {"application/json"}},
			Body:    []byte(`{"key":"value"}`),
		},
		Response: RecordedResp{
			StatusCode: 200,
			Headers:    http.Header{"Content-Type": {"application/json"}},
			Body:       []byte(`{"result":"ok"}`),
		},
	}
}

func TestExportBundle_WithFixtures(t *testing.T) {
	store := NewMemoryStore()
	tape1 := makeBundleTape("tape-001", "users-api", "GET", "https://api.example.com/users")
	tape2 := makeBundleTape("tape-002", "users-api", "POST", "https://api.example.com/users")
	tape3 := makeBundleTape("tape-003", "auth-service", "POST", "https://auth.example.com/token")
	saveTestTapes(t, store, tape1, tape2, tape3)

	r, err := ExportBundle(context.Background(), store)
	if err != nil {
		t.Fatalf("ExportBundle() error: %v", err)
	}

	entries := readBundle(t, r)

	// Verify manifest
	manifestData, ok := entries["manifest.json"]
	if !ok {
		t.Fatal("manifest.json not found in bundle")
	}

	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("failed to unmarshal manifest: %v", err)
	}

	if manifest.FixtureCount != 3 {
		t.Errorf("manifest.FixtureCount = %d, want 3", manifest.FixtureCount)
	}
	if manifest.ExportedAt.IsZero() {
		t.Error("manifest.ExportedAt is zero")
	}

	wantRoutes := []string{"auth-service", "users-api"}
	if len(manifest.Routes) != len(wantRoutes) {
		t.Fatalf("manifest.Routes = %v, want %v", manifest.Routes, wantRoutes)
	}
	for i, r := range manifest.Routes {
		if r != wantRoutes[i] {
			t.Errorf("manifest.Routes[%d] = %q, want %q", i, r, wantRoutes[i])
		}
	}

	// Verify each fixture file exists and round-trips correctly
	for _, original := range []Tape{tape1, tape2, tape3} {
		name := "fixtures/" + original.ID + ".json"
		data, ok := entries[name]
		if !ok {
			t.Errorf("fixture file %s not found in bundle", name)
			continue
		}

		var got Tape
		if err := json.Unmarshal(data, &got); err != nil {
			t.Errorf("failed to unmarshal %s: %v", name, err)
			continue
		}

		if got.ID != original.ID {
			t.Errorf("fixture %s: ID = %q, want %q", name, got.ID, original.ID)
		}
		if got.Route != original.Route {
			t.Errorf("fixture %s: Route = %q, want %q", name, got.Route, original.Route)
		}
		if got.Request.Method != original.Request.Method {
			t.Errorf("fixture %s: Method = %q, want %q", name, got.Request.Method, original.Request.Method)
		}
		if got.Request.URL != original.Request.URL {
			t.Errorf("fixture %s: URL = %q, want %q", name, got.Request.URL, original.Request.URL)
		}
		if got.Response.StatusCode != original.Response.StatusCode {
			t.Errorf("fixture %s: StatusCode = %d, want %d", name, got.Response.StatusCode, original.Response.StatusCode)
		}
		if !bytes.Equal(got.Response.Body, original.Response.Body) {
			t.Errorf("fixture %s: Body mismatch", name)
		}
	}

	// Verify total entry count: 1 manifest + 3 fixtures
	if len(entries) != 4 {
		t.Errorf("bundle has %d entries, want 4", len(entries))
	}
}

func TestExportBundle_Empty(t *testing.T) {
	store := NewMemoryStore()

	r, err := ExportBundle(context.Background(), store)
	if err != nil {
		t.Fatalf("ExportBundle() error: %v", err)
	}

	entries := readBundle(t, r)

	if len(entries) != 1 {
		t.Fatalf("bundle has %d entries, want 1 (manifest only)", len(entries))
	}

	manifestData, ok := entries["manifest.json"]
	if !ok {
		t.Fatal("manifest.json not found in bundle")
	}

	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("failed to unmarshal manifest: %v", err)
	}

	if manifest.FixtureCount != 0 {
		t.Errorf("manifest.FixtureCount = %d, want 0", manifest.FixtureCount)
	}
	if len(manifest.Routes) != 0 {
		t.Errorf("manifest.Routes = %v, want empty", manifest.Routes)
	}
	if manifest.Routes == nil {
		t.Error("manifest.Routes is nil, want non-nil empty slice")
	}
}

func TestExportBundle_ManifestRoutes(t *testing.T) {
	store := NewMemoryStore()

	// Save tapes with duplicate routes and verify deduplication + sorting
	tapes := []Tape{
		makeBundleTape("t1", "zebra", "GET", "http://z.test/1"),
		makeBundleTape("t2", "alpha", "GET", "http://a.test/1"),
		makeBundleTape("t3", "zebra", "POST", "http://z.test/2"),
		makeBundleTape("t4", "middle", "GET", "http://m.test/1"),
		makeBundleTape("t5", "alpha", "DELETE", "http://a.test/1"),
	}
	saveTestTapes(t, store, tapes...)

	r, err := ExportBundle(context.Background(), store)
	if err != nil {
		t.Fatalf("ExportBundle() error: %v", err)
	}

	entries := readBundle(t, r)
	var manifest Manifest
	if err := json.Unmarshal(entries["manifest.json"], &manifest); err != nil {
		t.Fatalf("failed to unmarshal manifest: %v", err)
	}

	wantRoutes := []string{"alpha", "middle", "zebra"}
	if len(manifest.Routes) != len(wantRoutes) {
		t.Fatalf("manifest.Routes = %v, want %v", manifest.Routes, wantRoutes)
	}
	for i, r := range manifest.Routes {
		if r != wantRoutes[i] {
			t.Errorf("manifest.Routes[%d] = %q, want %q", i, r, wantRoutes[i])
		}
	}

	// Verify routes are sorted
	if !sort.StringsAreSorted(manifest.Routes) {
		t.Errorf("manifest.Routes not sorted: %v", manifest.Routes)
	}
}

func TestExportBundle_WithSanitizerConfig(t *testing.T) {
	store := NewMemoryStore()
	saveTestTapes(t, store, makeBundleTape("t1", "api", "GET", "http://test/1"))

	summary := "headers: Authorization, Cookie"
	r, err := ExportBundle(context.Background(), store, WithSanitizerConfig(summary))
	if err != nil {
		t.Fatalf("ExportBundle() error: %v", err)
	}

	entries := readBundle(t, r)
	var manifest Manifest
	if err := json.Unmarshal(entries["manifest.json"], &manifest); err != nil {
		t.Fatalf("failed to unmarshal manifest: %v", err)
	}

	if manifest.SanitizerConfig != summary {
		t.Errorf("manifest.SanitizerConfig = %q, want %q", manifest.SanitizerConfig, summary)
	}
}

func TestExportBundle_ContextCancel(t *testing.T) {
	store := NewMemoryStore()
	// Save enough tapes to increase likelihood the goroutine checks context
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("tape-%03d", i)
		saveTestTapes(t, store, makeBundleTape(
			id, "route", "GET", fmt.Sprintf("http://test/%d", i),
		))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	r, err := ExportBundle(ctx, store)
	if err != nil {
		// List failed due to cancelled context — this is acceptable
		return
	}

	// If ExportBundle returned a reader, reading it should yield an error
	_, readErr := io.ReadAll(r)
	if readErr == nil {
		t.Error("expected error reading from cancelled export, got nil")
	}
}

func TestExportBundle_ValidGzip(t *testing.T) {
	store := NewMemoryStore()
	saveTestTapes(t, store,
		makeBundleTape("t1", "api", "GET", "http://test/1"),
		makeBundleTape("t2", "api", "POST", "http://test/2"),
	)

	r, err := ExportBundle(context.Background(), store)
	if err != nil {
		t.Fatalf("ExportBundle() error: %v", err)
	}

	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("failed to read bundle: %v", err)
	}

	// Verify valid gzip
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("not valid gzip: %v", err)
	}

	// Verify valid tar
	tr := tar.NewReader(gr)
	entryCount := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("invalid tar entry: %v", err)
		}
		// Drain the entry to advance the reader
		if _, err := io.Copy(io.Discard, tr); err != nil {
			t.Fatalf("failed to read tar entry %s: %v", hdr.Name, err)
		}
		entryCount++
	}

	gr.Close()

	// 1 manifest + 2 fixtures
	if entryCount != 3 {
		t.Errorf("tar entry count = %d, want 3", entryCount)
	}
}

func TestExportBundle_TarEntryHeaders(t *testing.T) {
	store := NewMemoryStore()
	tape := makeBundleTape("tape-hdr", "api", "GET", "http://test/1")
	saveTestTapes(t, store, tape)

	r, err := ExportBundle(context.Background(), store)
	if err != nil {
		t.Fatalf("ExportBundle() error: %v", err)
	}

	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("failed to read bundle: %v", err)
	}

	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("not valid gzip: %v", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("invalid tar entry: %v", err)
		}

		if hdr.Typeflag != tar.TypeReg {
			t.Errorf("entry %s: Typeflag = %d, want %d", hdr.Name, hdr.Typeflag, tar.TypeReg)
		}
		if hdr.Mode != 0o644 {
			t.Errorf("entry %s: Mode = %o, want 644", hdr.Name, hdr.Mode)
		}

		// Drain entry
		io.Copy(io.Discard, tr)
	}
}

// buildTestBundle creates a tar.gz bundle in memory from raw entries.
// Each entry is a name/content pair. Useful for constructing invalid bundles.
func buildTestBundle(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for name, content := range entries {
		err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		})
		if err != nil {
			t.Fatalf("failed to write tar header for %s: %v", name, err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatalf("failed to write tar content for %s: %v", name, err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("failed to close tar writer: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("failed to close gzip writer: %v", err)
	}
	return buf.Bytes()
}

// buildTestBundleOrdered creates a tar.gz bundle preserving entry order.
func buildTestBundleOrdered(t *testing.T, names []string, contents [][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for i, name := range names {
		err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(contents[i])),
			Typeflag: tar.TypeReg,
		})
		if err != nil {
			t.Fatalf("failed to write tar header for %s: %v", name, err)
		}
		if _, err := tw.Write(contents[i]); err != nil {
			t.Fatalf("failed to write tar content for %s: %v", name, err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("failed to close tar writer: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("failed to close gzip writer: %v", err)
	}
	return buf.Bytes()
}

func TestImportBundle_IntoEmptyStore(t *testing.T) {
	// Create source store with 3 tapes, export, then import into empty store.
	srcStore := NewMemoryStore()
	tape1 := makeBundleTape("tape-001", "users-api", "GET", "https://api.example.com/users")
	tape2 := makeBundleTape("tape-002", "users-api", "POST", "https://api.example.com/users")
	tape3 := makeBundleTape("tape-003", "auth-service", "POST", "https://auth.example.com/token")
	saveTestTapes(t, srcStore, tape1, tape2, tape3)

	r, err := ExportBundle(context.Background(), srcStore)
	if err != nil {
		t.Fatalf("ExportBundle() error: %v", err)
	}

	// Read the bundle fully so we can create a reader for import.
	bundleData, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("failed to read bundle: %v", err)
	}

	dstStore := NewMemoryStore()
	err = ImportBundle(context.Background(), dstStore, bytes.NewReader(bundleData))
	if err != nil {
		t.Fatalf("ImportBundle() error: %v", err)
	}

	// Verify all 3 tapes are present.
	tapes, err := dstStore.List(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(tapes) != 3 {
		t.Fatalf("got %d tapes, want 3", len(tapes))
	}

	// Verify each tape by loading it.
	for _, original := range []Tape{tape1, tape2, tape3} {
		got, err := dstStore.Load(context.Background(), original.ID)
		if err != nil {
			t.Errorf("Load(%s) error: %v", original.ID, err)
			continue
		}
		if got.ID != original.ID {
			t.Errorf("tape %s: ID = %q, want %q", original.ID, got.ID, original.ID)
		}
		if got.Request.Method != original.Request.Method {
			t.Errorf("tape %s: Method = %q, want %q", original.ID, got.Request.Method, original.Request.Method)
		}
		if got.Request.URL != original.Request.URL {
			t.Errorf("tape %s: URL = %q, want %q", original.ID, got.Request.URL, original.Request.URL)
		}
		if got.Response.StatusCode != original.Response.StatusCode {
			t.Errorf("tape %s: StatusCode = %d, want %d", original.ID, got.Response.StatusCode, original.Response.StatusCode)
		}
	}
}

func TestImportBundle_MergeOverwrite(t *testing.T) {
	ctx := context.Background()

	// Pre-populate destination store with tape A and tape B.
	dstStore := NewMemoryStore()
	tapeA := makeBundleTape("tape-A", "api", "GET", "https://api.example.com/a")
	tapeB := makeBundleTape("tape-B", "api", "GET", "https://api.example.com/b")
	saveTestTapes(t, dstStore, tapeA, tapeB)

	// Create bundle with modified tape A and new tape C.
	bundleStore := NewMemoryStore()
	tapeAModified := makeBundleTape("tape-A", "api", "GET", "https://api.example.com/a")
	tapeAModified.Response.StatusCode = 404
	tapeAModified.Response.Body = []byte(`{"error":"not found"}`)
	tapeC := makeBundleTape("tape-C", "api", "POST", "https://api.example.com/c")
	saveTestTapes(t, bundleStore, tapeAModified, tapeC)

	r, err := ExportBundle(ctx, bundleStore)
	if err != nil {
		t.Fatalf("ExportBundle() error: %v", err)
	}
	bundleData, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("failed to read bundle: %v", err)
	}

	// Import bundle into destination.
	err = ImportBundle(ctx, dstStore, bytes.NewReader(bundleData))
	if err != nil {
		t.Fatalf("ImportBundle() error: %v", err)
	}

	// Tape A should be overwritten.
	gotA, err := dstStore.Load(ctx, "tape-A")
	if err != nil {
		t.Fatalf("Load(tape-A) error: %v", err)
	}
	if gotA.Response.StatusCode != 404 {
		t.Errorf("tape-A StatusCode = %d, want 404 (overwritten)", gotA.Response.StatusCode)
	}
	if !bytes.Equal(gotA.Response.Body, []byte(`{"error":"not found"}`)) {
		t.Errorf("tape-A Body not overwritten")
	}

	// Tape B should still exist (untouched).
	gotB, err := dstStore.Load(ctx, "tape-B")
	if err != nil {
		t.Fatalf("Load(tape-B) error: %v", err)
	}
	if gotB.Response.StatusCode != 200 {
		t.Errorf("tape-B StatusCode = %d, want 200 (untouched)", gotB.Response.StatusCode)
	}

	// Tape C should be new.
	gotC, err := dstStore.Load(ctx, "tape-C")
	if err != nil {
		t.Fatalf("Load(tape-C) error: %v", err)
	}
	if gotC.Request.Method != "POST" {
		t.Errorf("tape-C Method = %q, want POST", gotC.Request.Method)
	}

	// Total should be 3.
	all, _ := dstStore.List(ctx, Filter{})
	if len(all) != 3 {
		t.Errorf("total tapes = %d, want 3", len(all))
	}
}

func TestImportBundle_MalformedGzip(t *testing.T) {
	dstStore := NewMemoryStore()
	err := ImportBundle(context.Background(), dstStore, strings.NewReader("not gzip"))
	if err == nil {
		t.Fatal("expected error for malformed gzip, got nil")
	}
	if !strings.Contains(err.Error(), "httptape: import:") {
		t.Errorf("error missing prefix: %v", err)
	}
}

func TestImportBundle_InvalidManifest(t *testing.T) {
	bundle := buildTestBundle(t, map[string][]byte{
		"manifest.json": []byte("this is not json{{{"),
	})

	dstStore := NewMemoryStore()
	err := ImportBundle(context.Background(), dstStore, bytes.NewReader(bundle))
	if err == nil {
		t.Fatal("expected error for invalid manifest JSON, got nil")
	}
	if !strings.Contains(err.Error(), "invalid manifest") {
		t.Errorf("error should mention 'invalid manifest': %v", err)
	}
}

func TestImportBundle_MissingManifest(t *testing.T) {
	tape := makeBundleTape("tape-1", "api", "GET", "http://test/1")
	tapeJSON, _ := json.Marshal(tape)

	bundle := buildTestBundle(t, map[string][]byte{
		"fixtures/tape-1.json": tapeJSON,
	})

	dstStore := NewMemoryStore()
	err := ImportBundle(context.Background(), dstStore, bytes.NewReader(bundle))
	if err == nil {
		t.Fatal("expected error for missing manifest, got nil")
	}
	if !strings.Contains(err.Error(), "missing manifest.json") {
		t.Errorf("error should mention 'missing manifest.json': %v", err)
	}
}

func TestImportBundle_FixtureCountMismatch(t *testing.T) {
	tape := makeBundleTape("tape-1", "api", "GET", "http://test/1")
	tapeJSON, _ := json.Marshal(tape)

	manifest := Manifest{
		ExportedAt:   time.Now().UTC(),
		FixtureCount: 5, // Declares 5 but only 1 fixture present.
		Routes:       []string{"api"},
	}
	manifestJSON, _ := json.Marshal(manifest)

	bundle := buildTestBundleOrdered(t,
		[]string{"manifest.json", "fixtures/tape-1.json"},
		[][]byte{manifestJSON, tapeJSON},
	)

	dstStore := NewMemoryStore()
	err := ImportBundle(context.Background(), dstStore, bytes.NewReader(bundle))
	if err == nil {
		t.Fatal("expected error for fixture count mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "manifest declares 5 fixtures but bundle contains 1") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestImportBundle_InvalidFixture(t *testing.T) {
	manifest := Manifest{
		ExportedAt:   time.Now().UTC(),
		FixtureCount: 1,
		Routes:       []string{},
	}
	manifestJSON, _ := json.Marshal(manifest)

	bundle := buildTestBundleOrdered(t,
		[]string{"manifest.json", "fixtures/bad.json"},
		[][]byte{manifestJSON, []byte("not valid json!!!")},
	)

	dstStore := NewMemoryStore()
	err := ImportBundle(context.Background(), dstStore, bytes.NewReader(bundle))
	if err == nil {
		t.Fatal("expected error for invalid fixture JSON, got nil")
	}
	if !strings.Contains(err.Error(), "invalid fixture") {
		t.Errorf("error should mention 'invalid fixture': %v", err)
	}
}

func TestImportBundle_EmptyBundle(t *testing.T) {
	// Export from empty store, import into another empty store.
	srcStore := NewMemoryStore()
	r, err := ExportBundle(context.Background(), srcStore)
	if err != nil {
		t.Fatalf("ExportBundle() error: %v", err)
	}
	bundleData, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("failed to read bundle: %v", err)
	}

	dstStore := NewMemoryStore()
	err = ImportBundle(context.Background(), dstStore, bytes.NewReader(bundleData))
	if err != nil {
		t.Fatalf("ImportBundle() error: %v", err)
	}

	tapes, err := dstStore.List(context.Background(), Filter{})
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(tapes) != 0 {
		t.Errorf("got %d tapes, want 0", len(tapes))
	}
}

func TestImportBundle_RoundTrip(t *testing.T) {
	ctx := context.Background()

	srcStore := NewMemoryStore()
	origTapes := []Tape{
		makeBundleTape("rt-001", "users", "GET", "https://api.example.com/users"),
		makeBundleTape("rt-002", "users", "POST", "https://api.example.com/users"),
		makeBundleTape("rt-003", "auth", "POST", "https://auth.example.com/token"),
		makeBundleTape("rt-004", "", "DELETE", "https://api.example.com/users/1"),
		makeBundleTape("rt-005", "billing", "PUT", "https://billing.example.com/invoice"),
	}
	saveTestTapes(t, srcStore, origTapes...)

	// Export
	r, err := ExportBundle(ctx, srcStore)
	if err != nil {
		t.Fatalf("ExportBundle() error: %v", err)
	}
	bundleData, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("failed to read bundle: %v", err)
	}

	// Import into fresh store
	dstStore := NewMemoryStore()
	err = ImportBundle(ctx, dstStore, bytes.NewReader(bundleData))
	if err != nil {
		t.Fatalf("ImportBundle() error: %v", err)
	}

	// Compare all tapes
	dstTapes, err := dstStore.List(ctx, Filter{})
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(dstTapes) != len(origTapes) {
		t.Fatalf("got %d tapes, want %d", len(dstTapes), len(origTapes))
	}

	// Build a map for easy lookup.
	dstMap := make(map[string]Tape)
	for _, tape := range dstTapes {
		dstMap[tape.ID] = tape
	}

	for _, orig := range origTapes {
		got, ok := dstMap[orig.ID]
		if !ok {
			t.Errorf("tape %s not found in destination store", orig.ID)
			continue
		}
		if got.Route != orig.Route {
			t.Errorf("tape %s: Route = %q, want %q", orig.ID, got.Route, orig.Route)
		}
		if got.Request.Method != orig.Request.Method {
			t.Errorf("tape %s: Method = %q, want %q", orig.ID, got.Request.Method, orig.Request.Method)
		}
		if got.Request.URL != orig.Request.URL {
			t.Errorf("tape %s: URL = %q, want %q", orig.ID, got.Request.URL, orig.Request.URL)
		}
		if got.Response.StatusCode != orig.Response.StatusCode {
			t.Errorf("tape %s: StatusCode = %d, want %d", orig.ID, got.Response.StatusCode, orig.Response.StatusCode)
		}
		if !bytes.Equal(got.Request.Body, orig.Request.Body) {
			t.Errorf("tape %s: Request.Body mismatch", orig.ID)
		}
		if !bytes.Equal(got.Response.Body, orig.Response.Body) {
			t.Errorf("tape %s: Response.Body mismatch", orig.ID)
		}
		if !got.RecordedAt.Equal(orig.RecordedAt) {
			t.Errorf("tape %s: RecordedAt = %v, want %v", orig.ID, got.RecordedAt, orig.RecordedAt)
		}
	}
}

func TestImportBundle_ContextCancellation(t *testing.T) {
	// Create a valid bundle first.
	srcStore := NewMemoryStore()
	saveTestTapes(t, srcStore, makeBundleTape("tape-1", "api", "GET", "http://test/1"))

	r, err := ExportBundle(context.Background(), srcStore)
	if err != nil {
		t.Fatalf("ExportBundle() error: %v", err)
	}
	bundleData, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("failed to read bundle: %v", err)
	}

	// Import with an already-cancelled context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dstStore := NewMemoryStore()
	err = ImportBundle(ctx, dstStore, bytes.NewReader(bundleData))
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("error should mention context canceled: %v", err)
	}
}

func TestImportBundle_FixtureEmptyID(t *testing.T) {
	tape := makeBundleTape("", "api", "GET", "http://test/1")
	tapeJSON, _ := json.Marshal(tape)

	manifest := Manifest{
		ExportedAt:   time.Now().UTC(),
		FixtureCount: 1,
		Routes:       []string{"api"},
	}
	manifestJSON, _ := json.Marshal(manifest)

	bundle := buildTestBundleOrdered(t,
		[]string{"manifest.json", "fixtures/bad.json"},
		[][]byte{manifestJSON, tapeJSON},
	)

	dstStore := NewMemoryStore()
	err := ImportBundle(context.Background(), dstStore, bytes.NewReader(bundle))
	if err == nil {
		t.Fatal("expected error for empty fixture ID, got nil")
	}
	if !strings.Contains(err.Error(), "fixture has empty ID") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestImportBundle_FixtureEmptyMethod(t *testing.T) {
	tape := makeBundleTape("tape-1", "api", "", "http://test/1")
	tapeJSON, _ := json.Marshal(tape)

	manifest := Manifest{
		ExportedAt:   time.Now().UTC(),
		FixtureCount: 1,
		Routes:       []string{"api"},
	}
	manifestJSON, _ := json.Marshal(manifest)

	bundle := buildTestBundleOrdered(t,
		[]string{"manifest.json", "fixtures/tape-1.json"},
		[][]byte{manifestJSON, tapeJSON},
	)

	dstStore := NewMemoryStore()
	err := ImportBundle(context.Background(), dstStore, bytes.NewReader(bundle))
	if err == nil {
		t.Fatal("expected error for empty request method, got nil")
	}
	if !strings.Contains(err.Error(), "empty request method") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestImportBundle_FixtureEmptyURL(t *testing.T) {
	tape := makeBundleTape("tape-1", "api", "GET", "")
	tapeJSON, _ := json.Marshal(tape)

	manifest := Manifest{
		ExportedAt:   time.Now().UTC(),
		FixtureCount: 1,
		Routes:       []string{"api"},
	}
	manifestJSON, _ := json.Marshal(manifest)

	bundle := buildTestBundleOrdered(t,
		[]string{"manifest.json", "fixtures/tape-1.json"},
		[][]byte{manifestJSON, tapeJSON},
	)

	dstStore := NewMemoryStore()
	err := ImportBundle(context.Background(), dstStore, bytes.NewReader(bundle))
	if err == nil {
		t.Fatal("expected error for empty request URL, got nil")
	}
	if !strings.Contains(err.Error(), "empty request URL") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestImportBundle_UnknownEntriesSkipped(t *testing.T) {
	// Bundle with manifest, a fixture, and an unknown entry — should succeed.
	tape := makeBundleTape("tape-1", "api", "GET", "http://test/1")
	tapeJSON, _ := json.Marshal(tape)

	manifest := Manifest{
		ExportedAt:   time.Now().UTC(),
		FixtureCount: 1,
		Routes:       []string{"api"},
	}
	manifestJSON, _ := json.Marshal(manifest)

	bundle := buildTestBundleOrdered(t,
		[]string{"manifest.json", "metadata/extra.txt", "fixtures/tape-1.json"},
		[][]byte{manifestJSON, []byte("some future metadata"), tapeJSON},
	)

	dstStore := NewMemoryStore()
	err := ImportBundle(context.Background(), dstStore, bytes.NewReader(bundle))
	if err != nil {
		t.Fatalf("ImportBundle() error: %v", err)
	}

	got, err := dstStore.Load(context.Background(), "tape-1")
	if err != nil {
		t.Fatalf("Load(tape-1) error: %v", err)
	}
	if got.Request.Method != "GET" {
		t.Errorf("tape-1 Method = %q, want GET", got.Request.Method)
	}
}

func TestExportBundle_EmptyRouteExcluded(t *testing.T) {
	store := NewMemoryStore()
	// Tape with empty route should not appear in manifest routes
	saveTestTapes(t, store, makeBundleTape("t1", "", "GET", "http://test/1"))

	r, err := ExportBundle(context.Background(), store)
	if err != nil {
		t.Fatalf("ExportBundle() error: %v", err)
	}

	entries := readBundle(t, r)
	var manifest Manifest
	if err := json.Unmarshal(entries["manifest.json"], &manifest); err != nil {
		t.Fatalf("failed to unmarshal manifest: %v", err)
	}

	if len(manifest.Routes) != 0 {
		t.Errorf("manifest.Routes = %v, want empty (tape has empty route)", manifest.Routes)
	}
	if manifest.FixtureCount != 1 {
		t.Errorf("manifest.FixtureCount = %d, want 1", manifest.FixtureCount)
	}
}
