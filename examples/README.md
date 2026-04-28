# httptape examples

Each subdirectory is a self-contained, runnable example of httptape used in a real scenario. They're built and tested against the version of httptape in this repository — when a feature lands here, the relevant example is updated alongside it.

| Example | What it shows |
|---|---|
| [`java-spring-boot/`](java-spring-boot/) | **Spring AI streaming chat completions + classic REST**, both tested deterministically via Testcontainers. Demonstrates SSE fixture replay in OpenAI's exact wire format with `@DynamicPropertySource` bean overrides. |
| [`kotlin-ktor-koog/`](kotlin-ktor-koog/) | **Koog AI agent with tool use**, tested via a single httptape Testcontainers instance. Demonstrates `body_fuzzy` matcher composition to distinguish two POST requests to the same URL by JSON body structure. |
| [`ts-frontend-first/`](ts-frontend-first/) | Vite + React frontend talking to an httptape proxy, with live source-state updates over SSE. Demonstrates fallback-to-cache (live → L1 → L2) and per-event redaction in `mocks/sanitize.json`. |

> More examples coming: Go-embedded library use, Python CI fixtures, Kotlin proxy integration. Tracked in `httptape-demos/` for now; will land here as each is polished.

## Conventions

- Each example owns its own dependencies — Go examples have their own `go.mod` so they don't pollute the library's stdlib-only constraint.
- `docker-compose.yml`, where present, pins to a published httptape image (`ghcr.io/httptape/httptape:<version>`) so examples Just Run on a fresh clone — no local build of httptape required. Bumped per release.
- Examples are kept opinionated and minimal — they showcase httptape behavior, not framework taste.

## CI smoke tests

Each example is built in CI by [`.github/workflows/examples.yml`](../.github/workflows/examples.yml) on every push/PR that touches `examples/**`. The workflow uses a matrix keyed on example name with `fail-fast: false`, so a broken example never blocks the others, and each example surfaces as its own named check (`build (<example-name>)`).

Note: this is a separate workflow from the library's `Tests` workflow on purpose — an example failure must never blink the library's CI badge.

### Adding a new example to CI

1. Drop the example under `examples/<name>/` and verify `npm ci && npm run build` (or the equivalent) works locally.
2. Add a row to the `matrix.example` list in `.github/workflows/examples.yml`:

   ```yaml
   - name: <example-name>
     path: examples/<example-name>
     node-version: "20"
     lockfile: examples/<example-name>/package-lock.json
     build-command: npm run build
   ```

3. Add matching `npm` and (if the example has a Dockerfile) `docker` blocks to [`.github/dependabot.yml`](../.github/dependabot.yml), copying the `ts-frontend-first` pattern.
4. Append a row to the table above.

That's the full recipe — no per-example workflow files, no shared dependency setup. Each example stays self-contained.
