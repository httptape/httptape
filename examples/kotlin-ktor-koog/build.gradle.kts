plugins {
    kotlin("jvm") version "2.3.20"
    kotlin("plugin.serialization") version "2.3.20"
    application
}

group = "dev.httptape"
version = "0.0.1-SNAPSHOT"

application {
    mainClass.set("dev.httptape.demo.ApplicationKt")
}

kotlin {
    jvmToolchain(25)
}

repositories {
    mavenCentral()
}

dependencies {
    // Ktor server
    implementation("io.ktor:ktor-server-netty:3.4.2")
    implementation("io.ktor:ktor-server-sse:3.4.2")
    implementation("io.ktor:ktor-server-content-negotiation:3.4.2")
    implementation("io.ktor:ktor-serialization-kotlinx-json:3.4.2")

    // Ktor client (for the weather tool's REST call)
    implementation("io.ktor:ktor-client-cio:3.4.2")
    implementation("io.ktor:ktor-client-content-negotiation:3.4.2")

    // Koog — AI agent framework (Ktor plugin + agents)
    implementation("ai.koog:koog-ktor:0.8.0")

    // kotlinx-serialization (Koog transitive, but explicit for clarity)
    implementation("org.jetbrains.kotlinx:kotlinx-serialization-json:1.10.0")

    // Logging
    implementation("ch.qos.logback:logback-classic:1.5.13")

    // Test — Kotest
    testImplementation("io.kotest:kotest-runner-junit5:6.1.11")
    testImplementation("io.kotest:kotest-assertions-core:6.1.11")

    // Test — Testcontainers
    testImplementation("org.testcontainers:testcontainers:2.0.4")
    testImplementation("io.kotest.extensions:kotest-extensions-testcontainers:2.0.2")

    // Test — Ktor server test host and client
    testImplementation("io.ktor:ktor-server-test-host:3.4.2")
    testImplementation("io.ktor:ktor-client-content-negotiation:3.4.2")
}

tasks.withType<Test>().configureEach {
    useJUnitPlatform()
}
