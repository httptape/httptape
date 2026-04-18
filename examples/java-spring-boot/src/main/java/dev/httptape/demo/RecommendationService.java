package dev.httptape.demo;

import org.springframework.ai.chat.client.ChatClient;

import reactor.core.publisher.Flux;

/**
 * Uses Spring AI's {@link ChatClient} to ask an LLM for product recommendations
 * via streaming chat completions.
 *
 * <p>In production, the ChatClient talks to OpenAI's streaming chat completions
 * endpoint. In tests, httptape serves pre-recorded SSE fixtures in the exact
 * OpenAI wire format, making tests deterministic and cost-free.
 *
 * <p>Registered as a bean via {@link AppConfig} rather than stereotype annotations.
 */
public class RecommendationService {

    private final ChatClient chatClient;

    public RecommendationService(ChatClient.Builder chatClientBuilder) {
        this.chatClient = chatClientBuilder.build();
    }

    /**
     * Asks the LLM for a product recommendation using streaming.
     * Collects the streamed tokens into a single string.
     *
     * @param product the product category to get a recommendation for
     * @return the LLM's recommendation as a plain text string
     */
    public String recommend(String product) {
        Flux<String> stream = chatClient.prompt()
                .user("Recommend the best " + product + " for office use. "
                        + "Keep the answer to 2-3 sentences.")
                .stream()
                .content();

        // Collect all streamed tokens into a single response string.
        // Using .toStream() keeps the calling code blocking and readable.
        StringBuilder sb = new StringBuilder();
        stream.toStream().forEach(sb::append);
        return sb.toString();
    }

    /**
     * Returns the raw streaming flux of content tokens from the LLM.
     * Useful for tests that need to verify streaming cadence.
     *
     * @param product the product category to get a recommendation for
     * @return a Flux of content token strings
     */
    public Flux<String> recommendStream(String product) {
        return chatClient.prompt()
                .user("Recommend the best " + product + " for office use. "
                        + "Keep the answer to 2-3 sentences.")
                .stream()
                .content();
    }
}
