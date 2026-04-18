package dev.httptape.demo

import ai.koog.agents.core.tools.annotations.LLMDescription
import ai.koog.agents.core.tools.annotations.Tool
import ai.koog.agents.core.tools.reflect.ToolSet
import io.ktor.client.*
import io.ktor.client.call.*
import io.ktor.client.engine.cio.*
import io.ktor.client.plugins.contentnegotiation.*
import io.ktor.client.request.*
import io.ktor.serialization.kotlinx.json.*
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject

/**
 * Koog tool set providing a weather forecast lookup tool.
 *
 * The [baseUrl] is injected via constructor so tests can redirect
 * HTTP calls to the httptape container.
 */
class WeatherTools(private val baseUrl: String) : ToolSet {

    private val client = HttpClient(CIO) {
        install(ContentNegotiation) {
            json(Json { ignoreUnknownKeys = true })
        }
    }

    @Tool
    @LLMDescription("Get the current weather forecast for a city")
    suspend fun getWeather(
        @LLMDescription("The city name to get weather for")
        city: String
    ): String {
        val response: JsonObject = client.get("$baseUrl/v1/forecast") {
            parameter("city", city)
        }.body()
        return response.toString()
    }
}
