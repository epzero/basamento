# Agent operating manual

This repository is built by coding agents that run unattended. The owner
sets direction, reviews specs by priority and owns decisions, all through
the tracker; agents draft specs, review each other's specs,
implement, and prove their work through the gate. This file is the contract
for every agent session. If it conflicts with anything else, it wins, except
for `specs/constitution.md`.

## Read first

1. [`specs/constitution.md`](specs/constitution.md): non-negotiable principles.
2. [`docs/architecture/overview.md`](docs/architecture/overview.md): layers, ports, profiles, runtimes.
3. [`docs/engineering/bdd-convention.md`](docs/engineering/bdd-convention.md): how specs and scenarios are written and executed.
4. [`docs/engineering/qa-gate.md`](docs/engineering/qa-gate.md): what `make gate` checks.
5. [`docs/engineering/agent-workflow.md`](docs/engineering/agent-workflow.md): sessions, maintainers and their leads, the tracker, who decides, pull requests.
   A lead also follows its playbook, the skill `lead-<maintainer>`, and a subagent its role, the skill `role-<role>`, both in [`.agents/skills/`](.agents/skills/).
6. [`docs/engineering/coding-guidelines.md`](docs/engineering/coding-guidelines.md): how code and comments are written here.
7. The work package you were assigned, in its epic file under [`docs/roadmap/`](docs/roadmap/README.md), its specs under `specs/<capability>/`, and the ADRs it cites.

## Roles

| Role | Does | Never does |
| --- | --- | --- |
| **Spec author** | Drafts EARS requirements and Gherkin scenarios, `design.md` if needed. | Approves its own specs. |
| **Spec reviewer** | Reviews with the rubric in `docs/engineering/spec-review.md`, approves via `gate spec approve` with a tier note. | Edits the spec it reviews. Approves specs it authored. |
| **Implementer** | Removes `@planned`, writes step definitions and code until the gate is green. | Edits approved scenario text, requirement text or the lock. |
| **Code reviewer** | Finds or verifies problems in a diff as described in `docs/engineering/code-review.md` (risk tiers, `REVIEW.md` files, verified findings, severities, every finding answered). | Reports unverified findings. Merges with red gates. |
| **Arbiter** | For the architect, when the reviewer has not approved after three rounds: approves the change, or proposes another direction for the owner to decide on. | Writes the spec or code it rules on. Approves a high-risk change: that is the owner's. |
| **Dispatcher** | A maintainer's lead: takes its tickets, spawns the sessions above one step at a time, hands tickets on in the tracker, acts on GitHub as its maintainer. | Writes specs or code. Approves a pull request its maintainer opened. |

These are session roles. Three other words are used throughout:

- A **maintainer** (developer, reviewer, architect, designer) is an account
  on GitHub and in the tracker, not a session.
- A **lead** is a maintainer's one long-running main session, with the role
  `dispatcher`. It acts as that account and starts the other sessions.
- A **subagent** is a session a lead started for one step, in one of the
  other roles. It reports to its lead and to nobody else.

The **owner** is none of them and decides only in the tracker
(`docs/engineering/agent-workflow.md` §2).

One session holds one role. Every session's first command names it and, unless
it is a dispatcher or already runs in its own worktree, creates its worktree:

```bash
go tool gate agent new -harness <claude-code|cursor|codex|…> -role <role> -issue <WP id> [-parent <lead identity>]
# identity <harness>:<role>:<instance>, branch <issue>-<instance>, worktree path → cd there
```

Then work only inside that worktree. Use the identity in approvals and
comments. A person is `human:<github-login>`; when no personal login is
given, the default owner identity is `human:epzero`. Never use model names. A lead session fans work out to
subagents as described in `docs/engineering/agent-workflow.md` §2.

## Lifecycle of a work package

0. **Claim.** Your lead gives you one step of one work item. Run
   `gate agent new … -issue <WP id>`; it puts you in the work item's
   worktree and prints the previous session's hand-off. Between maintainers
   the work moves as a ticket in the tracker: its assignee is whose turn it
   is (`docs/engineering/agent-workflow.md` §3).
1. **Spec.** The designer's spec author drafts or updates
   `specs/<capability>/` in a pull request of its own (`spec(<cap>): …`). New
   scenarios start with `@planned @wp:<WP>`.
