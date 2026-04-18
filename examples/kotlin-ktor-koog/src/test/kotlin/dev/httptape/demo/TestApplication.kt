package dev.httptape.demo

import io.ktor.server.engine.*
import io.ktor.server.netty.*

/**
 * Dev runner that boots the application with httptape served via Testcontainers.
 *
 * Run this class directly (IntelliJ: right-click -> Run) or via Gradle:
 * ```
 * ./gradlew testRun
 * ```
 *
 * The app starts at `http://localhost:8080` with a real httptape container
 * serving fixtures in the background. Exit the process to tear down the
 * container.
 */
fun main() {
    val baseUrl = HttptapeContainer.baseUrl
    embeddedServer(Netty, port = 8080) {
        configureApp(
            openAiBaseUrl = baseUrl,
            openAiApiKey = "sk-test-key",
            weatherBaseUrl = baseUrl
        )
    }.start(wait = true)
}
