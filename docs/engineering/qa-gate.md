# QA gate

One entry point, `make gate`, runs the same checks locally and in CI. A change
merges only when every blocking gate passes. Tools that are not installed
locally are skipped with a notice; CI sets `GATE_STRICT=1` and installs them
with `make tools`, which pins their versions in the `Makefile`.

| Gate | Checks | Blocks | Where |
| --- | --- | --- | --- |
| **G1 Static** | `gofmt`, `go vet`, `golangci-lint`, `govulncheck` in every module root (`make vuln` / nightly **line** job, `ADR-0014`), `CGO_ENABLED=0 go build`, `gate layering` (package graph against `layering.yaml`), `gate gomod` (every tracked `go.mod` names the same `go 1.M.P` line, `ADR-0014`), `go-licenses` (no forbidden or restricted licenses), `buf lint`, `buf breaking` against the base (after publication) | yes | `make gate` |
| **G2 Spec integrity** | `gate spec lint` (format, tags, ids that match their primary requirement, traceability, no scenario on a retired requirement, expired quarantine); `gate spec lock` (approved at current text, valid identities, author ≠ reviewer, tier floors and notes, no human rejection, every human decision names a person); `gate docs` (generated module and roadmap diagrams are current) | yes | `make gate` |
| **G3 Behavior** | `go test -race ./...` including the BDD suite in strict mode; runtime and port conformance suites across the matrix (SQLite + Postgres, local + temporal-embedded, fileblob + S3-compatible) as they land; replay determinism over recorded histories; fuzz and property tests for the flow machine and CEL. Postgres for the SQL dialect pass: [ADR-0015](../adr/0015-how-the-pr-gate-runs-postgres.md) (`BDD_POSTGRES_URL`, embedded 16, `make pg-prefetch`). | yes | `make gate` (+ CI matrix) |
| **G4 Red→green** | `gate redgreen -base <base>`: newly active scenarios fail at base and pass at head | yes | `make gate BASE=<ref>`: CI sets the base, agents and the lead use `BASE=main` |
| **G5 Test strength** | Mutation testing on changed packages (e.g. gremlins) above threshold; diff coverage as a secondary signal | yes once enabled (WP-L0 follow-up); nightly before | CI |
| **G6 System smoke** | Build the binary, start `kit serve --profile embedded` in a temp dir, run `@smoke` scenarios against the API | yes once `kit serve` exists (WP-L1-05) | CI |
| **G7 Journeys** | `@journey` scenarios replayed by the browser runner from recordings | UI changes only | CI + nightly |
| **G8 Review** | Agent code review by risk tier (`docs/engineering/code-review.md`): parallel finders, verified findings, important findings stop integration. High-tier changes need a person's approval before integration, P0 specs before a release (`agent-workflow.md` §5) | yes | The lead, or the pull request |

Release only: `gate release-check` fails while any P0 scenario lacks a
person's acceptance.

Go: the root and embedded `go.mod` files name the same minimum patch
(`ADR-0014`); CI sets `GOTOOLCHAIN=local` and installs Go with
`go-version-file: go.mod` (except the nightly **line** job, which uses
`check-latest` on the release line read from `go.mod` and runs
`govulncheck ./...` in every module). Locally, `make gate` exports
`GOTOOLCHAIN=go<patch>` from the root `go.mod` unless the environment already
set it.

Nightly: `@live`, `@slow`, `@eval` (live), full mutation run,
the **line** job above, and a flake hunt (repeated runs that look for
nondeterminism; a flaky scenario is quarantined with an issue and an expiry).
No automatic reader for nightly **line** failures yet (tracked **EP0-95**);
the **release-readiness** job uses `continue-on-error` and is informational only (P0 backlog is expected); **line** is the blocking nightly signal.

## Postgres in G3 (ADR-0015)

The SQL conformance driver's **Postgres pass** (not every `@profile:cloud`
scenario) needs a server once per `go test` process of `internal/bdd` (G3).
`gate redgreen` starts embedded again per activated scenario (G4); each base
worktree run reuses the head checkout's built supervisor (`EP_SUPERVISOR`) and
inherits `BDD_POSTGRES_CACHE_DIR`, matching the G3 BDD step environment.

**Precedence.** A non-empty `BDD_POSTGRES_URL` uses only that admin server
(typically `/postgres`); unreachable or unauthorised URLs never fall back to
embedded. An empty or unset variable starts embedded Postgres **16** (`V16` /
16.15.1) via `github.com/fergusstrange/embedded-postgres/v2` with `Port(0)`, a
random password per process, and loopback only. Under `GATE_STRICT=1`, any
skipped Postgres run fails the gate; otherwise unavailable servers record
`postgres-unavailable` skips and `go tool gate bdd-skips` prints
`gate: scenario runs skipped: <total>` after the BDD step.

**Local Docker (optional).**

```bash
docker run --rm -d --name basamento-gate-pg \
  -e POSTGRES_USER=kit -e POSTGRES_PASSWORD=kit -e POSTGRES_DB=kit \
  -p 127.0.0.1:5432:5432 postgres:16-bookworm
export BDD_POSTGRES_URL='postgres://kit:kit@127.0.0.1:5432/kit?sslmode=disable'
```

**Prefetch and cache.** Set `BDD_POSTGRES_CACHE_DIR` to one directory shared by
`make pg-prefetch`, CI `actions/cache`, and the BDD driver. Run
`make pg-prefetch` before a strict gate: it builds the supervisor CLI from the
pinned module (`EP_SUPERVISOR` / `.gate/bin/embedded-postgres`) and downloads
checksum-pinned bundles — not a vendor API call (bounded infrastructure, same
role as pulling a CI image). With `GATE_STRICT=1`, the BDD step sets offline
mode and never downloads; a cold cache fails until prefetch fills the cache.
Without strict mode, the first embedded start in a test process may download.

**Bundles and licences.** PostgreSQL in the bundles is under the
[PostgreSQL licence](https://www.postgresql.org/about/licence/) (similar to
BSD/MIT). The pinned module's manifest lists bundle contents; `go-licenses` in
G1 must accept the module dependency.

**Reporting.** Every BDD run writes `.gate/bdd.json` before `TestFeatures`
returns, naming the server `embedded` or `url` only (never connection strings).
Artifacts include this file in `.gate/`.

## Gate report

CI uploads `.gate/` with the traceability JSON (`gate spec trace -json`), BDD
Postgres summary (`bdd.json`), and the human review queue, and prints the
queue in the run summary. Scenario
results per requirement, coverage, the mutation score and journey artifacts
join the report as their gates (G5 to G7) are enabled.
