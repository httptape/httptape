# Multi-stage build: Gradle build -> JRE 25 runtime
FROM eclipse-temurin:25-jdk AS build
WORKDIR /app
COPY gradle/ gradle/
COPY gradlew build.gradle.kts settings.gradle.kts ./
# Download dependencies first (layer caching)
RUN ./gradlew dependencies --no-daemon
COPY src/ src/
RUN ./gradlew installDist --no-daemon

FROM eclipse-temurin:25-jre
WORKDIR /app
COPY --from=build /app/build/install/kotlin-ktor-koog-demo/ ./
EXPOSE 8080
ENTRYPOINT ["bin/kotlin-ktor-koog-demo"]
