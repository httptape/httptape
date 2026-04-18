package dev.httptape.demo

import org.testcontainers.containers.GenericContainer
import org.testcontainers.containers.wait.strategy.Wait
import org.testcontainers.utility.MountableFile

/**
 * JVM-singleton httptape container shared across all test classes.
 *
 * Lazy initialization ensures the container is started exactly once per
 * JVM and reused for every test that reads [baseUrl]. The container
 * serves three fixture files and uses a declarative matcher config that
 * distinguishes the two POST /v1/chat/completions requests via
 * `body_fuzzy` on `$.messages[*].role`.
 *
 */
object HttptapeContainer {

    val instance: GenericContainer<*> by lazy {
        GenericContainer("ghcr.io/vibewarden/httptape:0.12.0")
            .withCommand(
                "serve",
                "--fixtures", "/fixtures",
                "--config", "/config/httptape.config.json"
            )
            .withExposedPorts(8081)
            .waitingFor(Wait.forHttp("/").forStatusCode(404))
            .apply {
                // Mount fixtures -- flatten subdirectories into /fixtures/
                copyFixture("fixtures/openai/chat-1.json")
                copyFixture("fixtures/openai/chat-2.json")
                copyFixture("fixtures/weather/weather-berlin.json")
                // Mount matcher config
                withCopyFileToContainer(
                    MountableFile.forClasspathResource("httptape.config.json"),
                    "/config/httptape.config.json"
                )
            }
            .also { it.start() }
    }

    /** Base URL pointing at the httptape container's mapped port. */
    val baseUrl: String
        get() = "http://${instance.host}:${instance.getMappedPort(8081)}"

    private fun GenericContainer<*>.copyFixture(classpathPath: String) {
        val filename = classpathPath.substringAfterLast("/")
        withCopyFileToContainer(
            MountableFile.forClasspathResource(classpathPath),
            "/fixtures/$filename"
        )
    }
}
