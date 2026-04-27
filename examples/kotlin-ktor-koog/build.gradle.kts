plugins {
    kotlin("jvm") version "2.3.21"
    kotlin("plugin.serialization") version "2.3.21"
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
    mavenLocal()
}

dependencies {
    // Ktor server
    implementation("io.ktor:ktor-server-netty:3.4.3")
    implementation("io.ktor:ktor-server-sse:3.4.3")
    implementation("io.ktor:ktor-server-content-negotiation:3.4.3")
    implementation("io.ktor:ktor-serialization-kotlinx-json:3.4.3")

    // Ktor client (for the weather tool's REST call)
    implementation("io.ktor:ktor-client-cio:3.4.3")
    implementation("io.ktor:ktor-client-content-negotiation:3.4.3")

    // Koog — AI agent framework (Ktor plugin + agents)
    implementation("ai.koog:koog-ktor:0.8.0")

    // kotlinx-serialization (Koog transitive, but explicit for clarity)
    implementation("org.jetbrains.kotlinx:kotlinx-serialization-json:1.11.0")

    // Logging
    implementation("ch.qos.logback:logback-classic:1.5.32")

    // Test — Kotest
    testImplementation("io.kotest:kotest-runner-junit5:6.1.11")
    testImplementation("io.kotest:kotest-assertions-core:6.1.11")

    // Test — httptape SDK (brings Testcontainers transitively)
    testImplementation("dev.httptape:httptape-testcontainers-kotest:0.1.0-SNAPSHOT")

    // Test — Ktor server test host and client
    testImplementation("io.ktor:ktor-server-test-host:3.4.3")
    testImplementation("io.ktor:ktor-client-content-negotiation:3.4.3")
}

tasks.withType<Test>().configureEach {
    useJUnitPlatform()
}

tasks.register<JavaExec>("testRun") {
    group = "application"
    description = "Run the application against the Testcontainers httptape (debugging / manual curl)"
    classpath = sourceSets["test"].runtimeClasspath
    mainClass.set("dev.httptape.demo.TestApplicationKt")
}
