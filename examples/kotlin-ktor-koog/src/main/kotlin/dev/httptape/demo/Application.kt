package dev.httptape.demo

import io.ktor.client.*
import io.ktor.client.engine.cio.*
import io.ktor.client.plugins.contentnegotiation.*
import io.ktor.serialization.kotlinx.json.*
import io.ktor.server.application.*
import io.ktor.server.engine.*
import io.ktor.server.netty.*
import io.ktor.server.routing.*
import io.ktor.server.plugins.contentnegotiation.ContentNegotiation as ServerContentNegotiation
import io.ktor.server.sse.*
import kotlinx.serialization.json.Json

/**
 * Entry point for the Ktor demo application.
 *
 * Starts an embedded Netty server on port 8080 (configurable via the
 * PORT environment variable) with SSE and content negotiation support,
 * then registers the weather advice route.
 */
fun main() {
    val port = System.getenv("PORT")?.toIntOrNull() ?: 8080
    embeddedServer(Netty, port = port) {
        configureApp()
    }.start(wait = true)
}

/**
 * Configures the Ktor application with all required plugins and routes.
 *
 * Creates an application-scoped [HttpClient] that is shared across all
 * requests and closed when the application stops. Extracted as an
 * extension function so tests can call it with overridden base URLs
 * via [testApplication].
 */
fun Application.configureApp(
    openAiBaseUrl: String = System.getenv("OPENAI_BASE_URL") ?: "https://api.openai.com",
    openAiApiKey: String = System.getenv("OPENAI_API_KEY") ?: "sk-placeholder",
    weatherBaseUrl: String = System.getenv("WEATHER_BASE_URL") ?: "https://wttr.in"
) {
    install(ServerContentNegotiation) {
        json()
    }
    install(SSE)

    val weatherHttpClient = HttpClient(CIO) {
        install(ContentNegotiation) {
            json(Json { ignoreUnknownKeys = true })
        }
    }

    monitor.subscribe(ApplicationStopped) {
        weatherHttpClient.close()
    }

    routing {
        weatherAdviceRoute(openAiBaseUrl, openAiApiKey, weatherBaseUrl, weatherHttpClient)
    }
}
