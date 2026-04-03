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
httptape.NewCompositeMatcher(httptape.MatchMethod(), httptape.MatchPath())
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
    httptape.MatchMethod(),
    httptape.MatchPath(),
    httptape.MatchQueryParams(),
    httptape.MatchBodyHash(),
)
```

### How scoring works

Each `MatchCriterion` returns a score for a candidate tape:

- **Score 0** -- the candidate does not match on this dimension. It is **eliminated**.
- **Positive score** -- the candidate matches, with higher values indicating stronger matches.

A candidate must pass **all** criteria (non-zero score from each) to survive. The candidate with the highest total score wins. Ties are broken by order in the candidate list (first wins).

## Built-in criteria

### MatchMethod

```go
httptape.MatchMethod()
```

Requires the HTTP method (GET, POST, etc.) to match exactly. **Score: 1**

### MatchPath

```go
httptape.MatchPath()
```

Requires the URL path to match exactly. The tape's stored URL is parsed to extract only the path component. **Score: 2**

### MatchPathRegex

```go
criterion, err := httptape.MatchPathRegex(`^/users/\d+/orders$`)
if err != nil {
    log.Fatal(err)
}
matcher := httptape.NewCompositeMatcher(httptape.MatchMethod(), criterion)
```

Matches the URL path against a regular expression. Both the incoming request path and the tape's stored path must match the pattern. Returns an error if the pattern is invalid.

Use `MatchPathRegex` as a **replacement** for `MatchPath`, not alongside it. If both are present, `MatchPath` will eliminate candidates that don't exact-match, regardless of the regex result.

**Score: 1**

### MatchRoute

```go
httptape.MatchRoute("users-api")
```

Requires the tape's `Route` field to equal the given value. If the route is empty string, the criterion always matches (any tape). **Score: 1**

### MatchHeaders

```go
httptape.MatchHeaders("Accept", "application/json")
httptape.MatchHeaders("X-Feature-Flag", "new-checkout")
```

Requires a specific header to be present in both the request and the tape with an exact value match. Header names are case-insensitive. If the header has multiple values, the criterion checks if the specified value appears among them (any-of semantics).

To require multiple headers, add multiple `MatchHeaders` criteria -- they are AND-ed together naturally. **Score: 3**

### MatchQueryParams

```go
httptape.MatchQueryParams()
```

Requires all query parameters from the incoming request to be present in the tape's URL with the same values. Extra parameters in the tape are allowed (subset match). If the request has no query parameters, this criterion always matches (vacuously true). **Score: 4**

### MatchBodyHash

```go
httptape.MatchBodyHash()
```

Computes the SHA-256 hash of the incoming request body and compares it with the tape's stored `BodyHash`. This is an exact body match.

If both the tape and request have no body, it matches. If one has a body and the other doesn't, it doesn't match. **Score: 8**

### MatchBodyFuzzy

```go
httptape.MatchBodyFuzzy("$.action", "$.user.id", "$.items[*].sku")
```

Compares specific fields in the JSON request body between the incoming request and the tape. Only the fields at the specified paths are compared; all other fields are ignored. This is useful when request bodies contain volatile fields (timestamps, nonces) that vary per invocation.

Paths use the same JSONPath-like syntax as [RedactBodyPaths](sanitization.md#redactbodypaths).

Matching rules:
- Both bodies must be valid JSON (otherwise score 0)
- Paths that don't exist in either body are skipped (not a mismatch)
- Paths that exist in both must have deeply equal values
- At least one path must match for the criterion to return a positive score

Using both `MatchBodyFuzzy` and `MatchBodyHash` in the same matcher is safe but redundant. Choose one or the other.

**Score: 6**

## Score weight table

| Criterion | Score | Purpose |
|-----------|-------|---------|
| MatchMethod | 1 | HTTP method |
| MatchPath | 2 | Exact URL path |
| MatchPathRegex | 1 | Regex URL path |
| MatchRoute | 1 | Route label |
| MatchHeaders | 3 | Header key-value |
| MatchQueryParams | 4 | Query parameters |
| MatchBodyFuzzy | 6 | Partial body fields |
| MatchBodyHash | 8 | Exact body hash |

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
    httptape.MatchMethod(),
    httptape.MatchPath(),
    httptape.MatchQueryParams(),
)
```

### Method + path + specific header (API versioning)

```go
matcher := httptape.NewCompositeMatcher(
    httptape.MatchMethod(),
    httptape.MatchPath(),
    httptape.MatchHeaders("Accept", "application/vnd.api.v2+json"),
)
```

### Method + regex path + fuzzy body (complex POST APIs)

```go
pathCriterion, _ := httptape.MatchPathRegex(`^/api/v\d+/orders`)
matcher := httptape.NewCompositeMatcher(
    httptape.MatchMethod(),
    pathCriterion,
    httptape.MatchBodyFuzzy("$.action", "$.order.type"),
)
```

## See also

- [Replay](replay.md) -- using matchers with the Server
- [API Reference](api-reference.md) -- full type signatures
