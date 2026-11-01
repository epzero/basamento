# Browser runner: browser use and recording

Status: **Planned** (WP-L9-01). The browser runner is a separate service that
drives real browsers for agents and records what happens. It implements
`contracts/basamento/browser/v1` and the invocation contract. This repository
owns the contract and its conformance suite, not the browser automation.
Design: ADR-0009, `docs/architecture/browser-runner.md`.

### REQ-BROWSER-001 — Contract conformance
The browser runner shall implement `BrowserRunnerService` and the invocation contract, and pass the browser runner conformance suite.

### REQ-BROWSER-002 — DOM recording
Where rrweb recording is requested, the browser runner shall return the recording as an artifact whose timestamps align with the session's action events.

### REQ-BROWSER-003 — Input masking
While recording a session, the browser runner shall mask the values of input fields unless the session explicitly allows a field.

### REQ-BROWSER-004 — Scripted sessions
When a session is started with a feature, the browser runner shall execute its scenarios and report the result of each step.

### REQ-BROWSER-005 — Goal-driven sessions
When a session is started with a goal, the browser runner shall act in the browser until the goal is reached or the session's budget is spent, and report every action it took.

### REQ-BROWSER-006 — Models through the platform
The browser runner shall call models only through the platform's OpenAI-compatible endpoint, with the token issued for its session.
