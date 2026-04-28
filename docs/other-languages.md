# Using httptape with Other Languages

httptape ships as a Docker image (`ghcr.io/httptape/httptape:latest`), so any language with Docker or Testcontainers support can use it as a mock server. This guide shows how to start an httptape container, mount fixture files, make requests, and clean up in several popular languages.

All examples assume you have a directory of recorded fixtures at `./testdata/fixtures` relative to the project root. The container exposes port **8081** by default.

---

## Java (Testcontainers)

```java
import org.testcontainers.containers.GenericContainer;
import org.testcontainers.containers.wait.strategy.Wait;
import org.testcontainers.utility.MountableFile;

import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;

import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;

class HttptapeTest {

    static GenericContainer<?> httptape;
    static String baseUrl;

    @BeforeAll
    static void startContainer() {
        httptape = new GenericContainer<>("ghcr.io/httptape/httptape:latest")
            .withCommand("serve", "--fixtures", "/fixtures")
            .withExposedPorts(8081)
            .withFileSystemBind("./testdata/fixtures", "/fixtures")
            .waitingFor(Wait.forHttp("/").forStatusCode(404));
        httptape.start();

        baseUrl = "http://" + httptape.getHost()
            + ":" + httptape.getMappedPort(8081);
    }

    @AfterAll
    static void stopContainer() {
        if (httptape != null) {
            httptape.stop();
        }
    }

    @Test
    void replayFixture() throws Exception {
        var client = HttpClient.newHttpClient();
        var request = HttpRequest.newBuilder()
            .uri(URI.create(baseUrl + "/api/users"))
            .GET()
            .build();

        var response = client.send(request,
            HttpResponse.BodyHandlers.ofString());

        assertEquals(200, response.statusCode());
    }
}
```

---

## Kotlin (Testcontainers)

```kotlin
import org.testcontainers.containers.GenericContainer
import org.testcontainers.containers.wait.strategy.Wait
import org.junit.jupiter.api.AfterAll
import org.junit.jupiter.api.BeforeAll
import org.junit.jupiter.api.Test
import java.net.URI
import java.net.http.HttpClient
import java.net.http.HttpRequest
import java.net.http.HttpResponse
import kotlin.test.assertEquals

class HttptapeTest {

    companion object {
        private lateinit var httptape: GenericContainer<*>
        lateinit var baseUrl: String

        @BeforeAll
        @JvmStatic
        fun startContainer() {
            httptape = GenericContainer("ghcr.io/httptape/httptape:latest")
                .withCommand("serve", "--fixtures", "/fixtures")
                .withExposedPorts(8081)
                .withFileSystemBind("./testdata/fixtures", "/fixtures")
                .waitingFor(Wait.forHttp("/").forStatusCode(404))
            httptape.start()

            baseUrl = "http://${httptape.host}:${httptape.getMappedPort(8081)}"
        }

        @AfterAll
        @JvmStatic
        fun stopContainer() {
            httptape.stop()
        }
    }

    @Test
    fun `replay fixture`() {
        val client = HttpClient.newHttpClient()
        val request = HttpRequest.newBuilder()
            .uri(URI.create("$baseUrl/api/users"))
            .GET()
            .build()

        val response = client.send(request,
            HttpResponse.BodyHandlers.ofString())

        assertEquals(200, response.statusCode())
    }
}
```

---

## Python (testcontainers-python)

```python
import requests
from testcontainers.core.container import DockerContainer
from testcontainers.core.waiting_utils import wait_for_logs


def test_replay_fixture():
    with DockerContainer("ghcr.io/httptape/httptape:latest") \
        .with_command("serve --fixtures /fixtures") \
        .with_exposed_ports(8081) \
        .with_volume_mapping("./testdata/fixtures", "/fixtures") as container:

        wait_for_logs(container, "listening on")
        host = container.get_container_host_ip()
        port = container.get_exposed_port(8081)
        base_url = f"http://{host}:{port}"

        resp = requests.get(f"{base_url}/api/users")

        assert resp.status_code == 200
```

You can also use `pytest` fixtures for shared setup:

```python
import pytest
from testcontainers.core.container import DockerContainer
from testcontainers.core.waiting_utils import wait_for_logs


@pytest.fixture(scope="module")
def httptape_url():
    container = DockerContainer("ghcr.io/httptape/httptape:latest") \
        .with_command("serve --fixtures /fixtures") \
        .with_exposed_ports(8081) \
        .with_volume_mapping("./testdata/fixtures", "/fixtures")
    container.start()
    wait_for_logs(container, "listening on")

    host = container.get_container_host_ip()
    port = container.get_exposed_port(8081)
    yield f"http://{host}:{port}"

    container.stop()


def test_users(httptape_url):
    resp = requests.get(f"{httptape_url}/api/users")
    assert resp.status_code == 200


def test_health(httptape_url):
    resp = requests.get(f"{httptape_url}/health")
    assert resp.status_code == 404  # no fixture recorded for /health
```

