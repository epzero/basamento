# L10 — Presets

Ready-made flows that ship with the binary and prove the platform on itself.

### WP-L10-01 — Codegen preset
Depends: WP-L7-02, WP-L1-05.
Owns: `presets/codegen`.
Two flows over one loop (plan → implement per module → verify in a sandbox → repair under a guard → review gate → commit):
`codegen.bootstrap` turns a brief into a repository with specs and tests; `codegen.iterate` applies a change request to an existing one and opens a branch.
The preset is its own first user: its flows, prompts and evals live in a repository that `codegen.iterate` works on. A new version is accepted only when it compiles, its `@eval` suite does not regress and a person resolves the review gate; running runs keep the version they pinned. The end state is this repository's own lifecycle (`AGENTS.md`) run as that flow.
