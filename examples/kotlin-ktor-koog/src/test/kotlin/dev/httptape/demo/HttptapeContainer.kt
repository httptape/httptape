package dev.httptape.demo

import dev.httptape.testcontainers.kotlin.httptape

/**
 * JVM-singleton httptape container shared across all test classes.
 *
 * Lazy initialization ensures the container is started exactly once per
 * JVM and reused for every test that reads [baseUrl]. The SDK's
 * [httptape] DSL replaces ~50 lines of manual GenericContainer setup
 * with fixture classpath scanning and matcher config mounting.
 */
object HttptapeContainer {

    val instance by lazy {
        httptape {
            fixtures("fixtures/")
            matcherConfig("httptape.config.json")
        }.also { it.start() }
    }

    /** Base URL pointing at the httptape container's mapped port. */
    val baseUrl: String
        get() = instance.getBaseUrl()
}
