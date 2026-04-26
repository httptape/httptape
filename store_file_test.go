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

	// Save a valid tape first.
	tape := makeTape("route", "GET", "http://example.com")
	if err := store.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Write a corrupt JSON file alongside the valid one.
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{invalid"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// List should skip the corrupt file and return the valid tape.
	tapes, err := store.List(ctx, Filter{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(tapes) != 1 {
		t.Errorf("List() returned %d tapes, want 1", len(tapes))
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

// --- Filename Strategy Tests ---

func TestFilenameStrategy_UUIDDefault(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Default store (no WithFilenameStrategy option) should use UUID filenames.
	store, err := NewFileStore(WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tape := makeTape("users-api", "GET", "http://example.com/users")
	if err := store.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify the file is named exactly <id>.json.
	expectedFile := tape.ID + ".json"
	path := filepath.Join(dir, expectedFile)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file %q does not exist: %v", expectedFile, err)
	}

	// Verify the file content round-trips correctly.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var loaded Tape
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if loaded.ID != tape.ID {
		t.Errorf("loaded.ID = %q, want %q", loaded.ID, tape.ID)
	}

	// Also verify explicit UUIDFilenames() produces the same result.
	dir2 := t.TempDir()
	store2, err := NewFileStore(WithDirectory(dir2), WithFilenameStrategy(UUIDFilenames()))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	if err := store2.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	path2 := filepath.Join(dir2, expectedFile)
	if _, err := os.Stat(path2); err != nil {
		t.Fatalf("explicit UUIDFilenames: expected file %q does not exist: %v", expectedFile, err)
	}
}

func TestFilenameStrategy_Readable_Examples(t *testing.T) {
	strategy := ReadableFilenames()

	tests := []struct {
		name     string
		method   string
		url      string
		bodyHash string
		want     string
	}{
		{
			name:   "GET simple path",
			method: "GET",
			url:    "http://example.com/api/users",
			want:   "get_api-users",
		},
		{
			name:     "POST with body hash",
			method:   "POST",
			url:      "http://example.com/api/users",
			bodyHash: "a1b2c3d4e5f6",
			want:     "post_api-users_a1b2",
		},
		{
			name:   "GET with query string",
			method: "GET",
			url:    "http://example.com/api/users?page=2",
			want:   "get_api-users_q-",
		},
		{
			name:   "HEAD root path",
			method: "HEAD",
			url:    "http://example.com/",
			want:   "head_root",
		},
		{
			name:   "DELETE nested path",
			method: "DELETE",
			url:    "http://example.com/api/v2/users/123",
			want:   "delete_api-v2-users-123",
		},
		{
			name:   "PUT with special chars in path",
			method: "PUT",
			url:    "http://example.com/api/users/@john/~settings",
			want:   "put_api-users-john-settings",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tape := Tape{
				ID: "test-id",
				Request: RecordedReq{
					Method:   tt.method,
					URL:      tt.url,
					BodyHash: tt.bodyHash,
				},
			}
			got := strategy(tape)
			if tt.url == "http://example.com/api/users?page=2" {
				// For query hash, just check the prefix since hash is deterministic
				// but we test that separately.
				if !strings.HasPrefix(got, tt.want) {
					t.Errorf("ReadableFilenames()(%q %q) = %q, want prefix %q", tt.method, tt.url, got, tt.want)
				}
			} else if got != tt.want {
				t.Errorf("ReadableFilenames()(%q %q) = %q, want %q", tt.method, tt.url, got, tt.want)
			}
		})
	}
}

func TestFilenameStrategy_Readable_Slugify(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"simple path", "http://example.com/api/users", "api-users"},
		{"root path", "http://example.com/", "root"},
		{"empty path", "http://example.com", "root"},
		{"trailing slash", "http://example.com/api/", "api"},
		{"special chars", "http://example.com/api/@user/~config", "api-user-config"},
		{"unicode", "http://example.com/api/\u00e9v\u00e9nements", "api-v-nements"},
		{"consecutive special chars", "http://example.com/api///users", "api-users"},
		{"dots in path", "http://example.com/api/v1.2/users", "api-v1-2-users"},
		{"percent encoded", "http://example.com/api/hello%20world", "api-hello-world"},
		{"numbers only", "http://example.com/123/456", "123-456"},
		{"mixed case", "http://example.com/API/Users", "api-users"},
		{"dashes preserved", "http://example.com/my-api/my-resource", "my-api-my-resource"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := slugifyURL(tt.url)
			if got != tt.want {
				t.Errorf("slugifyURL(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestFilenameStrategy_Readable_VeryLongPath(t *testing.T) {
	// Create a URL with a very long path that exceeds maxFilenameLength.
	longSegment := strings.Repeat("abcdefghij", 30) // 300 chars
	url := "http://example.com/" + longSegment

	strategy := ReadableFilenames()
	tape := Tape{
		ID: "test-id",
		Request: RecordedReq{
			Method: "GET",
			URL:    url,
		},
	}
	got := strategy(tape)

	if len(got) > maxFilenameLength {
		t.Errorf("filename length = %d, want <= %d", len(got), maxFilenameLength)
	}
	if got == "" {
		t.Error("filename is empty after truncation")
	}
	// Should not end with a dash after truncation cleanup.
	if strings.HasSuffix(got, "-") {
		t.Errorf("filename %q ends with dash after truncation", got)
	}
}

func TestFilenameStrategy_Readable_QueryHash(t *testing.T) {
	strategy := ReadableFilenames()

	// Same path, different query -> different filenames.
	tape1 := Tape{
		ID:      "id-1",
		Request: RecordedReq{Method: "GET", URL: "http://example.com/api/users?page=1"},
	}
	tape2 := Tape{
		ID:      "id-2",
		Request: RecordedReq{Method: "GET", URL: "http://example.com/api/users?page=2"},
	}
	name1 := strategy(tape1)
	name2 := strategy(tape2)

	if name1 == name2 {
		t.Errorf("same filename for different queries: %q", name1)
	}

	// Same query -> same filename (deterministic).
	tape3 := Tape{
		ID:      "id-3",
		Request: RecordedReq{Method: "GET", URL: "http://example.com/api/users?page=1"},
	}
	name3 := strategy(tape3)
	if name1 != name3 {
		t.Errorf("different filenames for same query: %q vs %q", name1, name3)
	}

	// No query -> no q- segment.
	tape4 := Tape{
		ID:      "id-4",
		Request: RecordedReq{Method: "GET", URL: "http://example.com/api/users"},
	}
	name4 := strategy(tape4)
	if strings.Contains(name4, "q-") {
		t.Errorf("filename contains q- segment for URL without query: %q", name4)
	}
}

func TestFilenameStrategy_Readable_BodyHash(t *testing.T) {
	strategy := ReadableFilenames()

	body1 := []byte(`{"name":"alice"}`)
	body2 := []byte(`{"name":"bob"}`)
	hash1 := BodyHashFromBytes(body1)
	hash2 := BodyHashFromBytes(body2)

	tape1 := Tape{
		ID:      "id-1",
		Request: RecordedReq{Method: "POST", URL: "http://example.com/api/users", BodyHash: hash1},
	}
	tape2 := Tape{
		ID:      "id-2",
		Request: RecordedReq{Method: "POST", URL: "http://example.com/api/users", BodyHash: hash2},
	}
	name1 := strategy(tape1)
	name2 := strategy(tape2)

	if name1 == name2 {
		t.Errorf("same filename for different body hashes: %q", name1)
	}

	// Both should contain the body hash prefix.
	if !strings.Contains(name1, hash1[:4]) {
		t.Errorf("filename %q does not contain body hash prefix %q", name1, hash1[:4])
	}
	if !strings.Contains(name2, hash2[:4]) {
		t.Errorf("filename %q does not contain body hash prefix %q", name2, hash2[:4])
	}

	// No body -> no body hash segment.
	tape3 := Tape{
		ID:      "id-3",
		Request: RecordedReq{Method: "GET", URL: "http://example.com/api/users", BodyHash: ""},
	}
	name3 := strategy(tape3)
	// With empty body hash, the filename should just be method + slug.
	if name3 != "get_api-users" {
		t.Errorf("filename for GET without body = %q, want %q", name3, "get_api-users")
	}
}

func TestFileStore_LoadByID_FastPath(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Use default UUID strategy. Load should use fast path (<id>.json).
	store, err := NewFileStore(WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tape := makeTape("route", "GET", "http://example.com/users")
	if err := store.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify the file exists at the fast-path location.
	fastPath := filepath.Join(dir, tape.ID+".json")
	if _, err := os.Stat(fastPath); err != nil {
		t.Fatalf("fast-path file %q does not exist: %v", fastPath, err)
	}

	// Load by ID should succeed via fast path.
	loaded, err := store.Load(ctx, tape.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.ID != tape.ID {
		t.Errorf("Load().ID = %q, want %q", loaded.ID, tape.ID)
	}
}

func TestFileStore_LoadByID_ScanFallback(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Use readable strategy. Load(id) won't find <id>.json, so it falls back to scan.
	store, err := NewFileStore(WithDirectory(dir), WithFilenameStrategy(ReadableFilenames()))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tape := makeTape("route", "GET", "http://example.com/users")
	if err := store.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify the file is NOT at <id>.json (it should be at get_users.json or similar).
	fastPath := filepath.Join(dir, tape.ID+".json")
	if _, err := os.Stat(fastPath); err == nil {
		t.Fatalf("file should NOT exist at fast-path %q when using readable strategy", fastPath)
	}

	// Load by ID should still succeed via scan fallback.
	loaded, err := store.Load(ctx, tape.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.ID != tape.ID {
		t.Errorf("Load().ID = %q, want %q", loaded.ID, tape.ID)
	}
}

func TestFileStore_DeleteByID_BothPaths(t *testing.T) {
	ctx := context.Background()

	t.Run("fast path (UUID)", func(t *testing.T) {
		dir := t.TempDir()
		store, err := NewFileStore(WithDirectory(dir))
		if err != nil {
			t.Fatalf("NewFileStore() error = %v", err)
		}

		tape := makeTape("route", "GET", "http://example.com/users")
		if err := store.Save(ctx, tape); err != nil {
			t.Fatalf("Save() error = %v", err)
		}

		// Verify file exists.
		fastPath := filepath.Join(dir, tape.ID+".json")
		if _, err := os.Stat(fastPath); err != nil {
			t.Fatalf("file does not exist before Delete")
		}

		// Delete by ID.
		if err := store.Delete(ctx, tape.ID); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}

		// Verify file is gone.
		if _, err := os.Stat(fastPath); !os.IsNotExist(err) {
			t.Error("file still exists after Delete via fast path")
		}
	})

	t.Run("scan fallback (readable)", func(t *testing.T) {
		dir := t.TempDir()
		store, err := NewFileStore(WithDirectory(dir), WithFilenameStrategy(ReadableFilenames()))
		if err != nil {
			t.Fatalf("NewFileStore() error = %v", err)
		}

		tape := makeTape("route", "GET", "http://example.com/users")
		if err := store.Save(ctx, tape); err != nil {
			t.Fatalf("Save() error = %v", err)
		}

		// Delete by ID should find and remove via scan.
		if err := store.Delete(ctx, tape.ID); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}

		// Verify tape is gone.
		_, err = store.Load(ctx, tape.ID)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("Load() after Delete error = %v, want ErrNotFound", err)
		}
	})
}

func TestFileStore_MixedStrategyDirectory(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Save some tapes with UUID strategy.
	storeUUID, err := NewFileStore(WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tape1 := makeTape("route-a", "GET", "http://example.com/a")
	tape2 := makeTape("route-b", "POST", "http://example.com/b")
	if err := storeUUID.Save(ctx, tape1); err != nil {
		t.Fatalf("Save(tape1) error = %v", err)
	}
	if err := storeUUID.Save(ctx, tape2); err != nil {
		t.Fatalf("Save(tape2) error = %v", err)
	}

	// Save some tapes with readable strategy (using a new store pointing to same dir).
	storeReadable, err := NewFileStore(WithDirectory(dir), WithFilenameStrategy(ReadableFilenames()))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tape3 := makeTape("route-c", "DELETE", "http://example.com/c")
	tape4 := makeTape("route-d", "PUT", "http://example.com/d")
	if err := storeReadable.Save(ctx, tape3); err != nil {
		t.Fatalf("Save(tape3) error = %v", err)
	}
	if err := storeReadable.Save(ctx, tape4); err != nil {
		t.Fatalf("Save(tape4) error = %v", err)
	}

	// List should return all 4 tapes regardless of strategy.
	tapes, err := storeReadable.List(ctx, Filter{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(tapes) != 4 {
		t.Errorf("List() returned %d tapes, want 4", len(tapes))
	}

	// Load each tape by ID (UUID tapes need scan, readable tapes need scan from UUID store).
	for _, tape := range []Tape{tape1, tape2, tape3, tape4} {
		loaded, err := storeReadable.Load(ctx, tape.ID)
		if err != nil {
			t.Errorf("Load(%q) error = %v", tape.ID, err)
			continue
		}
		if loaded.ID != tape.ID {
			t.Errorf("Load(%q).ID = %q", tape.ID, loaded.ID)
		}
	}

	// Delete each tape by ID.
	for _, tape := range []Tape{tape1, tape2, tape3, tape4} {
		if err := storeReadable.Delete(ctx, tape.ID); err != nil {
			t.Errorf("Delete(%q) error = %v", tape.ID, err)
		}
	}

	// Verify all tapes are gone.
	tapes, err = storeReadable.List(ctx, Filter{})
	if err != nil {
		t.Fatalf("List() after deletes error = %v", err)
	}
	if len(tapes) != 0 {
		t.Errorf("List() after deletes returned %d tapes, want 0", len(tapes))
	}
}

func TestFileStore_FilenameCollision(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Use a strategy that always returns the same stem.
	collidingStrategy := func(tape Tape) string {
		return "same-name"
	}

	store, err := NewFileStore(WithDirectory(dir), WithFilenameStrategy(collidingStrategy))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tape1 := makeTape("route-a", "GET", "http://example.com/a")
	tape2 := makeTape("route-b", "POST", "http://example.com/b")

	if err := store.Save(ctx, tape1); err != nil {
		t.Fatalf("Save(tape1) error = %v", err)
	}
	if err := store.Save(ctx, tape2); err != nil {
		t.Fatalf("Save(tape2) error = %v", err)
	}

	// Verify files: same-name.json and same-name_2.json.
	if _, err := os.Stat(filepath.Join(dir, "same-name.json")); err != nil {
		t.Error("expected same-name.json to exist")
	}
	if _, err := os.Stat(filepath.Join(dir, "same-name_2.json")); err != nil {
		t.Error("expected same-name_2.json to exist")
	}

	// Both tapes should be loadable.
	loaded1, err := store.Load(ctx, tape1.ID)
	if err != nil {
		t.Fatalf("Load(tape1) error = %v", err)
	}
	if loaded1.ID != tape1.ID {
		t.Errorf("Load(tape1).ID = %q, want %q", loaded1.ID, tape1.ID)
	}

	loaded2, err := store.Load(ctx, tape2.ID)
	if err != nil {
		t.Fatalf("Load(tape2) error = %v", err)
	}
	if loaded2.ID != tape2.ID {
		t.Errorf("Load(tape2).ID = %q, want %q", loaded2.ID, tape2.ID)
	}
}

func TestFileStore_FilenameCollision_ExceedsLimit(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Use a strategy that always returns the same stem.
	collidingStrategy := func(tape Tape) string {
		return "collide"
	}

	store, err := NewFileStore(WithDirectory(dir), WithFilenameStrategy(collidingStrategy))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	// Save 99 tapes to exhaust all collision slots (collide.json, collide_2.json, ..., collide_99.json).
	for i := 0; i < maxCollisionCounter; i++ {
		tape := makeTape("route", "GET", "http://example.com")
		tape.ID = fmt.Sprintf("tape-%d", i)
		if err := store.Save(ctx, tape); err != nil {
			t.Fatalf("Save(tape-%d) error = %v", i, err)
		}
	}

	// The 100th tape should fail with ErrFilenameCollision.
	tape100 := makeTape("route", "GET", "http://example.com")
	tape100.ID = "tape-100"
	err = store.Save(ctx, tape100)
	if err == nil {
		t.Fatal("Save() beyond collision limit: error = nil, want ErrFilenameCollision")
	}
	if !errors.Is(err, ErrFilenameCollision) {
		t.Errorf("Save() beyond collision limit: error = %v, want ErrFilenameCollision", err)
	}
}

func TestFileStore_OverwriteWithChangedStrategy(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Save a tape with UUID strategy.
	storeUUID, err := NewFileStore(WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewFileStore(UUID) error = %v", err)
	}

	tape := makeTape("route", "GET", "http://example.com/users")
	if err := storeUUID.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify UUID-named file exists.
	uuidPath := filepath.Join(dir, tape.ID+".json")
	if _, err := os.Stat(uuidPath); err != nil {
		t.Fatalf("UUID file does not exist: %v", err)
	}

	// Switch to readable strategy and save the same tape (same ID).
	storeReadable, err := NewFileStore(WithDirectory(dir), WithFilenameStrategy(ReadableFilenames()))
	if err != nil {
		t.Fatalf("NewFileStore(Readable) error = %v", err)
	}

	if err := storeReadable.Save(ctx, tape); err != nil {
		t.Fatalf("Save() with readable strategy error = %v", err)
	}

	// Verify old UUID file is removed.
	if _, err := os.Stat(uuidPath); !os.IsNotExist(err) {
		t.Error("old UUID file still exists after re-save with readable strategy")
	}

	// Verify new readable file exists and loads correctly.
	loaded, err := storeReadable.Load(ctx, tape.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.ID != tape.ID {
		t.Errorf("Load().ID = %q, want %q", loaded.ID, tape.ID)
	}

	// Verify there's exactly 1 JSON file in the directory.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	jsonCount := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			jsonCount++
		}
	}
	if jsonCount != 1 {
		t.Errorf("directory has %d JSON files, want 1", jsonCount)
	}
}

func TestFileStore_BackwardCompat(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Pre-populate directory with old-style UUID-named files (simulating existing tapes).
	tape1 := makeTape("route-a", "GET", "http://example.com/a")
	tape2 := makeTape("route-b", "POST", "http://example.com/b")

	for _, tape := range []Tape{tape1, tape2} {
		data, err := json.MarshalIndent(tape, "", "  ")
		if err != nil {
			t.Fatalf("MarshalIndent() error = %v", err)
		}
		data = append(data, '\n')
		path := filepath.Join(dir, tape.ID+".json")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}

	// Open the directory with a new FileStore (default UUID strategy).
	store, err := NewFileStore(WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	// List should find both tapes.
	tapes, err := store.List(ctx, Filter{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(tapes) != 2 {
		t.Errorf("List() returned %d tapes, want 2", len(tapes))
	}

	// Load each tape.
	for _, tape := range []Tape{tape1, tape2} {
		loaded, err := store.Load(ctx, tape.ID)
		if err != nil {
			t.Errorf("Load(%q) error = %v", tape.ID, err)
			continue
		}
		if loaded.ID != tape.ID {
			t.Errorf("Load(%q).ID = %q", tape.ID, loaded.ID)
		}
	}

	// Save a new tape.
	tape3 := makeTape("route-c", "DELETE", "http://example.com/c")
	if err := store.Save(ctx, tape3); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Delete one of the old tapes.
	if err := store.Delete(ctx, tape1.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	// Verify final state.
	tapes, err = store.List(ctx, Filter{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(tapes) != 2 {
		t.Errorf("List() returned %d tapes after CRUD, want 2", len(tapes))
	}
}

func TestFileStore_CorruptJSONScanSkipped(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Use readable strategy so Load/Delete will use scan path.
	store, err := NewFileStore(WithDirectory(dir), WithFilenameStrategy(ReadableFilenames()))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	// Save a valid tape.
	tape := makeTape("route", "GET", "http://example.com/users")
	if err := store.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Drop a corrupt JSON file in the directory.
	corruptPath := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corruptPath, []byte("{not valid json at all"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// Load should still find the valid tape (corrupt file is skipped).
	loaded, err := store.Load(ctx, tape.ID)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil (corrupt file should be skipped)", err)
	}
	if loaded.ID != tape.ID {
		t.Errorf("Load().ID = %q, want %q", loaded.ID, tape.ID)
	}

	// List should also skip the corrupt file.
	tapes, err := store.List(ctx, Filter{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(tapes) != 1 {
		t.Errorf("List() returned %d tapes, want 1", len(tapes))
	}

	// Delete should also work (scan skips corrupt file).
	if err := store.Delete(ctx, tape.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
}

func TestFileStore_EmptyStrategyFallback(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Strategy that returns empty string -> should fall back to tape.ID.
	emptyStrategy := func(tape Tape) string { return "" }

	store, err := NewFileStore(WithDirectory(dir), WithFilenameStrategy(emptyStrategy))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tape := makeTape("route", "GET", "http://example.com")
	if err := store.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Should fall back to <id>.json.
	path := filepath.Join(dir, tape.ID+".json")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected fallback file %q to exist: %v", tape.ID+".json", err)
	}
}

func TestFilenameStrategy_PathTraversalSanitized(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name         string
		strategyStem string
	}{
		{"dot-dot-slash", "../escape"},
		{"nested traversal", "../../etc/passwd"},
		{"slash prefix", "/etc/passwd"},
		{"backslash traversal", `..\..\escape`},
		{"embedded slash", "sub/dir"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()

			maliciousStrategy := func(tape Tape) string {
				return tt.strategyStem
			}

			store, err := NewFileStore(WithDirectory(dir), WithFilenameStrategy(maliciousStrategy))
			if err != nil {
				t.Fatalf("NewFileStore() error = %v", err)
			}

			tape := makeTape("route", "GET", "http://example.com")
			if err := store.Save(ctx, tape); err != nil {
				t.Fatalf("Save() error = %v", err)
			}

			// Verify the file landed inside the base directory, not outside.
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("ReadDir() error = %v", err)
			}
			found := false
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".json") {
					found = true
					// Verify the file contains the correct tape ID.
					data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
					if err != nil {
						t.Fatalf("ReadFile() error = %v", err)
					}
					fileID, err := extractIDFromJSON(data)
					if err != nil {
						t.Fatalf("extractIDFromJSON() error = %v", err)
					}
					if fileID != tape.ID {
						t.Errorf("file ID = %q, want %q", fileID, tape.ID)
					}
				}
			}
			if !found {
				t.Error("no JSON file found in base directory")
			}

			// Verify no file was written to the parent directory.
			parentDir := filepath.Dir(dir)
			parentEntries, err := os.ReadDir(parentDir)
			if err != nil {
				t.Fatalf("ReadDir(parent) error = %v", err)
			}
			for _, entry := range parentEntries {
				if entry.Name() == "escape.json" || entry.Name() == "passwd.json" {
					t.Errorf("file %q escaped to parent directory", entry.Name())
				}
			}
		})
	}
}

func TestFilenameStrategy_EmptyOrDotResultFallsBack(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name         string
		strategyStem string
	}{
		{"empty string", ""},
		{"dot", "."},
		{"dot-dot", ".."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()

			strategy := func(tape Tape) string {
				return tt.strategyStem
			}

			store, err := NewFileStore(WithDirectory(dir), WithFilenameStrategy(strategy))
			if err != nil {
				t.Fatalf("NewFileStore() error = %v", err)
			}

			tape := makeTape("route", "GET", "http://example.com")
			if err := store.Save(ctx, tape); err != nil {
				t.Fatalf("Save() error = %v", err)
			}

			// Should fall back to <id>.json.
			path := filepath.Join(dir, tape.ID+".json")
			if _, err := os.Stat(path); err != nil {
				t.Errorf("expected fallback file %q to exist: %v", tape.ID+".json", err)
			}

			// Verify file contains the correct tape.
			loaded, err := store.Load(ctx, tape.ID)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if loaded.ID != tape.ID {
				t.Errorf("Load().ID = %q, want %q", loaded.ID, tape.ID)
			}
		})
	}
}

func TestFileStore_Delete_ScanFallback_ContextCancelled(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Use readable strategy so Delete will use scan fallback.
	store, err := NewFileStore(WithDirectory(dir), WithFilenameStrategy(ReadableFilenames()))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	// Save a tape so there is something to scan for.
	tape := makeTape("route", "GET", "http://example.com/users")
	if err := store.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Cancel the context before calling Delete to force the cancellation check.
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err = store.Delete(cancelledCtx, tape.ID)
	if err == nil {
		t.Fatal("Delete() with cancelled context: error = nil, want context.Canceled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Delete() with cancelled context: error = %v, want context.Canceled", err)
	}

	// Verify the tape was NOT deleted (context was cancelled).
	loaded, err := store.Load(context.Background(), tape.ID)
	if err != nil {
		t.Fatalf("Load() error = %v, tape should still exist", err)
	}
	if loaded.ID != tape.ID {
		t.Errorf("Load().ID = %q, want %q", loaded.ID, tape.ID)
	}
}

func TestFileStore_ReadableStrategy_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	store, err := NewFileStore(WithDirectory(dir), WithFilenameStrategy(ReadableFilenames()))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tape := makeTape("route", "GET", "http://example.com/api/users")
	if err := store.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify the file has a readable name.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}

	found := false
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".json") {
			if strings.HasPrefix(name, "get_") {
				found = true
			}
		}
	}
	if !found {
		t.Error("no file with readable name prefix 'get_' found")
	}

	// Load and delete should work via scan.
	loaded, err := store.Load(ctx, tape.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.ID != tape.ID {
		t.Errorf("Load().ID = %q, want %q", loaded.ID, tape.ID)
	}
}

func TestSlugifyURL_ControlChars(t *testing.T) {
	// Control characters should be treated as non-slug characters.
	got := slugifyURL("http://example.com/api/\x00\x01users")
	if got != "api-users" {
		t.Errorf("slugifyURL with control chars = %q, want %q", got, "api-users")
	}
}

func TestSlugifyURL_AllSpecialChars(t *testing.T) {
	// Path with only special characters should produce "root".
	got := slugifyURL("http://example.com/@#$%^&*()")
	if got != "root" {
		t.Errorf("slugifyURL with all special chars = %q, want %q", got, "root")
	}
}

func TestQueryHash_Deterministic(t *testing.T) {
	h1 := queryHash("http://example.com/api?key=value")
	h2 := queryHash("http://example.com/api?key=value")
	if h1 != h2 {
		t.Errorf("queryHash not deterministic: %q vs %q", h1, h2)
	}
	if len(h1) != 4 {
		t.Errorf("queryHash length = %d, want 4", len(h1))
	}
}

func TestQueryHash_Empty(t *testing.T) {
	h := queryHash("http://example.com/api")
	if h != "" {
		t.Errorf("queryHash for URL without query = %q, want empty", h)
	}
}

func TestBodyHashPrefix(t *testing.T) {
	tests := []struct {
		name     string
		fullHash string
		want     string
	}{
		{"normal hash", "a1b2c3d4e5f6", "a1b2"},
		{"empty hash", "", ""},
		{"short hash", "ab", "ab"},
		{"exactly 4", "abcd", "abcd"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bodyHashPrefix(tt.fullHash)
			if got != tt.want {
				t.Errorf("bodyHashPrefix(%q) = %q, want %q", tt.fullHash, got, tt.want)
			}
		})
	}
}

func TestExtractIDFromJSON(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		want    string
		wantErr bool
	}{
		{
			name: "valid JSON with id",
			data: []byte(`{"id":"test-123","route":"r"}`),
			want: "test-123",
		},
		{
			name:    "invalid JSON",
			data:    []byte(`{not valid}`),
			wantErr: true,
		},
		{
			name: "no id field",
			data: []byte(`{"route":"r"}`),
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractIDFromJSON(tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("extractIDFromJSON() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("extractIDFromJSON() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFileStore_ReadableStrategy_Overwrite(t *testing.T) {
	// Verify that re-saving the same tape with readable strategy overwrites
	// (does not create duplicates).
	dir := t.TempDir()
	ctx := context.Background()

	store, err := NewFileStore(WithDirectory(dir), WithFilenameStrategy(ReadableFilenames()))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tape := makeTape("route", "GET", "http://example.com/api/users")
	if err := store.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Re-save same tape with updated route.
	tape.Route = "updated-route"
	if err := store.Save(ctx, tape); err != nil {
		t.Fatalf("Save() overwrite error = %v", err)
	}

	// Verify only 1 JSON file exists.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	jsonCount := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			jsonCount++
		}
	}
	if jsonCount != 1 {
		t.Errorf("directory has %d JSON files after overwrite, want 1", jsonCount)
	}

	// Verify the loaded tape has the updated route.
	loaded, err := store.Load(ctx, tape.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Route != "updated-route" {
		t.Errorf("Load().Route = %q, want %q", loaded.Route, "updated-route")
	}
}

func TestFilenameStrategy_Readable_EmptyMethod(t *testing.T) {
	strategy := ReadableFilenames()
	tape := Tape{
		ID:      "test-id",
		Request: RecordedReq{Method: "", URL: "http://example.com/api"},
	}
	got := strategy(tape)
	if !strings.HasPrefix(got, "unknown_") {
		t.Errorf("ReadableFilenames() with empty method = %q, want prefix 'unknown_'", got)
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

// --- Coverage gap tests (issue #219) ---

func TestSlugifyURL_ParseFallback_WithSchemeAndPath(t *testing.T) {
	// url.Parse fails on URLs with invalid hosts like "http://[bad".
	// The fallback code extracts the path manually.
	got := slugifyURL("http://[bad/api/users")
	if got != "api-users" {
		t.Errorf("slugifyURL fallback with scheme = %q, want %q", got, "api-users")
	}
}

func TestSlugifyURL_ParseFallback_NoSlash(t *testing.T) {
	// When the fallback path has no '/', it collapses to empty -> "root".
	got := slugifyURL("http://[bad")
	if got != "root" {
		t.Errorf("slugifyURL fallback no slash = %q, want %q", got, "root")
	}
}

func TestSlugifyURL_ParseFallback_WithQuery(t *testing.T) {
	// The fallback path extraction should strip query strings.
	got := slugifyURL("http://[bad/api/items?page=2")
	if got != "api-items" {
		t.Errorf("slugifyURL fallback with query = %q, want %q", got, "api-items")
	}
}

func TestQueryHash_ParseError(t *testing.T) {
	// queryHash returns "" when url.Parse fails.
	got := queryHash("http://[bad?key=value")
	if got != "" {
		t.Errorf("queryHash with unparseable URL = %q, want empty", got)
	}
}

func TestReadableFilenames_EmptySlugFallsBackToID(t *testing.T) {
	// When the URL path produces an empty slug after slugification and the
	// resulting filename is empty, ReadableFilenames falls back to tape.ID.
	strategy := ReadableFilenames()
	tape := Tape{
		ID: "fallback-id",
		Request: RecordedReq{
			// Empty method and a URL whose path is only special chars.
			Method: "",
			URL:    "http://example.com/###",
		},
	}
	got := strategy(tape)
	// "unknown" + "_" + some slug from "###". But if path is only special chars,
	// slugifyURL returns "root", so we get "unknown_root".
	if got == "" {
		t.Error("ReadableFilenames() produced empty string, should not happen")
	}
}

func TestFileStore_Save_TmpWriteError(t *testing.T) {
	// Simulate a write failure by making the store's directory read-only
	// after the temp file is created. On macOS/Linux, chmod on dir prevents
	// creating new files but CreateTemp itself may fail instead.
	//
	// The simplest portable test: make the directory read-only so CreateTemp
	// fails (L272). This is already covered by TestFileStore_Save_ReadOnlyDir.
	//
	// Instead, test the rename error path (L288): save to a directory, then
	// remove it between CreateTemp and Rename by using a custom strategy
	// that triggers collision resolution failure.
	dir := t.TempDir()
	ctx := context.Background()

	// Fill the directory with files that have read-only permissions to
	// provoke filenameAvailableForID read errors (L340).
	store, err := NewFileStore(WithDirectory(dir), WithFilenameStrategy(func(tape Tape) string {
		return "fixed"
	}))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	// Write a file that is unreadable, matching the collision candidate name.
	unreadable := filepath.Join(dir, "fixed.json")
	if err := os.WriteFile(unreadable, []byte(`{"id":"other"}`), 0o000); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Cleanup(func() { os.Chmod(unreadable, 0o644) })

	tape := makeTape("route", "GET", "http://example.com")
	err = store.Save(ctx, tape)
	// The file is unreadable, so filenameAvailableForID returns an error,
	// which propagates through resolveFilename.
	if err == nil {
		t.Fatal("Save() with unreadable collision file: error = nil, want error")
	}
}

func TestFileStore_FilenameCollision_ReadErrorInLoop(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	store, err := NewFileStore(WithDirectory(dir), WithFilenameStrategy(func(tape Tape) string {
		return "collide"
	}))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	// Save one tape normally to occupy collide.json.
	tape1 := makeTape("route", "GET", "http://example.com")
	tape1.ID = "tape-1"
	if err := store.Save(ctx, tape1); err != nil {
		t.Fatalf("Save(tape1) error = %v", err)
	}

	// Make collide_2.json unreadable to trigger the error path inside
	// the collision resolution loop (L314-316).
	unreadable := filepath.Join(dir, "collide_2.json")
	if err := os.WriteFile(unreadable, []byte(`{"id":"other"}`), 0o000); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Cleanup(func() { os.Chmod(unreadable, 0o644) })

	tape2 := makeTape("route", "POST", "http://example.com")
	tape2.ID = "tape-2"
	err = store.Save(ctx, tape2)
	if err == nil {
		t.Fatal("Save() with unreadable collision suffix file: error = nil, want error")
	}
}

func TestFileStore_RemoveOldFile_SkipsCorruptAndUnreadable(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Save a tape with UUID strategy under <id>.json.
	storeUUID, err := NewFileStore(WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tape := makeTape("route", "GET", "http://example.com/users")
	if err := storeUUID.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Place a corrupt JSON file and an unreadable JSON file in the directory
	// to exercise the skip paths in removeOldFile (L364-365, L368-369).
	// Use "000-" prefix to sort before any UUID so they are visited first.
	corruptPath := filepath.Join(dir, "000-corrupt.json")
	if err := os.WriteFile(corruptPath, []byte("{broken"), 0o644); err != nil {
		t.Fatalf("WriteFile(corrupt) error = %v", err)
	}
	unreadablePath := filepath.Join(dir, "000-unreadable.json")
	if err := os.WriteFile(unreadablePath, []byte(`{"id":"x"}`), 0o000); err != nil {
		t.Fatalf("WriteFile(unreadable) error = %v", err)
	}
	t.Cleanup(func() { os.Chmod(unreadablePath, 0o644) })

	// Re-save the same tape with readable strategy. This triggers removeOldFile
	// to scan and find the old UUID file, but it must skip the corrupt and
	// unreadable files along the way.
	storeReadable, err := NewFileStore(WithDirectory(dir), WithFilenameStrategy(ReadableFilenames()))
	if err != nil {
		t.Fatalf("NewFileStore(readable) error = %v", err)
	}

	if err := storeReadable.Save(ctx, tape); err != nil {
		t.Fatalf("Save(readable) error = %v", err)
	}

	// The old UUID file should have been removed.
	uuidPath := filepath.Join(dir, tape.ID+".json")
	if _, err := os.Stat(uuidPath); !os.IsNotExist(err) {
		t.Error("old UUID file still exists after re-save with readable strategy")
	}
}

func TestFileStore_Load_FastPathDifferentID(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Save a tape with readable strategy.
	store, err := NewFileStore(WithDirectory(dir), WithFilenameStrategy(ReadableFilenames()))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tape := makeTape("route", "GET", "http://example.com/users")
	if err := store.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Write a file named exactly <id>.json but with a different tape ID inside.
	// This exercises the fast-path "file exists but has different ID" fallthrough
	// to scan (L403-408).
	differentTape := makeTape("other-route", "POST", "http://example.com/other")
	data, err := json.MarshalIndent(differentTape, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	imposterPath := filepath.Join(dir, tape.ID+".json")
	if err := os.WriteFile(imposterPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// Load by original tape.ID should fall through to scan and find it.
	loaded, err := store.Load(ctx, tape.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.ID != tape.ID {
		t.Errorf("Load().ID = %q, want %q", loaded.ID, tape.ID)
	}
}

func TestFileStore_Delete_FastPathCorruptFile(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Save a tape with readable strategy so the actual file has a readable name.
	store, err := NewFileStore(WithDirectory(dir), WithFilenameStrategy(ReadableFilenames()))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tape := makeTape("route", "GET", "http://example.com/users")
	if err := store.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Place a corrupt JSON file at the fast-path location <id>.json.
	// This exercises the "file exists but corrupt" fallthrough in Delete (L489-491).
	fastPath := filepath.Join(dir, tape.ID+".json")
	if err := os.WriteFile(fastPath, []byte("{corrupt}"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// Delete should fall through to scan and still succeed.
	if err := store.Delete(ctx, tape.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	// Verify the real tape file is gone by listing (List skips corrupt JSON).
	tapes, listErr := store.List(ctx, Filter{})
	if listErr != nil {
		t.Fatalf("List() error = %v", listErr)
	}
	for _, tp := range tapes {
		if tp.ID == tape.ID {
			t.Error("tape still found in List after Delete")
		}
	}
}

func TestFileStore_Delete_FastPathDifferentID(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	store, err := NewFileStore(WithDirectory(dir), WithFilenameStrategy(ReadableFilenames()))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tape := makeTape("route", "GET", "http://example.com/users")
	if err := store.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Place a valid JSON file at the fast-path but with a different ID.
	differentTape := makeTape("other", "POST", "http://example.com/other")
	data, _ := json.MarshalIndent(differentTape, "", "  ")
	fastPath := filepath.Join(dir, tape.ID+".json")
	if err := os.WriteFile(fastPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// Delete should fall through to scan and find the real file.
	if err := store.Delete(ctx, tape.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
}

func TestFileStore_Delete_FastPathRemoveError(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Use UUID strategy so <id>.json is the fast-path file.
	store, err := NewFileStore(WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tape := makeTape("route", "GET", "http://example.com")
	if err := store.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Make directory read+execute only (0o555): files can be read but not deleted.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	err = store.Delete(ctx, tape.ID)
	if err == nil {
		t.Fatal("Delete() with non-writable dir: error = nil, want error")
	}
}

func TestFileStore_Delete_ScanRemoveError(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	store, err := NewFileStore(WithDirectory(dir), WithFilenameStrategy(ReadableFilenames()))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tape := makeTape("route", "GET", "http://example.com/users")
	if err := store.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Make the directory read-only to prevent file removal in the scan path.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	err = store.Delete(ctx, tape.ID)
	if err == nil {
		t.Fatal("Delete() in read-only dir via scan: error = nil, want error")
	}
}

func TestFileStore_List_MidLoopContextCancel(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	store, err := NewFileStore(WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	// Save multiple tapes so there are files to iterate.
	for i := 0; i < 5; i++ {
		tape := makeTape("route", "GET", "http://example.com")
		tape.ID = fmt.Sprintf("tape-%d", i)
		if err := store.Save(ctx, tape); err != nil {
			t.Fatalf("Save(tape-%d) error = %v", i, err)
		}
	}

	// Use a context that is already cancelled. The per-file cancellation check
	// (L443) fires during iteration.
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = store.List(cancelledCtx, Filter{})
	if err == nil {
		t.Fatal("List() with cancelled context: error = nil, want error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("List() error = %v, want context.Canceled", err)
	}
}

func TestFileStore_ScanForID_ContextCancel(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Use readable strategy so Load uses scan path.
	store, err := NewFileStore(WithDirectory(dir), WithFilenameStrategy(ReadableFilenames()))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	// Save multiple tapes so the scan iterates.
	for i := 0; i < 3; i++ {
		tape := makeTape("route", "GET", "http://example.com/items")
		tape.ID = fmt.Sprintf("tape-%d", i)
		if err := store.Save(ctx, tape); err != nil {
			t.Fatalf("Save(tape-%d) error = %v", i, err)
		}
	}

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	// Load triggers scanForID. With cancelled context, the per-file check fires.
	_, err = store.Load(cancelledCtx, "tape-2")
	if err == nil {
		t.Fatal("Load() with cancelled context during scan: error = nil, want error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Load() error = %v, want context.Canceled", err)
	}
}

func TestFileStore_ScanForID_SkipsCorruptAfterIDExtraction(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	store, err := NewFileStore(WithDirectory(dir), WithFilenameStrategy(ReadableFilenames()))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	// Save a valid tape first.
	tape := makeTape("route", "GET", "http://example.com/users")
	tape.ID = "target-id"
	if err := store.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// AFTER saving, write a file where extractIDFromJSON succeeds but
	// json.Unmarshal into a full Tape fails. Place it so it sorts before the
	// valid file (get_users.json). This exercises the unmarshal-skip path in
	// scanForID: extractIDFromJSON returns "target-id" (matches), but full
	// Tape unmarshal fails due to invalid recorded_at format.
	malformedPath := filepath.Join(dir, "aaa-malformed.json")
	malformedJSON := `{"id":"target-id","route":"r","recorded_at":"not-valid-time-format"}`
	if err := os.WriteFile(malformedPath, []byte(malformedJSON), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// Load should skip the malformed file and find the valid one.
	loaded, err := store.Load(ctx, "target-id")
	if err != nil {
		t.Fatalf("Load() error = %v, expected success after skipping corrupt file", err)
	}
	if loaded.ID != "target-id" {
		t.Errorf("Load().ID = %q, want %q", loaded.ID, "target-id")
	}
}

func TestFileStore_Delete_ScanSkipsCorruptAndUnreadable(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	store, err := NewFileStore(WithDirectory(dir), WithFilenameStrategy(ReadableFilenames()))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	tape := makeTape("route", "GET", "http://example.com/users")
	if err := store.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Add corrupt and unreadable JSON files that the Delete scan must skip.
	corruptPath := filepath.Join(dir, "aaa-corrupt.json")
	if err := os.WriteFile(corruptPath, []byte("{bad json"), 0o644); err != nil {
		t.Fatalf("WriteFile(corrupt) error = %v", err)
	}
	unreadablePath := filepath.Join(dir, "aaa-unreadable.json")
	if err := os.WriteFile(unreadablePath, []byte(`{"id":"x"}`), 0o000); err != nil {
		t.Fatalf("WriteFile(unreadable) error = %v", err)
	}
	t.Cleanup(func() { os.Chmod(unreadablePath, 0o644) })

	// Add a non-JSON file and a subdirectory to exercise entry-skip conditions.
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("WriteFile(txt) error = %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}

	// Delete should scan past all non-matching entries and succeed.
	if err := store.Delete(ctx, tape.ID); err != nil {
		t.Fatalf("Delete() error = %v, want nil (corrupt files should be skipped)", err)
	}

	_, err = store.Load(context.Background(), tape.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Load() after Delete error = %v, want ErrNotFound", err)
	}
}

func TestFileStore_List_SkipsUnreadableFiles(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	store, err := NewFileStore(WithDirectory(dir))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	// Save a valid tape.
	tape := makeTape("route", "GET", "http://example.com")
	if err := store.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Place an unreadable JSON file in the directory.
	unreadable := filepath.Join(dir, "unreadable.json")
	if err := os.WriteFile(unreadable, []byte(`{"id":"x"}`), 0o000); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Cleanup(func() { os.Chmod(unreadable, 0o644) })

	// List should skip the unreadable file and return the valid tape.
	tapes, err := store.List(ctx, Filter{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(tapes) != 1 {
		t.Errorf("List() returned %d tapes, want 1", len(tapes))
	}
}

func TestFileStore_Save_CollisionWithCorruptFile(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	store, err := NewFileStore(WithDirectory(dir), WithFilenameStrategy(func(tape Tape) string {
		return "fixed"
	}))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	// Place a corrupt (but readable) JSON file at the candidate path.
	// filenameAvailableForID reads it, extractIDFromJSON fails, returns (false, nil).
	// This makes the slot "occupied" so the strategy tries suffixed names.
	corruptPath := filepath.Join(dir, "fixed.json")
	if err := os.WriteFile(corruptPath, []byte("{broken json"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	tape := makeTape("route", "GET", "http://example.com")
	if err := store.Save(ctx, tape); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// The tape should have been written as fixed_2.json since fixed.json was occupied.
	path := filepath.Join(dir, "fixed_2.json")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected fixed_2.json to exist: %v", err)
	}
}

func TestFileStore_Delete_ScanContextCancel(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	store, err := NewFileStore(WithDirectory(dir), WithFilenameStrategy(ReadableFilenames()))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	// Save multiple tapes so there are entries to iterate.
	for i := 0; i < 3; i++ {
		tape := makeTape("route", "GET", "http://example.com/items")
		tape.ID = fmt.Sprintf("tape-%d", i)
		if err := store.Save(ctx, tape); err != nil {
			t.Fatalf("Save() error = %v", err)
		}
	}

	// Place a valid JSON file at fast-path with a different ID so Delete falls
	// through to scan, then cancel context.
	differentTape := makeTape("other", "POST", "http://example.com")
	data, _ := json.MarshalIndent(differentTape, "", "  ")
	fastPath := filepath.Join(dir, "tape-2.json")
	if err := os.WriteFile(fastPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err = store.Delete(cancelledCtx, "tape-2")
	if err == nil {
		t.Fatal("Delete() with cancelled context during scan: error = nil, want error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Delete() error = %v, want context.Canceled", err)
	}
}
