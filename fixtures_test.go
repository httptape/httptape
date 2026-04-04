package httptape

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// newTestTape creates a minimal valid Tape for testing.
func newTestTape(id, route, method, url string) Tape {
	return Tape{
		ID:    id,
		Route: route,
		Request: RecordedReq{
			Method: method,
			URL:    url,
		},
		Response: RecordedResp{
			StatusCode: 200,
		},
	}
}

func marshalTape(t *testing.T, tape Tape) []byte {
	t.Helper()
	data, err := json.MarshalIndent(tape, "", "  ")
	if err != nil {
		t.Fatalf("marshal tape: %v", err)
	}
	return data
}

func TestLoadFixtures(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T) string // returns dir path
		wantErr bool
		wantN   int // expected number of tapes (validated via List)
	}{
		{
			name: "happy path with two tapes",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				tape1 := newTestTape("tape-1", "users", "GET", "/users")
				tape2 := newTestTape("tape-2", "users", "POST", "/users")
				os.WriteFile(filepath.Join(dir, "tape-1.json"), marshalTape(t, tape1), 0o644)
				os.WriteFile(filepath.Join(dir, "tape-2.json"), marshalTape(t, tape2), 0o644)
				return dir
			},
			wantN: 2,
		},
		{
			name: "empty directory",
			setup: func(t *testing.T) string {
				return t.TempDir()
			},
			wantN: 0,
		},
		{
			name: "non-json files are ignored",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				tape1 := newTestTape("tape-1", "users", "GET", "/users")
				os.WriteFile(filepath.Join(dir, "tape-1.json"), marshalTape(t, tape1), 0o644)
				os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("not a tape"), 0o644)
				os.WriteFile(filepath.Join(dir, "notes.md"), []byte("# notes"), 0o644)
				return dir
			},
			wantN: 1,
		},
		{
			name: "invalid json returns error",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{invalid json"), 0o644)
				return dir
			},
			wantErr: true,
		},
		{
			name: "non-existent directory returns error",
			setup: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "does-not-exist")
			},
			wantErr: true,
		},
		{
			name: "subdirectories are ignored (flat scan)",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				tape1 := newTestTape("tape-1", "users", "GET", "/users")
				os.WriteFile(filepath.Join(dir, "tape-1.json"), marshalTape(t, tape1), 0o644)
				subdir := filepath.Join(dir, "subdir")
				os.MkdirAll(subdir, 0o755)
				tape2 := newTestTape("tape-2", "orders", "GET", "/orders")
				os.WriteFile(filepath.Join(subdir, "tape-2.json"), marshalTape(t, tape2), 0o644)
				return dir
			},
			wantN: 1, // only top-level tape
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := tt.setup(t)
			store, err := LoadFixtures(dir)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			tapes, err := store.List(context.Background(), Filter{})
			if err != nil {
				t.Fatalf("list tapes: %v", err)
			}
			if len(tapes) != tt.wantN {
				t.Errorf("got %d tapes, want %d", len(tapes), tt.wantN)
			}
		})
	}
}

func TestLoadFixturesFS(t *testing.T) {
	tape1 := newTestTape("tape-1", "users", "GET", "/users")
	tape2 := newTestTape("tape-2", "orders", "POST", "/orders")
	tape3 := newTestTape("tape-3", "auth", "GET", "/auth/token")

	tape1JSON := marshalTape(t, tape1)
	tape2JSON := marshalTape(t, tape2)
	tape3JSON := marshalTape(t, tape3)

	tests := []struct {
		name    string
		fsys    fstest.MapFS
		dir     string
		wantErr bool
		wantN   int
	}{
		{
			name: "happy path with two tapes",
			fsys: fstest.MapFS{
				"fixtures/tape-1.json": &fstest.MapFile{Data: tape1JSON},
				"fixtures/tape-2.json": &fstest.MapFile{Data: tape2JSON},
			},
			dir:   "fixtures",
			wantN: 2,
		},
		{
			name:  "empty directory",
			fsys:  fstest.MapFS{"fixtures/placeholder": &fstest.MapFile{Data: []byte{}}},
			dir:   "fixtures",
			wantN: 0,
		},
		{
			name: "recursive walk finds nested tapes",
			fsys: fstest.MapFS{
				"fixtures/tape-1.json":           &fstest.MapFile{Data: tape1JSON},
				"fixtures/subdir/tape-2.json":    &fstest.MapFile{Data: tape2JSON},
				"fixtures/deep/nest/tape-3.json": &fstest.MapFile{Data: tape3JSON},
			},
			dir:   "fixtures",
			wantN: 3,
		},
		{
			name: "non-json files are ignored",
			fsys: fstest.MapFS{
				"fixtures/tape-1.json": &fstest.MapFile{Data: tape1JSON},
				"fixtures/readme.txt":  &fstest.MapFile{Data: []byte("not a tape")},
			},
			dir:   "fixtures",
			wantN: 1,
		},
		{
			name: "invalid json returns error with filename",
			fsys: fstest.MapFS{
				"fixtures/bad.json": &fstest.MapFile{Data: []byte("{invalid")},
			},
			dir:     "fixtures",
			wantErr: true,
		},
		{
			name:    "non-existent directory returns error",
			fsys:    fstest.MapFS{},
			dir:     "no-such-dir",
			wantErr: true,
		},
		{
			name: "root directory walk",
			fsys: fstest.MapFS{
				"tape-1.json": &fstest.MapFile{Data: tape1JSON},
			},
			dir:   ".",
			wantN: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := LoadFixturesFS(tt.fsys, tt.dir)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			tapes, err := store.List(context.Background(), Filter{})
			if err != nil {
				t.Fatalf("list tapes: %v", err)
			}
			if len(tapes) != tt.wantN {
				t.Errorf("got %d tapes, want %d", len(tapes), tt.wantN)
			}
		})
	}
}

func TestLoadFixturesFS_error_includes_filename(t *testing.T) {
	fsys := fstest.MapFS{
		"fixtures/good.json": &fstest.MapFile{
			Data: marshalTape(t, newTestTape("good", "r", "GET", "/ok")),
		},
		"fixtures/broken.json": &fstest.MapFile{
			Data: []byte("not valid json"),
		},
	}

	_, err := LoadFixturesFS(fsys, "fixtures")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	errMsg := err.Error()
	if !contains(errMsg, "broken.json") {
		t.Errorf("error should mention filename 'broken.json', got: %s", errMsg)
	}
}

func TestLoadFixtures_error_includes_filename(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "broken.json"), []byte("not valid json"), 0o644)

	_, err := LoadFixtures(dir)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	errMsg := err.Error()
	if !contains(errMsg, "broken.json") {
		t.Errorf("error should mention filename 'broken.json', got: %s", errMsg)
	}
}

// contains checks if s contains substr. Using a helper to avoid importing strings.
func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
