# Security Policy

## Supported versions

httptape is pre-release. The only supported version is the current `main`
branch. Once v0.9.0 ships, this table will be updated to list supported
released versions explicitly.

| Version | Supported          |
|---------|--------------------|
| `main`  | Yes (best-effort)  |
| `v0.x`  | Not yet released   |

## Reporting a vulnerability

**Preferred**: open a GitHub Private Vulnerability Report at
<https://github.com/VibeWarden/httptape/security/advisories/new>.
This keeps the report private until a fix is ready and lets us coordinate a
CVE if applicable.

**Alternative**: email `tibtof@gmail.com` with `[httptape security]` in the
subject line. Please do not open a public issue for security reports.

## Response time (best-effort SLA)

- Acknowledgement: within 7 days
- Initial assessment: within 14 days
- Coordinated disclosure window: typically 90 days from acknowledgement,
  shorter for actively exploited issues, longer if a coordinated upstream
  fix is required

These are best-effort targets, not contractual commitments. httptape is
maintained by a small team.

## Scope: what counts as a security issue in httptape

- **Sanitizer bypass**: any input pattern where a configured redaction or
  faker rule fails to redact data that the rule was meant to cover, causing
  sensitive data to be written to a tape on disk.
- **Path traversal in storage**: a tape ID or filename input that causes the
  filesystem store to read or write outside the configured store directory.
- **Replay leak**: the mock server returning data from a tape that was
  supposed to be redacted (e.g. recorded after the rule was added but the
  redaction did not apply, or a header rule that did not strip a configured
  header before serve).
- **TLS / certificate handling**: incorrect verification, leak of private
  keys, or downgrade in record/proxy modes.
- **Faker collisions**: deterministic-faker output that maps two distinct
  real values to the same fake (which would let a reader correlate a fake
  back to a real value via a known-plaintext pair).

## Out of scope

- Bugs in upstream HTTP services that httptape records — those are the
  upstream's problem.
- Denial-of-service via oversized tapes when the user has explicitly opted
  to record them (bound the input on your side).
- Crashes or hangs that require an attacker to control the embedder's Go
  code — that's a code-execution problem, not an httptape problem.
- Vulnerabilities in third-party CI Actions used in this repo's workflows —
  report those upstream.

## Acknowledgement

We credit reporters in the published advisory unless asked otherwise.
