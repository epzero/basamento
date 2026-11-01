# ADR-0009 — Browser use and recording as a separate service

**Status:** accepted · **Date:** 2026-11-01

## Context
Agents need to use web applications: follow a scripted feature or pursue a
goal, and leave a recording that people can replay. The strongest browser
automation and DOM recording tools (Playwright, rrweb) live in the
TypeScript ecosystem, while the platform is pure Go (ADR-0001).

## Decision
- A **browser runner** service drives real browsers and records sessions. This
  repository owns its contract (`contracts/basamento/browser/v1`), its
  conformance suite and its specs (`specs/browser`); the implementation lives
  in its own repository.
- A session **is** an invocation, and the invocation contract
  (`basamento.invoke.v1.InvocationService`) is the only way to start, follow
  and cancel one: op `browser.session`, a `SessionSpec` as params, a
  `SessionEvent` in each event. `BrowserRunnerService` adds only what is
  specific to browsers (`Describe`, `GetArtifacts`). The runner is not a
  Temporal worker:
  - it never holds orchestration credentials;
  - it works with every runtime;
  - it has a typed, versioned contract;
  - it streams events;
  - it scales its browser pool on its own.
- Sessions are **scripted** (a Gherkin feature) or **goal-driven** (a goal and a
  budget). Recordings use rrweb with input masking by default.
- The runner calls models only through the platform's OpenAI-compatible
  endpoint with a session-scoped token. The platform mints the token inside
  the start step and sends it in a request header, so it never enters params,
  state or history.

## Consequences
- Agents get browser use and replayable recordings without browser code in Go.
- The `@journey` lane of the BDD convention runs UI scenarios through the same
  service.
