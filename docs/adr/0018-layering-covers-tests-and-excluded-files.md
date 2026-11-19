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
| `TestImports` + `XTestImports` | 4 deduped messages (3 unique deps under `internal/bdd`) |
| `IgnoredGoFiles` (7 files in `internal/gate`, `platform/sql`) | 0 |

The four test messages are `platform` tests importing harness packages under
`internal/bdd` (`s3sim`, `blobmatrix`, `modroot`; seven import lines in six
files). No `deny_external` violation appears in tests on layers that define those
rules. `go list -json ./...` `Imports` show **0** production packages outside
`internal/bdd` importing anything under `internal/bdd`.

A lead survey of **35** unmerged branches plus `main` (same scratch program,
2026-11-19) found the same harness pattern on most branches; **16** branches
stacked on the bus work (WP-L1-03) add `platform/bus` tests importing
`internal/bdd/natstest`. Every layering finding in that set except one is a
harness helper import cleared by decision 2.

CI runs the gate on `ubuntu-latest` with default tags → **linux/amd64**
(`.github/workflows/gate.yml`); the same `go list` visibility applies to
`make gate` build and `go test -race ./...`.

## Decision

1. **Test files obey the layers.** Layer allow-lists and `deny_external` apply to
   `TestImports` and `XTestImports` using the layer of the package under test
   (`package foo` and `package foo_test` alike). Grants mean what they say; an
   external test can import any acyclic module path, so `deny_external` on
   `internal/config` (ADR-0016) applies to its tests too.

2. **The one exemption is `test_only`.** `layering.yaml` lists
   `test_only: [core/coretest, internal/bdd]`; `test_only_allowed_from:
   [internal/bdd]` is unchanged. **`internal/bdd` is test support by
   definition** — godog runner, step library, simulators, conformance matrices.
   Any `_test.go` file of any layer may import any package under `internal/bdd`;
   packages under `internal/bdd` may import each other and `core/coretest`.
   Production code outside the harness still may not import it: no `layers` row
   grants `internal/bdd` to product layers, and `test_only` makes a production
   import an explicit error (today `go list` `Imports`: 0 such edges). A new
   simulator or helper under `internal/bdd` needs no `layering.yaml` change. What
   this gives up: a lower layer's external test may import the step library, which
   pulls a wide graph; acceptable because tests are not shipped and an in-package
   test importing the steps is a Go import cycle anyway. No per-layer `test_allow`
   list.

3. **Build-constrained files are read.** For each package, paths in
   `IgnoredGoFiles` are parsed (`go/parser`, imports only) and checked as part
   of that package: as production except `_test.go` paths, which are checked as
   tests. A file that cannot be parsed is a gate error naming the path, never
   skipped (a skipped file would hide an import). The gate still runs one plain
   `go list -json ./...` on the CI platform, with no `-tags` sweep and no
   cross-compilation matrix.

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
| **Per-layer `test_allow` lists** | Two ways to express test support; today's cases are all harness helpers. |
| **List helpers one by one in `test_only`** | An ADR per new simulator; cannot widen what production may import, but blocks every harness addition behind graph review. |
| **Grant `internal/bdd` on `platform` and other layers** | Would let production code import the harness — strictly worse. |
| **Move helpers next to the packages they support** | Cleaner in principle, but a harness refactor unrelated to the rule; revisit if test support leaves `internal/bdd`. |
| **GOOS/GOARCH matrix instead of parsing `IgnoredGoFiles`** | Heavier CI; ignored shims on `main` already carry only stdlib/`core` imports. |
| **`go list -test`** | Duplicate package records (`pkg [pkg.test]`, `pkg.test`). |

## Consequences

- **On `main` after implementation:** 0 production violations; with
  `internal/bdd` in `test_only`, the four current test messages on `main` clear
  and `layering: ok` stays true. Reading ignored files adds a handful of parses
  (7 files, 2 packages today); cost is negligible next to `go list ./...`.
- **Branches in flight:** of the **35** unmerged branches surveyed, every
  layering finding except one is a harness import and clears with decision 2
  (including **16** branches whose `platform/bus` tests use `natstest`). The
  sole real break is **EP0-117** (WP-L5-04): external test
  `runtime/conformance/conformance_test.go` imports `platform/secrets` while that
  branch's layer row is `runtime/conformance: [core, runtime, flow]`; its author
  must fix the test or seek a grant in an ADR.
- **Still not checked:** nested `embedded` module graph (item 6), `testdata`,
  unlisted vendor prefixes on layers without `deny_external`, and code outside the
  module.
- **Graph changes:** adding a package to `test_only` outside `internal/bdd`, or
  changing a `layers` row, remains an ADR plus the owner's approval on high-risk
  rule files (`agent-workflow.md` §5). A new package **inside** `internal/bdd`
  needs neither.
- The gate implementation in `internal/gate/layering.go` follows as a separate
  developer change that must leave `main` green and does not edit this ADR. This
  pull request records the decision and sets `test_only` so `go tool gate layering`
  on current production imports stays `layering: ok`.