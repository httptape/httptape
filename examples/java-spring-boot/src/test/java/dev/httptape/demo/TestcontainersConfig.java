package dev.httptape.demo;

import dev.httptape.testcontainers.HttptapeContainer;
import dev.httptape.testcontainers.SseTimingMode;
import org.springframework.boot.test.context.TestConfiguration;
import org.springframework.context.annotation.Bean;
import org.springframework.test.context.DynamicPropertyRegistrar;

/**
 * Shared Testcontainers configuration for the dev runner ({@link TestApplication})
 * and all integration tests.
 *
 * <p>Strategy: a single httptape container serving fixtures auto-discovered
 * from the classpath. All {@code .json} files under
 * {@code src/test/resources/fixtures/**} are copied into the container's
 * flat {@code /fixtures} directory by the SDK's classpath scanning.
 * Drop a new fixture in the resources tree and it is picked up
 * automatically -- no code changes required.
 *
 * <p>One container per JVM (not per test class). Both the Spring AI ChatClient
 * and the REST UserService point at the same container.
 *
 * <p>Integration tests import this configuration via
 * {@code @Import(TestcontainersConfig.class)}.
 */
@TestConfiguration(proxyBeanMethods = false)
class TestcontainersConfig {

    /**
     * A single httptape container serving every {@code .json} fixture found
     * on the classpath under {@code fixtures/**}. Realtime SSE timing for a
     * realistic streaming experience.
     */
    @Bean
    HttptapeContainer httptapeContainer() {
        return new HttptapeContainer()
                .withFixturesFromClasspath("fixtures/")
                .withSseTiming(SseTimingMode.REALTIME);
    }

    /**
     * Wires the httptape container's dynamic port into the application properties
     * so that both Spring AI and the REST client point at the container.
     *
     * <p>Spring AI 2.0.0-M5 switched the OpenAI chat implementation from a
     * hand-rolled RestClient to the official openai-java SDK. That SDK
     * constructs request paths as {@code /chat/completions} (without a
     * {@code /v1} prefix) and expects the base URL to include the version
     * segment — e.g. {@code https://api.openai.com/v1}. The fixtures on
     * disk therefore record the full path {@code /v1/chat/completions}, and
     * the base URL must include the {@code /v1} prefix so that the SDK's
     * requests match the recorded fixtures.
     */
    @Bean
    DynamicPropertyRegistrar httptapeProperties(HttptapeContainer httptapeContainer) {
        return registry -> {
            String baseUrl = httptapeContainer.getBaseUrl();
            // The openai-java SDK appends /chat/completions to the base URL,
            // so we add /v1 to match the recorded fixture path /v1/chat/completions.
            registry.add("spring.ai.openai.base-url", () -> baseUrl + "/v1");
            registry.add("spring.ai.openai.api-key", () -> "sk-test-key");
            registry.add("app.external-api.base-url", () -> baseUrl);
        };
    }
}
