# Presets: design of the codegen preset

Interfaces and decisions the implementer must follow, for WP-L10-01
(`presets/codegen`). Requirements: `requirements.md`; scenarios: `loop.feature`,
`iterate.feature`, `versions.feature`, `served.feature`, `eval.feature`.
Decisions: ADR-0003 (flows as data), ADR-0011 (host and node handlers),
ADR-0010 (eval lane). This file adds no behavior: where it and the
requirements could disagree, the requirements win and this file is fixed first.

## 0. Changes outside `presets/codegen`, landed first

This work package owns `presets/codegen` only. The implementation lands each
of these in a pull request of its own, in this order, before the rest:

1. **Layer.** `presets/codegen` has no layer in `layering.yaml`. Add
   `presets/codegen: [core, flow, runtime, agent, model, tool, platform]`
   (no API, no `internal/gate`) with an ADR, as `AGENTS.md` requires for a
   change to the graph. The presets register handlers with a flow host and
   call models, sandbox tools and the repository; the flows themselves are
   IR and import nothing.
2. **Analysis bundle** (`REQ-PRESET-015`, `REQ-PRESET-016`). The version's
   "analysis bundle" is the thing the case `plan-modules` evaluates: the
   prompts and the state schema of the codegen flows, checked by the flow
   compiler at load. **The implementation must not name it a "compiler
   bundle" in code or docs.** The term collides with `presets/codegen-compiler`
   (the generated subsystem a `codegen.bootstrap` run produces, modeled on a
   compiler's phases), which may grow into its own work package; a name that
   reads as "the compiler's bundle" would later invite an over-generic
   extraction of data types from the bundle into that subsystem. If the
   bundle needs a better name (for example *preset manifest* or *prompt
   pack*), propose it only by an additive alias in the first pull request, so
   recorded versions keep their hashes.
3. **Sandbox commands** (`REQ-PRESET-005`). A plan's tests are a list of
   sandbox calls. Each `tool` node passes its command as literal `spec.args`,
   never built by a CEL expression: `expr` is deterministic and cost-limited,
   but it is not an expression language for composing untrusted shell text.
   If the tool node cannot take literal arguments yet, that is a change in
   `tool` (WP-L7-02's area) with a reviewer from that area.
4. **Resume** (`REQ-PRESET-003`). The codegen flow is long and model-heavy,
   so its runs must take the decision routes whose steps carry `dontcache`
   metadata, if that option is not yet set. Generic support belongs to the
   runtimes (WP-L5-*), but **do not land it first**: only the route a codegen
   run actually takes needs setting, in the same pull request that proves
   `PRESET-003-01`. Landing it globally first would move this work package's
   red→green checks, so the test stays uncached and red at base.

## 1. The flows

Both flows are IR in `presets/codegen/ir/` and are compiled to the same
canonical IR as any other flow (ADR-0003). They share the loop; they differ in
the first stage and in the commit.

```
codegen.bootstrap                      codegen.iterate
  brief ─► plan ─► implement(plan)       repository, base branch, change request
          ─► verify(sandbox)             ─► plan ─► implement(plan)
          ─► repair(guard) ─► verify     ─► verify(sandbox)
          ─► gate "review"               ─► repair(guard) ─► verify
          ─► commit                      ─► gate "review"
                                         ─► commit on codegen/<run id>
```

| Stage | Nodes | Requirement |
| --- | --- | --- |
| `plan` | One `agent` node, `planner` agent, structured output `plan` | 001 |
| `implement` | `map` over `plan.modules`, `max_parallel` 0 (one at a time), node `implement_module` (`agent` with `output_schema: workspace`) | 004 |
| `verify` | One `tool` node per plan test, in plan order, on the workspace the last stage returned | 005 |
| `repair` | `agent` node `repair` with `guard: {max_visits: 3, cost_mpts: 5000}`; an edge back to `verify` | 006, 007 |
| `review` | `gate` node with payload schema `decision: approved | rejected` | 008–012 |
| `commit` | One `tool` node, the only writer the flow calls | 009–013 |

Rules the implementer keeps:

- **Stage order is the IR's.** `PRESET-001-01`, `PRESET-005-01` and
  `PRESET-008-01` assert the stage log, not wall-clock order; nothing in the
  flows reads the clock.
- **`max_parallel` is 0.** The loop is sequential so that `PRESET-004-01`
  can assert plan order in the request log. Parallelism is a later change
  with its own spec change (P1).
- **One writer.** `commit` is the only node that calls the git host's write
  side. The sandbox is isolated; the read side of the git host (clone, in
  `plan`) is allowed before `review` (`REQ-PRESET-009` covers writes only).
- **The commit carries the reviewed workspace**, the same value `verify`
  tested, so what a person approved is what is committed.
- **`budget_exceeded` is the platform's code for a guarded cycle that ends**
  (`core.CodeBudgetExceeded`); `flow` reports it with the guard reached and
  the failing tests, which the codegen flow surfaces as the run's error. No
  code is added to `core` for it.
- **The version** is `sha256` over the canonical IR of both flows plus the
  analysis bundle's files, sorted by path: the definition the run pins
  (`REQ-PRESET-014`).
- `PRESET-015-01`: the run's first durable event is its run-started event,
  naming `flow`, `version`, `prompts` (the sorted path → sha256 of each
  prompt file) and `bundle` (`bundle-<hash[0:8]>` of the analysis bundle).

## 2. The preset version lifecycle

```
propose(v2) ─► compile ─┬─ fails ─► rejected invalid_argument, no eval, no gate
                        └─ ok ─► eval suite ─┬─ any case lower or missing ─► rejected conflict
                                             └─ none lower ─► gate "version-review"
                                                                    │ person approves
                                                                    ▼
                                                              accepted, current; v1 superseded
```

- The eval suite's cases are `plan-modules`, `verify-fixes` and
  `iterate-minimal` (`versions.feature` Background). A version's score per
  case is a whole number 0–100; a case with no score counts as a regression
  (`PRESET-017-03`).
- **Scores are records, not calls.** In the gate and in the flag `kit eval
  --scores <file>` they come from a recorded file. Nightly fills the file from
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
  the two run flows and never composed into them. It uses the same gate
  payload schema (`approved | rejected`) and the same no-write-before-the-gate
  discipline, and it is not a stage of a run.

## 3. Simulation in the gate

All of it lives in `internal/bdd` (or `_test.go`); production code cannot
import it (`AGENTS.md`, constitution 11).

| Double | Place | Drives |
| --- | --- | --- |
| Scripted codegen model | `internal/bdd/codegen/model.go` | Answers `planner` with the brief's `plan` table; answers each `implement_module` with the scripted workspace; answers `repair` with the next scripted workspace. Records every request with its agent, module and prompt version. |
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

| Case | Fixture | Item scored | Threshold (mean) |
| --- | --- | --- | --- |
| `plan-modules` | `expenses-app` | module coverage of the plan; prompt quality | ≥ 80; ≥ 75 |
| `verify-fixes` | `expenses-app` | does one repair visit fix the scripted failure | ≥ 75 (recorded; no live scenario yet) |
| `iterate-minimal` | `cli-tool` | changes only the requested command; leaves unrelated files untouched | ≥ 80; ≥ 90 |

These scores are a short quality lane over recorded inputs. They are not a
gate on generated code's correctness beyond what `verify` proves inside a run,
and this capability claims nothing more.
