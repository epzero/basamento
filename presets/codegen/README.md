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
- `store.go`: the durable version record, `<scores>.versions.json` next to the
  scores file; a rejection survives a re-run and is not re-proposed.
- `eval.go`: the `@eval` suite. It loads a case's recorded runs and collects
  their metric values; the thresholds stay in `specs/presets/eval.feature`.
- `run.go`: the runner. The model, sandbox and git host are ports; the gate
  drives them with the scripted doubles in `internal/bdd/codegen`. A run's id
  comes from an injected source (crypto/rand by default), verify runs exactly
  the plan's `tests`, and a commit carries a stable per-run idempotency key.

```bash
go test ./...                       # this module's unit tests
go run ./cmd/codegen version        # the current version hash
go run ./cmd/codegen flows          # each flow's canonical hash
go run ./cmd/codegen eval --scores scores.json
```

The `@eval` lane replays the recordings nightly:

```bash
BDD_TAGS='@eval && ~@planned && ~@quarantine' \
CODEGEN_EVAL_DIR="$PWD/presets/codegen/evals" \
go test -count=1 ./internal/bdd/
```

`eval --scores` reads a `case -> metric -> score` file; `propose` and
`resolve` read a `version -> case -> score` file and share the durable
`<scores>.versions.json` record beside it.

Specs: `specs/presets/`. The scenarios run in the repository's BDD gate.
