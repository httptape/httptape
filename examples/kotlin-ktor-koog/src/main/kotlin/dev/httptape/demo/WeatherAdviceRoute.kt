package dev.httptape.demo

import ai.koog.agents.core.agent.AIAgent
import ai.koog.agents.core.agent.singleRunStrategy
import ai.koog.agents.core.tools.ToolRegistry
import ai.koog.prompt.executor.clients.openai.OpenAIClientSettings
import ai.koog.prompt.executor.clients.openai.OpenAILLMClient
import ai.koog.prompt.executor.clients.openai.OpenAIModels
import ai.koog.prompt.executor.llms.MultiLLMPromptExecutor
import io.ktor.client.*
import io.ktor.server.routing.*
import io.ktor.server.sse.*

/**
 * Registers the `GET /weather-advice` route.
 *
 * The route accepts a `city` query parameter, creates a Koog agent
 * with the [WeatherTools] tool set, runs the agent against the OpenAI
 * chat completions API, and returns the final answer as a single SSE event.
 *
 * The [weatherHttpClient] is an application-scoped Ktor [HttpClient]
 * whose lifecycle is managed by the caller (closed on application stop).
 */
fun Route.weatherAdviceRoute(
    openAiBaseUrl: String,
    openAiApiKey: String,
    weatherBaseUrl: String,
    weatherHttpClient: HttpClient
) {
    sse("/weather-advice") {
        val city = call.parameters["city"]
        if (city == null) {
            send(data = "Missing 'city' query parameter")
            return@sse
        }

        val weatherTools = WeatherTools(weatherHttpClient, weatherBaseUrl)
        val toolRegistry = ToolRegistry { tools(weatherTools) }

        val llmClient = OpenAILLMClient(
            openAiApiKey,
            OpenAIClientSettings(baseUrl = openAiBaseUrl)
        )
        val executor = MultiLLMPromptExecutor(llmClient)

        val agent = AIAgent.builder()
            .promptExecutor(executor)
            .llmModel(OpenAIModels.Chat.GPT4oMini)
            .toolRegistry(toolRegistry)
            .systemPrompt("You are a helpful weather advisor.")
            .graphStrategy(singleRunStrategy())
            .build()

        val result = agent.run("What's the weather in $city? Should I bring an umbrella?")

        send(data = result)
    }
}
