# Test your Spring AI agents deterministically

Working example of [httptape](https://github.com/VibeWarden/httptape) used to test a **Spring AI streaming chat completion** and a classic REST integration -- both served from pre-recorded fixtures via [Testcontainers](https://testcontainers.com/), with zero real API calls.

## Architecture

```
                     +-- Spring AI ChatClient --+
                     |                          |
RecommendationService -----> httptape (SSE)     |  Testcontainers
                     |    POST /v1/chat/        |  (GenericContainer)
                     |    completions           |
                     |                          |
UserService ---------+-----> httptape (REST)    |
                          GET /users/{id}       |
                          GET /users            |
                     +--------------------------+
```

Two httptape containers in tests (one per test class):

| Container | Fixtures | What it proves |
|---|---|---|
| `RecommendationServiceIntegrationTest` | `fixtures/openai/` | Spring AI streaming chat completions replayed from an SSE fixture in OpenAI's exact wire format |
| `UserServiceIntegrationTest` | `fixtures/users/` | Classic REST `GET` requests replayed from JSON fixtures |

## How it works

### Headline: deterministic LLM streaming tests

The `RecommendationService` uses Spring AI's `ChatClient` to call an LLM via the OpenAI chat completions API. In tests, `@DynamicPropertySource` overrides `spring.ai.openai.base-url` to point at an httptape Testcontainer. httptape serves a pre-recorded SSE fixture in OpenAI's exact wire format -- every `data:` frame is a valid `chat.completion.chunk` JSON object, ending with the `[DONE]` sentinel.

Spring AI's `OpenAiApi` appends `/v1/chat/completions` to the base URL. The fixture's request URL is `/v1/chat/completions`. No custom `@Configuration` or `@TestConfiguration` classes needed -- Spring AI's auto-configuration reads the overridden properties and constructs everything correctly.

### Secondary: classic REST integration tests

The `UserService` uses Spring's `RestClient` to fetch user data from an external REST API. Same pattern: `@DynamicPropertySource` overrides the base URL, httptape serves recorded JSON fixtures.

The story: **one tool, one test approach, both integration shapes.**

## Prerequisites

- **Docker** (for Testcontainers and `docker compose`)
- **JDK 25** (for `./mvnw test`)

**Spring AI version**: this demo uses Spring AI 2.0.0-M4 (milestone) because Spring AI 1.x targets Spring Boot 3.x. We're tracking the upcoming Spring AI 2.0.0 GA -- bump to GA when available.

## Quick start

```bash
cd examples/java-spring-boot

# Run all integration tests (2 AI streaming + 3 REST)
./mvnw test
```

Tests spin up httptape containers via Testcontainers, run assertions, and tear down. No API keys. No real LLM calls. Deterministic on every run.

## Try it standalone

```bash
docker compose up -d

# Classic REST -- returns user JSON from fixture
curl http://localhost:8080/users/1

# AI streaming -- streams SSE events in real time
curl -N http://localhost:8080/recommendations?for=headphones

docker compose down
```

## How to record real OpenAI streams to fixtures

If you want to record from the real OpenAI API instead of hand-authoring fixtures:

```bash
# 1. Record a real streaming call via httptape proxy
httptape record \
  --upstream https://api.openai.com \
  --fixtures ./src/test/resources/fixtures/openai \
  --config ./mocks/sanitize.json

# 2. In another terminal, make the call through the proxy
curl http://localhost:8081/v1/chat/completions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"Recommend headphones"}]}'

# 3. httptape writes the fixture with PII redacted on write.
#    Commit the fixture. Tests replay it deterministically forever.
```

## Project layout

```
java-spring-boot/
  src/
    main/java/dev/httptape/demo/
      Application.java                 # @SpringBootApplication
      AppConfig.java                   # Explicit @Bean wiring for all services
      RecommendationService.java       # Spring AI ChatClient (streaming)
      UserService.java                 # Spring RestClient (blocking)
      User.java                        # record type
      DemoController.java              # REST + SSE endpoints
    main/resources/
      application.properties           # base URL configs
    test/java/dev/httptape/demo/
      RecommendationServiceIntegrationTest.java  # 2 tests: content + cadence
      UserServiceIntegrationTest.java            # 3 tests: happy, list, 404
    test/resources/fixtures/
      openai/
        chat-completion-headphones.json  # SSE fixture (OpenAI wire format)
      users/
        get-user-1.json                  # REST fixture
        get-users.json                   # REST fixture
        get-user-999.json                # 404 fixture
  mocks/
    sanitize.json                        # typed-Faker config (illustrative)
  pom.xml                               # Spring Boot 4.0.5, Spring AI 2.0.0-M4 (BOM-based)
  Dockerfile                            # multi-stage Maven -> JRE 25
  docker-compose.yml                    # httptape + app
  mvnw, mvnw.cmd, .mvn/                 # Maven Wrapper (no host Maven needed)
```

## Why not...?

| Alternative | Limitation |
|---|---|
| **WireMock** | No native SSE record/replay. No sanitize-on-write. Java-heavy setup for what should be a fixture file. |
| **Spring MockRestServiceServer** | Works for REST, but does not support streaming SSE responses from Spring AI. |
| **Real OpenAI calls in tests** | Slow (network round-trip), flaky (rate limits, outages), costs money, and leaks PII into CI logs. |
| **Manual mocking (Mockito)** | Skips the entire HTTP layer -- you are not testing the real integration. Breaks when the API contract changes. |
