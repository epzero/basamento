# QA gate

One entry point, `make gate`, runs the same checks locally and in CI. A change
merges only when every blocking gate passes. Tools that are not installed
locally are skipped with a notice; CI sets `GATE_STRICT=1` and installs them
with `make tools`, which pins their versions in the `Makefile`.

| Gate | Checks | Blocks | Where |
| --- | --- | --- | --- |
| **G1 Static** | `gofmt`, `go vet`, `golangci-lint`, `govulncheck`, `CGO_ENABLED=0 go build`, `gate layering` (package graph against `layering.yaml`), `gate gomod` (every tracked `go.mod` names the same `go 1.M.P` line, `ADR-0014`), `go-licenses` (no forbidden or restricted licenses), `buf lint`, `buf breaking` against the base (after publication) | yes | `make gate` |
| **G2 Spec integrity** | `gate spec lint` (format, tags, ids that match their primary requirement, traceability, no scenario on a retired requirement, expired quarantine); `gate spec lock` (approved at current text, valid identities, author ≠ reviewer, tier floors and notes, no human rejection, every human decision names a person); `gate docs` (generated module and roadmap diagrams are current) | yes | `make gate` |
| **G3 Behavior** | `go test -race ./...` including the BDD suite in strict mode; runtime and port conformance suites across the matrix (SQLite + Postgres, local + temporal-embedded, fileblob + S3-compatible) as they land; replay determinism over recorded histories; fuzz and property tests for the flow machine and CEL | yes | `make gate` (+ CI matrix) |
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
`govulncheck ./...`).

Nightly: `@live`, `@slow`, `@eval` (live), full mutation run,
the **line** job above, and a flake hunt (repeated runs that look for
nondeterminism; a flaky scenario is quarantined with an issue and an expiry).

## Gate report

CI uploads `.gate/` with the traceability JSON (`gate spec trace -json`) and
the human review queue, and prints the queue in the run summary. Scenario
results per requirement, coverage, the mutation score and journey artifacts
join the report as their gates (G5 to G7) are enabled.
