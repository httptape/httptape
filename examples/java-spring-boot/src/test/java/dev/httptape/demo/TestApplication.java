package dev.httptape.demo;

import org.springframework.boot.SpringApplication;

/**
 * Dev runner that boots the application with httptape served via Testcontainers.
 *
 * <p>Run this class directly (IntelliJ: right-click &rarr; Run/Debug) or via Maven:
 * <pre>{@code ./mvnw spring-boot:test-run}</pre>
 *
 * <p>The app starts at {@code http://localhost:8080} with a real httptape container
 * serving fixtures in the background. Exit the app to tear down the container.
 */
public class TestApplication {

    public static void main(String[] args) {
        SpringApplication.from(Application::main)
                .with(TestcontainersConfig.class)
                .run(args);
    }
}
