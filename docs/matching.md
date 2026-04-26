# Matching

Matchers control how incoming requests are paired with recorded tapes during [replay](replay.md). httptape uses a composable scoring system: each criterion evaluates one dimension of the request, and the highest-scoring tape wins.

## The Matcher interface

```go
type Matcher interface {
    Match(req *http.Request, candidates []Tape) (Tape, bool)
}
```

A `Matcher` receives the incoming request and a list of candidate tapes. It returns the best match, or `(Tape{}, false)` if nothing matches.

## DefaultMatcher

```go
matcher := httptape.DefaultMatcher()
```

Matches by HTTP method and URL path. Equivalent to:

```go
httptape.NewCompositeMatcher(httptape.MethodCriterion{}, httptape.PathCriterion{})
```

This is the default for `NewServer` if no matcher is specified.

## ExactMatcher

```go
matcher := httptape.ExactMatcher()
```

A simple matcher that returns the first tape whose HTTP method and URL path exactly match the request. Unlike `CompositeMatcher`, it does not score candidates -- it returns the first match.

## CompositeMatcher

The `CompositeMatcher` evaluates multiple criteria and returns the highest-scoring tape.

```go
matcher := httptape.NewCompositeMatcher(
    httptape.MethodCriterion{},
    httptape.PathCriterion{},
    httptape.QueryParamsCriterion{},
    httptape.BodyHashCriterion{},
)
```

### How scoring works

Each `Criterion` returns a score for a candidate tape:

- **Score 0** -- the candidate does not match on this dimension. It is **eliminated**.
- **Positive score** -- the candidate matches, with higher values indicating stronger matches.

A candidate must pass **all** criteria (non-zero score from each) to survive. The candidate with the highest total score wins. Ties are broken by order in the candidate list (first wins).

## Built-in criteria

### MethodCriterion

```go
httptape.MethodCriterion{}
```

Requires the HTTP method (GET, POST, etc.) to match exactly. **Score: 1**

### PathCriterion

```go
httptape.PathCriterion{}
```

Requires the URL path to match exactly. The tape's stored URL is parsed to extract only the path component. **Score: 2**

### PathRegexCriterion

```go
criterion, err := httptape.NewPathRegexCriterion(`^/users/\d+/orders$`)
if err != nil {
    log.Fatal(err)
}
matcher := httptape.NewCompositeMatcher(httptape.MethodCriterion{}, criterion)
```

Matches the URL path against a regular expression. Both the incoming request path and the tape's stored path must match the pattern. Returns an error if the pattern is invalid.

Use `PathRegexCriterion` as a **replacement** for `PathCriterion`, not alongside it. If both are present, `PathCriterion` will eliminate candidates that don't exact-match, regardless of the regex result.

**Score: 1**

### RouteCriterion

```go
httptape.RouteCriterion{Route: "users-api"}
```

Requires the tape's `Route` field to equal the given value. If the route is empty string, the criterion always matches (any tape). **Score: 1**

### HeadersCriterion

```go
httptape.HeadersCriterion{Key: "Accept", Value: "application/json"}
httptape.HeadersCriterion{Key: "X-Feature-Flag", Value: "new-checkout"}
```

Requires a specific header to be present in both the request and the tape with an exact value match. Header names are case-insensitive. If the header has multiple values, the criterion checks if the specified value appears among them (any-of semantics).

To require multiple headers, add multiple `HeadersCriterion` instances -- they are AND-ed together naturally. **Score: 3**

### QueryParamsCriterion

```go
httptape.QueryParamsCriterion{}
```

Requires all query parameters from the incoming request to be present in the tape's URL with the same values. Extra parameters in the tape are allowed (subset match). If the request has no query parameters, this criterion always matches (vacuously true). **Score: 4**

### BodyHashCriterion

```go
httptape.BodyHashCriterion{}
```

Computes the SHA-256 hash of the incoming request body and compares it with the tape's stored `BodyHash`. This is an exact body match.

If both the tape and request have no body, it matches. If one has a body and the other doesn't, it doesn't match. **Score: 8**

### BodyFuzzyCriterion

```go
httptape.NewBodyFuzzyCriterion("$.action", "$.user.id", "$.items[*].sku")
```

Compares specific fields in the JSON request body between the incoming request and the tape. Only the fields at the specified paths are compared; all other fields are ignored. This is useful when request bodies contain volatile fields (timestamps, nonces) that vary per invocation.

