// Package httptape provides HTTP traffic recording, sanitization, and replay
// for Go tests and development environments.
//
// httptape captures HTTP request/response pairs via a recording
// [http.RoundTripper], sanitizes sensitive data on write, and replays them
// through a mock [http.Handler]. It is designed to be embedded directly in
// Go applications and test suites with zero external dependencies.
//
// # Core types
//
// [Tape] is a value type representing a single recorded HTTP interaction
// (request + response). Tapes are created by the [Recorder] and replayed by
// the [Server].
//
// # Recording
//
// [Recorder] wraps an [http.RoundTripper] and transparently captures every
// HTTP call as a [Tape]. Recording is asynchronous by default to minimize
// hot-path overhead.
//
//	rec := httptape.NewRecorder(store, httptape.WithRoute("users-api"))
//	client := &http.Client{Transport: rec}
//	// ... use client normally ...
//	rec.Close() // flush pending recordings
//
// # Replay
//
// [Server] implements [http.Handler] and replays recorded tapes. It uses a
// [Matcher] to find the best-matching tape for each incoming request.
//
//	srv := httptape.NewServer(store)
//	ts := httptest.NewServer(srv)
//	defer ts.Close()
//
// # Storage
//
// The [Store] interface abstracts tape persistence. Two implementations are
// provided:
//   - [MemoryStore]: in-memory storage, ideal for tests.
//   - [FileStore]: filesystem-backed storage with JSON fixtures.
//
// # Matching
//
// The [Matcher] interface controls how incoming requests are matched to
// recorded tapes. [DefaultMatcher] provides method + path matching via a
// [CompositeMatcher]. Individual [Criterion] implementations (e.g.,
// [MethodCriterion], [PathCriterion], [QueryParamsCriterion], [BodyHashCriterion])
// can be composed for custom matching strategies.
//
// # Health endpoints (proxy mode)
//
// When a [Proxy] is constructed with [WithProxyHealthEndpoint], it exposes a
// small technical surface that downstream UIs can use to react to upstream
// state changes in real time:
//
//   - GET /__httptape/health        — JSON snapshot ([HealthSnapshot]).
//   - GET /__httptape/health/stream — text/event-stream emitting one event
//     on connect (initial seed) and one event per state transition.
//
// State values mirror the existing X-Httptape-Source header semantics
// ([StateLive], [StateL1Cache], [StateL2Cache]) and are fed by the same code
// path real client traffic takes, plus an optional active probe configured
// via [WithProxyProbeInterval]. SSE subscribers whose buffers overflow are
// disconnected; the EventSource auto-reconnect plus the initial-on-connect
// event re-seeds correct state. Mount the handler returned by
// [Proxy.HealthHandler] on your listener and pair [Proxy.Start] /
// [Proxy.Close] with the lifetime of your HTTP server.
//
// With these options absent, [Proxy.HealthHandler] returns nil, no
// goroutines are started, and proxy behavior is byte-for-byte unchanged.
//
// # Design principles
//
// httptape follows hexagonal architecture: core types have zero I/O, all
// persistence goes through the [Store] port, and all components are
// configured via functional options. The library has zero external
// dependencies (stdlib only) and no global mutable state.
//
// # Documentation
//
// Full documentation, guides, and examples live at
// https://vibewarden.dev/docs/httptape/. httptape is developed as part of
// VibeWarden — see https://vibewarden.dev/.
package httptape
