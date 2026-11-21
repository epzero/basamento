# QA gate

One entry point, `make gate`, runs the same checks locally and in CI. A change
merges only when every blocking gate passes. Tools that are not installed
locally are skipped with a notice; CI sets `GATE_STRICT=1` and installs them
with `make tools`, which pins their versions in the `Makefile`.

| Gate | Checks | Blocks | Where |
| --- | --- | --- | --- |
| **G1 Static** | `gofmt`, `go vet`, `golangci-lint`, `govulncheck` in every module root (`make vuln` / nightly **line** job, `ADR-0014`), `CGO_ENABLED=0 go build`, `gate layering` (production and test imports from `go list`, plus imports in build-excluded `.go` files under the module root, all against `layering.yaml`; `ADR-0018`; also fails when a `deny_external` or `test_only_allowed_from` key names no layer), `gate hygiene` (test determinism, production isolation and, with `BASE`, spec/code separation; see [Hygiene](#hygiene)), `gate gomod` (every tracked `go.mod` names the same `go 1.M.P` line, `ADR-0014`), `go-licenses` (no forbidden or restricted licenses), `buf lint`, `buf breaking` against the base (after publication) | yes | `make gate` |
| **G2 Spec integrity** | `gate spec lint` (format, tags, ids that match their primary requirement, traceability, no scenario on a retired requirement, expired quarantine); `gate spec lock` (approved at current text, valid identities, author ≠ reviewer, tier floors and notes, no human rejection, every human decision names a person); `gate docs` (generated module and roadmap diagrams, `CODEOWNERS`, and `specs/README.md` capability statuses are current) | yes | `make gate` |
| **G3 Behavior** | `go test -race ./...` including the BDD suite in strict mode; runtime and port conformance suites across the matrix (SQLite + Postgres, local + temporal-embedded, fileblob + S3-compatible) as they land; replay determinism over recorded histories; fuzz and property tests for the flow machine and CEL. Postgres for the SQL dialect pass: [ADR-0015](../adr/0015-how-the-pr-gate-runs-postgres.md) (`BDD_POSTGRES_URL`, embedded 16, `make pg-prefetch`). | yes | `make gate` (+ CI matrix) |
| **G4 Red→green** | `gate redgreen -base <base>`: newly active scenarios fail at base and pass at head | yes | `make gate BASE=<ref>`: CI sets the base, agents and the lead use `BASE=main` |
| **G5 Test strength** | Mutation testing on changed packages (e.g. gremlins) above threshold; diff coverage as a secondary signal | yes once enabled (WP-L0 follow-up); nightly before | CI |
| **G6 System smoke** | Build the binary, start `kit serve --profile embedded` in a temp dir, run `@smoke` scenarios against the API | yes (WP-L1-05) | CI |
| **G7 Journeys** | `@journey` scenarios replayed by the browser runner from recordings | UI changes only | CI + nightly |
| **G8 Review** | Agent code review by risk tier (`docs/engineering/code-review.md`): parallel finders, verified findings, important findings stop integration. High-tier changes need a person's approval before integration, P0 specs before a release (`agent-workflow.md` §5) | yes | The lead, or the pull request |

## Hygiene

`gate hygiene` enforces the mechanical rules before review:

- **Test determinism.** `*_test.go` files and fake packages from
  `layering.yaml`'s `test_only` (for example `core/coretest`) take time from
  `core/coretest`: no `time.Now`, `time.Since`, `time.Until`, `time.Sleep`,
  `time.After`, `time.AfterFunc`, `time.NewTimer`, `time.NewTicker`,
  `time.Tick`, `crypto/rand` or the global `math/rand` functions.
  `context.WithTimeout` and `context.WithDeadline` stay allowed: they read the
  wall clock, but a test uses them only as a hang guard, never as a source of
  time.
- **Production isolation.** Production code does not import `testing` or
  `testing/*`, and exports no identifier whose name matches `ForTest…`. The
  check is by name (`ForTest`, `ForTests`, `ForTesting`), so a hook under
  another name is still the reviewer's to find. Packages in
  `test_only_allowed_from` (the BDD harness) are neither fakes nor production:
  their non-test `.go` files skip both checks, so ADR-0015's `crypto/rand`
  Postgres password and harness `ForTest` helpers are allowed.
- **Spec/code separation**, with `-base <ref>`: a change that touches any file
  outside `specs/` and `docs/` does not edit `specs/<capability>/design.md`
  (`agent-workflow.md` §7.3). A design that turns out wrong or incomplete
  during implementation goes back to the designer as a `specs-needed` ticket.

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
the **line** job above, and a flake hunt (one `go test -race -count=10 -timeout 90m
./...` run that repeats every test ten times to look for nondeterminism; the
timeout is explicit because it applies per test binary and the default 10 minutes
is shorter than ten BDD suite runs under `-race`; a flaky scenario is quarantined
with an issue and an expiry).
**flake-hunt** and **slow-lanes** set `GATE_STRICT=1`, restore the embedded
Postgres bundle cache and run `make pg-prefetch` before tests so a missing
supervisor fails the job instead of skipping Postgres runs. In `internal/bdd`,
each repetition of `TestFeatures` resets and starts its own embedded server so
every `go test -count=N` repetition runs the Postgres dialect pass.
The **release-readiness** job uses `continue-on-error` and is informational
only (P0 backlog is expected); **line** is the blocking nightly signal.

## Who reads the nightly

The scheduled **nightly** workflow runs what must not stay silent:
**line** (`govulncheck` on the newest patch of the pinned Go minor,
`ADR-0014`), the flake hunt, the slow lanes and the release-readiness
summary.

The reviewer lead reads the schedule on every wake (first step of
`.agents/skills/lead-reviewer/SKILL.md`). `pipefail` applies only inside the
subshell below so a failing `gh` is not hidden and the lead's shell is unchanged:

```bash
( set -o pipefail
  gh run list --workflow nightly.yml --event schedule --limit 5 \
    --json conclusion,status,createdAt,url | go tool gate nightly
)
```

`go tool gate nightly` classifies the newest **completed** scheduled run
(ignoring runs still queued or in progress). **Stdout** is one summary line;
unusable input prints `nightly: ...` on **stderr** and exits **2**.

| Exit | State | Meaning | Lead action |
| --- | --- | --- | --- |
| 0 | **ok** | Newest completed run concluded `success` and was created within the last **36 hours** | None |
| 1 | **failed** | Newest completed run concluded anything other than `success` | Ticket for the architect (below) |
| 1 | **stale** | No completed run (`nightly: stale: no scheduled run` when the list is empty or only queued/in-progress), or the newest completed one was created more than **36 hours** ago | Ticket for the architect (below) |
| 2 | — | Empty or invalid JSON, or `gh` failed before a classified outcome | Retry once; then ticket `nightly reader: cannot read the schedule` |

Example stdout line:

`nightly: failed: run 44516903074 https://github.com/epzero/basamento/actions/runs/44516903074 created 2026-11-19T11:40:22Z`

On exit **1**, the reviewer lead opens one ticket for the architect unless an
**open** ticket titled `nightly failed: …` or `nightly stale: …` already
exists — dedupe on that **prefix**, not on the run id, so one ongoing failure
keeps one ticket; the exact stdout line goes in the body. The title is
`nightly failed: run <id>`, `nightly stale: run <id>`, or `nightly stale: no
scheduled run`. Hand-off: `design-needed`, state *Todo*, assign the architect
(`agent-workflow.md` §3).
The architect triages the failure and opens a developer ticket for the fix or
answers it on the ticket. On any other exit, retry once in the same wake; if
it still fails, one architect ticket titled `nightly reader: cannot read the
schedule` (dedupe while an **open** ticket has that title; stderr in the body).

**Cost of this choice:** a failure that happens in a quiet period is seen at
the next wake, not when the workflow finishes. Because only **completed** runs
count, while a long run or re-run is in flight the reader reports on the
previous completed run (it can say **ok** while a re-run of a failed workflow
is still running, after the failure was ticketed on first sight). It says
**stale** when the newest completed run is older than 36 hours even if a newer
run is still unfinished; a run still queued or running about 12 hours after it
was created is itself worth the architect's look (the next run is due 24 hours
after the previous one, and the reader says stale at 36 hours).

## Postgres in G3 (ADR-0015)

The SQL conformance driver's **Postgres pass** (not every `@profile:cloud`
scenario) needs a server once per `TestFeatures` run in `internal/bdd` (G3);
each `go test -count=N` repetition of that test starts its own embedded
server.
`gate redgreen` starts embedded again per activated scenario (G4); each base
worktree run reuses the head checkout's built supervisor (`EP_SUPERVISOR`) and
inherits `BDD_POSTGRES_CACHE_DIR`, matching the G3 BDD step environment. A base
run is the red proof only when it fails on the behavior: `gate redgreen` fails
closed when the base run cannot start Postgres (`embedded postgres
unavailable`, `BDD_POSTGRES_URL is set but the server is unavailable`), fails
the strict skip rule, or exits zero without a passed step (it reads the base's
`.gate/bdd.json` when the base writes one, and otherwise the step summary).
Each names `make pg-prefetch` as the remedy instead of passing as a vacuous red
or as "passes at base".

