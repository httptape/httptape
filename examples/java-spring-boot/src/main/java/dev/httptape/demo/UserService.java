package dev.httptape.demo;

import org.springframework.http.HttpStatus;
import org.springframework.web.client.RestClient;

import java.util.List;
import java.util.Optional;

/**
 * Fetches user data from an external REST API via Spring's {@link RestClient}.
 *
 * <p>In production the base URL points at the real API; in tests it points at
 * an httptape container that replays recorded fixtures.
 *
 * <p>Registered as a bean via {@link AppConfig} rather than stereotype annotations.
 */
public class UserService {

    private final RestClient restClient;

    public UserService(RestClient restClient) {
        this.restClient = restClient;
    }

    /**
     * Fetches a single user by ID.
     *
     * @param id user identifier
     * @return the user, or empty if not found (HTTP 404)
     */
    public Optional<User> getUser(int id) {
        return restClient.get()
                .uri("/users/{id}", id)
                .exchange((req, resp) -> {
                    if (resp.getStatusCode() == HttpStatus.NOT_FOUND) {
                        return Optional.empty();
                    }
                    return Optional.ofNullable(resp.bodyTo(User.class));
                });
    }

    /**
     * Fetches all users.
     *
     * @return list of users (never null)
     */
    public List<User> getUsers() {
        User[] users = restClient.get()
                .uri("/users")
                .retrieve()
                .body(User[].class);
        return users != null ? List.of(users) : List.of();
    }
}