2. **Review.** The reviewer's spec reviewer, a different session, reviews on
   that branch. The author iterates until approval (at most 3 rounds, then
   the architect decides).
   The reviewer runs `go tool gate spec approve …` with a tier note.
   Tiers decide when the owner looks later: P0 blocks release until accepted,
   others are queued.
3. **Implement.** Remove `@planned` from the scenarios you implement, add
   step definitions in `internal/bdd/steps/<domain>.go`, then the code. Write
   unit tests first for logic below the scenario level.
4. **Prove.** `make gate BASE=main` locally. `BASE` adds the red→green check,
   which CI repeats against the base branch: every newly active scenario must
   fail on the base and pass on your head.
5. **Integrate.** The developer's lead reviews the change with its own code
   reviewers first, then opens a pull request marked `risk: TBD`. The
   reviewer sets the risk and reviews it. A low- or medium-risk change goes
   in on the reviewer's approval; a high-risk one needs the owner's `approve`
   in the tracker before the reviewer approves it. Whoever opened the pull
   request merges it, squashed
   (`docs/engineering/agent-workflow.md` §5, §7).
6. **Report.** The pull request description lists the WP, the scenario
   ids made active, the gate summary, how it was tested, what the self-review
   found and how to revert it. Every session ends with `gate agent status <status> -note …`,
   the hand-off to the next one.

## Commands

```bash
make gate                         # everything CI runs that needs only Go
make gate BASE=main               # the same, plus red→green against main
make tools                        # install the pinned optional tools (lint, buf, vuln, licenses)
make bdd                          # scenarios only
go tool gate spec lint       # spec format, tags, traceability
go tool gate spec lock       # approvals match current scenario text
go tool gate spec queue      # what the owner still needs to review
go tool gate wp              # every work package: done, in-progress, ready or blocked
go tool gate agent whoami    # this session's identity
go tool gate agent list      # all sessions, their last status, branches and worktrees
go tool gate agent status    # record how this session ended and the hand-off note
go tool gate review plan     # risk tier, review depth and REVIEW.md files of a change (-head <branch>)
go tool gate layering        # package graph
go tool gate hygiene         # test determinism, production isolation and conflict markers; with -base <ref> also spec/code separation
go tool gate nightly         # scheduled nightly ok, failed or stale (gh JSON on stdin)
go tool gate redgreen -base origin/main
```

## Hard rules

- **Do not change approved scenario or requirement text** to make a test
  pass. A behavior change is a spec change: new review, tier at least P1.
- **Do not add `@wip`, `@skip`, `@focus`.** Quarantine needs `@quarantine
  @issue:<key> @until:<date>`; it expires.
- **No sleeps, no wall clock, no global randomness in tests.** Use the fake
  clock and seeded randomness from `core/coretest`.
- **No test doubles in production code.** `core/coretest` and other
  test-only packages are importable only from `_test.go` and `internal/bdd`.
- **No `if testing`-style branches** or environment checks that change
  production behavior under test.
- **No live vendor calls in the gate.** Use simulators; live checks are
  `@live` and run nightly.
- **Golden files** change only with `-update` and a reason in the commit
  body.
- **Contracts are additive:** `buf breaking` must pass; error codes and event
  types are never renamed or removed.
- **Layering is law.** If `gate layering` fails, fix the design, not
  `layering.yaml`; changing the graph needs an ADR.
- **Stay in your WP's owned directories.** Touching another WP's directory
  needs a note in the PR and a reviewer from that area.
- **Docs move with code.** A change to behavior, interfaces or commands updates
  the affected doc in the same change.

## Definition of done

- Scenarios for the WP are active (no `@planned`), approved, and green.
- `make gate BASE=main` passes locally; CI gate passes, including red→green.
- The pull request is approved by someone other than its author, and merged.
- New exported identifiers have doc comments; packages have a `doc.go`.
- Docs are updated; the hand-off note carries the gate summary. The work
  package shows as done in `gate wp` once its pull request is merged.
- No new `TODO` without an issue reference.

## Commit messages

Conventional Commits with the capability as scope: `feat(stream): …`,
`fix(gate): …`, `spec(models): …`, `docs(adr): …`. The body says why, lists
scenario ids that changed state, and references the WP.
