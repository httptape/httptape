# Test your Koog AI agents deterministically

Working example of [httptape](https://github.com/VibeWarden/httptape) used to test a **Koog single-tool AI agent** with a weather REST integration -- both served from pre-recorded fixtures via [Testcontainers](https://testcontainers.com/), with zero real API calls.

## What this demo shows

A Ktor server hosts a `GET /weather-advice?city={city}` endpoint that runs a Koog AI agent. The agent calls OpenAI's chat completions API (non-streaming) and a weather REST API, then returns weather-based advice. Tests exercise the full agent loop against a single httptape container serving hand-crafted fixtures.

The story: **one httptape container, three HTTP calls, three different fixtures, matched by a declarative config.** The matcher config (`httptape.config.json`) distinguishes the two POST requests to `/v1/chat/completions` via `body_fuzzy` on `$.messages[*].role`, while the body-less GET request to `/v1/forecast` coexists via vacuous-true matching.

### Headline scenario

```
User -> GET /weather-advice?city=Berlin -> Koog agent

  HTTP #1: POST /v1/chat/completions
           messages = [system, user]
           -> httptape returns tool_call: getWeather("Berlin")

  HTTP #A: GET /v1/forecast?city=Berlin
           -> httptape returns weather JSON (rain, 12C)

  HTTP #2: POST /v1/chat/completions
           messages = [system, user, assistant(tool_call), tool(weather_json)]
           -> httptape returns final answer: "...bring an umbrella."

User <- SSE: "Based on the weather in Berlin, yes, bring an umbrella."
```

### Matcher composition callout

This demo exercises the matcher composition stack from #178, #179, and #180:

- **#178 (vacuous-true fix)**: The GET `/v1/forecast` tape has no request body. The `body_fuzzy` criterion returns vacuous-true (score 1) instead of eliminating it, keeping it alive in the composite matcher.
- **#179 (Criterion interface)**: `MethodCriterion`, `PathCriterion`, and `BodyFuzzyCriterion` implement the `Criterion` interface.
- **#180 (declarative config)**: The `httptape.config.json` file declares the `CompositeMatcher` with all three criteria. No Go code changes needed -- the config drives the matching.

## Prerequisites

- **Docker** (for Testcontainers and the optional `docker compose` flow)
- **JDK 25**

## Quick start

```bash
cd examples/kotlin-ktor-koog
./gradlew test
```

Tests spin up an httptape container via Testcontainers, run the Koog agent against it, and assert the response. No API keys. No real LLM calls. Deterministic on every run.

## Adding a new fixture

Drop a JSON file under `src/test/resources/fixtures/` and add a `copyFixture(...)` line in `HttptapeContainer.kt`. Fixtures are enumerated explicitly (Kotlin has no stdlib equivalent of Spring's classpath-glob scanner).

The httptape Tape JSON schema (and how to record real upstream traffic into one) is documented at [vibewarden.dev/docs/httptape](https://vibewarden.dev/docs/httptape/).

## Dev workflow

For local debugging, boot the app with httptape via Docker Compose:

```bash
docker compose up -d httptape
OPENAI_BASE_URL=http://localhost:8081 WEATHER_BASE_URL=http://localhost:8081 ./gradlew run
```

In IntelliJ: run `Application.kt` directly with the environment variables set. Set breakpoints, step through the agent flow interactively.

## Faster local tests with Testcontainers reuse

Opt-in per developer (CI must NOT enable it):

```bash
echo "testcontainers.reuse.enable=true" >> ~/.testcontainers.properties
```

Subsequent test runs reuse the same httptape container, dropping startup overhead.

## Try it standalone

For non-JVM users who want to `curl` the demo (builds httptape from source):

```bash
docker compose up -d --build
curl -N http://localhost:8080/weather-advice?city=Berlin
docker compose down
```

IDE users: open [`api.http`](./api.http) -- IntelliJ's HTTP Client and VS Code's REST Client extension let you click-to-run.

## Stack

| | |
|---|---|
| Kotlin | 2.3.20 |
| JDK | 25 |
| Ktor | 3.4.2 (Netty server + CIO client) |
| Koog | 0.8.0 (AI agent framework, Apache 2.0) |
| Kotest | 6.1.11 (BehaviorSpec) |
| Testcontainers | 2.0.4 (single shared container) |
| Gradle | 9.4.1 (wrapper committed) |

## Why not...?

| Alternative | Limitation |
|---|---|
| **WireMock** | No native body-fuzzy matching. No sanitize-on-write. |
| **Koog's built-in mocks (`getMockExecutor`, `mockTool`)** | Mocks the LLM at executor level, skipping the HTTP layer entirely. You are not testing that your agent works with a real OpenAI-compatible endpoint. httptape tests the full HTTP integration -- serialization, headers, content negotiation -- same as production. |
| **Real OpenAI calls in tests** | Slow, flaky, costs money, leaks PII into CI logs. |
| **Manual Ktor test stubs** | Skips the HTTP layer. Breaks when the API contract changes. |
