# BDD convention

How behavior is specified, approved and executed. Normative for every agent and
person working in this repository. Checked by `go tool gate spec lint`
and `spec lock`.

## 1. Principles

1. **Specs are the contract between people and agents.** People decide *what*
   (and review it by priority); agents decide *how*.
2. **Every acceptance criterion is executable**: a Gherkin scenario, or a
   tagged check in a non-deterministic lane (`@eval`, `@journey`, `@live`).
3. **Traceable both ways**: requirement ↔ scenario ↔ step ↔ code ↔ commit,
   checked by machine.
4. **Approved text is locked.** Scenario text is hashed at approval; a change
   needs a new review (§6).
5. **The gate is deterministic.** Scripted models, a fake clock, vendor
   simulators. No AI judges the gate.
6. **One scenario, many targets.** Tags select runtime, profile and level, so
   the same scenario proves every implementation.

## 2. Layout

```
specs/
  constitution.md   glossary.md
  <capability>/
    requirements.md       # ### REQ-<CAP>-<NNN> — <title>, then one EARS sentence
    <topic>.feature       # scenarios
    design.md             # optional: interfaces/decisions the implementer must follow
    spec.lock.yaml        # approval ledger of this capability
internal/bdd/
  bdd_test.go             # the godog suite (strict mode)
  steps/<domain>.go       # step library, one file per domain
```

## 3. Requirements (EARS)

One requirement per heading, one behavior per requirement, in one of the EARS
patterns:

| Pattern | Template |
| --- | --- |
| Ubiquitous | The `<system>` shall `<response>`. |
| Event | When `<trigger>`, the `<system>` shall `<response>`. |
| State | While `<state>`, the `<system>` shall `<response>`. |
| Unwanted | If `<condition>`, then the `<system>` shall `<response>`. |
| Optional | Where `<feature>`, the `<system>` shall `<response>`. |

Ids are `REQ-<CAP>-<NNN>`, never reused. A retired requirement keeps its
heading, followed by a line `Status: Retired`, and no scenarios point to it
(lint).

## 4. Scenarios

### 4.1 Writing rules

- **Declarative**: describe behavior and observable outcomes, never clicks,
  selectors, SQL or internal calls. *"When ana detaches"*, not *"When the
  websocket is closed"*.
- **One behavior** per scenario; at most **12 steps** (lint), aim for ≤ 8.
- **Glossary language**: terms come from `specs/glossary.md`; add new terms in
  the same change.
- **Concrete Then-steps**: exact values, counts or states. Never "works",
  "correctly" or "as expected".
- **Explicit time and chance**: "Given the clock is frozen at …", "30 minutes
  pass", seeded randomness. Nothing depends on real time.
- **Data** in tables or doc strings; use Scenario Outline for parameter sets.
- **Background** only for setup shared by every scenario of the file.

### 4.2 Required tags

Every scenario carries, on the scenario itself:

- `@id:<CAP>-<NNN>-<NN>`: stable, unique, never reused. `<NNN>` is the primary
  requirement number: the scenario also carries `@REQ-<CAP>-<NNN>` (lint).

And, on the scenario or inherited from the feature or rule:

- at least one `@REQ-<CAP>-<NNN>` that exists in a `requirements.md`.

### 4.3 Tag vocabulary

Only these tags are allowed (lint rejects others):

| Tag | Meaning |
| --- | --- |
| `@planned` + `@wp:<WP-…>` | Approved, not implemented; excluded from the gate run. The WP must exist. |
| `@wp:<WP-…>` | Work package that implements (or implemented) the scenario. |
| `@level:component`, `@level:system` | Driver level: in-process packages, or the real API of `kit serve --profile embedded`. A scenario may carry both. A work package proves the level it can reach: `@level:system` only where it depends on `kit serve` (`docs/roadmap/README.md`). |
| `@runtime:any`, `@runtime:local`, `@runtime:temporal` | Runtime the scenario targets; `any` runs on each available runtime. |
| `@profile:embedded`, `@profile:cloud` | Profile the scenario targets. |
| `@security` `@cost` `@contract` | Critical areas; force tier P0. |
| `@smoke` | Part of the binary smoke run (gate G6). |
| `@slow` | Excluded from the PR gate; nightly. |
| `@live` | Needs real vendor credentials; nightly only. |
| `@eval` | Statistical LLM-quality check with a threshold; the nightly eval lane replays the recordings in `presets/codegen/evals` (`CODEGEN_EVAL_DIR`). The PR gate excludes it. |
| `@journey` | A UI scenario, executed by the browser runner. |
| `@quarantine` + `@issue:<key>` + `@until:<YYYY-MM-DD>` | Temporarily excluded; expires (lint fails after the date). |

