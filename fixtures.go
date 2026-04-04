package httptape

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// LoadFixtures creates a FileStore rooted at the given filesystem directory.
// It validates that the directory exists and that all .json files in it are
// valid Tape JSON. The returned FileStore is immediately usable with NewServer.
//
// Only the top-level directory is scanned (non-recursive), matching FileStore's
// flat-directory model. Files that fail to parse as a Tape cause an error that
// includes the filename. An empty directory produces an empty (but valid) store.
func LoadFixtures(dir string) (*FileStore, error) {
	// Verify the directory exists before creating a FileStore, since
	// NewFileStore creates the directory if absent (MkdirAll). LoadFixtures
	// is for loading existing fixtures, not creating new directories.
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("httptape: load fixtures: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("httptape: load fixtures: %s is not a directory", dir)
	}

	store, err := NewFileStore(WithDirectory(dir))
	if err != nil {
		return nil, fmt.Errorf("httptape: load fixtures: %w", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("httptape: load fixtures: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("httptape: load fixtures: read %s: %w", entry.Name(), err)
		}

		var tape Tape
		if err := json.Unmarshal(data, &tape); err != nil {
			return nil, fmt.Errorf("httptape: load fixtures: parse %s: %w", entry.Name(), err)
		}
	}

	return store, nil
}

// LoadFixturesFS reads all .json Tape files from the given fs.FS directory
// (recursive walk) and returns a MemoryStore populated with the decoded tapes.
// This is designed for use with embed.FS for self-contained test binaries.
//
// Files that fail to decode as a Tape cause an error that includes the file
// path. An empty directory produces an empty (but valid) store. The returned
// MemoryStore is immediately usable with NewServer.
func LoadFixturesFS(fsys fs.FS, dir string) (*MemoryStore, error) {
	store := NewMemoryStore()
	ctx := context.Background()

	err := fs.WalkDir(fsys, dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("httptape: load fixtures fs: walk %s: %w", path, walkErr)
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}

		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return fmt.Errorf("httptape: load fixtures fs: read %s: %w", path, err)
		}

		var tape Tape
		if err := json.Unmarshal(data, &tape); err != nil {
			return fmt.Errorf("httptape: load fixtures fs: parse %s: %w", path, err)
		}

		if err := store.Save(ctx, tape); err != nil {
			return fmt.Errorf("httptape: load fixtures fs: store %s: %w", path, err)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return store, nil
}
