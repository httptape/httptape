pluginManagement {
    repositories {
        gradlePluginPortal()
        mavenCentral()
    }
}

rootProject.name = "kotlin-ktor-koog-demo"

// Composite build: resolve httptape SDK from sibling repo if available.
// Falls back to mavenLocal / Maven Central if the sibling isn't checked out.
val sdkDir = file("../../../httptape-jvm")
if (sdkDir.exists()) {
    includeBuild(sdkDir) {
        dependencySubstitution {
            substitute(module("dev.httptape:httptape-testcontainers"))
                .using(project(":testcontainers"))
            substitute(module("dev.httptape:httptape-testcontainers-kotlin"))
                .using(project(":testcontainers-kotlin"))
            substitute(module("dev.httptape:httptape-testcontainers-kotest"))
                .using(project(":testcontainers-kotest"))
        }
    }
}
