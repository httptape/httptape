# Test your Spring AI agents deterministically

Working example of [httptape](https://github.com/VibeWarden/httptape) used to test a **Spring AI streaming chat completion** and a classic REST integration — both served from pre-recorded fixtures via [Testcontainers](https://testcontainers.com/), with zero real API calls.

## What this demo shows

A real Spring Boot service that integrates with two external APIs — an LLM (via Spring AI's `ChatClient`) and a generic REST API — tested end-to-end with **deterministic fixtures** instead of real network calls.

The story: **one tool, one test approach, both integration shapes.** The same pattern that makes flaky-API REST tests deterministic also makes flaky-AI streaming tests deterministic, on the same stack, with no extra mocking framework.

### Headline scenario — Spring AI streaming

The service calls an LLM via OpenAI's chat completions API. Tests serve a hand-crafted SSE fixture in OpenAI's exact wire format (each `data:` event is a real `chat.completion.chunk` JSON, ending with `[DONE]`). Spring AI's auto-configuration deserializes the stream as it would a real call.

### Secondary scenario — classic REST

The service also calls a regular REST endpoint via Spring's modern `RestClient`. Same Testcontainers + httptape pattern, recorded JSON fixtures.

## Prerequisites

- **Docker** (for Testcontainers and the optional `docker compose` flow)
- **JDK 25**
- **httptape-jvm SDK** published to local Maven (see below)

> **Spring AI version note**: the demo uses Spring AI 2.0.0-M4 (milestone) because Spring AI 1.x targets Spring Boot 3.x. Bump to 2.0.0 GA when it ships.

## SDK setup (local development)

This demo uses the `httptape-testcontainers` SDK (`dev.httptape:httptape-testcontainers`) which is not yet published to Maven Central. For local development, clone the SDK repo as a sibling and publish to local Maven:

```bash
# From the parent directory of this httptape checkout
git clone https://github.com/VibeWarden/httptape-jvm.git
cd httptape-jvm
./gradlew publishToMavenLocal
```

This installs `dev.httptape:httptape-testcontainers:0.1.0-SNAPSHOT` into `~/.m2/repository/`. The demo's `pom.xml` already declares this dependency with `<scope>test</scope>`.

## Quick start

```bash
cd examples/java-spring-boot
./mvnw test
```

Tests spin up an httptape container via Testcontainers, run assertions against both integration scenarios, and tear down. No API keys. No real LLM calls. Deterministic on every run.

## Adding a new fixture

Drop a JSON file anywhere under `src/test/resources/fixtures/` — the test container auto-discovers it via classpath scan. No code changes needed.

The httptape Tape JSON schema (and how to record real upstream traffic into one) is documented at [vibewarden.dev/docs/httptape](https://vibewarden.dev/docs/httptape/).

## Development workflow — run with the same Testcontainers setup

For local debugging or manual `curl`-ing, boot the app with the same Testcontainers wiring the integration tests use:

```bash
./mvnw spring-boot:test-run
```

In IntelliJ: right-click the test-time runner class → Run/Debug. Set breakpoints, step through the streaming + REST flows interactively, no separate `docker compose up` needed.

## Faster local tests with Testcontainers reuse

Opt-in per developer (CI must NOT enable it — containers should always be ephemeral on CI):

```bash
echo "testcontainers.reuse.enable=true" >> ~/.testcontainers.properties
```

One-time setup. Subsequent test runs reuse the same httptape container, dropping startup overhead from ~2s to near-zero. Combined with the shared single-container test config, the cycle stays snappy as more test classes are added.

Why per-developer and not project-shipped? Testcontainers deliberately requires `reuse.enable` to live in `~/.testcontainers.properties` (not the classpath) — it's a local-dev convenience that would be unsafe in CI.

## A note on virtual threads

The app enables Java 21+ **virtual threads** via `spring.threads.virtual.enabled=true`. Tomcat's request executor switches to virtual threads — each in-flight HTTP request occupies a virtual thread (JVM-managed, essentially free) instead of an OS thread from a bounded pool.

Effect: blocking calls like `.block()` on a Reactor `Mono` (used to collect the LLM stream into a string) are idiomatic. Parking a virtual thread is the same primitive as subscribing to a Reactor `Mono` — no thread-pool starvation under heavy concurrent load. Imperative code, reactive scaling.

This is the canonical Spring Boot 4 + Java 25 best practice for blocking-style code.

## Try it standalone

For non-Java users who want to `curl` the demo without running tests:

```bash
docker compose up -d
curl http://localhost:8080/users/1
curl -N http://localhost:8080/recommendations?for=headphones
docker compose down
```

IDE users: open [`api.http`](./api.http) — IntelliJ's HTTP Client (and VS Code with the REST Client extension) lets you click-to-run each request, including the SSE stream.

## Stack

| | |
|---|---|
| Java | 25 (LTS), virtual threads enabled |
| Spring Boot | 4.0.5, BOM-based dependency management (no `spring-boot-starter-parent`) |
| Spring AI | 2.0.0-M4 (OpenAI client, milestone) |
| HTTP client | Spring's modern blocking `RestClient` |
| httptape-jvm SDK | 0.1.0-SNAPSHOT (httptape-testcontainers) |
| Tests | JUnit 5 + Testcontainers (single shared container) |
| Build | Maven Wrapper committed |

## Why not...?

| Alternative | Limitation |
|---|---|
| **WireMock** | No native SSE record/replay. No sanitize-on-write. |
| **Spring `MockRestServiceServer`** | Works for REST, no streaming SSE support for Spring AI. |
| **Real OpenAI calls in tests** | Slow (network), flaky (rate limits / outages), costs money, leaks PII into CI logs. |
| **Manual mocking (Mockito)** | Skips the HTTP layer — you're not testing the real integration. Breaks when the API contract changes. |