---

## Node.js (testcontainers-node)

```javascript
const { GenericContainer, Wait } = require("testcontainers");
const { describe, it, before, after } = require("node:test");
const assert = require("node:assert");

describe("httptape replay", () => {
  let container;
  let baseUrl;

  before(async () => {
    container = await new GenericContainer(
      "ghcr.io/httptape/httptape:latest"
    )
      .withCommand(["serve", "--fixtures", "/fixtures"])
      .withExposedPorts(8081)
      .withBindMounts([
        { source: "./testdata/fixtures", target: "/fixtures" },
      ])
      .withWaitStrategy(Wait.forLogMessage("listening on"))
      .start();

    const host = container.getHost();
    const port = container.getMappedPort(8081);
    baseUrl = `http://${host}:${port}`;
  });

  after(async () => {
    if (container) {
      await container.stop();
    }
  });

  it("replays a recorded fixture", async () => {
    const response = await fetch(`${baseUrl}/api/users`);
    assert.strictEqual(response.status, 200);
  });
});
```

Or with ES modules and a test framework like Vitest:

```typescript
import { GenericContainer, Wait } from "testcontainers";
import { describe, it, beforeAll, afterAll, expect } from "vitest";

describe("httptape replay", () => {
  let container: any;
  let baseUrl: string;

  beforeAll(async () => {
    container = await new GenericContainer(
      "ghcr.io/httptape/httptape:latest"
    )
      .withCommand(["serve", "--fixtures", "/fixtures"])
      .withExposedPorts(8081)
      .withBindMounts([
        { source: "./testdata/fixtures", target: "/fixtures" },
      ])
      .withWaitStrategy(Wait.forLogMessage("listening on"))
      .start();

    const host = container.getHost();
    const port = container.getMappedPort(8081);
    baseUrl = `http://${host}:${port}`;
  });

  afterAll(async () => {
    await container?.stop();
  });

  it("replays a recorded fixture", async () => {
    const response = await fetch(`${baseUrl}/api/users`);
    expect(response.status).toBe(200);
  });
});
```

---

## Generic Docker CLI

If your language or test framework does not have a Testcontainers library, you can use the Docker CLI directly. This works from any language via shell commands or a Docker SDK.

### Start the container

```bash
docker run -d \
  --name httptape-mock \
  -p 8081:8081 \
  -v "$PWD/testdata/fixtures:/fixtures" \
  ghcr.io/httptape/httptape:latest \
  serve --fixtures /fixtures
```

### Verify it is running

```bash
curl -s -o /dev/null -w "%{http_code}" http://localhost:8081/api/users
# 200 (if a fixture exists for GET /api/users)
```

### Use replay headers

```bash
docker run -d \
  --name httptape-mock \
  -p 8081:8081 \
  -v "$PWD/testdata/fixtures:/fixtures" \
  ghcr.io/httptape/httptape:latest \
  serve --fixtures /fixtures \
  --replay-header "Authorization=Bearer test-token" \
  --replay-header "X-Request-Id=integration-test-001"
```

### Stop and remove

```bash
docker stop httptape-mock && docker rm httptape-mock
```

### Docker Compose

```yaml
# docker-compose.test.yml
services:
  httptape:
    image: ghcr.io/httptape/httptape:latest
    command: ["serve", "--fixtures", "/fixtures"]
    ports:
      - "8081:8081"
    volumes:
      - ./testdata/fixtures:/fixtures:ro
```

```bash
docker compose -f docker-compose.test.yml up -d
# run your tests against http://localhost:8081
docker compose -f docker-compose.test.yml down
```

---

## Tips

- **Fixture directory**: always mount it read-only (`:ro`) in serve mode to prevent accidental writes.
- **Port conflicts**: use `--port` inside the container and map to a random host port (`-p 0:8081`) to avoid conflicts in CI.
- **Wait strategies**: prefer waiting for the `"listening on"` log line rather than a fixed sleep.
- **Replay headers**: use `--replay-header` to inject environment-specific headers (tokens, trace IDs) without editing fixtures.
- **CORS**: pass `--cors` if your frontend tests make requests from a browser context.

## See also

- [Docker](docker.md) -- Dockerfile reference and Docker Compose examples
- [Testcontainers (Go)](testcontainers.md) -- native Go Testcontainers module
- [CLI](cli.md) -- full CLI flag reference
- [Replay](replay.md) -- server options and replay behavior
