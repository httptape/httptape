package httptape

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrInvalidID is returned when a tape ID contains path separators or
// directory traversal components that could escape the base directory.
var ErrInvalidID = errors.New("httptape: invalid tape ID")

// ErrFilenameCollision is returned when a filename collision cannot be
// resolved after exhausting the counter suffix range (_2 through _99).
var ErrFilenameCollision = errors.New("httptape: filename collision limit exceeded")

// maxCollisionCounter is the maximum counter suffix attempted when
// resolving filename collisions.
const maxCollisionCounter = 99

// maxFilenameLength is the maximum number of characters allowed in a
// filename before the ".json" extension. This keeps filenames under
// filesystem limits (typically 255 bytes).
const maxFilenameLength = 200

// FilenameStrategy is a function that computes the filename stem (without
// the ".json" extension) for a given Tape. The returned string must not
// contain path separators. Built-in strategies are provided by
// UUIDFilenames and ReadableFilenames.
type FilenameStrategy func(tape Tape) string

// UUIDFilenames returns a FilenameStrategy that uses the tape's ID as the
// filename stem. This is the default strategy and produces filenames
// identical to the original behavior (e.g., "550e8400-e29b-41d4-a716-446655440000.json").
func UUIDFilenames() FilenameStrategy {
	return func(tape Tape) string {
		return tape.ID
	}
}

// ReadableFilenames returns a FilenameStrategy that produces human-readable
// filenames based on the tape's HTTP method, URL path, query string, and
// request body hash.
//
// The format is: <method>_<url-slug>[_q-<query-hash>][_<body-hash>].json
//
// Examples:
//
//	GET /api/users              -> get_api-users.json
//	POST /api/users (with body) -> post_api-users_a1b2.json
//	GET /api/users?page=2       -> get_api-users_q-3f4a.json
//	HEAD /                      -> head_root.json
func ReadableFilenames() FilenameStrategy {
	return func(tape Tape) string {
		method := strings.ToLower(tape.Request.Method)
		if method == "" {
			method = "unknown"
		}

		slug := slugifyURL(tape.Request.URL)

		parts := []string{method, slug}

		// Append query hash if query string is non-empty.
		if qh := queryHash(tape.Request.URL); qh != "" {
			parts = append(parts, "q-"+qh)
		}

		// Append body hash if body is non-empty.
		if bh := bodyHashPrefix(tape.Request.BodyHash); bh != "" {
			parts = append(parts, bh)
		}

		name := strings.Join(parts, "_")

		// Truncate to maxFilenameLength.
		if len(name) > maxFilenameLength {
			name = name[:maxFilenameLength]
			// Clean up any trailing dash from truncation.
			name = strings.TrimRight(name, "-")
		}

		if name == "" {
			return tape.ID
		}
		return name
	}
}

// slugifyURL extracts the path from a URL and normalizes it to a slug
// containing only lowercase alphanumeric characters and dashes.
// Returns "root" for empty or "/" paths.
func slugifyURL(rawURL string) string {
	path := ""
	if u, err := url.Parse(rawURL); err == nil {
		path = u.Path
	} else {
		// Fallback: try to extract path manually.
		path = rawURL
		if i := strings.Index(path, "://"); i >= 0 {
			path = path[i+3:]
		}
		if i := strings.IndexByte(path, '/'); i >= 0 {
			path = path[i:]
		} else {
			path = ""
		}
		if i := strings.IndexByte(path, '?'); i >= 0 {
			path = path[:i]
		}
	}

	// Strip leading and trailing slashes.
	path = strings.Trim(path, "/")
	if path == "" {
		return "root"
	}

	// Normalize: replace any non-[a-z0-9] character with a dash,
	// collapse consecutive dashes, trim leading/trailing dashes.
	var b strings.Builder
	b.Grow(len(path))
	lastDash := false
	for _, r := range strings.ToLower(path) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
		} else {
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}

	result := strings.TrimRight(b.String(), "-")
	if result == "" {
		return "root"
	}
	return result
}

// queryHash returns the first 4 hex characters of the SHA-256 hash of the
// raw query string from the given URL. Returns "" if the query is empty.
func queryHash(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	raw := u.RawQuery
	if raw == "" {
		return ""
	}
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:2])
}

// bodyHashPrefix returns the first 4 hex characters of the given body hash.
// Returns "" if the hash is empty.
func bodyHashPrefix(fullHash string) string {
	if fullHash == "" {
		return ""
	}
	if len(fullHash) < 4 {
		return fullHash
	}
	return fullHash[:4]
}