Banned on main: `@wip`, `@skip`, `@ignore`, `@focus`, `@only`.

## 5. Execution

[godog](https://github.com/cucumber/godog) runs the features through `go test`
(`internal/bdd/bdd_test.go`) in **strict mode**: undefined or pending steps
fail. Default exclusions: `@planned @live @eval @journey @slow @quarantine`.

**Step definitions are code written once per phrase**, not generated per
scenario. They stay thin: parse the phrase, call a **driver**, assert. A
driver hides the level:

- the **component driver** calls packages in-process (fast, runs across the
  runtime matrix);
- the **system driver** calls the public API of the binary.

The same scenario runs at either level by tag. godog prints a skeleton for
each undefined step; implementers fill it in.

**Shared step texts live in `internal/bdd/steps/shared.go`.** A capability that
needs a new generic text adds it there, plus a source for its world, and never
registers a shared text itself; `World` fields and the `register*` calls in
`Initialize` stay sorted alphabetically.

The step library is registered **once per godog suite** (`TestSuiteInitializer`
in `internal/bdd/bdd_test.go`), not before every scenario. Per-scenario state
belongs in `World`, which the `Before` hook in `steps.Initialize` creates for
each scenario. A `register*` function must not declare variables for its steps
to share across scenarios; `register_state_test.go` enforces that rule on the
package's register functions.

**No AI in the gate.** AI takes part only in labelled lanes:

| Lane | Where AI acts | Determinism in the PR gate |
| --- | --- | --- |
| `@eval` | An LLM judge scores outputs over N runs against a threshold; the nightly eval lane replays the recorded case scores. | Re-recording is a reviewed change; `CODEGEN_EVAL_DIR` names the recordings. |
| `@journey` | The browser runner resolves a declarative step into UI actions **once**, then records them (with rrweb). | The gate replays recorded actions. When the UI changes, a nightly job re-resolves and opens a change, reviewed like a spec change, so a real bug is never "healed" into a pass. |

The `@eval` lane runs with `BDD_TAGS='@eval && ~@planned && ~@quarantine'` and
`CODEGEN_EVAL_DIR=<repo>/presets/codegen/evals`. The scenarios read their
thresholds from `eval.feature`; the suite only collects the recorded values.

## 6. Approval and change control

See [`spec-review.md`](spec-review.md). In short: a reviewer agent that did
not author the spec approves it and assigns a tier; `specs/<capability>/spec.lock.yaml`
stores the hash of the approved text; the gate fails while any scenario is unapproved or
changed since approval; people review asynchronously by tier, and P0
scenarios block a release until a person accepts them.

The scenario hash covers the name, all steps (including Background and
expanded Examples), doc strings and tables, normalized for whitespace. It
excludes tags and step keywords, so adding `@slow` or removing `@planned` does
not need re-approval, while any change in wording does.

## 7. Red, then green

A scenario becomes **active** when it is added without `@planned` or when
`@planned` is removed. CI runs `gate redgreen -base <base>`: each newly active
scenario must **fail** against the base code (with the new specs copied in)
and **pass** at head. A scenario that already passes on the base does not test
the change, and the gate rejects it.

## 8. Example

```gherkin
@planned @wp:WP-L6-02 @runtime:any @level:system
Feature: Detach from and reattach to a running chat

  @REQ-STRM-003 @id:STRM-003-01
  Scenario: Reattaching mid-message sends a snapshot, then the rest
    Given user "ana" starts a run of "assistant" and receives 2 deltas
    When ana detaches
    And the scripted model streams 2 more deltas
    And ana reattaches with her last cursor
    And the scripted model finishes
    Then ana receives a snapshot covering the first 4 deltas
    And ana then receives deltas 5 to 6 with no gap and no duplicate
```
