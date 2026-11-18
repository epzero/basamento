# ADR-0015 — How the PR gate runs Postgres

**Status:** accepted · **Date:** 2026-11-19

## Context

ADR-0004 names Postgres (`pgx`) as the cloud SQL backend and does not pin a
server major. ADR-0005 assumes SQL event storage works on both dialects. The
SQL conformance driver in [`specs/sql/design.md`](../../specs/sql/design.md) §5
always runs a SQLite pass on a fresh file; it runs a **Postgres pass** when the
test environment names a server, once per scenario in a fresh database, and
otherwise records that dialect run as a **skip**, like any tool missing
locally. Scenarios tagged `@profile:cloud` that need no database server (for
example configuration checks) are not Postgres passes — only the Postgres
dialect pass of that driver counts.

The `gate` workflow (`.github/workflows/gate.yml`) sets `GATE_STRICT=1` and
starts no Postgres service and sets no server URL. Postgres dialect passes
do not run in CI while `docs/engineering/qa-gate.md` lists SQLite and Postgres
in the G3 conformance matrix. Skips are silent: nothing in CI reports that the
Postgres half of G3 never executed. WP-L1-01 went through three review rounds
with no run against a live Postgres server; the matrix promised both dialects
but CI proved only SQLite.

Embedded-postgres was weighed against a CI service container so a developer or
agent without Docker runs the same Postgres pass as CI. A Postgres server for the
gate is not a live vendor call when bounded: checksum-pinned bundles are
downloaded in the prefetch step (like pulling a CI image), and the BDD step does
no network I/O under strict mode. The server is infrastructure beside the test
process, like SQLite on disk.

