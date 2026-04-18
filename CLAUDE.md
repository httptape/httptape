# httptape — Project Constitution

## What this project is

httptape is an embeddable Go library for HTTP traffic recording, sanitization, and replay.
It captures HTTP request/response pairs, sanitizes sensitive data on write, and replays
them as a mock server. Think WireMock, but native Go, embeddable, and with sanitization
built into the core.

Target: Go developers who need deterministic, safe API mocking without Java, external
processes, or manual fixture management.

License: Apache 2.0

---

## Locked decisions (do not relitigate)

| Decision | Choice | Reason |
|---|---|---|
| Language | Go | Embeddable in Go applications |
| License | Apache 2.0 | Permissive, compatible with most projects |
| Architecture | Hexagonal (ports & adapters) | Clean boundaries, testable, embeddable |
| Dependencies | stdlib only (v1) | Zero transitive deps for embedders |
| Sanitization | On write, not on export | Sensitive data never touches disk |
| Deterministic faking | HMAC-SHA256 based | Same input → same fake, preserves consistency |
| Fixture format | JSON | Human-readable, easy to inspect and edit |
| Storage | Pluggable interface | Filesystem for production, memory for tests |
| Matching | Progressive | Exact match first, fuzzy/regex as opt-in |
| Recording | Async by default | Non-blocking, minimal hot-path overhead |
| Body handling | Store hash + full body | Hash for matching, full body for replay |
| Distribution | Go module only (v1) | CLI wrapper is a future goal, not v1 |

---

## Architecture principles

- **Hexagonal architecture**: core domain has zero external dependencies.
  All I/O goes through ports (interfaces). Adapters implement ports.
- **DDD-lite**: model the domain explicitly. Value objects, domain types.
  No full aggregates/entities needed — this is a library, not a service.
- **SOLID**: single responsibility per type, dependency inversion via interfaces.
- **Functional where Go allows**: prefer pure functions, immutable value objects,
  explicit error handling over panics.
- **No global state**: everything passed via dependency injection or functional options.
- **Embeddable first**: no `init()`, no package-level state, no side effects on import.

### Package structure

```
httptape/
  tape.go              # Core Tape type and related types
  tape_test.go
  recorder.go          # RoundTripper wrapper for recording (includes RecorderOption)
  recorder_test.go
  sanitizer.go         # Redaction and deterministic faking (includes SanitizeFunc, Pipeline)
  sanitizer_test.go
  server.go            # Mock HTTP server (http.Handler, includes ServerOption)
  server_test.go
  matcher.go           # Matcher interface, Criterion, CompositeMatcher, ExactMatcher
  matcher_test.go
  store.go             # Storage port (interface)
  store_file.go        # Filesystem storage adapter (includes FileStoreOption)
  store_file_test.go
  store_memory.go      # In-memory storage adapter (for tests)
  store_memory_test.go
  config.go            # Declarative JSON config for sanitization pipeline
  config_test.go
  config.schema.json   # JSON Schema for config file validation (IDE/CI use)
  bundle.go            # Import/export (tar.gz)
  bundle_test.go
  integration_test.go  # End-to-end integration tests
  race_test.go         # Dedicated concurrency/race-condition tests
  doc.go               # Package-level documentation
```

Note: functional options are co-located with their respective types (e.g.,
`RecorderOption` in `recorder.go`, `ServerOption` in `server.go`) rather than
in a monolithic `options.go` file. The `Matcher` interface lives in `matcher.go`
alongside its implementations.

### Layer rules

- **Core types** (`tape.go`): zero imports outside stdlib. Defines `Tape`, `RecordedReq`, `RecordedResp`.
- **Ports** (`store.go`, top of `matcher.go`): interfaces only. No implementations.
- **Adapters** (`store_file.go`, `store_memory.go`): implement port interfaces. May use stdlib I/O.
- **Services** (`recorder.go`, `server.go`, `sanitizer.go`): orchestrate core types and ports. Accept ports via constructor injection.

Since this is a single Go package (library), we don't have `internal/ports/` and `internal/adapters/` directories.
Instead, the hexagonal boundaries are enforced by convention:
- Interfaces are defined at the top of their respective files or in `store.go`
- Implementations live in separate files (`store_file.go`, `store_memory.go`)
- Core types have zero I/O — they are pure data structures

---

## Dependency rules

- **v1: stdlib only** — no external dependencies whatsoever
- This is a hard constraint: embedders should not inherit transitive dependencies
- If a stdlib-only approach is genuinely impossible for a feature, document why in an ADR
  and get approval before adding any dependency
- **Approved licenses** (for future versions): Apache 2.0, MIT, BSD-2, BSD-3
- **Rejected**: GPL, AGPL, LGPL, CC-BY-SA, proprietary

---

## Code style

- Go standard formatting (`gofmt`, `goimports`)
- Error wrapping: `fmt.Errorf("context: %w", err)` — never swallow errors
- No `panic` in hot paths — this is a library, never panic on behalf of the caller.
  Exception: constructor guards (e.g., `NewRecorder`, `NewServer`) may panic on nil
  required dependencies. These are programming errors, not runtime failures, following
  the `regexp.MustCompile` precedent in the Go standard library.
- Table-driven tests preferred
- Every exported type and function must have a godoc comment
- Functional options pattern for all public constructors
- No `init()` functions — zero side effects on import

---

## Testing

- Unit tests for all logic
- Tests live next to the code they test (`foo_test.go`)
- Minimum coverage target: 90% (this is a library — high coverage is expected)
- No test dependencies — stdlib `testing` package only
- Use `net/http/httptest` for HTTP server tests
- Test fixtures stored in `testdata/` directory

---

## Agent pipeline

The standard flow for any GitHub issue:

```
PM Agent → Architect Agent → Dev Agent → Reviewer Agent → (your PR review) → repeat until merged
```

---

## GitHub conventions

- Org: `VibeWarden`
- Repo: `httptape`
- Branch naming: `feat/<issue-number>-<short-slug>`, `fix/<issue-number>-<short-slug>`
- Commit style: conventional commits (`feat:`, `fix:`, `chore:`, `docs:`, `test:`)
- PR title = conventional commit style
- Labels: `milestone:*` for milestones, `priority:*` for priority

---

## Sub-agent routing rules

**Sequential dispatch** (this project always uses sequential):
- PM → Architect → Dev → Reviewer pipeline is always sequential
- Each stage depends on output of previous

**Do not parallelize** stages — shared files and state between stages.

**Background dispatch** is fine for:
- Research tasks (looking up library docs, license checks)
- Codebase exploration that doesn't modify files

---

## Key differentiator

**Sanitize-on-write** is httptape's most original idea.
Sensitive data (secrets, PII) is redacted before it touches disk — there is no "raw"
recording mode. Combined with deterministic faking (HMAC-based, same input always
produces the same fake), recorded fixtures are safe to commit to version control,
share across teams, and move between environments.

Treat the sanitization pipeline with the same care as a security boundary.
