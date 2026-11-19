# Codegen preset

The codegen preset is an application built on Basamento: its own Go module and
binary, importing Basamento as a library (WP-L10-01,
`docs/roadmap/L10-presets.md`). It is not compiled into `kit` and has no entry in
`layering.yaml`.

Two flows run one loop:

```
codegen.bootstrap   brief            → plan → implement → verify → repair → self-review → commit
codegen.iterate     change request   → plan → implement → verify → repair → self-review → commit
```

`codegen.bootstrap` commits once on a new repository's default branch.
`codegen.iterate` creates `codegen/<run id>` from the base branch and commits
there; it never writes the default branch. Neither flow starts a reviewer
session or asks a person before the commit. `codegen.version` is a separate flow
that compiles a proposed version, runs its `@eval` suite and puts it in front of
a person's review gate; it is never composed into a run.

- `ir/`: the three flows as `flow.IR`, compiled to canonical IR.
- `prompts/`: the flow prompts; `evals/`: recorded `@eval` judge scores.
- `version.go` semantics live in `definition.go`: the version is the SHA-256 of
  the canonical flows plus the prompt and eval files, sorted by path.
- `registry.go`: the version lifecycle (compile, eval, review gate).
- `run.go`: the runner. The model, sandbox and git host are ports; the gate
  drives them with the scripted doubles in `internal/bdd/codegen`.

```bash
go test ./...                       # this module's unit tests
go run ./cmd/codegen version        # the current version hash
go run ./cmd/codegen flows          # each flow's canonical hash
go run ./cmd/codegen eval --scores scores.json
```

Specs: `specs/presets/`. The scenarios run in the repository's BDD gate.
