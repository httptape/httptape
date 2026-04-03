# Import/Export

httptape supports exporting fixtures to `tar.gz` bundles and importing them back. This enables sharing fixture sets between environments (record in production, replay in CI) and between team members.

## ExportBundle

```go
func ExportBundle(ctx context.Context, s Store, opts ...ExportOption) (io.Reader, error)
```

Exports all tapes from a store as a streaming `tar.gz` archive. The returned `io.Reader` streams the archive -- it is not buffered entirely in memory.

### Basic export

```go
store, _ := httptape.NewFileStore(httptape.WithDirectory("./fixtures"))

reader, err := httptape.ExportBundle(context.Background(), store)
if err != nil {
    log.Fatal(err)
}

f, _ := os.Create("fixtures.tar.gz")
io.Copy(f, reader)
f.Close()
```

### Bundle layout

```
manifest.json          -- bundle metadata
fixtures/<id>.json     -- one file per tape
```

The `manifest.json` contains:

```go
type Manifest struct {
    ExportedAt      time.Time `json:"exported_at"`
    FixtureCount    int       `json:"fixture_count"`
    Routes          []string  `json:"routes"`
    SanitizerConfig string    `json:"sanitizer_config,omitempty"`
}
```

### Export options

#### WithRoutes

```go
httptape.WithRoutes("users-api", "payments-api")
```

Filters the export to include only tapes with matching route labels. Route matching is exact and case-sensitive.

#### WithMethods

```go
httptape.WithMethods("GET", "POST")
```

Filters the export to include only tapes with matching HTTP methods. Methods are compared case-insensitively.

#### WithSince

```go
cutoff, _ := time.Parse(time.RFC3339, "2024-01-01T00:00:00Z")
httptape.WithSince(cutoff)
```

Filters the export to include only tapes recorded at or after the given timestamp.

#### WithSanitizerConfig

```go
httptape.WithSanitizerConfig("RedactHeaders + FakeFields(email, user_id)")
```

Attaches a human-readable summary of the sanitizer configuration to the bundle manifest. This is purely informational -- it does not affect import behavior.

### Combining filters

All filters are AND-ed. A tape must pass every active filter to be included:

```go
reader, err := httptape.ExportBundle(ctx, store,
    httptape.WithRoutes("payments-api"),
    httptape.WithMethods("POST"),
    httptape.WithSince(lastWeek),
)
```

This exports only POST requests to the "payments-api" route recorded in the last week.

## ImportBundle

```go
func ImportBundle(ctx context.Context, s Store, r io.Reader) error
```

Imports tapes from a `tar.gz` bundle into the given store.

### Basic import

```go
store, _ := httptape.NewFileStore(httptape.WithDirectory("./fixtures"))

f, _ := os.Open("fixtures.tar.gz")
defer f.Close()

err := httptape.ImportBundle(context.Background(), store, f)
if err != nil {
    log.Fatal(err)
}
```

### Merge strategy

- Fixtures in the bundle **overwrite** any existing fixtures with the same ID in the store.
- Fixtures already in the store whose IDs are not in the bundle are **left untouched**.

This is an additive merge, not a replacement.

### Validation

The entire bundle is validated before any fixtures are persisted:

1. The `manifest.json` must exist and be valid JSON
2. The manifest's `fixture_count` must match the actual number of fixture files
3. Each fixture must have a non-empty `ID`, `Method`, and `URL`
4. All fixture files must be valid JSON

If validation fails, the store is not modified.

### Size limits

Individual tar entries are limited to 50 MB to prevent zip-bomb-style attacks.

## Workflow example

### Record in staging, replay in CI

**Staging server:**
```bash
httptape record \
  --upstream https://api.staging.example.com \
  --fixtures ./recorded \
  --config sanitize.json

# After recording, export:
httptape export --fixtures ./recorded --output fixtures.tar.gz
```

**CI pipeline:**
```bash
httptape import --fixtures ./fixtures --input fixtures.tar.gz
httptape serve --fixtures ./fixtures --port 8081
# Run tests against localhost:8081
```

### Programmatic transfer

```go
// Export from source
reader, _ := httptape.ExportBundle(ctx, sourceStore,
    httptape.WithRoutes("api-v2"),
)

// Import to destination
httptape.ImportBundle(ctx, destStore, reader)
```

## See also

- [Storage](storage.md) -- the Store interface
- [CLI](cli.md) -- export and import commands
- [Docker](docker.md) -- sharing fixtures via volumes
