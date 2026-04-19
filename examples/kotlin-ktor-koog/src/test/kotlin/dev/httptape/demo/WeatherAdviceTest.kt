package dev.httptape.demo

import dev.httptape.testcontainers.kotest.httptapeExtension
import io.kotest.core.extensions.install
import io.kotest.core.spec.style.FreeSpec
import io.kotest.matchers.string.shouldContain
import io.ktor.client.plugins.sse.*
import io.ktor.server.testing.*

/**
 * End-to-end integration test for the weather advice agent.
 *
 * Exercises the full Koog agent flow against a single httptape
 * Testcontainers instance managed by the httptape-jvm SDK's Kotest
 * extension:
 *
 * 1. POST /v1/chat/completions (system + user messages) -> tool_call
 * 2. GET /v1/forecast?city=Berlin -> weather JSON
 * 3. POST /v1/chat/completions (system + user + assistant + tool) -> final answer
 *
 * The httptape matcher config distinguishes the two POST requests via
 * `body_fuzzy` on `$.messages[*].role`, proving that #178 (vacuous-true),
 * #179 (Criterion interface), and #180 (declarative config) work
 * together end-to-end.
 */
class WeatherAdviceTest : FreeSpec({

    val httptape = install(httptapeExtension {
        fixtures("fixtures/")
        matcherConfig("httptape.config.json")
    })

    "advises bringing an umbrella when it is rainy in the requested city" {
        testApplication {
            application {
                configureApp(
                    openAiBaseUrl = httptape.baseUrl,
                    openAiApiKey = "sk-test-key",
                    weatherBaseUrl = httptape.baseUrl
                )
            }

            val client = createClient {
                install(SSE)
            }

            val events = mutableListOf<String>()
            client.sse("/weather-advice?city=Berlin") {
                incoming.collect { event ->
                    event.data?.let { events.add(it) }
                }
            }

            val fullResponse = events.joinToString("")
            fullResponse shouldContain "umbrella"
        }
    }
})
