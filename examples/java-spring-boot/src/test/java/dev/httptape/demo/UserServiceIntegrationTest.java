package dev.httptape.demo;

import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.DynamicPropertyRegistry;
import org.springframework.test.context.DynamicPropertySource;
import org.testcontainers.containers.GenericContainer;
import org.testcontainers.containers.wait.strategy.Wait;
import org.testcontainers.junit.jupiter.Container;
import org.testcontainers.junit.jupiter.Testcontainers;
import org.testcontainers.utility.MountableFile;

import java.util.List;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Integration tests for {@link UserService} using httptape served via
 * Testcontainers. Demonstrates the classic REST integration testing pattern:
 * pre-recorded fixtures replayed deterministically.
 */
@SpringBootTest
@Testcontainers
class UserServiceIntegrationTest {

    @Container
    static final GenericContainer<?> httptape = new GenericContainer<>(
            "ghcr.io/vibewarden/httptape:0.10.1"
    )
            .withCommand("serve", "--fixtures", "/fixtures")
            .withExposedPorts(8081)
            .withCopyFileToContainer(
                    MountableFile.forClasspathResource("fixtures/users/"),
                    "/fixtures/"
            )
            .waitingFor(Wait.forHttp("/").forStatusCode(404));

    @DynamicPropertySource
    static void overrideProperties(DynamicPropertyRegistry registry) {
        registry.add("app.external-api.base-url", () ->
                "http://" + httptape.getHost() + ":" + httptape.getMappedPort(8081));
    }

    @Autowired
    private UserService userService;

    @Test
    void getUserReturnsUserWhenFound() {
        Optional<User> user = userService.getUser(1);

        assertTrue(user.isPresent(), "user should be present");
        assertEquals(1, user.get().id());
        assertEquals("Alice Johnson", user.get().name());
        assertEquals("alice@example.com", user.get().email());
    }

    @Test
    void getUsersReturnsList() {
        List<User> users = userService.getUsers();

        assertEquals(2, users.size());
        assertEquals("Alice Johnson", users.get(0).name());
        assertEquals("Bob Smith", users.get(1).name());
    }

    @Test
    void getUserReturnsEmptyWhenNotFound() {
        Optional<User> user = userService.getUser(999);

        assertTrue(user.isEmpty(), "user 999 should not be found");
    }
}
