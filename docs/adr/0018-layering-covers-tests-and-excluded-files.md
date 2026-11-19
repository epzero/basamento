# ADR-0018 — Layering applies to test files and build-constrained files

**Status:** accepted · **Date:** 2026-11-19

## Context

`go tool gate layering` (`internal/gate/layering.go`) loads `layering.yaml` and
runs `go list -json ./...` once. It has checked only each package's `Imports`
(production `.go` files that match the gate host's build context). It never read
`TestImports` (in-package `_test.go`), `XTestImports` (`package foo_test`), or
imports in files `go list` excludes via build constraints (`IgnoredGoFiles`:
other GOOS/GOARCH, custom tags). ADR-0016 documented the gap ("`_test.go` is out
of scope (as `test_only`)"; other-platform files "reviewed by hand").

A survey on `origin/main` (2026-11-19, `d12611d`) replayed the rule logic against
`TestImports`, `XTestImports`, and parsed `IgnoredGoFiles`:

| Import source | Layer violations on `main` |
| --- | ---: |
| `Imports` | 0 (`go tool gate layering` → `layering: ok`) |
| `TestImports` + `XTestImports` | 4 deduped messages (3 unique deps) |
| `IgnoredGoFiles` (7 files in `internal/gate`, `platform/sql`) | 0 |

The four test messages are `platform` tests importing BDD helpers
`internal/bdd/s3sim`, `internal/bdd/blobmatrix`, and `internal/bdd/modroot`
(seven import lines in six files). No `deny_external` violation appears in
tests on layers that define those rules. Production code outside `internal/bdd`
imports none of the three helpers today (`rg` on non-`_test.go` paths).

CI runs the gate on `ubuntu-latest` with default tags → **linux/amd64**
(`.github/workflows/gate.yml`); the same `go list` visibility applies to
`make gate` build and `go test -race ./...`.

## Decision

1. **Test files obey the layers.** Layer allow-lists and `deny_external` apply to
   `TestImports` and `XTestImports` using the layer of the package under test
   (`package foo` and `package foo_test` alike). Grants mean what they say; an
   external test can import any acyclic module path, so `deny_external` on
   `internal/config` (ADR-0016) applies to its tests too.

2. **The one exemption is `test_only`.** Packages listed in `test_only` (fakes,
   simulators, harness helpers) may be imported from any `_test.go` file of any
   layer and from packages in `test_only_allowed_from` (`internal/bdd`), and
   from nowhere else. Production imports outside those callers stay forbidden.
   `layering.yaml` lists
   `[core/coretest, internal/bdd/s3sim, internal/bdd/blobmatrix,
   internal/bdd/modroot]`. No second mechanism (no per-layer `test_allow`).

3. **Build-constrained files are read.** For each package, paths in
   `IgnoredGoFiles` are parsed (`go/parser`, imports only) and checked as part
   of that package: as production except `_test.go` paths, which are checked as
   tests. The gate still runs one plain `go list -json ./...` on the CI
   platform, with no `-tags` sweep and no cross-compilation matrix.

4. **No `go list -test`.** The plain listing already exposes `TestImports`,
   `XTestImports`, and `IgnoredGoFiles` without synthetic `pkg.test` duplicates.

5. **Output.** A failure names the kind, for example
   `platform/blob (layer platform, in a test) may not import internal/bdd/s3sim
   (layer internal/bdd)` or
   `platform/sql (layer platform, in an excluded file migrate_lock_other.go) …`.
   The success line stays `layering: ok`.

6. **Limits that stay:**
   - **Nested modules:** `runtime/temporal/embedded` has its own `go.mod`. The
     root gate does not build it. `.github/workflows/embedded.yml` runs only
     when that tree changes (plus nightly): `go vet`, `go-licenses`, `go build`
     of `bench`, and a crash-replay bench. It does **not** run root `gate layering`.
   - **`testdata`:** not module code; imports there are not checked.
   - **Vendor libraries on a layer with no `deny_external`:** unchanged (add the
     prefix in the change that brings the library, ADR-0016).

## Alternatives considered

| Alternative | Why it lost |
| --- | --- |
| **Keep the limit and document it** | Leaves external tests as a loophole (no import cycle blocks them). |
| **Exempt `XTestImports` only** | Same loophole for `package foo_test`. |
| **Per-layer `test_allow` lists** | Two ways to express test support; today's four cases are all harness helpers. |
| **Move `s3sim`, `blobmatrix`, `modroot` into the packages they support** | Cleaner graph, but a harness refactor unrelated to the rule; revisit if `test_only` grows past a handful. |
| **GOOS/GOARCH matrix instead of parsing `IgnoredGoFiles`** | Heavier CI; ignored shims on `main` already carry only stdlib/`core` imports. |
| **`go list -test`** | Duplicate package records (`pkg [pkg.test]`, `pkg.test`). |

## Consequences

- **On `main` after implementation:** 0 production violations; with the three BDD
  helpers in `test_only`, the four current test messages clear and `layering: ok`
  stays true. Reading ignored files adds a handful of parses (7 files, 2 packages
  today); cost is negligible next to `go list ./...`.
- **Branches in flight (lead survey, 2026-11-19):** most open worktrees share the
  same three helpers; once the rule ships they need only merge `main` with this
  ADR's `layering.yaml`. Branches that add new cross-layer test imports of
  `internal/bdd/*` helpers (for example `internal/bdd/natstest` from
  `platform/bus` tests on EP0-112+) must add that package to `test_only` in a
  graph change with an ADR, or stop importing it from tests outside `internal/bdd`.
  EP0-117's external test importing `platform` from `runtime/conformance` is a
  real layer break, not a `test_only` case — those authors must fix layering or
  add grants before the gate turns green.
- **Still not checked:** nested `embedded` module graph (item 6), `testdata`,
  unlisted vendor prefixes on layers without `deny_external`, and code outside the
  module.
- **Changing `test_only` or layers** remains an ADR plus owner approval on
  high-risk rule files (`agent-workflow.md` §5).
- **Implementation:** a follow-up developer change updates `internal/gate/layering.go`
  and must leave `main` green without editing this ADR's text. This pull request
  only records the decision and pre-lists `test_only` so `go tool gate layering`
  on current code stays `layering: ok`.

## Implementation changes these files

| Path | Change |
| --- | --- |
| `layering.yaml` | `test_only` list and comments; header describes full check scope. |
| `docs/adr/README.md` | Row for ADR-0018. |
| `docs/adr/0016-internal-config-layer.md` | Dated status note; superseded gate-scope sentences. |
| `docs/architecture/overview.md` §3 | Wording on what the gate reads (not the generated diagram). |
| `docs/engineering/qa-gate.md` | G1 / layering scope. |
| `specs/config/design.md` §1 | Align gate-scope wording with ADR-0018. |
| `internal/gate/layering.go` | **Follow-up ticket:** enforce test and ignored-file imports. |