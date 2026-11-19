# ADR-0014 — Pinned Go toolchain

**Status:** accepted · **Date:** 2026-11-18

## Context

The root `go.mod` and `runtime/temporal/embedded/go.mod` each name
`go 1.26.10` and neither has a `toolchain` line. There is no `go.work`. CI
installs Go with `actions/setup-go@v5` and `go-version: stable`. `govulncheck`
reports **GO-2026-6859** in the standard-library `net/http`; the advisory is
fixed in **Go 1.26.11**. Nothing under `./...` depends on `net/http`
(`go list -deps ./...`), so the current tree passes `govulncheck`, but any
dependency path that reaches `net/http` fails until the repository's minimum
patch is at least **1.26.11**.

## Decision

### Go toolchain

| Rule | Detail |
| --- | --- |
| **Minimum patch** | The `go` directive in every module in the repository is the same minimum patch — **`1.26.11`**. |
| **Modules** | Root `go.mod` and `runtime/temporal/embedded/go.mod`. |
| **No `toolchain` line** | Only the `go` directive in each module file. |
| **CI toolchain env** | Every job in `.github/workflows/gate.yml`, `nightly.yml` and `embedded.yml` sets job `env` **`GOTOOLCHAIN=local`**. The Go installed by `setup-go` is the toolchain the build uses — including the nightly **line** job, which runs exactly the release `check-latest` installed. If that toolchain is older than the `go` minimum in `go.mod`, the job fails instead of downloading another toolchain. |
| **CI install (pinned jobs)** | Every job that builds or tests except the nightly line job uses **`go-version-file: go.mod`** on the root module file, not `stable`. |
| **CI install (line job)** | The nightly **line** job alone uses **`go-version`** set to the release line named by `go.mod` (for example `1.26`), **`check-latest: true`**, and **does not** use `go-version-file`. |
| **Nightly line job behavior** | After `make tools`, runs `govulncheck ./...`. The workflow fails on any finding or on a Go install failure — no `continue-on-error` and no suppressed exit status. |
| **Local** | **`make gate`** exports **`GOTOOLCHAIN=go<patch>`** from the root `go.mod` when the environment has not set it, so local runs match CI's pinned patch (`README.md`). |
| **Patch bumps** | A security patch is a coordinated bump of every `go.mod` `go` line, the `README.md` minimum-Go line, and the embedded Go minimums in `docs/architecture/runtime.md` and `runtime/temporal/embedded/BENCHMARKS.md` (requirements text only; leave recorded benchmark environments alone). CI reads the version from `go.mod`, so the bump touches no workflow file; it is the case **`Review tier of a bump`** treats as **medium**. |
| **Review tier of a bump** | A change to a `go.mod` file whose only difference from the base is the **patch number** of the `go` directive (`go 1.26.N` → `go 1.26.M` with `M > N`, same release line; no `toolchain` line; no other line added, removed or changed) is **medium** tier. Every other change to a `go.mod` file stays **high** (dependency, minor-line move, downgrade, `toolchain` line, any other directive). The gate evaluates the rule on the base-to-head diff of each `go.mod` file, reading **`review.yaml` from the base branch** like every other tier rule, so a change cannot weaken its own review. A change that also touches any high-tier path is **high** as a whole. The test is **syntactic**: the gate does not check that the version exists (a missing version fails the build under `GOTOOLCHAIN=local`). |
| **Same `go` line** | **`make gate`** fails if any tracked `go.mod` (there is no `go.work`) names a different `go` directive than the others, comparing lines parsed by the same code as the bump rule. |

#### Alternatives considered (toolchain)