// FileStore is a filesystem-backed Store implementation. Each tape is persisted
// as a single JSON file.
//
// FileStore is safe for concurrent use by multiple goroutines within a single
// process. It is not safe for multi-process concurrent access to the same
// directory.
type FileStore struct {
	dir      string           // base directory for fixtures
	strategy FilenameStrategy // filename strategy for new saves
	mu       sync.RWMutex
}

// FileStoreOption configures a FileStore.
type FileStoreOption func(*FileStore)

// WithDirectory sets the base directory for fixture storage.
// If not set, defaults to "fixtures" in the current working directory.
func WithDirectory(dir string) FileStoreOption {
	return func(fs *FileStore) {
		fs.dir = dir
	}
}

// WithFilenameStrategy sets the filename strategy used when saving tapes.
// If not set, defaults to UUIDFilenames() which produces <id>.json filenames.
func WithFilenameStrategy(s FilenameStrategy) FileStoreOption {
	return func(fs *FileStore) {
		fs.strategy = s
	}
}

// NewFileStore creates a new FileStore. The base directory is created if it
// does not exist (with mode 0o755).
func NewFileStore(opts ...FileStoreOption) (*FileStore, error) {
	fs := &FileStore{
		dir:      "fixtures",
		strategy: UUIDFilenames(),
	}
	for _, opt := range opts {
		opt(fs)
	}

	if err := os.MkdirAll(fs.dir, 0o755); err != nil {
		return nil, fmt.Errorf("httptape: filestore create directory %s: %w", fs.dir, err)
	}
	return fs, nil
}

// Save persists a tape as a JSON file. If a tape with the same ID already exists
// (possibly under a different filename), the old file is removed before writing
// the new one. Writes are atomic via a temporary file and rename.
//
// When the filename strategy produces a name that already exists for a different
// tape, a counter suffix (_2, _3, ..., _99) is appended. If all suffixes are
// exhausted, Save returns ErrFilenameCollision.
func (fs *FileStore) Save(ctx context.Context, tape Tape) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("httptape: filestore save %s: %w", tape.ID, err)
	}
	if err := validateID(tape.ID); err != nil {
		return fmt.Errorf("httptape: filestore save: %w", err)
	}

	data, err := json.MarshalIndent(tape, "", "  ")
	if err != nil {
		return fmt.Errorf("httptape: filestore save %s: %w", tape.ID, err)
	}
	data = append(data, '\n') // POSIX trailing newline

	fs.mu.Lock()
	defer fs.mu.Unlock()

	// Determine the target filename using the strategy.
	stem := fs.strategy(tape)

	// Sanitize strategy output to prevent path traversal from custom strategies.
	// Built-in strategies (UUIDFilenames, ReadableFilenames) produce safe values,
	// but user-provided strategies could return "../escape" or similar.
	stem = filepath.Base(stem)
	if stem == "" || stem == "." || stem == ".." {
		// Defensive fallback: empty, ".", or ".." strategy output -> use tape ID.
		stem = tape.ID
	}

	targetName, err := fs.resolveFilename(stem, tape.ID)
	if err != nil {
		return fmt.Errorf("httptape: filestore save %s: %w", tape.ID, err)
	}

	// Clean up old file if the tape was previously saved under a different name.
	fs.removeOldFile(tape.ID, targetName)

	// Write to a temporary file first for atomicity.
	tmpFile, err := os.CreateTemp(fs.dir, "tape-*.tmp")
	if err != nil {
		return fmt.Errorf("httptape: filestore save %s: %w", tape.ID, err)
	}
	tmpPath := tmpFile.Name()

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("httptape: filestore save %s: %w", tape.ID, err)
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("httptape: filestore save %s: %w", tape.ID, err)
	}

	target := filepath.Join(fs.dir, targetName)
	if err := os.Rename(tmpPath, target); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("httptape: filestore save %s: %w", tape.ID, err)
	}
	return nil
}

