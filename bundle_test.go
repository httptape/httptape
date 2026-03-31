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
