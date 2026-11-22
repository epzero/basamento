# Presets: design of the codegen preset

Interfaces and decisions the implementer must follow, for WP-L10-01
(`presets/codegen`). Requirements: `requirements.md`; scenarios: `loop.feature`,
`iterate.feature`, `versions.feature`, `eval.feature`.
Decisions: ADR-0003 (flows as data), ADR-0011 (host and node handlers),
ADR-0010 (eval lane). This file adds no behavior: where it and the
requirements could disagree, the requirements win and this file is fixed first.

## 0. How the preset ships, and changes landed first

The codegen preset is an application built on Basamento, not part of the
foundation: `presets/codegen` is its own Go module, with its own binary, that
imports Basamento as a library, like
`runtime/temporal/embedded`, which already has its own `go.mod`. It is not
compiled into `kit`, has no entry in `layering.yaml` and needs no ADR for
this.

This work package owns `presets/codegen` only. Before implementation of
`presets/codegen` starts:

1. **Sandbox commands** (`REQ-PRESET-005`). The plan is a runtime model output,
   so the static IR cannot carry one node per test. The IR declares `verify` as
   a `map` over `state.plan.tests` with `max_parallel` 0 whose child is a
   `run-tests` `tool` node; the map delivers each test as the child node's
   input, and the host passes a `tool` node's input as its call args, so the
   child carries no static `spec.args`. The runner executes the plan's tests
   directly. A literal-`spec.args` host (one static node per test) is **out of
   scope**, not a later change in `tool` (WP-L7-02's area): static IR cannot
   know a runtime plan's tests and the host already passes node input as call
   args, so no tool capability change is needed. The safety rule stands: never
   build untrusted shell text with a CEL expression — `expr` is deterministic
   and cost-limited, but it is not an expression language for composing
   untrusted shell text. The command comes from the runtime plan as node
   input, not a CEL expression.

## 1. The flows

Both flows are IR in `presets/codegen/ir/` and are compiled to the same
canonical IR as any other flow (ADR-0003). They share the loop; they differ in
the first stage and in the commit.

```
codegen.bootstrap                      codegen.iterate
  brief ─► plan ─► implement(plan)       repository, base branch, change request
          ─► verify(sandbox)             ─► plan ─► implement(plan)
          ─► repair(guard) ─► verify     ─► verify(sandbox)
          ─► self-review                 ─► repair(guard) ─► verify
          ─► commit                      ─► self-review
                                         ─► commit on codegen/<run id>
```

The loop mirrors the developer's workflow (`AGENTS.md`): no `gate` node and
no reviewer session in either flow. The platform's `gate` node
(`await_gate`, `dc.Await`) still exists for a long-lived human wait after the
commit or on the pull request, owned by an outer orchestrator; the codegen
preset does not use it.

| Stage | Nodes | Requirement |
| --- | --- | --- |
| `plan` | One `agent` node, `planner` agent, structured output `plan` | 001 |
| `implement` | `map` over `plan.modules`, `max_parallel` 0 (one at a time), node `implement_module` (`agent` with `output_schema: workspace`) | 004 |
| `verify` | `map` over `state.plan.tests` (`max_parallel` 0) whose child `run-tests` `tool` node receives each test as its input/call args; one execution per test in plan order, on the workspace the last stage returned; the runner path executes the plan's tests directly | 005 |
| `repair` | `agent` node `repair` with `guard: {max_visits: 3, cost_mpts: 5000}`; it receives the failing tests or the self-review findings; an edge back to `verify` | 006, 007, 009 |
| `self-review` | `agent` node `reviewer` with `output_schema: findings` and its own `guard: {max_visits: 3}`; one edge to `commit`, taken when it raises no findings, one to `repair` | 008, 009, 012, 020 |
| `commit` | One `tool` node, the only writer the flow calls | 010–013 |

The `reviewer` agent returns a list of findings. It is a stage of the run,
not a reviewer session in the sense of `AGENTS.md`, and it never asks a
person.

Rules the implementer keeps:

- **Stage order is the IR's.** `PRESET-001-01`, `PRESET-005-01` and
  `PRESET-008-01` assert the stage log, not wall-clock order; nothing in the
  flows reads the clock.
- **`max_parallel` is 0.** The loop is sequential so that `PRESET-004-01`
  can assert plan order in the request log. Parallelism is a later change
  with its own spec change (P1).
- **One writer.** `commit` is the only node that calls the git host's write
  side. The sandbox is isolated; the read side of the git host (clone, in
  `plan`) is allowed before `commit` (`REQ-PRESET-011` covers writes only).
- **The commit carries the workspace the last `verify` tested**, so what was
  tested is what is committed.
- **`budget_exceeded` is the platform's code for a guarded cycle that ends**
  (`core.CodeBudgetExceeded`); `flow` reports it with the guard reached and
  the failing tests, which the codegen flow surfaces as the run's error. The
  `self-review` guard ends the run the same way with its visits. No code is
  added to `core` for it.
- **The version** is `sha256` over the canonical IR of both flows plus the
  prompt and eval files, sorted by path: the definition the run pins
  (`REQ-PRESET-014`).
- `PRESET-015-01`: the run's first durable event is its run-started event,
  naming `flow`, `version`, `prompts` (the sorted path → sha256 of each
  prompt file) and `evals` (the sorted path → sha256 of each eval file).

## 2. The preset version lifecycle

```
propose(v2) ─► compile ─┬─ fails ─► rejected invalid_argument, no eval, no gate
                        └─ ok ─► eval suite ─┬─ any case lower or missing ─► rejected conflict
                                             └─ none lower ─► gate "version-review"
                                                                    │ person approves
                                                                    ▼
                                                              accepted, current; v1 superseded
```

- The `@eval` scenarios in `eval.feature` cover, and `versions.feature`
  scores, the suite's two cases, `plan-modules` and `iterate-minimal`. A
  version's score for a case is a recorded per-case score (0–100); a case
  itself is identified by its id in `eval.feature`. `versions.feature` tests
  version regression against those scores, where a case the suite did not run
  for the new version counts as a regression (`PRESET-017-03`).
- **Scores are records, not calls.** In the gate and in the preset's own
  binary, `eval --scores <file>`, they come from a recorded file. Nightly fills the file from
  live runs and the judge. The judge contract (`design.md` §4) is fixed; a
  prompt or judge change is re-recorded like any `@eval` change (ADR-0010).
- A proposal is a flow run of `codegen.version`: stages `compile`, `eval`,
  `review`, and the same gate discipline as above — nothing becomes current
  before a person approves. It ends as one of:

  | Outcome | Code | Scenario |
  | --- | --- | --- |
  | `compile` fails | `invalid_argument`, no `eval`, no gate | 016-01 |
  | a case lower or missing | `conflict`, no gate | 017-01, 017-03 |
  | a case not lower, no worse | gate opens | 017-02 |
  | the person rejects | `cancelled`, `v1` stays current | 018-02 |
  | the person approves | accepted, current | 018-01 |

  Rejected proposals stay recorded, so the reason stays readable later.
- Version review is a second preset flow, `codegen.version`, separate from
  the two run flows and never composed into them. It uses a gate node with
  payload schema `decision: approved | rejected` and records no accepted
  version before the gate is resolved, and it is not a stage of a run.

## 3. Simulation in the gate

All of it lives in `internal/bdd` (or `_test.go`); production code cannot
import it (`AGENTS.md`, constitution 11).

| Double | Place | Drives |
| --- | --- | --- |
| Scripted codegen model | `internal/bdd/codegen/model.go` | Answers `planner` with the brief's `plan` table; answers each `implement_module` with the scripted workspace; answers `repair` with the next scripted workspace; answers `reviewer` with the next scripted findings. Records every request with its agent, module and prompt version. |
| Simulated sandbox | same package | Owns workspaces by id. A test command returns the next scripted result and records `(workspace, command)`. |
| Simulated git host | same package | Branches, commits and repositories in memory. Records writer calls apart from reader calls; asserts "no branch", "still at its Nth commit". |
| Restart | `coretest` fake clock plus a harness that drops the run's process state | Stops a run after a named stage and resumes it against the same store (`PRESET-003-01`). |

The `@eval` fixtures are recordings: `internal/bdd/codegen/evals/<case>/*.json`.

## 4. The judge

The judge is a model call with a fixed prompt that returns JSON:
`{"score": <0-100>, "reasons": [...]}`. Per case the judge scores the item
named in the scenario. The threshold is the arithmetic mean over the runs
(nightly N = 5; the PR gate replays N = 5 recordings). Thresholds are in the
scenarios, not here; a change to a threshold is a spec change.

| Case | Fixture | Item scored |
| --- | --- | --- |
| `plan-modules` | `expenses-app` | module coverage of the plan; prompt quality |
| `iterate-minimal` | `cli-tool` | changes only the requested command; leaves unrelated files untouched |

These scores are a short quality lane over recorded inputs. They are not a
gate on generated code's correctness beyond what `verify` proves inside a run,
and this capability claims nothing more.

## Open questions

1. **System-level API coverage (`@level:system`):** `WP-L10-01` depends on `WP-L7-02` and `WP-L1-05`, not `WP-L6-03` (`docs/roadmap/L10-presets.md`), so API-driven `@level:system` scenarios are excluded from this work package (`docs/roadmap/README.md`). Decide whether a later task/WP or a roadmap dependency update should add a system-level codegen scenario once `WP-L6-03` is a dependency.
