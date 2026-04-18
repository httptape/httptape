package dev.httptape.demo;

import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.context.annotation.Import;

import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.List;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Integration tests for {@link RecommendationService} using httptape served via
 * Testcontainers. Proves that Spring AI streaming chat completions can be tested
 * deterministically with pre-recorded OpenAI SSE fixtures.
 *
 * <p>The shared httptape container (from {@link TestcontainersConfig}) serves
 * fixtures with {@code --sse-timing=realtime}, preserving the original
 * inter-event timing from the recording.
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
    void streamingPreservesCadence() {
        // Use the raw streaming method to verify events arrive over time
        // (not all at once), proving httptape's --sse-timing=realtime works.
        List<Instant> timestamps = new ArrayList<>();

        recommendationService.recommendStream("headphones")
                .doOnNext(token -> timestamps.add(Instant.now()))
                .toStream()
                .forEach(token -> {
                    // consume to drive the stream
                });

        // We should have received multiple tokens
        assertTrue(timestamps.size() > 5,
                "expected multiple streamed tokens, got: " + timestamps.size());

        // The total elapsed time should be > 500ms given the fixture has events
        // spread over ~2.9 seconds with realtime timing. We use a generous lower
        // bound to avoid flakiness while still proving events are not instant.
        Duration elapsed = Duration.between(timestamps.getFirst(), timestamps.getLast());
        assertTrue(elapsed.toMillis() > 500,
                "expected streaming to take >500ms with realtime timing, got: " + elapsed.toMillis() + "ms");
    }
}
