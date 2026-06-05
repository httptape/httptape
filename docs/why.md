# Why httptape

httptape exists because building an LLM-powered product means handling HTTP traffic that's full of secrets, prompts, PII, and streaming responses — and no existing tool treated any of that as a first-class concern.

## Origin

httptape was extracted from [VibeWarden](https://vibewarden.dev/), where we needed an egress proxy that could:

1. **Record** real LLM and API traffic for replay in tests and demos.
2. **Strip secrets, prompts, and PII *before* anything hit disk** — not as an opt-in afterthought.
3. **Replay Server-Sent Event streams** with per-event timing, so streaming UIs behaved the same in tests as in production.
4. **Embed directly into a Go binary**, with no external service to deploy alongside.

We tried WireMock (Java process, no SSE replay), go-vcr (test-time-only, redaction via user hooks), and a handful of intercept-the-client mocking libraries. None of them treated the safe path as the default. So we built httptape and shipped it as a Go library, a CLI, and a 3 MB Docker image.

The locked decision: **there is no "raw" recording mode**. Sanitization happens on write, deterministically (HMAC-SHA256 for fakes), before any tape touches a store. The safe path is the only path.

## How it compares to other tools

### Standalone mock servers

**[WireMock](https://wiremock.org/)** is the reference standalone mock server. It supports recording, advanced matching, and a rich extension ecosystem — but it runs as a Java process (200 MB+ resident), can't be embedded in a Go binary, has no built-in SSE record/replay, and treats redaction as a plugin concern rather than a default.

**[Hoverfly](https://hoverfly.io/)** is the closest direct competitor in Go: a standalone HTTP proxy with record/replay modes. Where httptape differs: sanitize-on-write is built in (rather than user-written middleware), SSE is replayed event-by-event with timing preserved, and the same code is consumable as an `http.RoundTripper` / `http.Handler` so you don't *have* to run a separate process.

**[smocker](https://smocker.dev/)** is a Go-based WireMock-alike with a strong dynamic-matching engine. No record mode, no redaction, no SSE.

### Cassette-style recording libraries

**[go-vcr](https://github.com/dnaeon/go-vcr)** is the de-facto cassette library for Go tests. It records real HTTP traffic to YAML/JSON cassettes and replays in tests. Redaction is supported via `BeforeSaveHook` callbacks (user-written) — there are no built-in rules, no SSE, no standalone server, and it stays inside `*_test.go`. If your only need is replaying cassettes inside tests, go-vcr is smaller and a perfectly good fit. httptape covers a broader surface.

### Intercept-the-client libraries (test-time only)

**[gock](https://github.com/h2non/gock)** and **[jarcoal/httpmock](https://github.com/jarcoal/httpmock)** intercept HTTP calls from the Go test process. They're great for unit tests, but they don't record, don't run standalone, don't redact, and the fixtures are hand-written Go code.

### Frontend-first mocking

**[json-server](https://github.com/typicode/json-server)** and **[Mockoon](https://mockoon.com/)** are great for hand-written REST stubs and have nice UIs. No recording, no redaction, no SSE event-level support.

**[MSW](https://mswjs.io/)** intercepts inside the JS runtime (browser or Node) for test-time mocking. Different deployment model from httptape — runs *inside* the application rather than as a sibling proxy. No record-from-upstream, no Go embedding.

### Feature matrix

| Feature | httptape | WireMock | Hoverfly | go-vcr | gock | json-server | MSW |
|---|---|---|---|---|---|---|---|
| Embeddable in Go | **yes** | no (Java) | partial (Go API) | yes (test-only) | yes | no (Node) | no (browser/Node) |
| Standalone server | **yes** | yes | yes | no | no | yes | no |
| Docker | **3 MB** | 200 MB+ | 60 MB | n/a | n/a | 50 MB+ | n/a |
| Recording | **yes** | yes | yes | yes | no | no | no |
| Redaction on write | **yes, default** | plugin | manual middleware | manual hooks | no | no | no |
| Deterministic faking | **yes (HMAC-SHA256)** | no | no | no | no | no | no |
| Proxy with fallback | **yes (L1/L2)** | no | partial | no | no | no | no |
| SSE record/replay | **yes (per-event)** | no | no | no | no | no | partial (mock-only) |
| Frontend mock backend | **yes** | yes | yes | no | no | yes | yes (browser) |
| Fixture import/export | **yes (tar.gz)** | partial | partial | no | no | no | no |
| Dependencies | **zero** | JVM | Go deps | yaml.v3 | 1 | npm tree | npm tree |

The matrix is a thumbnail. If you're choosing between two of these for a specific problem, read the linked projects' docs for the full picture.
