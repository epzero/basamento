# ADR-0014 — Pinned Go toolchain and Postgres in the PR gate

**Status:** accepted

## Context

Two gaps block the first platform pull requests from matching what the gate
is meant to prove.

**Toolchain.** `go.mod` names `go 1.26.10` and has no `toolchain` line. CI
uses `actions/setup-go@v5` with `go-version: stable`, which tracks the newest
release on the current Go release line and can diverge from the patch named in
`go.mod`. `govulncheck` reports **GO-2026-6859** in the standard-library
`net/http`; the fix is in **Go 1.26.11**. No production package imports
`net/http` today (`go list -deps ./...` lists it for nothing), so the gate
stays green, but the first dependency path that reaches `net/http` (HTTP
serving, several third-party stacks) will fail `govulncheck` until the
repository's minimum Go patch matches the advisory. Some in-flight work
packages already raised their branch `go` lines to `1.26.11` for that reason;
**main** must remain the single place the line moves once this decision lands.

**Postgres in G3.** ADR-0004 and ADR-0005 name Postgres (`pgx`) as the cloud
SQL implementation but do not pin a server major version. WP-L1-01's SQL store
specs tag Postgres scenarios `@profile:cloud`; the component driver runs them
only when the environment names a server (`specs/sql/design.md`). CI's `gate`
job sets `GATE_STRICT=1` but does not start Postgres or set a URL, so those
scenarios never execute on pull requests even though G3 lists "SQLite +
Postgres" in the conformance matrix (`docs/engineering/qa-gate.md`). Advisory
locks, placeholder rewrite and SQLSTATE mapping are unproven in CI until a
server runs there.

A service container is not a live vendor call (constitution: no live vendor
calls in the gate): it is hermetic infrastructure beside the test process,
like SQLite on disk.

## Decision

### 1. Go toolchain

| Rule | Detail |
| --- | --- |
| **Minimum patch** | The `go` directive in **main**'s `go.mod` is the minimum patch release that carries the standard-library fixes this repository needs — **`1.26.11`** for GO-2026-6859. |
| **No `toolchain` line** | Only the `go` directive; no separate `toolchain` pin. |
| **CI (PR gate)** | `actions/setup-go` with **`go-version-file: go.mod`**, not `stable`, so every build uses exactly the patch `go.mod` names. |
| **Local** | Default **`GOTOOLCHAIN=auto`**: an older local Go downloads the patch `go.mod` requires (`docs/engineering/agent-workflow.md` §6). |
| **Nightly (toolchain line)** | A scheduled job installs the **newest patch of the same release line** as `go.mod` (today the `1.26` line), runs `govulncheck ./...`, and **fails** when a new advisory appears before a pull request would. |
| **Patch bumps** | A later security patch is a **one-line** `go` directive change on an ordinary developer ticket. `go.mod` is in the **high** tier of `review.yaml`; the owner approves each bump. |

The embedded Temporal module workflow may keep its own `go.sum` cache path; it
should use the same **`go-version-file`** pattern against **main**'s `go.mod`
when that job runs in the same repository checkout.

#### Alternatives considered (toolchain)

| Alternative | Why rejected |
| --- | --- |
| Keep `stable` in CI and a loose `go` line | CI and `go.mod` drift; `stable` can leap ahead of the named line or lag behind a needed stdlib fix. |
| Add a `toolchain` directive next to `go` | Two pins to reason about; `go` alone plus `GOTOOLCHAIN=auto` already binds local and CI to one patch. |
| Pin Go through a container image or vendoring the toolchain | Extra moving parts; fights the "pure Go, one binary" setup without buying reproducibility `go.mod` already gives. |
| Move to the next Go release line now | Out of scope for closing GO-2026-6859; line jumps belong in their own decision when dependencies and stdlib require it. |

### 2. Postgres in the PR gate

