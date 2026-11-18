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
therefore do not run in CI today while `docs/engineering/qa-gate.md` lists
SQLite and Postgres in the G3 conformance matrix. Skips are silent: nothing in
CI reports that the Postgres half of G3 never executed. WP-L1-01 (PR #39) went
through three review rounds with no run against a live Postgres server; the
matrix promised both dialects but CI proved only SQLite.

A Postgres server for the gate is not a live vendor call: it is hermetic
infrastructure beside the test process, like SQLite on disk. The owner asked
whether [embedded-postgres](https://github.com/fergusstrange/embedded-postgres)
could supply that server instead of a CI service container
(Linear EP0-49).

**embedded-postgres v2** (sources: the repository README and
[release v2.0.0](https://github.com/fergusstrange/embedded-postgres/releases);
[CLI docs](https://github.com/fergusstrange/embedded-postgres/blob/main/docs/cli.md);
[issue #95](https://github.com/fergusstrange/embedded-postgres/issues/95)):
module `github.com/fergusstrange/embedded-postgres/v2`; v2.0.0 released
2026-10-19, v2.0.1 current; requires Go 1.26+. It downloads real PostgreSQL
binaries on first start (checksums pinned in the library manifest; default
download base is the project's GitHub release assets). Cache defaults to
`os.UserCacheDir()/embedded-postgres/v2`. It exposes `StartContext`,
`ConnectionURL()`, `Close`, version pins `V15`…`V18` (16 → **16.15.1**), and
`RunAs` for root test runners. PostgreSQL cannot run as root; GitHub-hosted
`ubuntu-latest` runners are not root.

## Decision

A **run** is one scenario in one **pass** (a pass is a profile or a SQL
dialect: SQLite pass, Postgres pass, embedded profile, cloud profile, as
scenario tags and `specs/sql/design.md` §5 define). A **skip** is one run that
did not execute. `.gate/bdd.json` records every run; strict mode fails on any
skip; the summary line counts skipped runs, and **`<total>` is the sum of the
per-reason counts**.

### Server source and precedence

| Rule | Detail |
| --- | --- |
| **URL first** | If **`BDD_POSTGRES_URL`** is set, the SQL conformance driver uses that server (CI service container, a developer's own instance, or `docker run` below). The URL names the **server** (maintenance database on that server), not one shared scenario database. The role may **create databases**. |
| **Embedded fallback** | If the URL is unset, the driver starts one embedded server with **`github.com/fergusstrange/embedded-postgres/v2`**, then connects via the URL the library returns. |
| **Unavailable** | With **`GATE_STRICT=1`** (always in CI), a Postgres pass that can neither reach the URL nor start the embedded server **fails** the gate — never a skip. Without strict (local default), the same situation is a **skip** with reason from the closed set below. In CI a skip is impossible. |

### Postgres major and pins

| Rule | Detail |
| --- | --- |
| **Conformance major** | **Postgres 16** is the gate's conformance target (`postgres.V16` → **16.15.1** per the library manifest). The supported major stays 16 until a later ADR adds a matrix. |
| **Where pinned** | The library version is pinned in the root **`go.mod`**; the major is chosen in code (`V16`), not only by the library default (which is V18). |
| **Who moves it** | Bumping the library or changing the chosen major touches high-tier paths (`go.mod`, possibly `internal/bdd`). The **owner** approves every move. |

### Process and database lifecycle

| Rule | Detail |
| --- | --- |
| **Scope** | One embedded server (or one URL) per **`go test` process** of **`internal/bdd`** — not one global server for the whole module unless the implementation shares it via `TestMain` inside that package. |
| **Per scenario** | One **fresh database** per scenario on that server, created and dropped by the driver as `specs/sql/design.md` §5 requires, so parallel packages and separate head and base runs of `gate redgreen` never share database state. |
| **Driver surface** | The conformance driver depends only on a **server URL**; the library that supplies it is replaceable (embedded, container, or local install). |
| **Root runners** | When the test process runs as root (some containers), start uses the library's **`RunAs`** so PostgreSQL runs as a non-root account. |

### Cache, CI prefetch, and offline behaviour

| Rule | Detail |
| --- | --- |
| **Cache location** | Use the library default cache under the user cache dir, or an explicit cache dir the workflow sets for CI. |
| **CI cache** | **`actions/cache`** keyed on the **library module version** and the **Postgres bundle version** (16.15.1). |
| **Prefetch** | Before `make gate`, a workflow step runs the library's **`embedded-postgres prefetch`** (or equivalent) with retries so the BDD test step does **no network** on a warm cache. The CLI supports **`exec --offline`** for offline runs ([CLI docs](https://github.com/fergusstrange/embedded-postgres/blob/main/docs/cli.md)). |
| **Supply chain** | Binary checksums are pinned in the library release; our pin is the **`go.mod`** version. A project-owned mirror may replace `DownloadProvider` later without changing the driver contract. |

### Skip reporting, strict mode, summary, red→green, nightly

| Rule | Detail |
| --- | --- |
| **Postgres pass** | A **Postgres pass** is the Postgres dialect pass of the SQL conformance driver (`specs/sql/design.md` §5), not every scenario tagged `@profile:cloud`. |
| **Skip reasons** | Codes live in **`internal/bdd`** as a closed set; the initial member is **`postgres-unavailable`**. Adding a code changes that set. |
| **Report file** | After every BDD run, **`internal/bdd`** writes **`.gate/bdd.json`**: every run, with outcome passed, failed, or skip with reason. |
| **Enforcement** | With **`GATE_STRICT=1`**, `TestFeatures` **fails if any run was a skip**, for any reason — godog strict mode does not fail on skips and `make gate` runs `go test` directly. A missing Postgres dialect run while the SQLite run executed is one skipped run. |
| **Summary line** | When the BDD test step completes successfully, **`make gate`** invokes a **gate tool** subcommand that reads `.gate/bdd.json` and prints exactly one line: **`gate: scenario runs skipped: <total>`**, and when `<total>` is not zero, **` (<reason>: <count>, ...)`** with reasons sorted lexicographically, decimal counts, no trailing space. In CI the line is always **`gate: scenario runs skipped: 0`**. |
| **Local `make gate`** | No Docker required for Postgres when the embedded path works. Optional: set **`BDD_POSTGRES_URL`** to an external server. |
| **Red→green** | In CI the head run cannot record a skip under strict mode, so every newly active scenario that includes a Postgres run has executed that run at head; the base run uses the same job environment (including embedded start or the same URL). Locally a skipped Postgres run appears in the summary line; CI is authoritative. |
| **Nightly (Postgres majors)** | Nightly does not matrix Postgres majors while only one major is supported. If a later ADR declares oldest and newest supported majors, nightly runs the Postgres conformance passes on both. |

Local server via Docker (also copied into `docs/engineering/qa-gate.md` by the implementation):

```bash
docker run --rm -d --name basamento-gate-pg \
  -e POSTGRES_USER=kit -e POSTGRES_PASSWORD=kit -e POSTGRES_DB=kit \
  -p 127.0.0.1:5432:5432 postgres:16-bookworm
export BDD_POSTGRES_URL='postgres://kit:kit@127.0.0.1:5432/kit?sslmode=disable'
```

The container role is the image superuser so it may create databases. The port binds loopback only.

### CI workflow shape

| Rule | Detail |
| --- | --- |
| **No service container** | The `gate` job does **not** declare a `services.postgres` block when this ADR is implemented as written. Postgres comes from embedded start or from `BDD_POSTGRES_URL` if the workflow sets it. |
| **Strict** | Job `env` keeps **`GATE_STRICT=1`**. |

## Alternatives considered

| Alternative | Why rejected (or kept open) |
| --- | --- |
| **Postgres 16 service container only** (prior draft on ADR-0014) | Works and stays valid: `services.postgres` with **`image: postgres:16-bookworm@sha256:<digest>`**, **`--health-cmd pg_isready`**, and **`BDD_POSTGRES_URL`** to the maintenance database. Rejected as the *only* path because developers and agents without Docker never run the Postgres pass before CI — the failure mode WP-L1-01 showed. **Choosing this instead is a small edit:** set the URL in the workflow and omit the embedded dependency; the driver unchanged. |
| **Hybrid: container in CI, embedded locally** | Two code paths; CI proves a different stack than a laptop unless both always run the same driver entry. Rejected: one precedence rule (URL then embedded) keeps one behaviour everywhere when CI does not set the URL. |
| **Separate required job only for Postgres passes** | Splits G3, duplicates checkout and tool install, and still needs strict rules so the main job cannot ignore Postgres. |
| **Nightly only** | Pull requests could merge without Postgres dialect passes in CI. |
| **Wire-protocol simulator** | Cannot prove advisory locks, migration locking, or real SQLSTATE mapping. |
| **Skip and warn only in CI** | Contradicts `GATE_STRICT=1` on the `gate` job. |
| **Download Postgres with our own script** | Re-implements checksum manifests, cache layout, supervisor lifecycle, and cross-platform bundles the library already ships; higher maintenance for the same outcome. |
| **Vendored Postgres tree in the repository** | Large binary artifacts in git, per-platform paths, and manual security updates; fights the gate's "no live vendor calls" spirit without matching the library's lease-based cache. |
| **embedded-postgres v1 or ad-hoc in-process Postgres** | The old one-line rejection was "not hermetic, platform-specific, and slow." Against **v2:** checksum-pinned downloads and a shared cache make runs **more hermetic** after prefetch, but the **first** fetch still needs the network — partly true. **Platform-specific** is **weaker**: upstream CI covers Linux and macOS amd64/arm64 and Windows amd64 ([README](https://github.com/fergusstrange/embedded-postgres)). **Slow** **still holds**: `initdb` plus server start per `go test` process costs seconds; `gate redgreen` runs head and base, doubling startup (unverified wall-clock; see below). v2 is one month old with one maintainer — maturity risk, not a reason to dismiss the mechanism if CI caches and the URL fallback stay available. |

## Consequences

**Cost.** A test-only dependency in the root `go.mod`. First embedded start downloads PostgreSQL from GitHub releases (megabytes per platform); CI caches and prefetches that blob. Each `internal/bdd` test process pays startup time before scenarios run. Disk holds cache entries under the user cache dir.

**Risks.** v2 is young (2026-10-19) with a single maintainer. CI depends on GitHub release download availability unless prefetch and `actions/cache` always hit. The library licence and the licences of downloaded PostgreSQL bundles are **not yet checked** against `go-licenses`.

**Revert.** Roll back the `go.mod` pin, the embedded provider in `internal/bdd`, workflow cache/prefetch steps, strict skip rules, and `.gate/bdd.json` reporting together. Point CI at a service container with `BDD_POSTGRES_URL` only if embedded is withdrawn.

**Open facts the implementation must prove before merging**

| Fact | If it fails |
| --- | --- |
| `go-licenses` accepts the library and any bundles it pulls in | Do not merge until licences are cleared or an exception is recorded; fallback: URL + service container only. |
| `govulncheck` and `gate layering` stay green with a test-only import from `internal/bdd` | Fix layering or dependency placement; fallback: isolate bootstrap in a `_test`-only helper package. |
| Prefetch + cache key (library version + Postgres 16.15.1) makes the BDD step network-free on warm CI; retries handle rate limits | Keep prefetch retries; if downloads remain flaky, set `BDD_POSTGRES_URL` to a service container in the workflow. |
| Wall-clock cost of embedded start per `go test` process is acceptable on CI and locally; `gate redgreen` (two runs) stays within job time limits | Tune one-server-per-process vs `TestMain`; worst case revert to container-only in CI. |
| Postgres **16.15.1** passes the conformance driver: advisory locks for migration locking, SQLSTATE mapping, `CREATE DATABASE` per scenario | Fix driver or pin a different 16.x bundle via library bump; fallback external server on 16. |
| Behaviour matches strict/skip rules: unreachable URL and failed embedded start fail under `GATE_STRICT=1` | Fix enforcement before merge; do not ship silent skips in CI. |

## Questions for the owner

1. **Embedded default in CI** — Approve starting Postgres from embedded-postgres when `BDD_POSTGRES_URL` is unset, rather than requiring a service container? (Yes → implement this ADR; no → keep container-only and drop embedded from CI.)
2. **Major 16** — Approve Postgres **16** (not 18, the library default) as the sole gate major until a later matrix ADR? (Approve / defer.)

## Implementation changes these files

| Path | Change |
| --- | --- |
| `go.mod` and `go.sum` | Pin `github.com/fergusstrange/embedded-postgres/v2` (test dependency via `internal/bdd`). |
| `internal/bdd` | Postgres provider: honor `BDD_POSTGRES_URL`, else embedded `V16`; closed skip-reason set; write `.gate/bdd.json`; strict-mode fail on skip and on unavailable Postgres when strict. |
| The gate tool (`go tool gate`) | Subcommand reading `.gate/bdd.json` and printing `gate: scenario runs skipped: …`. |
| `Makefile` | After the BDD test step succeeds, run the summary subcommand. |
| `.github/workflows/gate.yml` | Cache and prefetch steps; **no** `services.postgres`; keep `GATE_STRICT=1`. |
| `review.yaml` and `CODEOWNERS` | High tier for new enforcement paths if needed; regenerate with `go tool gate docs -write`. |
| `docs/engineering/qa-gate.md` | Postgres paragraph (embedded default, optional `BDD_POSTGRES_URL`, `docker run`, strict vs local); G3/G4 rows; summary-line contract. |