// resolveFilename determines the final filename (including ".json" extension)
// for a tape with the given stem and ID. If the stem filename is already taken
// by a different tape, counter suffixes are tried.
// Must be called with fs.mu held.
func (fs *FileStore) resolveFilename(stem, tapeID string) (string, error) {
	candidate := stem + ".json"
	path := filepath.Join(fs.dir, candidate)

	// Check if the candidate is available or belongs to the same tape.
	if ok, err := fs.filenameAvailableForID(path, tapeID); err != nil {
		return "", err
	} else if ok {
		return candidate, nil
	}

	// Try counter suffixes _2 through _99.
	for i := 2; i <= maxCollisionCounter; i++ {
		candidate = fmt.Sprintf("%s_%d.json", stem, i)
		path = filepath.Join(fs.dir, candidate)
		if ok, err := fs.filenameAvailableForID(path, tapeID); err != nil {
			return "", err
		} else if ok {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("%w: stem %q", ErrFilenameCollision, stem)
}

// filenameAvailableForID reports whether the file at the given path is
// available for use by a tape with the given ID. A path is available if:
// - the file does not exist, or
// - the file exists and contains a tape with the same ID.
// Must be called with fs.mu held.
func (fs *FileStore) filenameAvailableForID(path, tapeID string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil // file doesn't exist, available
		}
		return false, err
	}

	// File exists; check if it belongs to the same tape.
	existingID, err := extractIDFromJSON(data)
	if err != nil {
		// Corrupt JSON file: treat as occupied (don't overwrite unknown files).
		return false, nil
	}
	return existingID == tapeID, nil
}

// removeOldFile scans the directory for a file containing a tape with the
// given ID and removes it, unless it has the same name as newFilename.
// This handles the case where a tape was previously saved under a different
// filename strategy. Must be called with fs.mu held.
func (fs *FileStore) removeOldFile(tapeID, newFilename string) {
	entries, err := os.ReadDir(fs.dir)
	if err != nil {
		return // best effort
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") || name == newFilename {
			continue
		}

		data, err := os.ReadFile(filepath.Join(fs.dir, name))
		if err != nil {
			continue
		}
		id, err := extractIDFromJSON(data)
		if err != nil {
			continue // corrupt JSON, skip
		}
		if id == tapeID {
			// Best-effort: if removal fails (e.g., concurrent delete), the orphan
			// file is left behind but Save still succeeds with the new file.
			os.Remove(filepath.Join(fs.dir, name))
			return // found and removed; IDs are unique
		}
	}
}

// Load retrieves a single tape by ID from the filesystem.
// It first tries the fast path (<id>.json), then falls back to scanning the
// directory for a file containing a tape with the matching ID.
// Returns an error wrapping ErrNotFound if no file contains the tape.
func (fs *FileStore) Load(ctx context.Context, id string) (Tape, error) {
	if err := ctx.Err(); err != nil {
		return Tape{}, fmt.Errorf("httptape: filestore load %s: %w", id, err)
	}
	if err := validateID(id); err != nil {
		return Tape{}, fmt.Errorf("httptape: filestore load: %w", err)
	}

	fs.mu.RLock()
	defer fs.mu.RUnlock()

	// Fast path: try <id>.json directly.
	fastPath := filepath.Join(fs.dir, id+".json")
	data, err := os.ReadFile(fastPath)
	if err == nil {
		var tape Tape
		if err := json.Unmarshal(data, &tape); err != nil {
			return Tape{}, fmt.Errorf("httptape: filestore load %s: %w", id, err)
		}
		if tape.ID == id {
			return tape, nil
		}
		// File exists but has a different ID (unlikely but possible with
		// manual file management). Fall through to scan.
	}

	// Scan fallback: look through all JSON files for a matching ID.
	tape, found, err := fs.scanForID(ctx, id)
	if err != nil {
		return Tape{}, fmt.Errorf("httptape: filestore load %s: %w", id, err)
	}
	if !found {
		return Tape{}, fmt.Errorf("httptape: filestore load %s: %w", id, ErrNotFound)
	}
	return tape, nil
}

// List returns all tapes matching the given filter by scanning all JSON files
// in the base directory. Returns an empty slice (not nil) if no tapes match.
// Corrupt JSON files are silently skipped.
func (fs *FileStore) List(ctx context.Context, filter Filter) ([]Tape, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("httptape: filestore list: %w", err)
	}

	fs.mu.RLock()
	defer fs.mu.RUnlock()

	entries, err := os.ReadDir(fs.dir)
	if err != nil {
		return nil, fmt.Errorf("httptape: filestore list: %w", err)
	}

	result := make([]Tape, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("httptape: filestore list: %w", err)
		}

		data, err := os.ReadFile(filepath.Join(fs.dir, entry.Name()))
		if err != nil {
			continue // skip unreadable files
		}

		var tape Tape
		if err := json.Unmarshal(data, &tape); err != nil {
			continue // skip corrupt JSON silently
		}

		if filter.Route != "" && tape.Route != filter.Route {
			continue
		}
		if filter.Method != "" && tape.Request.Method != filter.Method {
			continue
		}
		result = append(result, tape)
	}
	return result, nil
}

