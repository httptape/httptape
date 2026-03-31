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
// [CompositeMatcher]. Individual [MatchCriterion] functions (e.g.,
// [MatchMethod], [MatchPath], [MatchQueryParams], [MatchBodyHash]) can be
// composed for custom matching strategies.
//
// # Design principles
//
// httptape follows hexagonal architecture: core types have zero I/O, all
// persistence goes through the [Store] port, and all components are
// configured via functional options. The library has zero external
// dependencies (stdlib only) and no global mutable state.
package httptape
