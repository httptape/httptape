package dev.httptape.demo;

/**
 * A user returned by the external REST API.
 *
 * @param id    unique identifier
 * @param name  display name
 * @param email email address
 */
public record User(int id, String name, String email) {
}
