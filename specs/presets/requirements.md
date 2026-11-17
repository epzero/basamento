# Presets: code generation as flows

Status: **Planned** (WP-L10-01). Roadmap: `docs/roadmap/L10-presets.md`. Design: ADR-0003, ADR-0011, `design.md`.

The code generator is a preset: its own Go module and binary, importing
Basamento as a library, not compiled into `kit` (owner decision, 2026-11-17).
It defines two flows, `codegen.bootstrap` and
`codegen.iterate`, over one loop — plan, implement each planned module, verify
in a sandbox, repair under a guard, self-review, commit (owner decision,
2026-11-17). The loop mirrors the developer's own workflow (`AGENTS.md`): it
starts no reviewer session, and no person decides before the commit. A
**preset version**
is the content hash of the flows, prompts and evals, with its recorded `@eval`
scores. A **run** of either flow pins the version it started with.

The gate drives the preset with a scripted model and a simulated sandbox and
git host, so it proves the loop's order, its bounds, its checkpoints and its
refusals. What the model generates is a quality question, answered by the
`@eval` suite and never by a scenario.

### REQ-PRESET-001 — The loop's order
When a run starts, the codegen flow shall start its `plan` stage before any other stage, and start `implement` only after `plan` has completed.

### REQ-PRESET-002 — A stage is checkpointed when it completes
When a codegen stage completes, the codegen flow shall record its outcome as a stage checkpoint of the run.

### REQ-PRESET-003 — A resumed run keeps its completed stages
When a codegen run resumes after a restart, the codegen flow shall resume from the last stage checkpoint, without starting again any stage that has a checkpoint or repeating its sandbox calls.

### REQ-PRESET-004 — One implement step per planned module
The `implement` stage shall run exactly one implement step for each module of the plan, in plan order.

### REQ-PRESET-005 — Verification follows implementation
When every implement step of the `implement` stage has completed, the codegen flow shall run the plan's tests in the run's sandbox in the next stage.

### REQ-PRESET-006 — Failed verification leads to repair
If the plan's tests fail in the sandbox, then the codegen flow shall run the repair stage, and run it again after each further failing verification.

### REQ-PRESET-007 — The repair guard bounds the loop
If the repair stage reaches its visit limit or its cost limit, then the codegen flow shall end the run in the `failed` state with code `budget_exceeded`, naming the limit reached and the last test failures.

### REQ-PRESET-008 — Passing verification goes to the author's self-review
When the plan's tests pass in the `verify` stage, the codegen flow shall run the `self-review` stage, in which no person decides and no reviewer session is started.

### REQ-PRESET-009 — Self-review findings go back to repair or end the run
If the `self-review` stage raises findings, then the codegen flow shall run the repair stage with those findings, and if it raises none, then the codegen flow shall commit. A `self-review` stage that has raised findings and has reached its visit limit shall end the run in the `failed` state with code `budget_exceeded`.

### REQ-PRESET-010 — A bootstrap run commits once
When the plan's tests pass and the `self-review` stage raises no findings, the codegen flow shall commit the workspace of the last `verify` stage once, as the only commit on a new repository's default branch for a bootstrap run.

### REQ-PRESET-011 — Only the commit writes
While a run has not reached its `commit` stage, the codegen flow shall make no write to the repository and no commit on any branch.

### REQ-PRESET-012 — An iteration commits on a new branch
When an iterate run's `self-review` stage raises no findings, the codegen flow shall open a new branch `codegen/<run id>` from the base branch and make its only commit there.

### REQ-PRESET-013 — Iteration never writes the default branch
The `codegen.iterate` flow shall never commit to the repository's default branch.

### REQ-PRESET-014 — The pin survives a version change
While a version of a codegen flow is superseded by a new one, runs of it shall keep the version they started with.

### REQ-PRESET-015 — The pin is visible
The codegen flow shall expose the flow, prompts and evals of a run, and their version, with that run's events.

### REQ-PRESET-016 — New versions must compile
If a new version of the preset does not compile, then the system shall record the version as rejected with code `invalid_argument`.

### REQ-PRESET-017 — New versions must not regress
If a new version of the preset scores lower than its predecessor on any case of the `@eval` suite, or has no recorded score for a case its predecessor scored, then the system shall record the version as rejected with code `conflict` naming that case.

### REQ-PRESET-018 — A person decides on a new version
Where a new version of the preset compiles and does not regress its predecessor on any `@eval` case, when a person resolves the version's review gate with a rejected decision, the system shall record the version as rejected with code `cancelled`, and with an approved decision as accepted.

### REQ-PRESET-019 — Generated code quality is measured, not asserted
The preset's `@eval` suite shall score the plans, prompts and generated code on recorded fixtures against the thresholds in `eval.feature`.