| Rule | Detail |
| --- | --- |
| **CI service** | The `gate` job in `.github/workflows/gate.yml` starts a **Postgres service container**. **Major version 16** is the conformance target: current pgx and GitHub's service examples align on 16; ADRs 0004 and 0005 do not name a major, so **the version CI runs is the supported major** until a later ADR adds a matrix. |
| **Image pin** | Document **`postgres:16-bookworm`** for humans; the workflow pins **`image: …@sha256:…`** because major tags move when upstream publishes new image digests, and the gate should not shift underneath a green main without a deliberate change. |
| **URL** | Job env sets **`BDD_POSTGRES_URL`** (e.g. `postgres://kit:kit@localhost:5432/kit?sslmode=disable` matching the service credentials). |
| **Strict CI** | With **`GATE_STRICT=1`**, a scenario that would run on Postgres but is **skipped for missing server** fails the run — CI cannot go green with Postgres code unexecuted. |
| **Local `make gate`** | Without a server, Postgres-tagged scenarios **skip** with a visible notice; **`make gate` still means everything CI runs that needs only Go** — providing Postgres locally is optional via Docker, documented in `qa-gate.md`. |
| **Gate summary** | After G3, the gate prints one line: **`gate: scenarios skipped: <total> (<reason>: <count>, …)`** (e.g. `postgres-unavailable: 4`). A skip is never silent. |
| **Strict contract** | When `GATE_STRICT` is set, **any** skip with reason `postgres-unavailable` is a **non-zero exit** before the summary line is treated as success. |
| **Ownership** | **`internal/bdd`**: per-scenario skip vs run, reason codes, strict failure. **`tools/gate`** (invoked from `make gate` / CI): aggregate skip counts and print the summary line. |
| **Local server** | One documented `docker run` in `qa-gate.md` plus `BDD_POSTGRES_URL`; no Makefile target unless maintainers later want one. |
| **Nightly (Postgres)** | **Not** a separate matrix today: only one major is supported. If a later ADR declares a oldest/newest major pair, nightly runs Postgres scenarios on both; until then, PR CI on 16 is enough. |

#### Alternatives considered (Postgres)

| Alternative | Why rejected |
| --- | --- |
| Separate required job only for Postgres scenarios | Splits G3, duplicates checkout and tool install, and still needs strict skip rules so the main job cannot ignore Postgres. |
| Nightly only | Pull requests could merge with broken Postgres behavior; defeats red→green on `@profile:cloud` scenarios. |
| Download Postgres and run in-process at test time | Not hermetic, platform-specific, and slow; unlike SQLite files, not reproducible in `go test` alone. |
| Wire-protocol simulator | Cannot prove advisory locks, migration locking, or real SQLSTATE mapping the SQL port depends on. |
| Skip and warn only | Already the local default; CI with `GATE_STRICT=1` must not allow it. |

## Consequences

**Cost.** CI runs one Postgres service per `gate` job (memory and startup time).
Developers without Docker rely on SQLite scenarios locally and on CI for
Postgres. Each `go` security patch needs an owner-approved high-tier change.
The nightly Go-line job may fail when the Go team publishes a new advisory
before a patch bump lands — that failure is intentional signal.

**Revert.** Restore `go 1.26.10` and `go-version: stable` only together with
removing the Postgres service and relaxing strict skip rules; both decisions
assume each other for a truthful G3 matrix.

**Owner option (separate decision).** Whether a **patch-only** bump of the `go`
line alone could be reviewed at **medium** tier would require changing
`review.yaml`; this ADR does not propose that edit.

**Follow-up implementation** (developer ticket after this ADR merges):

| File | Change |
| --- | --- |
| `go.mod` | Set `go 1.26.11`; **the only place on main** the minimum patch moves (reconcile in-flight branches that already set `1.26.11`). |
| `.github/workflows/gate.yml` | `go-version-file: go.mod`; Postgres 16 service; job `env` with `BDD_POSTGRES_URL`; health-check wait for the service. |
| `.github/workflows/nightly.yml` | New job: `setup-go` on newest `1.26` patch, `make tools`, `govulncheck ./...`. |
| `.github/workflows/embedded.yml` | Use `go-version-file: go.mod` from the repo root (same patch as main). |
| `README.md` | Minimum Go version matches `go.mod`. |
| `internal/bdd` | Read `BDD_POSTGRES_URL` / `GATE_STRICT`; skip with reason `postgres-unavailable` or run Postgres passes; fail strict when required skips occur. |
| `tools/gate` | Collect skip counts from the BDD run (or test JSON) and emit the summary line; exit non-zero on strict + `postgres-unavailable`. |
| `Makefile` | Wire skip summary into the `gate` / `test` target output if not wholly inside `tools/gate`. |
| `specs/sql/design.md` | Name `BDD_POSTGRES_URL` explicitly; point at this ADR and `qa-gate.md`. |
| WP-L1-01 implementation | Step definitions and `platform/sql` that the scenarios already describe. |

This pull request only adds the ADR and updates `docs/adr/README.md` and
`docs/engineering/qa-gate.md` so docs and the decision agree.