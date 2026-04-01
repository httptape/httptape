package httptape

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFileStore_NewFileStore_CreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "subdir", "fixtures")
	_, err := NewFileStore(WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("os.Stat(%q) error = %v", dir, err)
	}
	if !info.IsDir() {
		t.Errorf("%q is not a directory", dir)
	}
}

func TestFileStore_Save(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileStore(WithDirectory(t.TempDir()))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tape := makeTape("users-api", "GET", "http://example.com/users")
	if err := store.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, err := store.Load(ctx, tape.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if loaded.ID != tape.ID {
		t.Errorf("Load().ID = %q, want %q", loaded.ID, tape.ID)
	}
	if loaded.Route != tape.Route {
		t.Errorf("Load().Route = %q, want %q", loaded.Route, tape.Route)
	}
	if loaded.Request.Method != tape.Request.Method {
		t.Errorf("Load().Request.Method = %q, want %q", loaded.Request.Method, tape.Request.Method)
	}
	if loaded.Response.StatusCode != tape.Response.StatusCode {
		t.Errorf("Load().Response.StatusCode = %d, want %d", loaded.Response.StatusCode, tape.Response.StatusCode)
	}
}

func TestFileStore_Load_NotFound(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileStore(WithDirectory(t.TempDir()))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	_, err = store.Load(ctx, "nonexistent-id")
	if err == nil {
		t.Fatal("Load() error = nil, want ErrNotFound")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Load() error = %v, want error wrapping ErrNotFound", err)
	}
}

func TestFileStore_List_All(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileStore(WithDirectory(t.TempDir()))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tape1 := makeTape("route-a", "GET", "http://example.com/a")
	tape2 := makeTape("route-b", "POST", "http://example.com/b")
	tape3 := makeTape("route-a", "DELETE", "http://example.com/c")

	for _, tape := range []Tape{tape1, tape2, tape3} {
		if err := store.Save(ctx, tape); err != nil {
			t.Fatalf("Save() error = %v", err)
		}
	}

	tapes, err := store.List(ctx, Filter{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(tapes) != 3 {
		t.Errorf("List() returned %d tapes, want 3", len(tapes))
	}
}

func TestFileStore_List_ByRoute(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileStore(WithDirectory(t.TempDir()))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tape1 := makeTape("route-a", "GET", "http://example.com/a")
	tape2 := makeTape("route-b", "POST", "http://example.com/b")
	tape3 := makeTape("route-a", "DELETE", "http://example.com/c")

	for _, tape := range []Tape{tape1, tape2, tape3} {
		if err := store.Save(ctx, tape); err != nil {
			t.Fatalf("Save() error = %v", err)
		}
	}

	tapes, err := store.List(ctx, Filter{Route: "route-a"})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(tapes) != 2 {
		t.Errorf("List(Route=route-a) returned %d tapes, want 2", len(tapes))
	}
	for _, tape := range tapes {
		if tape.Route != "route-a" {
			t.Errorf("List(Route=route-a) returned tape with route %q", tape.Route)
		}
	}
}

func TestFileStore_List_ByMethod(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileStore(WithDirectory(t.TempDir()))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tape1 := makeTape("route-a", "GET", "http://example.com/a")
	tape2 := makeTape("route-b", "POST", "http://example.com/b")
	tape3 := makeTape("route-a", "GET", "http://example.com/c")

	for _, tape := range []Tape{tape1, tape2, tape3} {
		if err := store.Save(ctx, tape); err != nil {
			t.Fatalf("Save() error = %v", err)
		}
	}

	tapes, err := store.List(ctx, Filter{Method: "GET"})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(tapes) != 2 {
		t.Errorf("List(Method=GET) returned %d tapes, want 2", len(tapes))
	}
	for _, tape := range tapes {
		if tape.Request.Method != "GET" {
			t.Errorf("List(Method=GET) returned tape with method %q", tape.Request.Method)
		}
	}
}

func TestFileStore_List_Empty(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileStore(WithDirectory(t.TempDir()))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tapes, err := store.List(ctx, Filter{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if tapes == nil {
		t.Error("List() returned nil, want empty slice")
	}
	if len(tapes) != 0 {
		t.Errorf("List() returned %d tapes, want 0", len(tapes))
	}
}

func TestFileStore_Delete(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileStore(WithDirectory(t.TempDir()))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tape := makeTape("users-api", "GET", "http://example.com/users")
	if err := store.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if err := store.Delete(ctx, tape.ID); err != nil {
		t.Fatalf("Delete() error = %v, want nil", err)
	}

	_, err = store.Load(ctx, tape.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Load() after Delete() error = %v, want ErrNotFound", err)
	}
}

func TestFileStore_Delete_NotFound(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileStore(WithDirectory(t.TempDir()))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	err = store.Delete(ctx, "nonexistent-id")
	if err == nil {
		t.Fatal("Delete() error = nil, want ErrNotFound")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete() error = %v, want error wrapping ErrNotFound", err)
	}
}

func TestFileStore_Save_Overwrite(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileStore(WithDirectory(t.TempDir()))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tape := makeTape("users-api", "GET", "http://example.com/users")
	if err := store.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	tape.Route = "updated-route"
	if err := store.Save(ctx, tape); err != nil {
		t.Fatalf("Save() overwrite error = %v", err)
	}

	loaded, err := store.Load(ctx, tape.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Route != "updated-route" {
		t.Errorf("Load().Route = %q, want %q", loaded.Route, "updated-route")
	}
}

func TestFileStore_JSONFormat(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	store, err := NewFileStore(WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tape := makeTape("users-api", "GET", "http://example.com/users")
	if err := store.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Read raw file and verify it is valid JSON with expected fields.
	data, err := os.ReadFile(filepath.Join(dir, tape.ID+".json"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("JSON unmarshal error = %v", err)
	}

	requiredFields := []string{"id", "route", "recorded_at", "request", "response"}
	for _, field := range requiredFields {
		if _, ok := raw[field]; !ok {
			t.Errorf("JSON missing field %q", field)
		}
	}

	if raw["id"] != tape.ID {
		t.Errorf("JSON id = %v, want %q", raw["id"], tape.ID)
	}
	if raw["route"] != tape.Route {
		t.Errorf("JSON route = %v, want %q", raw["route"], tape.Route)
	}
}

func TestFileStore_Persistence(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Save with one FileStore instance.
	store1, err := NewFileStore(WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tape := makeTape("users-api", "GET", "http://example.com/users")
	if err := store1.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Load with a new FileStore instance pointing to the same directory.
	store2, err := NewFileStore(WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	loaded, err := store2.Load(ctx, tape.ID)
	if err != nil {
		t.Fatalf("Load() from second store error = %v", err)
	}
	if loaded.ID != tape.ID {
		t.Errorf("Load().ID = %q, want %q", loaded.ID, tape.ID)
	}
	if loaded.Route != tape.Route {
		t.Errorf("Load().Route = %q, want %q", loaded.Route, tape.Route)
	}
}

func TestFileStore_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	store, err := NewFileStore(WithDirectory(t.TempDir()))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tape := makeTape("users-api", "GET", "http://example.com/users")

	tests := []struct {
		name string
		fn   func() error
	}{
		{"Save", func() error { return store.Save(ctx, tape) }},
		{"Load", func() error { _, err := store.Load(ctx, "any"); return err }},
		{"List", func() error { _, err := store.List(ctx, Filter{}); return err }},
		{"Delete", func() error { return store.Delete(ctx, "any") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.fn()
			if err == nil {
				t.Errorf("%s() with cancelled context: error = nil, want non-nil", tt.name)
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("%s() with cancelled context: error = %v, want context.Canceled", tt.name, err)
			}
		})
	}
}

func TestFileStore_Save_InvalidDir(t *testing.T) {
	// Point FileStore at a directory that cannot be created.
	_, err := NewFileStore(WithDirectory("/dev/null/impossible"))
	if err == nil {
		t.Fatal("NewFileStore() error = nil, want error for impossible directory")
	}
}

func TestFileStore_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	store, err := NewFileStore(WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tape := makeTape("route", "GET", "http://example.com")
	if err := store.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify no temp files remain after save.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("Temp file %q still exists after Save()", entry.Name())
		}
	}
}

func TestFileStore_Load_CorruptJSON(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	store, err := NewFileStore(WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	// Write a corrupt JSON file.
	if err := os.WriteFile(filepath.Join(dir, "bad-id.json"), []byte("{invalid json"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err = store.Load(ctx, "bad-id")
	if err == nil {
		t.Fatal("Load() of corrupt JSON: error = nil, want error")
	}
}

func TestFileStore_List_CorruptJSON(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	store, err := NewFileStore(WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	// Write a corrupt JSON file alongside a valid one.
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{invalid"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err = store.List(ctx, Filter{})
	if err == nil {
		t.Fatal("List() with corrupt JSON: error = nil, want error")
	}
}

func TestFileStore_Save_ReadOnlyDir(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	// Make directory read-only.
	if err := os.Chmod(dir, 0o444); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	ctx := context.Background()
	tape := makeTape("route", "GET", "http://example.com")

	err = store.Save(ctx, tape)
	if err == nil {
		t.Fatal("Save() to read-only dir: error = nil, want error")
	}
}

func TestFileStore_Delete_PermissionError(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	store, err := NewFileStore(WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tape := makeTape("route", "GET", "http://example.com")
	if err := store.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Make directory read-only to prevent deletion.
	if err := os.Chmod(dir, 0o444); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	err = store.Delete(ctx, tape.ID)
	if err == nil {
		t.Fatal("Delete() in read-only dir: error = nil, want error")
	}
}

func TestFileStore_List_SkipsNonJSON(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	store, err := NewFileStore(WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tape := makeTape("route", "GET", "http://example.com")
	if err := store.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Create a non-JSON file and a subdirectory; both should be skipped.
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hello"), 0o644)
	os.Mkdir(filepath.Join(dir, "subdir"), 0o755)

	tapes, err := store.List(ctx, Filter{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(tapes) != 1 {
		t.Errorf("List() returned %d tapes, want 1", len(tapes))
	}
}

func TestFileStore_PathTraversal(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileStore(WithDirectory(t.TempDir()))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	maliciousIDs := []struct {
		name string
		id   string
	}{
		{"dot-dot", ".."},
		{"dot-dot-slash", "../../etc/passwd"},
		{"slash-prefix", "/etc/passwd"},
		{"backslash", `..\..\etc\passwd`},
		{"embedded-slash", "foo/bar"},
		{"embedded-backslash", `foo\bar`},
		{"empty", ""},
		{"dot", "."},
	}

	for _, tc := range maliciousIDs {
		t.Run("Save_"+tc.name, func(t *testing.T) {
			tape := makeTape("route", "GET", "http://example.com")
			tape.ID = tc.id
			err := store.Save(ctx, tape)
			if err == nil {
				t.Fatalf("Save(ID=%q) error = nil, want ErrInvalidID", tc.id)
			}
			if !errors.Is(err, ErrInvalidID) {
				t.Errorf("Save(ID=%q) error = %v, want ErrInvalidID", tc.id, err)
			}
		})

		t.Run("Load_"+tc.name, func(t *testing.T) {
			_, err := store.Load(ctx, tc.id)
			if err == nil {
				t.Fatalf("Load(ID=%q) error = nil, want ErrInvalidID", tc.id)
			}
			if !errors.Is(err, ErrInvalidID) {
				t.Errorf("Load(ID=%q) error = %v, want ErrInvalidID", tc.id, err)
			}
		})

		t.Run("Delete_"+tc.name, func(t *testing.T) {
			err := store.Delete(ctx, tc.id)
			if err == nil {
				t.Fatalf("Delete(ID=%q) error = nil, want ErrInvalidID", tc.id)
			}
			if !errors.Is(err, ErrInvalidID) {
				t.Errorf("Delete(ID=%q) error = %v, want ErrInvalidID", tc.id, err)
			}
		})
	}
}

func TestFileStore_ValidIDs(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileStore(WithDirectory(t.TempDir()))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	validIDs := []string{
		"simple",
		"with-dashes",
		"with_underscores",
		"CamelCase",
		"123-numeric",
		"uuid-like-550e8400-e29b-41d4-a716-446655440000",
	}

	for _, id := range validIDs {
		t.Run(id, func(t *testing.T) {
			tape := makeTape("route", "GET", "http://example.com")
			tape.ID = id
			if err := store.Save(ctx, tape); err != nil {
				t.Fatalf("Save(ID=%q) unexpected error = %v", id, err)
			}
			loaded, err := store.Load(ctx, id)
			if err != nil {
				t.Fatalf("Load(ID=%q) unexpected error = %v", id, err)
			}
			if loaded.ID != id {
				t.Errorf("Load().ID = %q, want %q", loaded.ID, id)
			}
		})
	}
}

func TestFileStore_TrailingNewline(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	store, err := NewFileStore(WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tape := makeTape("route", "GET", "http://example.com")
	if err := store.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, tape.ID+".json"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	if len(data) == 0 {
		t.Fatal("file is empty")
	}
	if data[len(data)-1] != '\n' {
		t.Error("JSON file does not end with trailing newline")
	}
}

// --- Benchmarks ---

func makeBenchFileTape(id string, body []byte) Tape {
	return Tape{
		ID:    id,
		Route: "bench-route",
		Request: RecordedReq{
			Method:   "POST",
			URL:      "https://example.com/api/data",
			Headers:  http.Header{"Content-Type": {"application/json"}},
			Body:     body,
			BodyHash: BodyHashFromBytes(body),
		},
		Response: RecordedResp{
			StatusCode: 200,
			Headers:    http.Header{"Content-Type": {"application/json"}},
			Body:       body,
		},
	}
}

// BenchmarkFileStore_Save measures sequential write throughput.
func BenchmarkFileStore_Save(b *testing.B) {
	smallBody := []byte(`{"key":"value"}`)
	mediumBody := make([]byte, 10*1024)
	for i := range mediumBody {
		mediumBody[i] = 'x'
	}

	benchCases := []struct {
		name string
		body []byte
	}{
		{"SmallTape", smallBody},
		{"MediumTape", mediumBody},
	}

	for _, bc := range benchCases {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()

			dir := b.TempDir()
			store, err := NewFileStore(WithDirectory(dir))
			if err != nil {
				b.Fatalf("NewFileStore: %v", err)
			}
			ctx := context.Background()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				id := fmt.Sprintf("tape-%d", i)
				tape := makeBenchFileTape(id, bc.body)
				if err := store.Save(ctx, tape); err != nil {
					b.Fatalf("Save: %v", err)
				}
			}
		})
	}
}

// BenchmarkFileStore_Save_Concurrent measures concurrent write throughput.
func BenchmarkFileStore_Save_Concurrent(b *testing.B) {
	smallBody := []byte(`{"key":"value"}`)
	mediumBody := make([]byte, 10*1024)
	for i := range mediumBody {
		mediumBody[i] = 'x'
	}

	benchCases := []struct {
		name string
		body []byte
	}{
		{"SmallTape", smallBody},
		{"MediumTape", mediumBody},
	}

	for _, bc := range benchCases {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()

			dir := b.TempDir()
			store, err := NewFileStore(WithDirectory(dir))
			if err != nil {
				b.Fatalf("NewFileStore: %v", err)
			}
			ctx := context.Background()
			var counter atomic.Int64

			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					n := counter.Add(1)
					id := fmt.Sprintf("tape-%d", n)
					tape := makeBenchFileTape(id, bc.body)
					if err := store.Save(ctx, tape); err != nil {
						b.Errorf("Save: %v", err)
					}
				}
			})
		})
	}
}

func TestFileStore_DefaultDirectory(t *testing.T) {
	// Change to a temp dir to avoid polluting the working directory.
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { os.Chdir(origDir) })

	store, err := NewFileStore()
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	if store.dir != "fixtures" {
		t.Errorf("default dir = %q, want %q", store.dir, "fixtures")
	}

	info, err := os.Stat(filepath.Join(tmpDir, "fixtures"))
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if !info.IsDir() {
		t.Error("default fixtures directory was not created")
	}
}
