# Browser runner

Status: contract in `contracts/basamento/browser/v1`; conformance suite
**Planned** (WP-L9-01). Decision: ADR-0009. Specs: `specs/browser`.

The browser runner is a **separate service** that drives real browsers for
agents and records what happens. This repository owns its contract and its
conformance suite. The implementation (expected: TypeScript, Playwright and
rrweb) lives in its own repository, because browser automation is strongest
outside Go.

## 1. Transport: the invocation contract over connect

The runner implements `basamento.invoke.v1.InvocationService`, and that is
the only way to start, follow and cancel a session: to the platform it is one
more invocation, with op `browser.session`. The model-node machinery applies
unchanged: durable start and wait, events, cancel, cost, artifacts.

| Invocation | Carries |
| --- | --- |
| `params` | A `SessionSpec`: mode, target, feature or goal, budget, recording. |
| Event `data` | A `SessionEvent`: an action or a step result. |
| Handle id | The session id, used with `GetArtifacts`. |
| Start request headers | The platform's model endpoint and the session token, kept out of params because params are recorded. |

`BrowserRunnerService` adds only `Describe` and `GetArtifacts`. Compared with
a Temporal worker:

- the runner never holds Temporal credentials or reaches the orchestration;
- it works with every runtime;
- it has a typed, versioned contract;
- its events stream naturally;
- its browser pool scales on its own.

## 2. Sessions

| Mode | Input | Output |
| --- | --- | --- |
| **Scripted** | A Gherkin feature | The result of every step (REQ-BROWSER-004). Declarative steps are resolved into UI actions once, recorded, and replayed deterministically afterwards. |
| **Goal-driven** | A goal, optional instructions, a budget of steps, time and cost | Every action taken, until the goal is reached or the budget is spent (REQ-BROWSER-005). |

## 3. Recording and evidence

- **rrweb** DOM recording (`@rrweb/record` 2.x), injected at page start:
  - input values are masked by default (REQ-BROWSER-003);
  - timestamps line up with action events (REQ-BROWSER-002);
  - the run inspector replays it with rrweb-player.
- **Optional extras:**
  - screenshots per action;
  - video, needed for canvas, WebGL and video content, which rrweb does not capture;
  - Playwright trace, HAR, console log, and accessibility report.
- Artifacts are uploaded to blob storage through scoped URLs.

## 4. Models

The runner calls models only through the platform's OpenAI-compatible
endpoint, with its session token (REQ-BROWSER-006). Metering, routing and keys
stay central.
