# L10 — Presets

Ready-made flows that ship with the binary and prove the platform on itself.

### WP-L10-01 — Codegen preset
Depends: WP-L7-02, WP-L1-05.
Owns: `presets/codegen`.
Two flows over one loop (plan → implement per module → verify in a sandbox → repair under a guard → self-review → commit):
`codegen.bootstrap` turns a brief into a repository with specs and tests; `codegen.iterate` applies a change request to an existing one and commits only on a new `codegen/<run id>` branch.
The preset is its own first user: its flows, prompts and evals live in a repository that `codegen.iterate` works on. A new version is accepted only when it compiles, its `@eval` suite does not regress and a person resolves the review gate; running runs keep the version they pinned. The end state is this repository's own lifecycle (`AGENTS.md`) run as that flow up to the commit: the run does its own self-review and never waits for a person before committing. A human wait, if any, belongs after the commit or on its pull request, or in an outer orchestrator, through the platform's existing `gate` node; the codegen flows themselves use none.