**Precedence.** A non-empty `BDD_POSTGRES_URL` uses only that admin server
(typically `/postgres`); unreachable or unauthorised URLs never fall back to
embedded. The role must hold `CREATEDB`: after the ping, init asks
`pg_roles` for `rolcreatedb` and an admin role that cannot create databases is
**unavailable** (one skip, or one strict failure), never a first-scenario
`42501`. An empty or unset variable starts embedded Postgres **16** (`V16` /
16.15.1) via `github.com/fergusstrange/embedded-postgres/v2` with `Port(0)`, a
random password per process (128 bits from `crypto/rand`, never the PID), and
loopback only. Under `GATE_STRICT=1`, any
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
role as pulling a CI image). The prefetched bundle version is the driver's pin
(`embeddedpostgres.V16`); a test in `internal/gate` fails the gate when the
recipe's literal drifts from that constant, and the CI cache key derives its
module version from `go.mod` with `go list -m`, never from a hand-written copy.
With `GATE_STRICT=1`, the BDD step sets offline
mode and never downloads; a cold cache fails until prefetch fills the cache.
Without strict mode, the first embedded start in a test process may download.

**Cache trust.** A cache hit trusts the extracted bundle directory through the
library's completion digest marker; the module re-hashes only on download, so
`BDD_POSTGRES_CACHE_DIR` must be writable by the gate's user only (a shared or
world-writable cache directory is an injection point, not just a correctness
risk).

