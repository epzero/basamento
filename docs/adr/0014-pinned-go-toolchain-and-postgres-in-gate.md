# ADR-0014 — Pinned Go toolchain and Postgres in the PR gate

**Status:** accepted

## Context

**Toolchain.** The root `go.mod` and `runtime/temporal/embedded/go.mod` each name
`go 1.26.10` and neither has a `toolchain` line. There is no `go.work`. CI
installs Go with `actions/setup-go@v5` and `go-version: stable`. `govulncheck`
reports **GO-2026-6859** in the standard-library `net/http`; the advisory is
fixed in **Go 1.26.11**. Nothing under `./...` depends on `net/http`
(`go list -deps ./...`), so the current tree passes `govulncheck`, but any
dependency path that reaches `net/http` fails until the repository's minimum
patch is at least **1.26.11**.

**Postgres in G3.** ADR-0004 and ADR-0005 name Postgres (`pgx`) as the cloud
SQL backend and do not pin a server major version.
[`specs/sql/design.md`](../../specs/sql/design.md) §5 defines the SQL
conformance driver: it always runs a SQLite pass on a fresh file; it runs a
Postgres pass when the test environment names a server, once per scenario in a
fresh database, and otherwise reports that pass as skipped, like any tool
missing locally. Scenarios tagged `@profile:cloud` that need no database server
(for example configuration checks) are not Postgres passes — only the
Postgres dialect pass of that driver counts. The `gate` workflow sets
`GATE_STRICT=1` and does not start Postgres or set a server URL, so Postgres
passes do not run in CI while `docs/engineering/qa-gate.md` lists SQLite and
Postgres in the G3 conformance matrix.

A Postgres service container in CI is not a live vendor call: it is hermetic
infrastructure beside the test process, like SQLite on disk.

## Decision

### 1. Go toolchain

| Rule | Detail |
| --- | --- |
| **Minimum patch** | The `go` directive in every module in the repository is the same minimum patch — **`1.26.11`**. |
| **Modules** | Root `go.mod` and `runtime/temporal/embedded/go.mod`. |
| **No `toolchain` line** | Only the `go` directive in each module file. |
| **CI toolchain env** | Job `env` sets **`GOTOOLCHAIN=local`**. The Go installed by `setup-go` is the toolchain the build uses; if `go.mod` requires a newer patch than the one installed, the job fails instead of downloading another toolchain. |
| **CI install (pinned jobs)** | Every workflow job that builds or tests (PR `gate`, embedded module, existing nightly jobs that today use `stable`) uses **`go-version-file: go.mod`** on the root module file, not `stable`. |
| **CI install (line job)** | The nightly **line** job alone uses **`go-version`** set to the release line named by `go.mod` (for example `1.26`), **`check-latest: true`**, and **does not** use `go-version-file` (that would pin the same patch as the PR gate and duplicate it). |
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

### 2. Postgres in the PR gate

| Rule | Detail |
| --- | --- |
| **CI service** | The `gate` job starts a **Postgres 16** service container. **Postgres 16 is the conformance target; the version CI runs is the supported major until a later ADR adds a matrix.** |
| **Image pin** | The workflow sets **`image: postgres:16-bookworm@sha256:<digest>`**; the digest is chosen in the implementing change and recorded in the workflow. |
| **Health check** | `services.postgres.options` includes **`--health-cmd pg_isready`** with interval, timeout and retries so job steps start only after the server accepts connections. |
| **Server URL** | **`BDD_POSTGRES_URL`** names the **server** (maintenance database on that server), not one shared scenario database. The URL uses a role that may **create databases**. The SQL conformance driver creates one fresh database per scenario and drops it afterwards (`specs/sql/design.md` §5), so parallel packages and separate head and base runs of `gate redgreen` never share database state. The base run inherits the job environment (URL and strict flag). |
| **Postgres pass** | A **Postgres pass** is the Postgres dialect pass of the SQL conformance driver (`specs/sql/design.md` §5), not every scenario tagged `@profile:cloud`. |
| **Skip reasons** | Skip reason codes live in **`internal/bdd`** as a closed set; the initial member is **`postgres-unavailable`**. Adding a code is a change to that set. |
| **Report file** | After every BDD run, **`internal/bdd`** writes **`.gate/bdd.json`**: machine-readable outcome of every scenario and every pass (passed, failed, or skipped with reason). |
| **Enforcement** | **`internal/bdd`**. With **`GATE_STRICT=1`**, `TestFeatures` **fails if any scenario or pass was skipped**, for any reason — godog strict mode does not fail on skips and `make gate` runs `go test` directly. In strict mode, a Postgres pass with empty `BDD_POSTGRES_URL` or an unreachable server **fails**; it is never recorded as a skip. In CI, a skip is therefore impossible. Without strict (local default), a Postgres pass with no server **skips** with a notice and reason `postgres-unavailable`. |
| **Summary line** | After the G3 test run, **`make gate`** invokes a subcommand of **the gate tool (`go tool gate`)** that reads `.gate/bdd.json` and prints exactly one line: **`gate: scenarios skipped: <total>`**, and when `<total>` is not zero, **` (<reason>: <count>, ...)`** with reasons sorted lexicographically, decimal counts, no trailing space. In CI the line is always **`gate: scenarios skipped: 0`**. |
| **Local `make gate`** | Postgres passes are optional locally (no server required for the rest of the gate). |
| **Red→green** | In CI the head run cannot skip under strict mode, so every newly active scenario that includes a Postgres pass has executed that pass at head; the base run uses the same job environment. Locally a skipped Postgres pass appears in the summary line; CI is authoritative. |
| **Nightly (Postgres majors)** | Nightly does not matrix Postgres majors while only one major is supported. If a later ADR declares oldest and newest supported majors, nightly runs the Postgres conformance passes on both. |

