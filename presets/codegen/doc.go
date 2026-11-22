// Package codegen is the codegen preset: two flows, codegen.bootstrap and
// codegen.iterate, over one loop that mirrors the developer's own workflow
// (AGENTS.md): plan, implement each planned module, verify in a sandbox,
// repair under a guard, self-review, and commit. No person decides before the
// commit and no reviewer session is started; the self-review stage is a stage
// of the run, not a reviewer session.
//
// The preset is an application built on Basamento, not part of the foundation:
// package codegen lives in its own Go module under presets/codegen and imports
// Basamento as a library. It is not compiled into kit and has no entry in
// layering.yaml (docs/roadmap/L10-presets.md, specs/presets/design.md §0).
//
// The flows are IR (package ir) compiled to the same canonical IR as any other
// flow (ADR-0003). A run pins the version it started with; the version is the
// content hash of the flows, prompts and evals (definition.go). A new version
// is accepted only when it compiles, its @eval suite does not regress and a
// person resolves its review gate (registry.go).
//
// The Runner implements the loop itself: its stage order, routing and guards
// live in run.go and stage.go. The flows in package ir are the declarative
// mirror of that loop, and Compile cross-checks their order, edges, guards and
// sequential max_parallel against the runner. The model, the sandbox and the
// git host are injected ports so that a run is deterministic in the gate: the
// BDD harness in internal/bdd/codegen supplies a scripted model, a simulated
// sandbox and a simulated git host (ADR-0011, specs/presets/design.md §3).
//
// The @eval suite scores recorded cases: EvalSuite collects a case's metric
// values and the scenario's thresholds decide pass/fail (REQ-PRESET-019).
package codegen