**embedded-postgres v2.0.1** ([README](https://github.com/fergusstrange/embedded-postgres/tree/v2.0.1);
[CLI](https://github.com/fergusstrange/embedded-postgres/blob/v2.0.1/docs/cli.md)):
module `github.com/fergusstrange/embedded-postgres/v2`; requires Go 1.26+. The
module embeds `internal/binaries/manifest.json`: each Postgres bundle has a
SHA-256 and a URL on
`theseus-rs/postgresql-binaries` releases; `DownloadProvider.Acquire` verifies
the hash before extract (the pin rides on `go.mod`/`go.sum`). A companion supervisor is built from `./cmd/embedded-postgres` at the pinned
module version; the gate never downloads it from the library's release page
(see **Supply chain** and **`make pg-prefetch`**). Cache defaults to `os.UserCacheDir()/embedded-postgres/v2`.
API: `StartContext`, `ConnectionURL()`, `Close`, version pins `V15`…`V18`
(16 → **16.15.1**), `RunAs` for root runners. `DefaultConfig()` uses port 5432
and user/password/database `postgres`; `Port(0)` picks a free port. Host in the
URL is `127.0.0.1`, `sslmode=disable`. PostgreSQL cannot run as root;
`ubuntu-latest` runners are not root.

WP-L1-01's driver reads **`BDD_POSTGRES_URL`** as the **admin** URL (typically
`/postgres`), creates `bdd_<pid>_<seq>` databases and drops only those
(`DROP DATABASE IF EXISTS ... WITH (FORCE)`). An empty variable counts as unset.

## Decision

A **run** is one scenario in one **pass** (SQLite pass, Postgres pass, or a
profile pass, as tags and `specs/sql/design.md` §5 define). A **skip** is one
run that did not execute. With **`GATE_STRICT=1`**, any skip fails the gate; the
summary line counts skips and **`<total>`** is the sum of per-reason counts.
This ADR does not edit requirement text; the designer updates
`specs/sql/design.md` §5 in its own spec pull request to name `BDD_POSTGRES_URL`,
embedded fallback when the variable is empty, and strict vs skip behaviour
(before or with implementation).

### Server source and precedence

| Rule | Detail |
| --- | --- |
| **Precedence** | **`BDD_POSTGRES_URL` non-empty** → use that server only. **Empty or absent** → start embedded `github.com/fergusstrange/embedded-postgres/v2` and use `ConnectionURL()`. |
| **URL path** | The URL is the **server** (maintenance database), not one scenario database. The role must **create databases**. No fallback to embedded if the URL path is chosen: malformed, unreachable, unauthorised, or a role without `CREATEDB` is **unavailable** on the URL path, not a switch to embedded. |
| **Unavailable** | URL path failed or embedded start failed: under **`GATE_STRICT=1`** the gate **fails**; otherwise one skipped Postgres run with reason **`postgres-unavailable`**. A server up but unable to create or use a scenario's fresh database is a **failed run**, never a skip. |
| **Admin URL** | On any path the driver creates and drops **only** the databases it created. |

### Postgres major and pins

| Rule | Detail |
| --- | --- |
| **Conformance major** | **Postgres 16** in CI (`postgres.V16` → **16.15.1** per the embedded manifest). Stays 16 until a later ADR adds a matrix. |
| **URL major** | A URL names whatever major the operator runs; the driver does not check it. The authoritative major-16 run is CI's (embedded, or an owner-pinned 16 container if CI moves). |
| **Pins** | Library version in root **`go.mod`**; major **`V16`** in code (library default is V18). Bumps touch high-tier paths; the **owner** approves. |

### Process, databases, and embedded settings

| Rule | Detail |
| --- | --- |
| **G3** | One embedded server (or one URL) per **`go test` process** of **`internal/bdd`** inside `go test -race ./...`. |
| **G4** | `gate redgreen` runs `go test ./internal/bdd/ -run TestFeatures` **once per newly activated scenario** in a detached base worktree (`internal/gate/redgreen.go`): each is a new process and may start embedded again. |
| **Per scenario** | One **fresh database** per scenario, as `specs/sql/design.md` §5 requires. |
| **Driver surface** | The conformance driver depends only on a **server URL**; the supplier is replaceable. |
| **Loopback** | Embedded: **`Port(0)`** (never default 5432), random password per process, or **`UnixSocket(dir)`** without TCP — no listener beyond loopback. |
| **Collation** | Text order is pinned by REQ-SQL-017 (`COLLATE "C"`); embedded uses `C`/UTF8; a URL server may differ. |
| **Root runners** | Embedded start uses **`RunAs`** when the test process is root. |

### Supply chain

| Asset | Rule |
| --- | --- |
| **Bundles** | SHA-256 in the embedded module manifest; download from `theseus-rs/postgresql-binaries` (or `DownloadProvider.BaseURL` + `/<version>/<asset>` for a mirror later). |
| **Supervisor and CLI** | Same executable built from the **pinned module** (see **`make pg-prefetch`**); gate **never** fetches supervisor binaries from the library release page, on any run. **`EP_SUPERVISOR`** is set from that target's output or resolved by **`internal/bdd`** from the pinned module — a run without it does not fall back to the library's download. |
| **Secrets in output** | `.gate/bdd.json`, the summary line, test logs and the `gate-report` artifact name the server **`embedded`** or **`url`** only — never URL, host, user or password. |

### Cache, CI prefetch, and offline

| Rule | Detail |
| --- | --- |
| **One directory** | CI sets one cache path: **`actions/cache`**, prefetch `--cache-dir`, and driver `Storage.CacheDir` (from env in `internal/bdd`; locally the library default). |
| **Prefetch** | Before `make gate`, run **`make pg-prefetch`**: build supervisor and CLI from the `go.mod` pin and prefetch into the shared cache dir, with retries — **no** `curl \| bash` or unpinned workflow binary. The `gate` workflow calls the same target. |
| **Strict offline** | **`GATE_STRICT=1` means hermetic**: wherever it runs, the BDD step sets **`DownloadProvider{Offline: true}`** and **never downloads**. Without strict mode, a first start may download bundles in the test process. A cold-cache strict run fails with the library's not-cached (offline mode) message; the driver names **`make pg-prefetch`** as the remedy. |
| **Cache key** | Runner **OS and architecture** plus **library module version** (bundle version is fixed by that pin). GitHub evicts a cache entry unused for seven days and scopes entries to a branch; a cold run runs the prefetch target again. |
| **Bad cache** | `invalid cache entry ... remove or repair` fails the start; under strict, the gate; CI fixes by bumping the cache key. |

### Reporting, summary, red→green, nightly, CI

| Rule | Detail |
| --- | --- |
| **Postgres pass** | The Postgres dialect pass of the SQL conformance driver, not every `@profile:cloud` scenario. |
| **Skip reasons** | Closed set in **`internal/bdd`**; initial code **`postgres-unavailable`**. |
| **`.gate/bdd.json`** | Written **before `TestFeatures` returns**, every run and outcome (including failed scenarios). |
| **Summary line** | Only if the BDD step exits zero: **`make gate`** runs a **gate tool** subcommand printing **`gate: scenario runs skipped: <total>`** and, if nonzero, sorted **`(reason: count, ...)`**. CI expects **`: 0`**. |
| **Strict enforcement** | `TestFeatures` fails on any skip (godog strict does not). Unavailable Postgres under strict fails per the rule above. |
| **Red→green** | CI head cannot skip under strict; each activated scenario's base run uses the same job environment. |
| **Nightly** | No Postgres major matrix until a later ADR; then conformance on declared oldest and newest. |
| **CI job** | No `services.postgres` as implemented here; **`GATE_STRICT=1`**. Postgres from embedded or from workflow-set **`BDD_POSTGRES_URL`**. |

Local server via Docker (documented in `docs/engineering/qa-gate.md` once implemented):

```bash
docker run --rm -d --name basamento-gate-pg \
  -e POSTGRES_USER=kit -e POSTGRES_PASSWORD=kit -e POSTGRES_DB=kit \
  -p 127.0.0.1:5432:5432 postgres:16-bookworm
export BDD_POSTGRES_URL='postgres://kit:kit@127.0.0.1:5432/kit?sslmode=disable'
```

Loopback bind; superuser may create databases.

## Alternatives considered

| Alternative | Why rejected (or kept open) |
| --- | --- |
| **Postgres 16 service container only** | Valid: `postgres:16-bookworm@sha256:<digest>`, health check, **`BDD_POSTGRES_URL`**. Requires **Docker locally**; switching CI is a **one-file** `gate.yml` edit (high tier, owner approval). Rejected as the only path: without Docker, nothing runs the Postgres pass before CI (WP-L1-01). Driver unchanged if CI sets the URL only. |
| **Hybrid: container in CI, embedded locally** | CI and laptop prove different stacks. Rejected: one precedence rule when CI leaves the URL empty. |
| **Separate required job only for Postgres passes** | Splits G3, duplicates setup, still needs strict main-job rules. |
| **Nightly only** | PRs merge without Postgres dialect passes in CI. |
| **Wire-protocol simulator** | Cannot prove advisory locks, migration locking, or real SQLSTATE mapping. |
| **Skip and warn only in CI** | Contradicts `GATE_STRICT=1`. |
| **Our own download script** | Rebuilds manifests, cache, supervisor; same outcome, more maintenance. |
| **Vendored Postgres in git** | Large binaries, manual security updates. |
| **embedded-postgres without this design** | v2 checksum-pinned bundles and multi-OS CI reduce platform risk; first fetch still needs network; **startup cost per `go test` process** remains (G3 one start, G4 one per activated scenario). Young module (v2.0.1); URL/container fallback stays. |

## Consequences

**Cost.** Test-only dependency in root `go.mod`. Prefetch downloads bundles and fills cache; G3 pays one embedded start per `internal/bdd` process, G4 adds one per activated scenario. Disk under the cache directory.

**Risks.** v2 is recent with a small maintainer set. Prefetch depends on `theseus-rs` release availability unless cache hits.

**Cleanup.** If a test process or CI job is killed, the library supervisor stops PostgreSQL ([CLI wrapper protocol](https://github.com/fergusstrange/embedded-postgres/blob/v2.0.1/docs/cli.md)); hosted runners are discarded after the job.

**Revert.** Roll back `go.mod`, embedded provider, workflow cache/prefetch, skip rules and `.gate/bdd.json` together. **If embedded proves unreliable in CI** (cold-cache download failures, startup past job limits, licence block), the owner moves CI to a digest-pinned **`postgres:16-bookworm`** service with **`BDD_POSTGRES_URL`** in `gate.yml` in its own pull request; driver, skip rules and summary line stay.

## Conditions

| Condition | If it fails |
| --- | --- |
| `go-licenses` accepts the library and tool dependency; bundle licences (PostgreSQL and contents) are recorded in `docs/engineering/qa-gate.md` and covered by owner approval of the implementation | Licence block; fallback URL + service container in CI. |
| `govulncheck` and `gate layering` stay green | Fix placement; fallback isolate bootstrap in test-only package. |
| `make pg-prefetch` + cache key keep the BDD step offline under strict; retries handle 429/5xx | Set `BDD_POSTGRES_URL` to a service container in the workflow. |
| Wall-clock of embedded starts fits job limits (G3 + G4 process counts above) | Tune sharing; worst case container-only CI. |
| Postgres **16.15.1** passes conformance (advisory locks, SQLSTATE, per-scenario `CREATE DATABASE`) | Bump library or use external 16. |
| Unavailable and strict rules behave as written | Do not merge silent CI skips. |

## Implementation changes these files

| Path | Change |
| --- | --- |
| `go.mod` and `go.sum` | Pin `github.com/fergusstrange/embedded-postgres/v2`; tool for prefetch CLI. |
| `internal/bdd` | Precedence, embedded `V16`, `Offline: true` when `GATE_STRICT=1`, `EP_SUPERVISOR` resolution, loopback settings, closed skips, `.gate/bdd.json`, strict failures; offline error names `make pg-prefetch`; shared cache dir env. |
| The gate tool (`go tool gate`) | Subcommand for `gate: scenario runs skipped: …`. |
| `Makefile` | Target **`pg-prefetch`** (supervisor, CLI, prefetch for shared cache dir); summary subcommand after BDD success only. |
| `.github/workflows/gate.yml` | Cache, **`make pg-prefetch`** before gate; no `services.postgres`; `GATE_STRICT=1`. |
| `review.yaml` and `CODEOWNERS` | Regenerate with `go tool gate docs -write` if needed. |
| `docs/engineering/qa-gate.md` | Postgres (embedded, `BDD_POSTGRES_URL`, docker recipe, strict hermetic BDD vs non-strict download, **`make pg-prefetch`**, not-a-vendor-call bound, bundle licences, G3/G4, summary line). |
| `specs/sql/design.md` §5 | Designer: name `BDD_POSTGRES_URL`, embedded when empty, skip vs strict; own spec PR before or with implementation. |
