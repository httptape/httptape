package dev.httptape.demo

import ai.koog.agents.core.tools.annotations.LLMDescription
import ai.koog.agents.core.tools.annotations.Tool
import ai.koog.agents.core.tools.reflect.ToolSet
import io.ktor.client.*
import io.ktor.client.call.*
import io.ktor.client.request.*
import kotlinx.serialization.json.JsonObject

/**
 * Koog tool set providing a weather forecast lookup tool.
 *
 * The [client] and [baseUrl] are injected via constructor so the caller
 * controls the HTTP client lifecycle and tests can redirect HTTP calls
 * to the httptape container.
 */
class WeatherTools(
    private val client: HttpClient,
    private val baseUrl: String
) : ToolSet {

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