Local server (also copied into `docs/engineering/qa-gate.md` by the implementation):

```bash
docker run --rm -d --name basamento-gate-pg \
  -e POSTGRES_USER=kit -e POSTGRES_PASSWORD=kit -e POSTGRES_DB=kit \
  -p 127.0.0.1:5432:5432 postgres:16-bookworm
export BDD_POSTGRES_URL='postgres://kit:kit@127.0.0.1:5432/kit?sslmode=disable'
```

The container role is the image superuser so it may create databases. The port binds loopback only.

#### Alternatives considered (Postgres)

| Alternative | Why rejected |
| --- | --- |
| Separate required job only for Postgres passes | Splits G3, duplicates checkout and tool install, and still needs strict rules so the main job cannot ignore Postgres. |
| Nightly only | Pull requests could merge without running Postgres dialect passes in CI. |
| Download Postgres and run in-process at test time | Not hermetic, platform-specific, and slow. |
| Wire-protocol simulator | Cannot prove advisory locks, migration locking, or real SQLSTATE mapping. |
| Skip and warn only in CI | Contradicts `GATE_STRICT=1` on the `gate` job. |

## Consequences

**Cost.** CI runs one Postgres service per `gate` job. Developers without Docker
rely on SQLite passes locally and on CI for Postgres. The nightly Go-line job
may fail when a new stdlib advisory appears before `go.mod` is bumped — that
failure is signal, not noise.

**Pins and approval.** The Postgres image digest and the `go` line change only
in high-tier paths (`.github/**`, `go.mod`). The owner approves every move of
either pin. A withdrawn or invalid image digest fails the job at pull time
instead of falling back silently.

**Revert.** Roll back `go 1.26.11`, `go-version-file`, the Postgres service,
and strict BDD skip rules together; the two decisions assume each other for a
truthful G3 matrix.

**Owner option (separate decision).** Whether a patch-only bump of the `go`
line alone could be reviewed at **medium** tier would require changing
`review.yaml`; this ADR does not propose that edit.

**Implementation changes these files:**

| Path | Change |
| --- | --- |
| `go.mod` | `go 1.26.11`. |
| `runtime/temporal/embedded/go.mod` | Same `go` line as the root module. |
| `README.md` | Minimum Go version matches the root `go` directive. |
| `.github/workflows/gate.yml` | `go-version-file: go.mod`; job `GOTOOLCHAIN=local`; Postgres 16 service with digest, health check and `BDD_POSTGRES_URL`. |
| `.github/workflows/nightly.yml` | New line job: `go-version` + `check-latest` on the release line, `govulncheck ./...`, hard fail; existing jobs use `go-version-file: go.mod`. |
| `.github/workflows/embedded.yml` | `go-version-file: go.mod`; `GOTOOLCHAIN=local`. |
| `internal/bdd` | Closed skip-reason set; `.gate/bdd.json` reporter; Postgres passes via `BDD_POSTGRES_URL`; strict-mode fail on any skip; strict fail when Postgres is required but unavailable. |
| The gate tool (`go tool gate`) | Subcommand (name chosen in implementation) reading `.gate/bdd.json` and printing the summary line. |
| `Makefile` | Run the summary subcommand after G3 on every gate path. |
| `review.yaml` and `CODEOWNERS` | High tier for the files that write `.gate/bdd.json` and enforce strict mode; regenerate with `go tool gate docs -write`. |
| `docs/engineering/qa-gate.md` | Go-version paragraph; Postgres paragraph (loopback `docker run`, `BDD_POSTGRES_URL`, strict vs local); update **G3** and **G4** table rows for Postgres in CI and red→green with the same server env; nightly line job for govulncheck; summary-line contract. |