# Changelog

All notable changes to httptape are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- **`WithCacheLookupDisabled()`**: CachingOption that disables the cache
  hit path entirely. Every request is forwarded to upstream and recorded.
  Single-flight dedup, SSE tee, sanitization, and stale fallback remain
  active. Replaces the internal `neverMatcher` workaround used by Proxy. (#208)

- **Template helpers**: `{{now}}`, `{{uuid}}`, `{{randomHex}}`, `{{randomInt}}`,
  `{{counter}}`, and `{{faker.*}}` template expressions in response bodies and
  headers. Helpers support keyword arguments (e.g., `{{now format=unix}}`). (#196)

- **`PathPatternCriterion`**: Express-style path pattern matching with named
  segments (e.g., `/users/:id`). Captured path parameters are available in
  templates via `{{pathParam.NAME}}`. Score 3. (#196)

- **`{{pathParam.*}}` accessor**: resolves captured path segments from
  `PathPatternCriterion` in template expressions. (#196)

- **Nested template evaluation**: helper arguments can contain `{{...}}`
  expressions that are resolved before the helper runs (e.g.,
  `{{faker.name seed=user-{{pathParam.id}}}}`). (#196)

- **JSON-aware template resolution**: JSON response bodies are parsed,
  templates in string values are resolved, and the result is properly
  JSON-escaped and re-serialized. (#196)

- **`Server.ResetCounter`**: resets named or all template counters. (#196)

- **`ResolveTemplateBodySimple`**: backward-compatible wrapper for template
  resolution using only request data. (#196)

- **Synthesis mode**: exemplar tapes with URL patterns and template expressions
  generate responses for unmatched URLs. Opt-in via `WithSynthesis()` (library)
  or `--synthesize` (CLI). Exemplar tapes use `"exemplar": true` and
  `"url_pattern"` fields. Two-phase match: exact tapes first, exemplar fallback
  second. (#199, ADR-43)

- **Type coercion**: `| int`, `| float`, `| bool` coercion pipes in JSON
  template expressions convert resolved strings to native JSON types. Only
  effective in JSON response bodies of exemplar tapes. (#199)

- **`ValidateTape` / `ValidateExemplar`**: structural validation for tapes,
  including exemplar-specific constraints (url_pattern required, SSE not
  supported, mutual exclusivity with url). (#199)

- **Startup tape validation**: the CLI `serve` command validates all loaded
  tapes at startup. Invalid exemplar tapes produce a startup error. (#199)

- **Config support for `path_pattern`**: declarative `"type": "path_pattern"`
  criterion with `"pattern"` field. (#196)

### Breaking Changes

- **`ResolveTemplateBody` and `ResolveTemplateHeaders` signatures changed**:
  These now accept `*templateCtx` (unexported) instead of `*http.Request`.
  External callers should use `ResolveTemplateBodySimple` instead. Pre-1.0,
  acceptable. (#196)

- **Unknown template namespaces**: expressions like `{{state.counter}}` that
  were previously left as literal text are now replaced with empty string in
  lenient mode (error in strict mode). All supported expressions are now
  explicitly dispatched. (#196)

### Breaking Changes (prior)

- **`NewServer` signature change**: `NewServer(store Store, opts ...ServerOption)`
  now returns `(*Server, error)` instead of `*Server`. The constructor validates
  option values (e.g., error rate) after all options are applied and returns an
  error if any are invalid. Nil-store panics are retained (programming error
  convention). (#215, ADR-46)

- **`NewProxy` signature change**: `NewProxy(l1, l2 Store, opts ...ProxyOption)`
  now returns `(*Proxy, error)` instead of `*Proxy`. The constructor validates
  cross-option constraints (e.g., `WithProxyHealthEndpoint` requires
  `WithProxyUpstreamURL`) and returns an error instead of panicking. Nil-store
  panics are retained. (#215, ADR-46)

- **`SSETimingAccelerated` signature change**: `SSETimingAccelerated(factor float64)`
  now returns `(SSETimingMode, error)` instead of `SSETimingMode`. Returns an
  error when factor is <= 0 instead of panicking. (#215, ADR-46)

### Migration

All three changes are caught by the Go compiler -- no silent breakage. Update
call sites as follows:

```go
// Before
srv := httptape.NewServer(store, opts...)

// After
srv, err := httptape.NewServer(store, opts...)
if err != nil {
    // handle error
}
```

The same pattern applies to `NewProxy` and `SSETimingAccelerated`. Behavior on
success is unchanged.

## [0.13.1] - 2026-04-18

### Changed

- `Proxy` now composes `CachingTransport` internally, completing ADR-44
  Option B (deferred from v0.13.0 in PR #204). The L1 cache + fallback
  remain Proxy concerns; L2 cache + SSE tee + stale fallback are
  delegated to CachingTransport. **No observable behavior change for
  CLI users or Go embedders** -- all existing `ProxyOption` functions
  continue to work identically. Single-flight deduplication is now active
  in proxy mode. See #205.

### Fixed

- Documentation previously claimed "Proxy composes CachingTransport"
  as of v0.13.0; this is now actually true.

## [0.13.0] - 2026-04-18

### Added

- **CachingTransport**: New `http.RoundTripper` that provides transparent,
  store-backed caching. On cache hit, returns the recorded response without
  contacting upstream. On cache miss, forwards to upstream, records the
  response (with optional sanitization), and returns it. This is the library
  primitive for cache-through-upstream logic. (#202)
- `NewCachingTransport(upstream, store, opts...)` constructor with functional
  options pattern.
- `CachingOption` type with 10 option functions: `WithCacheMatcher`,
  `WithCacheSanitizer`, `WithCacheFilter`, `WithCacheSingleFlight`,
  `WithCacheMaxBodySize`, `WithCacheRoute`, `WithCacheOnError`,
  `WithCacheSSERecording`, `WithCacheUpstreamDownFallback`,
  `WithCacheUpstreamTimeout`.
- **Single-flight deduplication**: Concurrent identical cache misses share a
  single upstream call (stdlib-only implementation, no external dependencies).
- **Stale-fallback policy** (`WithCacheUpstreamDownFallback`): When upstream
  is unreachable and a cached tape exists, returns it with
  `X-Httptape-Stale: true` header. Opt-in, disabled by default. (#164)
- **Upstream timeout** (`WithCacheUpstreamTimeout`): Configurable deadline for
  upstream requests on cache miss. On timeout, the stale-fallback path is
  entered (if enabled).
- **SSE tee recording**: SSE responses on the miss path are streamed to the
  caller unchanged while events are accumulated and persisted. Partial
  disconnects (client close before upstream EOF) are detected and the
  partial tape is discarded.
- `docs/caching-transport.md`: Complete guide for CachingTransport.

## [0.12.0] - 2026-04-18

### Breaking Changes

- **Content-Type-driven body shape**: The `body` field in fixture JSON now uses
  Content-Type-aware serialization. JSON bodies (`application/json`, `+json` suffix)
  are stored as native JSON objects/arrays. Text bodies (`text/*`, `application/xml`)
  are stored as JSON strings. Binary bodies are stored as base64-encoded strings.
  Previously, all bodies used Go's default `[]byte` JSON encoding (base64).
- **Removed `BodyEncoding` type**: The `BodyEncoding` type, `BodyEncodingIdentity`
  and `BodyEncodingBase64` constants, and `body_encoding` field on `RecordedReq`
  and `RecordedResp` have been removed. The body encoding is now determined
  automatically from the Content-Type header.
- **Custom JSON marshaling**: `RecordedReq` and `RecordedResp` now implement
  `json.Marshaler` and `json.Unmarshaler` for Content-Type-aware body serialization.
  The `Body` field's JSON struct tag is now `json:"-"` (excluded from default marshaling).

### Added

- `media_type.go`: New `MediaType` struct and utilities (`ParseMediaType`,
  `ParseAccept`, `IsJSON`, `IsText`, `IsBinary`, `MatchesMediaRange`, `Specificity`)
  for Content-Type classification and RFC 7231 content negotiation.
- `ContentNegotiationCriterion` in `matcher.go`: Matches incoming requests to
  tapes based on the request's `Accept` header and the tape response's
  `Content-Type`. Enables multiple fixtures at the same path with different
  content types. Score: 3-5 (based on specificity).
- `"content_negotiation"` criterion type in config: Configurable via the
  declarative JSON config under `matcher.criteria`.
- `httptape migrate-fixtures` CLI subcommand: Migrates fixtures from v0.11
  format (base64 bodies with `body_encoding`) to v0.12 format (Content-Type-aware
  body shape). Supports `--recursive` flag for nested directories.

### Migration

Run the migration tool on all fixture directories before upgrading:

```bash
httptape migrate-fixtures --recursive ./fixtures
```

The tool is idempotent and safe to run multiple times. It skips non-tape
JSON files and reports a summary of migrated/skipped/errored files.
