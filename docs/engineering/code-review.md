# Code review

Agents review every change before it reaches `main`. The aim is the least
human time per change without more reverts. Review effort follows risk, not
lines read; findings are verified before anyone acts on them; only real
problems block; and every mistake that gets through improves the checks.

## 1. Before the pull request: the author's self-review

Most review happens before anyone else looks, on the author's machine.

- The implementer runs `make gate BASE=main` and shows the evidence: the
  commands it ran and their output, not a claim that it is done.
- The author's lead then runs this review (§2 to §4) with its own code
  reviewer sessions, using the tier's finders, and sends the surviving
  findings back to the implementer. It uses at most one verifier per
  finding: one at high and medium risk, none at low, as the low row in §2
  says (§4). The reviewer's own review re-checks every change, at full
  depth for high risk, so a finding wrongly dropped here gets a second,
  stronger check before merge. From the second round on the review is
  incremental (§6): each open finding, and the whole diff since the last
  reviewed commit.
- After **three rounds** the lead stops spawning: it reads what is still
  open itself and settles each finding, fixed or dropped with a reason.
- Only then is the pull request opened. Its description says what the
  self-review found and how each finding was settled, so the reviewer does
  not raise it again.
- A change of direction is raised with the lead (`needs-human`) before
  implementing, not as a blocking comment on a finished change.

The reviewer's review of the pull request (`agent-workflow.md` §7.2) is the
second, independent one. It follows the same sections. The reviewer's lead
may read a low-risk change itself and uses code reviewer sessions for the
rest. It reads the change on GitHub and checks the branch out only when it
needs the code around the diff. **It does not run the gate or the test
suite: CI does**, and a pull request with a red `gate` goes back unread. A
verifier may still run one command to prove a finding.

## 2. Plan the review

```bash
go tool gate review plan -base main -head <branch>
```

Without `-head` it plans the working tree. The change is what the branch
added since it left the base, so commits that landed on the base in the
meantime do not count. It prints:
- the change's **risk tier**: the highest tier of any path it touches,
  defined in `review.yaml`;
- every changed file with its tier;
- the `REVIEW.md` files that apply.

Reviewers take `review.yaml` and `REVIEW.md` from the **base branch**, so a
change cannot lower the bar it is reviewed against. Changing them is itself a
high-risk change.

The tier is the change's **risk**. The author uses it only to choose the
depth of the self-review and opens the pull request as `risk: TBD`.

**The reviewer sets the risk, not the author**: it replaces the label with
the tier the tool reports (`agent-workflow.md` §5), and may raise the tier of
a change that is riskier than its paths suggest (a retry policy in an
ordinary package, a new dependency), never lower it.

| Tier | Typical paths | Depth | Person |
| --- | --- | --- | --- |
| **high** | CI, the gate, `Makefile`, lint configuration, contracts, auth, secrets, the model node (money), runtimes, rule files (`AGENTS.md`, the constitution, ADRs, `layering.yaml`, `review.yaml`, `REVIEW.md`, agent role definitions), `go.mod` | Deep: five finders, three verifiers per finding in the reviewer's review (at most one in the self-review, §1), a separate security pass | Yes: the owner's approval in the tracker before it is merged (`agent-workflow.md` §5) |
| **medium** | Other Go code, specs (requirements, scenarios, glossary, approval ledgers: their own tiers decide when a person looks, `spec-review.md` §4) | Standard: four finders, one verifier per finding | No |
| **low** | Other docs | Quick: one finder, no verifier (in the self-review too) | No |

Each session's effort follows `agent-workflow.md` §2.3: finders are heavy at
high and medium risk and standard at low risk; verifiers are standard at high
risk and light at low and medium risk; a round-2+ re-review stays standard.

An agent's approval never counts as a person's. Medium changes need no
person because people here are scarce; the revert rate (§7) is the guardrail,
and medium moves to a person's review if it rises.

## 3. Find

Finders are code reviewer subagents spawned by a lead (`agent-workflow.md` §2),
each with one lens, all working in parallel on the same diff and the work
package's stated intent:

1. **Spec and contract:** does the change solve its problem, do what its
   scenarios say, and nothing an approved scenario, ADR or contract forbids?
   For a bug fix, does it fix the root cause?
