# Frontend-first development with httptape proxy mode

Working example of [httptape](https://github.com/VibeWarden/httptape) used as a fallback proxy in front of a backend API. The frontend never breaks, even when the backend goes down — it transparently falls back to cached data, and the UI reflects the current source **live, without any user action**.

## Architecture

```
Browser ──► proxy (:3001) ──► upstream (:8081)
              │ │                  │
              │ └─ on failure ◄─┐  └─ serves "real" data
              │                 │     from mocks/upstream-fixtures/
              │                 │
              │  L1 (memory)  ──┤
              │  L2 (disk)    ──┘
              │
              └─ /__httptape/health/stream  (SSE — live state updates)
```

Three services in docker-compose:

| Service | Role |
|---|---|
| `upstream` | Simulated real backend (`httptape serve` from `mocks/upstream-fixtures/`) |
| `proxy` | `httptape proxy` with L1+L2 caching, CORS, `--fallback-on-5xx`, and the `/__httptape/health` endpoint enabled |
| `frontend` | React + Vite UI (this example's source) |

## How cache fallback works

| Upstream state | Source served | Badge |
|---|---|---|
| Reachable | Live response from upstream, cached in L1 (raw) and L2 (redacted on write) | green **Live** |
| Down, L1 has the entry | L1 cached response (raw, current session) | yellow **L1 Cache** |
| Down, L1 empty (fresh proxy start) | L2 cache (disk, redacted) | red **L2 Cache** |

L2 is generated, not committed — the proxy fills it on first request from upstream and falls back to it when upstream is gone. Lives at `./.httptape-cache/fixtures/` (gitignored).

### Telling the sources apart

The compelling signal is **on-write redaction in action** — L2 is what hits disk, so httptape's typed fakers (configured in `mocks/sanitize.json`) replace every PII field with realistic-but-fake equivalents on the way down. Real example from one run:

| Field (profile) | Live (from upstream) | L2 (after redaction) |
|---|---|---|
| `name` | `Alice Johnson` | `Evelyn Martinez` (NameFaker) |
| `email` | `alice.johnson@acme-corp.com` | `user_3b5de929@example.com` (EmailFaker — `user_` prefix marks it as faked in the UI) |
| `phone` | `+1-555-867-5309` | `+0-472-470-7484` (PhoneFaker) |
| `card.number` | `4532-0158-2736-9841` | `4532-0194-9786-3174` (CreditCardFaker — Luhn-valid) |
| `card.expiry` | `03/28` | `11/98` (DateFaker) |
| `card.cvv` | `847` | `686` (NumericFaker, length=3) |
| `address` | `742 Evergreen Terrace, Springfield` | `7564 Pine Way, Fairview, KY 85992` (AddressFaker) |

Each fake is **deterministic for a given seed** — the same input always produces the same fake, so fixtures stay stable across reruns. Product fields have no PII, so they look identical in live vs L2 — the visible signal there is just the badge color.

## Live status updates

The proxy exposes `GET /__httptape/health/stream` (Server-Sent Events) when started with `--health-endpoint`. The frontend opens an `EventSource` on that URL and:

1. Updates the source badge whenever the proxy's perceived upstream state changes.
2. Re-fetches data so the page reflects the new source.

No polling, no manual refresh. See [`src/useHealthStream.ts`](src/useHealthStream.ts).

## Ask the assistant

Below the product grid, the demo includes an **AI shopping assistant** section with three pre-defined questions. Click a button and watch the response stream in word-by-word, exactly like a real LLM chat completion.

### How it works (and why it's the same as recording an OpenAI streaming call)

The three assistant fixtures in `mocks/upstream-fixtures/` (`get-assist-headphones.json`, `get-assist-keyboard.json`, `get-assist-hub.json`) are hand-authored httptape `Tape` recordings with `sse_events` arrays. Each fixture contains 25 SSE events using OpenAI-style `{"delta": "..."}` JSON payloads, with a final `[DONE]` sentinel to signal stream completion.

The wire format is **identical** to what you would get by recording a real `chat.completions.create(stream=True)` call with httptape:

1. The Recorder detects `Content-Type: text/event-stream` and parses individual SSE frames.
2. Each frame is stored as an `SSEEvent` with its `offset_ms` timestamp (milliseconds since response headers).
3. On replay, `WithSSETiming(SSETimingRealtime())` (the default) re-introduces the original inter-event delays, producing the typewriter effect in the browser.

The only difference is that these fixtures were written by hand rather than captured from a live API -- the recording flow, storage format, and replay behavior are exactly the same. You could replace these fixtures with real recorded LLM responses and the demo would work identically.

### Per-event sanitization

httptape's `RedactSSEEventData` and `FakeSSEEventData` sanitization functions operate on individual SSE event payloads, so PII inside streamed LLM completions is redacted before it touches disk. To see this in action, configure the proxy with `RedactSSEEventData` in the sanitization pipeline and watch the L2 cache in `.httptape-cache/`. The JSON config parser does not yet support SSE-specific rules, so this requires programmatic (Go API) configuration.

## Quick start

```bash
docker compose up
```

Pulls `ghcr.io/vibewarden/httptape:0.10.1` (v0.10.0 introduced SSE record/replay; v0.10.1 added the `--sse-timing` CLI flag this demo uses to preserve typewriter cadence on cache fallback). Also builds the React frontend. No local Go build needed.

Open [http://localhost:3000](http://localhost:3000).

> Pinned to `0.10.1` for reproducibility. To track latest, change the image to `ghcr.io/vibewarden/httptape:latest`.

## Try it

The badge in the sidebar shows the current data source. Run any of these and watch it flip live (~2 second debounce on the upstream probe):

```bash
docker compose stop upstream    # → yellow "L1 Cache"
docker compose restart proxy    # → red "L2 Cache" (L1 cleared on restart)
docker compose start upstream   # → green "Live"
```

The `./scripts/toggle-upstream.sh` script does the stop/start dance.

## Local development without Docker

```bash
# Build the httptape CLI (needs Go 1.26+)
( cd ../.. && go build -o /tmp/httptape ./cmd/httptape )

# Terminal 1: upstream
/tmp/httptape serve --fixtures ./mocks/upstream-fixtures --cors --addr :8082

# Terminal 2: proxy with health endpoint
/tmp/httptape proxy \
  --upstream http://localhost:8082 \
  --fixtures ./.httptape-cache/fixtures \
  --config ./mocks/sanitize.json \
  --port 3001 \
  --cors --fallback-on-5xx \
  --health-endpoint --upstream-probe-interval=2s

# Terminal 3: Vite dev server
VITE_API_URL=http://localhost:3001 npm run dev
```

## Adding new endpoints

Drop a JSON fixture into `mocks/upstream-fixtures/` — that's the "real" backend response the simulated upstream serves. The L2 cache will populate automatically on first request through the proxy. httptape picks up new upstream fixtures without restart.

Each file is a single httptape `Tape` (request/response pair).

## Project layout

```
ts-frontend-first/
  src/
    api.ts                       # fetch wrapper
    App.tsx                      # main app: profile + product grid + assistant + add-to-cart
    useHealthStream.ts           # SSE hook driving the badge + refetch
    useAssistantStream.ts        # SSE hook for AI assistant streaming (OpenAI-style deltas)
    components/
      ProfileCard.tsx            # identity + credit-card visualization (shows redaction)
      Assistant.tsx              # AI assistant: query buttons + streaming output
      ArchitectureDiagram.tsx    # live diagram of upstream/cache state
      Instructions.tsx           # the "Try it" copy-button list
  mocks/
    upstream-fixtures/           # source of truth — committed
      get-assist-headphones.json # SSE fixture: headphones recommendation (25 events)
      get-assist-keyboard.json   # SSE fixture: keyboard recommendation (25 events)
      get-assist-hub.json        # SSE fixture: hub recommendation (25 events)
    sanitize.json                # httptape redaction config (typed fakers)
  scripts/
    toggle-upstream.sh           # one-liner to flip upstream up/down
  .httptape-cache/               # L2 cache — generated, gitignored
    fixtures/                    # populated on first request, used as L2 fallback
  docker-compose.yml             # 3 services, pinned to httptape v0.10.1 from GHCR
  Dockerfile                     # multi-stage build for the React frontend
```
