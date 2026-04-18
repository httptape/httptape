package dev.httptape.demo;

import java.io.IOException;
import java.io.UncheckedIOException;
import java.nio.file.Path;
import java.util.Arrays;
import java.util.function.Function;
import java.util.stream.Collectors;

import org.springframework.boot.test.context.TestConfiguration;
import org.springframework.context.annotation.Bean;
import org.springframework.core.io.Resource;
import org.springframework.core.io.support.PathMatchingResourcePatternResolver;
import org.springframework.test.context.DynamicPropertyRegistrar;
import org.testcontainers.containers.GenericContainer;
import org.testcontainers.containers.wait.strategy.Wait;
import org.testcontainers.utility.MountableFile;

/**
 * Shared Testcontainers configuration for the dev runner ({@link TestApplication})
 * and all integration tests.
 *
 * <p>Strategy: a single httptape container serving fixtures auto-discovered
 * from the classpath. All {@code .json} files under
 * {@code src/test/resources/fixtures/**} are copied into the container's
 * flat {@code /fixtures} directory at bean-construction time. Drop a new
 * fixture in the resources tree and it is picked up automatically -- no
 * code changes required.
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
    GenericContainer<?> httptapeContainer() throws IOException {
        GenericContainer<?> container = new GenericContainer<>("ghcr.io/vibewarden/httptape:0.10.1")
                .withCommand("serve", "--fixtures", "/fixtures", "--sse-timing=realtime")
                .withExposedPorts(8081)
                .waitingFor(Wait.forHttp("/").forStatusCode(404));

        // Auto-discover all fixtures under classpath:fixtures/**/*.json and
        // mount each into the container's flat /fixtures dir. Httptape's
        // FileStore is flat (no recursive subdirectory scanning), so we
        // collapse subdirs (openai/, users/, ...) into one container-side dir.
        // Filename collisions across subdirs would be ambiguous -- the toMap
        // merge function fails fast at config time rather than silently
        // overwriting.
        PathMatchingResourcePatternResolver resolver = new PathMatchingResourcePatternResolver();
        Arrays.stream(resolver.getResources("classpath:fixtures/**/*.json"))
                .filter(f -> f.getFilename() != null && !f.getFilename().isBlank())
                .collect(Collectors.toMap(
                        Resource::getFilename,
                        Function.identity(),
                        TestcontainersConfig::collide))
                .forEach((name, fixture) -> container.withCopyFileToContainer(
                        MountableFile.forHostPath(toPath(fixture)),
                        "/fixtures/" + name));

        return container;
    }

    private static Resource collide(Resource a, Resource b) {
        throw new IllegalStateException(
                "Fixture filename collision in flat /fixtures mount: '" + a.getFilename()
                        + "' appears in both '" + uri(a) + "' and '" + uri(b) + "'. "
                        + "Rename one (e.g., add a subject prefix) so filenames are unique across all subdirs.");
    }

    private static String uri(Resource r) {
        try {
            return r.getURI().toString();
        } catch (IOException e) {
            throw new UncheckedIOException(e);
        }
    }

    private static Path toPath(Resource r) {
        try {
            return r.getFile().toPath();
        } catch (IOException e) {
            throw new UncheckedIOException(e);
        }
    }

    /**
     * Wires the httptape container's dynamic port into the application properties
     * so that both Spring AI and the REST client point at the container.
     */
    @Bean
    DynamicPropertyRegistrar httptapeProperties(GenericContainer<?> httptapeContainer) {
        return registry -> {
            String baseUrl = "http://" + httptapeContainer.getHost()
                    + ":" + httptapeContainer.getMappedPort(8081);
            registry.add("spring.ai.openai.base-url", () -> baseUrl);
            registry.add("spring.ai.openai.api-key", () -> "sk-test-key");
            registry.add("app.external-api.base-url", () -> baseUrl);
        };
    }
}