// Delete removes a tape by ID from the filesystem.
// It first tries the fast path (<id>.json), then falls back to scanning the
// directory for a file containing a tape with the matching ID.
// Returns an error wrapping ErrNotFound if no file contains the tape.
func (fs *FileStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("httptape: filestore delete %s: %w", id, err)
	}
	if err := validateID(id); err != nil {
		return fmt.Errorf("httptape: filestore delete: %w", err)
	}

	fs.mu.Lock()
	defer fs.mu.Unlock()

	// Fast path: try <id>.json directly.
	fastPath := filepath.Join(fs.dir, id+".json")
	data, err := os.ReadFile(fastPath)
	if err == nil {
		fileID, parseErr := extractIDFromJSON(data)
		if parseErr == nil && fileID == id {
			if err := os.Remove(fastPath); err != nil {
				return fmt.Errorf("httptape: filestore delete %s: %w", id, err)
			}
			return nil
		}
		// File exists but has different ID or is corrupt. Fall through to scan.
	}

	// Scan fallback: look through all JSON files for a matching ID.
	entries, err := os.ReadDir(fs.dir)
	if err != nil {
		return fmt.Errorf("httptape: filestore delete %s: %w", id, err)
	}

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("httptape: filestore delete %s: %w", id, err)
		}

		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		fpath := filepath.Join(fs.dir, entry.Name())
		data, err := os.ReadFile(fpath)
		if err != nil {
			continue
		}

		fileID, err := extractIDFromJSON(data)
		if err != nil {
			continue // corrupt JSON, skip
		}

		if fileID == id {
			if err := os.Remove(fpath); err != nil {
				return fmt.Errorf("httptape: filestore delete %s: %w", id, err)
			}
			return nil
		}
	}

	return fmt.Errorf("httptape: filestore delete %s: %w", id, ErrNotFound)
}

// scanForID scans all JSON files in the directory looking for a tape with the
// given ID. Corrupt JSON files are silently skipped. Must be called with at
// least fs.mu.RLock held.
func (fs *FileStore) scanForID(ctx context.Context, id string) (Tape, bool, error) {
	entries, err := os.ReadDir(fs.dir)
	if err != nil {
		return Tape{}, false, err
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		if err := ctx.Err(); err != nil {
			return Tape{}, false, err
		}

		data, err := os.ReadFile(filepath.Join(fs.dir, entry.Name()))
		if err != nil {
			continue
		}

		// Quick check: extract just the ID field first to avoid full unmarshal.
		fileID, err := extractIDFromJSON(data)
		if err != nil {
			continue // corrupt JSON, skip
		}
		if fileID != id {
			continue
		}

		var tape Tape
		if err := json.Unmarshal(data, &tape); err != nil {
			continue // corrupt JSON, skip
		}
		return tape, true, nil
	}

	return Tape{}, false, nil
}

// extractIDFromJSON extracts the "id" field from a JSON byte slice by
// unmarshalling into a minimal struct with only the ID field. The rest of
// the document is parsed but discarded. This is used for scanning directory
// contents when only the tape ID is needed.
func extractIDFromJSON(data []byte) (string, error) {
	var partial struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &partial); err != nil {
		return "", err
	}
	return partial.ID, nil
}

// validateID checks that id is safe to use as a filename component.
// It rejects IDs containing path separators or ".." traversal components.
func validateID(id string) error {
	if id == "" {
		return fmt.Errorf("%w: empty ID", ErrInvalidID)
	}
	if strings.ContainsAny(id, `/\`) {
		return fmt.Errorf("%w: contains path separator", ErrInvalidID)
	}
	if id == ".." || strings.HasPrefix(id, ".."+string(filepath.Separator)) ||
		strings.HasSuffix(id, string(filepath.Separator)+"..") ||
		strings.Contains(id, string(filepath.Separator)+".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: contains path traversal", ErrInvalidID)
	}
	// Also reject the bare ".." even without separators (already handled above)
	// and any cleaned path that would escape.
	if id == "." {
		return fmt.Errorf("%w: invalid ID", ErrInvalidID)
	}
	return nil
}
