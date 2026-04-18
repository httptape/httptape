package dev.httptape.demo;

import org.springframework.ai.chat.client.ChatClient;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.web.client.RestClient;

/**
 * Explicit bean configuration for all application services.
 *
 * <p>Wiring is visible in one place rather than scattered across stereotype
 * annotations ({@code @Service}, {@code @Component}) on individual classes.
 * Spring AI's auto-configured beans (e.g. {@link ChatClient.Builder}) are
 * injected as method parameters.
 */
@Configuration
public class AppConfig {

    /**
     * Provides a pre-configured {@link RestClient} with the external API
     * base URL injected from application properties.
     *
     * <p>In tests, {@code app.external-api.base-url} is overridden via
     * {@code @DynamicPropertySource} to point at the httptape Testcontainer.
     */
    @Bean
    public RestClient restClient(@Value("${app.external-api.base-url}") String baseUrl) {
        return RestClient.builder()
                .baseUrl(baseUrl)
                .build();
    }

    /**
     * Constructs the {@link UserService} with an explicit {@link RestClient} dependency.
     */
    @Bean
    public UserService userService(RestClient restClient) {
        return new UserService(restClient);
    }

    /**
     * Constructs the {@link RecommendationService} with Spring AI's auto-configured
     * {@link ChatClient.Builder}.
     */
    @Bean
    public RecommendationService recommendationService(ChatClient.Builder chatClientBuilder) {
        return new RecommendationService(chatClientBuilder);
    }
}