2. **Correctness:** logic, error paths, boundaries, concurrency, resource leaks.
3. **Determinism and durability:** flow code, retries, idempotency, replay.
4. **Security:** secrets, tenancy, untrusted input, permissions.
5. **Simplicity and guidelines:** `coding-guidelines.md`, needless code, stale
   comments and docs, and lines in `AGENTS.md` or docs the change makes untrue.

Standard depth uses lenses 1–4; quick depth uses lens 1; deep depth uses all
five. Every finder reads the applicable `REVIEW.md` files. A finding names the
file and line, the problem, a concrete failure scenario, and a proposed fix.
When the problem is a class of bug, the finding says so and lists every
instance, so the author fixes them all at once.

## 4. Verify

Each finding goes to separate **verifier** sessions that did not produce it.
A verifier confirms a finding only with evidence:
- a failing test or command;
- a scenario it breaks;
- or a code path it can cite step by step.

A finding survives when a majority of its verifiers confirm it: two of three
in the reviewer's deep review, one of one at standard depth and for the one
verifier the self-review gives it at high and medium risk. At low risk
neither review uses a verifier. Unconfirmed findings and findings
only about style, formatting or optional cleanup are dropped without being
reported, which keeps noise out of the author's way.

## 5. Report

One review is reported per round. In the self-review the lead records it as
the implementer's hand-off note (`code-changes-requested`). On the pull
request the reviewer posts it as one GitHub review, each important finding
as a thread on its line, and summarises it in the ticket's hand-off comment
(`agent-workflow.md` §3). It starts with the number of findings and says
plainly when there is nothing to fix, names the commit it reviewed, then
lists surviving findings by severity:

| Severity | Meaning | Effect |
| --- | --- | --- |
| **important** | Introduced by this change: a bug, a spec or contract violation, a security problem, data or money at risk, a missing test | Blocks until fixed, or resolved with a reason the reviewer accepts |
| **nit** | Worth fixing, harmless if not | Never blocks; at most three per review |
| **pre-existing** | A real problem the change did not introduce | Never blocks; filed in the tracker as a ticket of its own |

Every finding is answered: fixed, or resolved with a reason (wrong,
intentional, not worth it). A fix in a later commit resolves it.

**An approval covers one commit.** New commits after approval are reviewed
again (the diff since the approved commit). The merge queue's rebuild on the
latest `main` is not a new commit.

## 6. Iterate

- The author fixes or answers every important finding, then asks again. From
  the second round on, the review covers each finding still open and the
  whole diff since the last reviewed commit. Fixes introduce new defects, so
  it is not limited to the lines a finding named, and it does not re-run the
  round-1 fan-out of lenses on unchanged text. A finding already posted or
  resolved is never repeated.
- After **three rounds** on the pull request without approval the loop
  stops with `needs-architect`: the architect approves the change or
  proposes another direction for the owner to decide on
  (`agent-workflow.md` §5). The rounds of the self-review (§1) do not count.
- Keep changes **small**: one behavior or one refactoring per change, ideally
  under about 400 changed lines excluding specs and generated files. Small
  changes are easier to review, to debug and to revert.
- Every change says how it was tested, its risk, and how to revert it (the
  pull request template and the report in `AGENTS.md`).

## 7. Improve the reviewer

- **Fix at the source.** When reviews keep raising the same thing, move it
  into a `REVIEW.md`, a lint rule, a gate check, a scenario or `AGENTS.md`.
- **Escaped bugs.** A revert or a bug found after integration gets a note on
  which layer should have caught it (and the `escaped` label where a tracker
  is used) and a fix at the source. Escaped cases are the reviewer's eval
  set: rerun them when reviewer instructions change.
- **Noise and convergence.** The reviewer's lead records each finding's outcome (fixed,
  wrong, intentional, not worth it) and the rounds a change took. Many wrong
  findings or rising rounds mean the lenses, specs or `REVIEW.md` files need
  work.

## 8. Where people stay

The owner decides whether to build something, accepts high-risk changes and
technical designs, reads the evidence rather than every line, and owns the
outcome. All of it happens in the tracker (`agent-workflow.md` §5).
