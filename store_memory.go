package httptape

import (
	"context"
	"fmt"
	"net/http"
	"sync"
)

// MemoryStore is an in-memory Store implementation.
// Intended primarily for testing, but usable in production for ephemeral recordings.
//
// MemoryStore is safe for concurrent use by multiple goroutines.
type MemoryStore struct {
	mu    sync.RWMutex
	tapes map[string]Tape // keyed by Tape.ID
}

// MemoryStoreOption configures a MemoryStore.
type MemoryStoreOption func(*MemoryStore)

// NewMemoryStore creates a new empty MemoryStore.
func NewMemoryStore(opts ...MemoryStoreOption) *MemoryStore {
	ms := &MemoryStore{
		tapes: make(map[string]Tape),
	}
	for _, opt := range opts {
		opt(ms)
	}
	return ms
}

// Save persists a tape into memory. If a tape with the same ID already exists,
// it is overwritten.
func (ms *MemoryStore) Save(ctx context.Context, tape Tape) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("httptape: memorystore save %s: %w", tape.ID, err)
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()

	ms.tapes[tape.ID] = deepCopyTape(tape)
	return nil
}

// Load retrieves a single tape by ID from memory.
// Returns an error wrapping ErrNotFound if the tape does not exist.
func (ms *MemoryStore) Load(ctx context.Context, id string) (Tape, error) {
	if err := ctx.Err(); err != nil {
		return Tape{}, fmt.Errorf("httptape: memorystore load %s: %w", id, err)
	}

	ms.mu.RLock()
	defer ms.mu.RUnlock()

	tape, ok := ms.tapes[id]
	if !ok {
		return Tape{}, fmt.Errorf("httptape: memorystore load %s: %w", id, ErrNotFound)
	}
	return deepCopyTape(tape), nil
}

// List returns all tapes matching the given filter.
// An empty filter returns all tapes. Returns an empty slice (not nil) if
// no tapes match.
func (ms *MemoryStore) List(ctx context.Context, filter Filter) ([]Tape, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("httptape: memorystore list: %w", err)
	}

	ms.mu.RLock()
	defer ms.mu.RUnlock()

	result := make([]Tape, 0)
	for _, tape := range ms.tapes {
		if filter.Route != "" && tape.Route != filter.Route {
			continue
		}
		if filter.Method != "" && tape.Request.Method != filter.Method {
			continue
		}
		result = append(result, deepCopyTape(tape))
	}
	return result, nil
}

// Delete removes a tape by ID from memory.
// Returns an error wrapping ErrNotFound if the tape does not exist.
func (ms *MemoryStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("httptape: memorystore delete %s: %w", id, err)
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()

	if _, ok := ms.tapes[id]; !ok {
		return fmt.Errorf("httptape: memorystore delete %s: %w", id, ErrNotFound)
	}
	delete(ms.tapes, id)
	return nil
}

// deepCopyTape returns a deep copy of the given tape, copying headers and body
// slices to prevent aliasing between caller and store internals.
func deepCopyTape(t Tape) Tape {
	cp := t
	cp.Request.Headers = copyHeaders(t.Request.Headers)
	cp.Request.Body = copyBytes(t.Request.Body)
	cp.Response.Headers = copyHeaders(t.Response.Headers)
	cp.Response.Body = copyBytes(t.Response.Body)
	if t.Response.SSEEvents != nil {
		cp.Response.SSEEvents = make([]SSEEvent, len(t.Response.SSEEvents))
		copy(cp.Response.SSEEvents, t.Response.SSEEvents)
	}
	return cp
}

// copyHeaders returns a deep copy of an http.Header map.
func copyHeaders(h http.Header) http.Header {
	if h == nil {
		return nil
	}
	cp := make(http.Header, len(h))
	for k, vs := range h {
		vsCopy := make([]string, len(vs))
		copy(vsCopy, vs)
		cp[k] = vsCopy
	}
	return cp
}

// copyBytes returns a copy of a byte slice.
func copyBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	return cp
}