| Alternative | Why rejected |
| --- | --- |
| Keep `stable` in CI and a loose `go` line | CI and `go.mod` drift; `stable` can leap ahead of the named line or lag behind a needed stdlib fix. |
| Add a `toolchain` directive next to `go` | Two pins to reason about; `go` alone with `GOTOOLCHAIN=auto` locally and `GOTOOLCHAIN=local` in CI already binds builds to one patch. |
| Pin Go through a container image or vendoring the toolchain | Extra moving parts; fights the pure-Go single-binary setup without reproducibility beyond what `go.mod` gives. |
| Move to the next Go release line now | A line jump belongs in its own decision when dependencies and stdlib require it. |
| Float every CI job on `check-latest` | Only the line job may float; pinned jobs must match the patch in `go.mod`. |
| Keep every `go.mod` change high | The owner would see every move; rejected by the owner — a security patch would wait for the owner on every release while the checks above already bound a patch bump. |
| Make `go.mod` medium by path | A new dependency or a minor-line move must stay high, and a path glob cannot tell them apart. |

## Consequences

**Cost.** The nightly Go-line job may fail when a new stdlib advisory appears
before `go.mod` is bumped — that failure is signal, not noise.

**Pins and approval.** A minor-line move, a dependency, or any other
`go.mod` change is **high** tier and needs the owner's approval before merge. A
patch-only bump of the `go` directive is **medium** tier: the reviewer's
approval alone, bounded by this ADR's checks (same `go` line in every module,
`GOTOOLCHAIN=local` and `go-version-file` in CI, `govulncheck` on every gate
run and nightly on the newest patch on the release line).

**Revert.** Roll back `go 1.26.11`, `go-version-file`, `GOTOOLCHAIN=local` and
the nightly line job together.

**Implementation changes these files:**

| Path | Change |
| --- | --- |
| `go.mod` | `go 1.26.11`. |
| `runtime/temporal/embedded/go.mod` | Same `go` line as the root module. |
| `README.md` | Minimum Go version matches the root `go` directive. |
| `docs/architecture/runtime.md` | Embedded profile Go minimum matches the root `go` directive. |
| `runtime/temporal/embedded/BENCHMARKS.md` | Requirements Go minimum matches the root `go` directive (not the recorded measurement environment). |
| `.github/workflows/gate.yml` | Every job: `GOTOOLCHAIN=local`. `go-version-file: go.mod`. |
| `.github/workflows/nightly.yml` | Every job: `GOTOOLCHAIN=local`. **flake-hunt**, **slow-lanes** and **release-readiness**: `go-version-file: go.mod`. New **line** job: `go-version` on the release line (for example `1.26`) with `check-latest: true`, `govulncheck ./...`, hard fail. |
| `.github/workflows/embedded.yml` | Every job: `GOTOOLCHAIN=local`; `go-version-file: go.mod`. |
| `review.yaml` | Rule for a patch-only `go` bump; regenerate `.github/CODEOWNERS` with `go tool gate docs -write` when generator output changes. |
| `internal/gate/review.go` and its test | Evaluate the rule on the base-to-head diff of each `go.mod` file so `go tool gate review plan` reports **medium** for a patch-only bump and **high** for everything else; table-driven cases: patch-only bump in both modules, patch bump plus a dependency change, minor-line move, downgrade, added `toolchain` line, comment-only or whitespace-only change, new `go.mod` (including in a new directory), deleted or renamed `go.mod`, base with no `go.mod` at that path, base or head `go` line not `go 1.M.P` (for example `go 1.26` without a patch or a release-candidate form), `go 1.M.P` with a trailing comment. |
| `internal/gate` | Static check that every `go.mod` names the same `go` line, with test. |
| `docs/engineering/code-review.md` | Section 2 (**Plan the review**): tiers from `review.yaml` paths plus this content rule; the high-tier row's `go.mod` entry except a patch-only `go` bump. |
| `docs/engineering/agent-workflow.md` | Section 5: the high-risk row names `go.mod` except a patch-only `go` bump (medium). Section 7.1: `CODEOWNERS` stays path-based from the high tier, so `go.mod` stays owned by the reviewer and the architect while the review plan can rate a patch-only bump medium (the reviewer's approval satisfies what `CODEOWNERS` requires). |
| `docs/engineering/qa-gate.md` | Go-version paragraph; nightly line job for `govulncheck`; the same-`go`-line static check. |

The implementation of this tier rule is itself a high-tier change and is
reviewed as usual.
