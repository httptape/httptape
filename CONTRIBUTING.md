# Contributing to httptape

Thanks for your interest in contributing. This document describes how this
repository is actually maintained today, not how a generic OSS project
might be.

## How issues become PRs in this repo

httptape is built using a four-stage agent pipeline backed by a human
reviewer. The flow is:

1. **PM agent** drafts the issue spec (problem, scope, acceptance criteria,
   non-goals).
2. **Architect agent** reads the spec, makes design decisions, and appends an
   ADR to `decisions.md`.
3. **Dev agent** implements the design exactly as specified.
4. **Reviewer agent** reviews the PR for spec compliance, test coverage, and
   adherence to project rules.
5. The repo owner reviews and merges.

The full pipeline rules live in `CLAUDE.md`. External contributors do not
need to use the agent pipeline — opening a clear issue and a focused PR is
fine. The same coding rules apply either way.

## Hard rules (from `CLAUDE.md`)

- **stdlib only**: no new Go-module dependencies. v1 is committed to a
  zero-transitive-deps surface for embedders.
- **No `init()` functions**: zero side effects on import.
- **No package-level mutable state**: everything via dependency injection or
  functional options.
- **Race-clean tests**: `go test ./... -race` must pass. CI enforces this.
- **godoc on every exported type and function**: no exceptions.
- **Functional options for all public constructors**: see existing
  `WithStore`, `WithMatcher`, etc. patterns.

## Conventional commits

Commit messages and PR titles use these prefixes (matching what's already
in `git log`):

- `feat:`  new feature
- `fix:`   bug fix
- `chore:` maintenance / housekeeping (no behavior change)
- `docs:`  documentation only
- `test:`  test-only changes
- `ci:`    CI/workflow changes

Use the imperative mood: "add X", not "added X" or "adds X".

## Branch naming

- `feat/<issue-number>-<short-slug>` for features
- `fix/<issue-number>-<short-slug>` for bug fixes

Example: `feat/129-repo-polish`, `fix/142-recorder-deadlock`.

## Running tests locally

```sh
go test ./... -race -count=1
```

`-count=1` disables the test cache and ensures every run is fresh. Required
before opening a PR.

## Architectural decisions

All architectural decisions are recorded in `decisions.md`. The "Locked
decisions" table at the top of that file lists choices that are not open
for relitigation in a PR — examples include "stdlib only", "Apache 2.0
license", "JSON fixture format", "sanitize on write".

If you believe a locked decision should change, **open an issue first** with
an ADR-style write-up (Context / Decision / Consequences). Do not send a PR
that quietly changes a locked decision — it will be closed and asked to go
through the issue-first path.

For additions to the architecture (new ports, new adapters, new sanitizer
types), an ADR is appended to `decisions.md` by the architect stage of the
pipeline. External contributors don't need to write the ADR themselves; the
maintainer will. But please flag the architectural intent clearly in your
issue.

## Reporting security issues

See `SECURITY.md`. Do **not** open a public issue for security reports.
