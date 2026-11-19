# ADR-0018 — Layering applies to test files and build-constrained files

**Status:** accepted · **Date:** 2026-11-19

## Context

`go tool gate layering` today runs plain `go list -json ./...` and checks only
each package's `Imports`. It never reads `TestImports`, `XTestImports`, imports
in `IgnoredGoFiles`, or `.go` files in directories `go list ./...` omits
because every file is build-excluded (including lone `//go:build ignore`
sources). ADR-0016 recorded the old limit in **Gate vs review**.

Survey on `origin/main` (`d12611d`, 2026-11-19):

| Import source | Layer violations on `main` |
| --- | ---: |
| `Imports` | 0 (`layering: ok`) |
| `TestImports` + `XTestImports` | 4 messages (harness under `internal/bdd`) |
| Extra `.go` on CI linux/amd64 | 4 ignored shims in 2 packages, 0 violations |

```bash
GOOS=linux GOARCH=amd64 go list -json ./internal/gate ./platform/sql
```

→ `internal/gate`: `netgit_other.go`, `sessionlock_other.go`; `platform/sql`:
`migrate_lock_other.go`, `sqlite_file_other.go`. `go list` `Imports`: **0**
production importers outside `internal/bdd` of paths under `internal/bdd`.

A scratch survey of **36** unmerged branches plus `main` on 2026-11-19 (same
method as above, not kept) found: **16** of the 36 have `platform/bus` tests
importing `internal/bdd/natstest`; every finding except one is a harness import
cleared by decision 2; the exception is **EP0-117's** external test
`runtime/conformance/conformance_test.go` importing `platform/secrets` without a
grant.

## Decision

1. **Test files obey the layers.** `TestImports` and `XTestImports` use the
   package under test's layer; `deny_external` on that layer applies to direct
   test imports (ADR-0016 for `internal/config`).

2. **The one exemption is `test_only`.** `test_only: [core/coretest,
   internal/bdd]`; `test_only_allowed_from: [internal/bdd]` unchanged.
   `internal/bdd` is test support (godog, steps, simulators); any `_test.go` may
   import any package under it; new helpers need no graph change. Production
   outside the harness must not import `test_only` paths — today's production
   graph has 0 such edges. **Implementer:** the existing
   `imports test-only package` error applies to **production** imports only. For
   `TestImports`, `XTestImports`, and `_test.go` files from the filesystem walk,
   a `test_only` import is allowed and **layer checks skip that dep**; allow-list
   and `deny_external` still apply to every other import in the test.

3. **Every `.go` file is read.** The gate walks the module's own code — the same
   set `go list ./...` would cover if no file were build-excluded — every `.go`
   file under the module root. Skip `testdata`, `vendor`, directories whose name
   begins with `.` or `_`, and nested modules (a directory below the root with its
   own `go.mod`). Files
   attributed by plain `go list -json ./...` to a package are checked through
   its `Imports`, `TestImports`, and `XTestImports`. Every other file — paths in
   `IgnoredGoFiles`, files in directories `go list ./...` omits because all
   sources are build-excluded, and lone `//go:build ignore` sources — is parsed
   with `go/parser.ImportsOnly` and checked with the **layer of its directory**
   (production rules, except `_test.go`, which use test rules). A directory with
   such files and no `layering.yaml` row is an error, like an unlisted package
   today. **Invariant:** the union of imports from every `.go` file is checked,
   so the result does not depend on GOOS, GOARCH, build tags, or `CGO_ENABLED`
   on the host. Plain `go list -json ./...` stays; when `go list` cannot read a
   package it fails with its own message (unchanged). `ImportsOnly` suffices
   because every import precedes every declaration; if the import section cannot
   be parsed, the gate errors naming the file.

4. **No `go list -test`** (duplicate `pkg.test` records).

5. **Output** names the kind (`in a test`, `in an excluded file …`, etc.);
   success stays `layering: ok`.

6. **Limits that stay:**
   - Nested `runtime/temporal/embedded` module — root `gate layering` does not
     apply; `embedded.yml` runs vet, licenses, bench build/replay only.
   - `testdata` not checked.
   - Unlisted vendors on layers with no `deny_external` — add prefix in the
     import change (ADR-0016).
   - **Harness transitive imports:** a test may import any `internal/bdd`
     package; harness production code is checked only against the `internal/bdd`
     row, which has no `deny_external`, so a test's transitive port vendors are
     not bounded by the test package's layer. Tests are not shipped; a closure
     rule would forbid the simulators the harness exists to provide.

## Alternatives considered

| Alternative | Why it lost |
| --- | --- |
| Keep limit / document only | External tests stay a loophole. |
| Exempt `XTestImports` only | Same loophole. |
| Per-layer `test_allow` | Two mechanisms for test support. |
| List helpers one by one in `test_only` | ADR per simulator; cannot widen production imports. |
| Grant `internal/bdd` on product layers | Production could import the harness. |
| Move helpers next to consumers | Harness refactor; revisit if support leaves `internal/bdd`. |
| GOOS/GOARCH matrix | Heavier; walk plus parse gives one result on every host. |
| `go list -test` | Duplicate package records. |

## Consequences

- Until the implementation change merges, `go tool gate layering` still checks
  production `Imports` only; `overview.md` §3, `qa-gate.md`, and
  `specs/config/design.md` §1 update in that change (docs move with code).
- On `main` after implementation: 0 production violations; decision 2 clears the
  four test messages; walk plus parse cost stays small (4 CI shims today).
- **36** unmerged branches: all findings but EP0-117's grant gap clear with
  decision 2 (including **16** with `natstest`).
- Adding `test_only` outside `internal/bdd` or changing a `layers` row needs an
  ADR and owner approval; new packages under `internal/bdd` need neither.
- `internal/gate/layering.go` follows in a separate developer change that must
  leave `main` green and does not edit this ADR.