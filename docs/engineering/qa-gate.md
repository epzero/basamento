# QA gate

One entry point, `make gate`, runs the same checks locally and in CI. A change
merges only when every blocking gate passes. Tools that are not installed
locally are skipped with a notice; CI sets `GATE_STRICT=1` and installs them
with `make tools`, which pins their versions in the `Makefile`.

**Go version.** CI installs the exact patch named in `go.mod` via
`actions/setup-go` and `go-version-file: go.mod` (ADR-0014). Locally, default
`GOTOOLCHAIN=auto` downloads that patch when the installed Go is older.

**Postgres (optional locally, required in CI).** The PR `gate` job runs Postgres
16 in a service container and sets `BDD_POSTGRES_URL`, so `@profile:cloud`
scenarios in G3 execute on every pull request (ADR-0014). A service container
is hermetic test infrastructure, not a live vendor call. Local `make gate` does
not require Docker; Postgres-tagged scenarios skip with reason
`postgres-unavailable` unless a server is provided:

```bash
docker run --rm -d --name basamento-gate-pg \
  -e POSTGRES_USER=kit -e POSTGRES_PASSWORD=kit -e POSTGRES_DB=kit \
  -p 5432:5432 postgres:16-bookworm
export BDD_POSTGRES_URL='postgres://kit:kit@localhost:5432/kit?sslmode=disable'
```

With `GATE_STRICT=1`, any skip tagged `postgres-unavailable` fails the run
(CI always sets strict mode). After G3, the gate prints
`gate: scenarios skipped: <total> (<reason>: <count>, …)` so skips are never
silent (`internal/bdd` records reasons; `tools/gate` aggregates the line).

| Gate | Checks | Blocks | Where |
| --- | --- | --- | --- |
| **G1 Static** | `gofmt`, `go vet`, `golangci-lint`, `govulncheck`, `CGO_ENABLED=0 go build`, `gate layering` (package graph against `layering.yaml`), `go-licenses` (no forbidden or restricted licenses), `buf lint`, `buf breaking` against the base (after publication) | yes | `make gate` |
| **G2 Spec integrity** | `gate spec lint` (format, tags, ids that match their primary requirement, traceability, no scenario on a retired requirement, expired quarantine); `gate spec lock` (approved at current text, valid identities, author ≠ reviewer, tier floors and notes, no human rejection, every human decision names a person); `gate docs` (generated module and roadmap diagrams are current) | yes | `make gate` |
| **G3 Behavior** | `go test -race ./...` including the BDD suite in strict mode; runtime and port conformance suites across the matrix (SQLite locally; Postgres 16 in CI via service container — ADR-0014; local + temporal-embedded, fileblob + S3-compatible as they land); replay determinism over recorded histories; fuzz and property tests for the flow machine and CEL | yes | `make gate` (Postgres optional locally); CI `gate` job + service |
| **G4 Red→green** | `gate redgreen -base <base>`: newly active scenarios fail at base and pass at head | yes | `make gate BASE=<ref>`: CI sets the base, agents and the lead use `BASE=main` |
| **G5 Test strength** | Mutation testing on changed packages (e.g. gremlins) above threshold; diff coverage as a secondary signal | yes once enabled (WP-L0 follow-up); nightly before | CI |
| **G6 System smoke** | Build the binary, start `kit serve --profile embedded` in a temp dir, run `@smoke` scenarios against the API | yes once `kit serve` exists (WP-L1-05) | CI |
| **G7 Journeys** | `@journey` scenarios replayed by the browser runner from recordings | UI changes only | CI + nightly |
| **G8 Review** | Agent code review by risk tier (`docs/engineering/code-review.md`): parallel finders, verified findings, important findings stop integration. High-tier changes need a person's approval before integration, P0 specs before a release (`agent-workflow.md` §5) | yes | The lead, or the pull request |

Release only: `gate release-check` fails while any P0 scenario lacks a
person's acceptance.

Nightly: `@live`, `@slow`, `@eval` (live), full mutation run,
a flake hunt (repeated runs that look for nondeterminism; a flaky scenario
is quarantined with an issue and an expiry), and **govulncheck on the newest
patch of the Go release line** named in `go.mod` (ADR-0014), so a new stdlib
advisory surfaces before a pull request must bump the `go` line.

## Gate report

CI uploads `.gate/` with the traceability JSON (`gate spec trace -json`) and
the human review queue, and prints the queue in the run summary. Scenario
results per requirement, coverage, the mutation score and journey artifacts
join the report as their gates (G5 to G7) are enabled.
