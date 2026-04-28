package dev.httptape.demo;

import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.context.annotation.Import;

import java.util.ArrayList;
import java.util.List;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Integration tests for {@link RecommendationService} using httptape served via
 * Testcontainers. Proves that Spring AI streaming chat completions can be tested
 * deterministically with pre-recorded OpenAI SSE fixtures.
 *
 * <p>The shared httptape container (from {@link TestcontainersConfig}) serves
 * fixtures with {@code --sse-timing=realtime}. Note: Spring AI 2.0.0-M5 switched
 * the OpenAI chat implementation from a hand-rolled RestClient to the official
 * openai-java SDK, which uses OkHttp internally for async streaming. OkHttp
 * buffers SSE events before dispatching them through the Reactor pipeline, so
 * inter-event timing cannot be verified at this layer. The streaming cadence
 * test therefore only asserts that multiple tokens are delivered incrementally.
 */
@SpringBootTest
@Import(TestcontainersConfig.class)
class RecommendationServiceIntegrationTest {

    @Autowired
    private RecommendationService recommendationService;

    @Test
    void streamingRecommendationReturnsExpectedContent() {
        String result = recommendationService.recommend("headphones");

        // The assembled response should contain key phrases from the fixture.
        assertNotNull(result, "recommendation should not be null");
        assertFalse(result.isBlank(), "recommendation should not be blank");
        assertTrue(result.contains("Sony WH-1000XM5"),
                "expected product name in response, got: " + result);
        assertTrue(result.contains("noise"),
                "expected 'noise' in response, got: " + result);
    }

    @Test
    void streamingDeliversTokensIncrementally() {
        // Verify that the streaming path delivers individual content tokens
        // rather than a single monolithic response. This proves that httptape's
        // SSE fixture is consumed correctly through the openai-java SDK's
        // async streaming pipeline.
        //
        // Note: inter-event timing cannot be asserted here because the
        // openai-java SDK's OkHttp transport buffers SSE events before
        // dispatching them through the Reactor chain, collapsing the
        // original realtime gaps.
        List<String> tokens = new ArrayList<>();

        recommendationService.recommendStream("headphones")
                .doOnNext(tokens::add)
                .toStream()
                .forEach(token -> {
                    // consume to drive the stream
                });

        // The fixture contains ~30 SSE chunks; we expect multiple tokens.
        assertTrue(tokens.size() > 5,
                "expected multiple streamed tokens, got: " + tokens.size());

        // Verify the assembled content matches the fixture.
        String assembled = String.join("", tokens);
        assertTrue(assembled.contains("Sony WH-1000XM5"),
                "expected product name in assembled stream, got: " + assembled);
    }
}