**Bundles and licences.** PostgreSQL in the bundles is under the
[PostgreSQL licence](https://www.postgresql.org/about/licence/) (similar to
BSD/MIT). The pinned module's manifest lists bundle contents; `go-licenses` in
G1 must accept the module dependency.

**Reporting.** Every BDD run writes `.gate/bdd.json` before `TestFeatures`
returns, naming the server `embedded` or `url` only (never connection strings).
Artifacts include this file in `.gate/`.

## Conflict markers

`gate hygiene` also fails a tracked text file that holds a merge conflict
marker at the start of a line. A marker is an opening line of seven or more
`<`, or a closing line of seven or more `>`, followed by a space and the label
git writes (`HEAD`, a branch, or a revision and its subject); or a line of
nothing but `=`, seven or more. A diff3 conflict's `|||||||` base line needs
no rule of its own: the opening line above it fails the file anyway.

Two line shapes pass, and neither can hide a conflict. A line of only `=`
directly under a non-blank line in a Markdown file (`.md`, `.markdown`) is a
setext heading underline, not a separator; the opening and closing lines of
the conflict that holds it still fail the file. A marker inside a running line
passes too, since only column one counts: a document may quote one in a
sentence or an indented block.

Every file `git ls-files` lists is read, in every module: Markdown, YAML,
`.feature`, migrations and data files such as `go.sum` are covered, and an
untracked file is covered once it is added. A file is skipped only for its
content or its type, never for its extension or directory: a NUL byte in its
first 8000 bytes makes it binary (git's own test); a tracked symbolic link is
not read, since the files it points to are tracked on their own and following
it could leave the tree. A line's trailing carriage return is ignored.

The whole tracked tree is read, not just the diff, so a marker inherited from
the base branch fails the next `make gate` even when the change does not touch
the file; the check runs as part of `gate hygiene`, locally and in CI, so any
hit blocks the merge.

A file that must show a marker, such as a fixture testing this check, is named
in the rule's glob allowlist under `internal/gate`, a reviewed change to the
gate; there is no directive inside the file, no exempt extension and no exempt
directory. No path is exempt today: the rule's own tests build their trees in
`t.TempDir`, which is untracked. The check reads files only, with no clock and
no network, and reports one `path:line` per hit in sorted order, so the same
commit always gives the same list.

## Gate report

CI uploads `.gate/` with the traceability JSON (`gate spec trace -json`), BDD
Postgres summary (`bdd.json`), and the human review queue, and prints the
queue in the run summary. Scenario
results per requirement, coverage, the mutation score and journey artifacts
join the report as their gates (G5 to G7) are enabled.
