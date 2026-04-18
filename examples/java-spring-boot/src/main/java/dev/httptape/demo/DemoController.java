package dev.httptape.demo;

import org.springframework.http.MediaType;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;
import org.springframework.web.servlet.mvc.method.annotation.SseEmitter;

import java.util.List;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

/**
 * REST controller exposing the demo's two scenarios:
 * <ul>
 *   <li>{@code GET /users} and {@code GET /users/{id}} for classic REST</li>
 *   <li>{@code GET /recommendations?for=...} for AI streaming via SSE</li>
 * </ul>
 */
@RestController
public class DemoController {

    private final UserService userService;
    private final RecommendationService recommendationService;
    private final ExecutorService executor = Executors.newCachedThreadPool();

    public DemoController(UserService userService, RecommendationService recommendationService) {
        this.userService = userService;
        this.recommendationService = recommendationService;
    }

    @GetMapping("/users/{id}")
    public ResponseEntity<User> getUser(@PathVariable int id) {
        return userService.getUser(id)
                .map(ResponseEntity::ok)
                .orElse(ResponseEntity.notFound().build());
    }

    @GetMapping("/users")
    public List<User> getUsers() {
        return userService.getUsers();
    }

    @GetMapping(value = "/recommendations", produces = MediaType.TEXT_EVENT_STREAM_VALUE)
    public SseEmitter recommend(@RequestParam("for") String product) {
        SseEmitter emitter = new SseEmitter(30_000L);

        executor.execute(() -> {
            try {
                recommendationService.recommendStream(product)
                        .doOnNext(token -> {
                            try {
                                emitter.send(SseEmitter.event().data(token));
                            } catch (Exception e) {
                                emitter.completeWithError(e);
                            }
                        })
                        .doOnComplete(emitter::complete)
                        .doOnError(emitter::completeWithError)
                        .subscribe();
            } catch (Exception e) {
                emitter.completeWithError(e);
            }
        });

        return emitter;
    }
}
