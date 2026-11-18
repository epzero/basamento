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
| **Local** | Default **`GOTOOLCHAIN=auto`**: an older installed Go downloads the patch `go.mod` requires (`README.md`). |
| **Patch bumps** | A security patch is a coordinated bump of every `go.mod` `go` line. Each change touches high-tier paths (`go.mod`, `.github/**`). |

#### Alternatives considered (toolchain)

| Alternative | Why rejected |
| --- | --- |
| Keep `stable` in CI and a loose `go` line | CI and `go.mod` drift; `stable` can leap ahead of the named line or lag behind a needed stdlib fix. |
| Add a `toolchain` directive next to `go` | Two pins to reason about; `go` alone with `GOTOOLCHAIN=auto` locally and `GOTOOLCHAIN=local` in CI already binds builds to one patch. |
| Pin Go through a container image or vendoring the toolchain | Extra moving parts; fights the pure-Go single-binary setup without reproducibility beyond what `go.mod` gives. |
| Move to the next Go release line now | A line jump belongs in its own decision when dependencies and stdlib require it. |
| Float every CI job on `check-latest` | Only the line job may float; pinned jobs must match the patch in `go.mod`. |

## Consequences

**Cost.** The nightly Go-line job may fail when a new stdlib advisory appears
before `go.mod` is bumped — that failure is signal, not noise.

**Pins and approval.** The `go` line changes only in high-tier paths
(`.github/**`, `go.mod`). The owner approves every move of that pin.

**Revert.** Roll back `go 1.26.11`, `go-version-file`, `GOTOOLCHAIN=local` and
the nightly line job together.

**Owner option (separate decision).** Whether a patch-only bump of the `go`
line alone could be reviewed at **medium** tier would require changing
`review.yaml`; this ADR does not propose that edit.

**Implementation changes these files:**

| Path | Change |
| --- | --- |
| `go.mod` | `go 1.26.11`. |
| `runtime/temporal/embedded/go.mod` | Same `go` line as the root module. |
| `README.md` | Minimum Go version matches the root `go` directive. |
| `.github/workflows/gate.yml` | Every job: `GOTOOLCHAIN=local`. `go-version-file: go.mod`. |
| `.github/workflows/nightly.yml` | Every job: `GOTOOLCHAIN=local`. **flake-hunt**, **slow-lanes** and **release-readiness**: `go-version-file: go.mod`. New **line** job: `go-version` on the release line (for example `1.26`) with `check-latest: true`, `govulncheck ./...`, hard fail. |
| `.github/workflows/embedded.yml` | Every job: `GOTOOLCHAIN=local`; `go-version-file: go.mod`. |
| `docs/engineering/qa-gate.md` | Go-version paragraph; nightly line job for govulncheck. |