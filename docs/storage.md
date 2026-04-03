# Storage

The `Store` interface is httptape's persistence abstraction. All recording and replay goes through this interface, making it easy to swap implementations or write your own.

## The Store interface

```go
type Store interface {
    Save(ctx context.Context, tape Tape) error
    Load(ctx context.Context, id string) (Tape, error)
    List(ctx context.Context, filter Filter) ([]Tape, error)
    Delete(ctx context.Context, id string) error
}
```

All methods accept a `context.Context` for cancellation and deadline support.

### Filter

```go
type Filter struct {
    Route  string // empty = no filter
    Method string // empty = no filter
}
```

An empty `Filter{}` returns all tapes. Filters are AND-ed: setting both `Route` and `Method` returns only tapes matching both.

### ErrNotFound

```go
var ErrNotFound = errors.New("httptape: tape not found")
```

`Load` and `Delete` return an error wrapping `ErrNotFound` when the tape does not exist. Use `errors.Is(err, httptape.ErrNotFound)` to check.

## MemoryStore

In-memory storage, ideal for tests and ephemeral recordings.

```go
store := httptape.NewMemoryStore()
```

Characteristics:
- All data lives in memory (map keyed by tape ID)
- Safe for concurrent use by multiple goroutines
- Deep-copies tapes on save and load to prevent aliasing
- No persistence -- data is lost when the process exits

### When to use

- Unit and integration tests
- Short-lived recording sessions
- Anywhere you don't need fixtures on disk

## FileStore

Filesystem-backed storage. Each tape is persisted as a JSON file.

```go
store, err := httptape.NewFileStore(
    httptape.WithDirectory("./fixtures"),
)
if err != nil {
    // handle error (e.g., permission denied)
}
```

Characteristics:
- One JSON file per tape, named `<tape-id>.json`
- Atomic writes via temp file + rename
- Base directory is created automatically (mode 0755) if it doesn't exist
- Safe for concurrent use within a single process
- **Not** safe for multi-process concurrent access to the same directory
- Default directory: `"fixtures"` in the current working directory

### WithDirectory

```go
httptape.WithDirectory("./testdata/fixtures")
```

Sets the base directory for fixture storage.

### Fixture file format

Each tape is stored as pretty-printed JSON with a trailing newline:

```json
{
  "id": "a1b2c3d4-e5f6-7890-abcd-ef0123456789",
  "route": "users-api",
  "recorded_at": "2024-01-15T10:30:00Z",
  "request": {
    "method": "GET",
    "url": "https://api.example.com/users/42",
    "headers": {
      "Accept": ["application/json"]
    },
    "body": null,
    "body_hash": ""
  },
  "response": {
    "status_code": 200,
    "headers": {
      "Content-Type": ["application/json"]
    },
    "body": "eyJ1c2VyIjoib2N0b2NhdCJ9"
  }
}
```

Fixtures are human-readable and safe to commit to version control (especially when sanitized).

### ID validation

The `FileStore` validates tape IDs to prevent path traversal attacks. IDs containing path separators (`/`, `\`) or directory traversal components (`..`) are rejected with `ErrInvalidID`.

## Custom Store implementations

Implement the `Store` interface to back httptape with any storage system:

```go
type RedisStore struct {
    client *redis.Client
    prefix string
}

func (s *RedisStore) Save(ctx context.Context, tape httptape.Tape) error {
    data, err := json.Marshal(tape)
    if err != nil {
        return fmt.Errorf("redis store save: %w", err)
    }
    return s.client.Set(ctx, s.prefix+tape.ID, data, 0).Err()
}

func (s *RedisStore) Load(ctx context.Context, id string) (httptape.Tape, error) {
    data, err := s.client.Get(ctx, s.prefix+id).Bytes()
    if err != nil {
        if errors.Is(err, redis.Nil) {
            return httptape.Tape{}, fmt.Errorf("redis store load %s: %w", id, httptape.ErrNotFound)
        }
        return httptape.Tape{}, fmt.Errorf("redis store load %s: %w", id, err)
    }
    var tape httptape.Tape
    if err := json.Unmarshal(data, &tape); err != nil {
        return httptape.Tape{}, fmt.Errorf("redis store load %s: %w", id, err)
    }
    return tape, nil
}

// Implement List and Delete similarly...
```

### Implementation guidelines

- All methods must respect `context.Context` cancellation
- `Load` and `Delete` must return errors wrapping `ErrNotFound` for missing tapes
- `List` must return an empty slice (not nil) when no tapes match
- `Save` uses upsert semantics -- overwrite if the ID already exists
- Implementations should be safe for concurrent use

## See also

- [Recording](recording.md) -- using stores with the Recorder
- [Replay](replay.md) -- using stores with the Server
- [Import/Export](import-export.md) -- moving fixtures between stores