Paths use the same JSONPath-like syntax as [RedactBodyPaths](sanitization.md#redactbodypaths).

Matching rules:
- Both bodies must be valid JSON (otherwise score 0)
- Paths that don't exist in either body are skipped (not a mismatch)
- Paths that exist in both must have deeply equal values
- At least one path must match for the criterion to return a positive score

Using both `BodyFuzzyCriterion` and `BodyHashCriterion` in the same matcher is safe but redundant. Choose one or the other.

**Score: 6**

### ContentNegotiationCriterion

```go
httptape.ContentNegotiationCriterion{}
```

Matches based on RFC 7231 content negotiation: the incoming request's `Accept` header is compared against the tape response's `Content-Type`. This enables multiple fixtures at the same path with different content types (e.g., JSON and XML).

Scoring uses a two-pass algorithm:
1. **Exclusion pass**: any tape whose Content-Type matches a `q=0` range in Accept is eliminated (score 0).
2. **Match pass**: the best matching Accept range determines the score based on specificity:
   - **Exact match** (e.g., `application/json` matches `application/json`): score 5
   - **Subtype wildcard** (e.g., `application/*` matches `application/json`): score 4
   - **Full wildcard** (`*/*` matches anything): score 3

If the incoming request has no `Accept` header, the criterion defaults to `*/*` (matches all content types with score 3). Charset and other parameters (except `q`) are ignored.

**Score: 3-5** (variable based on specificity)

**Note:** Using `ContentNegotiationCriterion` alongside `HeadersCriterion{Key: "Accept", ...}` is allowed but redundant.

### PathPatternCriterion

```go
c, err := httptape.NewPathPatternCriterion("/users/:id")
```

Matches Express-style URL patterns with named segments. Named segments (prefixed with `:`) match any single non-empty path segment. Captured values are available to templates via `{{pathParam.NAME}}`.

Pattern examples:
- `/users/:id` matches `/users/42` (captures `id=42`)
- `/users/:id/orders/:oid` matches `/users/1/orders/7`
- `/api/v1/items` matches only `/api/v1/items` (no captures)

The pattern is compiled to a regex at construction time. Trailing slashes are significant: `/users/:id` does NOT match `/users/42/`.

**Score: 3**

**Important:** `PathPatternCriterion` should NOT be used alongside `PathCriterion` in the same `CompositeMatcher`. `PathCriterion` returns 0 for non-exact paths, which eliminates candidates that `PathPatternCriterion` would accept.

After matching, extract captured parameters for template evaluation:

```go
params := criterion.ExtractParams(req.URL.Path)
// params["id"] == "42"
```

The `Server` handles this automatically when `PathPatternCriterion` is in the matcher.

## Score weight table

| Criterion | Score | Purpose |
|-----------|-------|---------|
| MethodCriterion | 1 | HTTP method |
| PathCriterion | 2 | Exact URL path |
| PathRegexCriterion | 1 | Regex URL path |
| RouteCriterion | 1 | Route label |
| PathPatternCriterion | 3 | Express-style path pattern |
| HeadersCriterion | 3 | Header key-value |
| ContentNegotiationCriterion | 3-5 | Accept/Content-Type |
| QueryParamsCriterion | 4 | Query parameters |
| BodyFuzzyCriterion | 6 | Partial body fields |
| BodyHashCriterion | 8 | Exact body hash |

Higher-specificity criteria have higher scores, so a body-hash match dominates a path-only match. The weights are designed so that more specific criteria generally outweigh combinations of less specific ones.

## MatcherFunc

You can use any function as a `Matcher`:

```go
matcher := httptape.MatcherFunc(func(req *http.Request, candidates []httptape.Tape) (httptape.Tape, bool) {
    for _, t := range candidates {
        if t.Route == "special" {
            return t, true
        }
    }
    return httptape.Tape{}, false
})
```

## Common patterns

### Method + path + query (recommended for most APIs)

```go
matcher := httptape.NewCompositeMatcher(
    httptape.MethodCriterion{},
    httptape.PathCriterion{},
    httptape.QueryParamsCriterion{},
)
```

### Method + path + specific header (API versioning)

```go
matcher := httptape.NewCompositeMatcher(
    httptape.MethodCriterion{},
    httptape.PathCriterion{},
    httptape.HeadersCriterion{Key: "Accept", Value: "application/vnd.api.v2+json"},
)
```

### Method + path + content negotiation (multi-format APIs)

```go
matcher := httptape.NewCompositeMatcher(
    httptape.MethodCriterion{},
    httptape.PathCriterion{},
    httptape.ContentNegotiationCriterion{},
)
```

This pattern enables multiple fixtures at the same endpoint that return different content types. For example, `GET /api/data` could have a JSON fixture and an XML fixture, selected based on the client's `Accept` header.

### Method + regex path + fuzzy body (complex POST APIs)

```go
pathCriterion, _ := httptape.NewPathRegexCriterion(`^/api/v\d+/orders`)
matcher := httptape.NewCompositeMatcher(
    httptape.MethodCriterion{},
    pathCriterion,
    httptape.NewBodyFuzzyCriterion("$.action", "$.order.type"),
)
```

## Declarative matcher config

All examples above show programmatic composition in Go. The same matchers can be declared via a JSON config file, which is especially useful with the CLI and Docker:

```json
{
  "version": "1",
  "matcher": {
    "criteria": [
      { "type": "method" },
      { "type": "path" },
      { "type": "content_negotiation" }
    ]
  },
  "rules": []
}
```

See [Config](config.md#matcher-criteria) for the full syntax and supported criterion types.

## See also

- [Replay](replay.md) -- using matchers with the Server
- [Config](config.md) -- declarative JSON configuration (including matcher composition)
- [API Reference](api-reference.md) -- full type signatures
