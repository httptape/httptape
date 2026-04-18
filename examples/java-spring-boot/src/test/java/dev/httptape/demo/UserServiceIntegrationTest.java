package dev.httptape.demo;

import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.context.annotation.Import;

import java.util.List;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Integration tests for {@link UserService} using httptape served via
 * Testcontainers. Demonstrates the classic REST integration testing pattern:
 * pre-recorded fixtures replayed deterministically.
 *
 * <p>Uses the shared httptape container from {@link TestcontainersConfig},
 * which serves all fixtures (OpenAI + users) from a single container.
 */
@SpringBootTest
@Import(TestcontainersConfig.class)
class UserServiceIntegrationTest {

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
