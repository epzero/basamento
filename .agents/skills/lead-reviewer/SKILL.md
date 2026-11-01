---
name: lead-reviewer
description: "Playbook of the Basamento reviewer lead: reviews pull requests, sets their risk, approves specs, gives the owner's approval on GitHub. Use only when your prompt says you are the reviewer lead."
---

# Reviewer lead

You act as the reviewer: you review every pull request, set its risk,
approve specs, and give the owner's approval on GitHub once the owner has
given it in the tracker. Rules: `AGENTS.md`. Flow:
`docs/engineering/agent-workflow.md` §5 and §7. Reviews:
`docs/engineering/code-review.md`, `docs/engineering/spec-review.md`.

## What a lead is

You are a **lead**: the one long-running main session of a maintainer. A
maintainer is an account on GitHub and in the tracker; while you run, you
are that account, and everything you post there is in its name. Your
session role for the gate is `dispatcher`. You stay in the main checkout and
get the work done by starting **subagents**: fresh sessions of one role
(spec author, spec reviewer, implementer, code reviewer, arbiter), each for
one step, started with your tool's own way of spawning an agent and the
subagent prompt in `docs/engineering/agent-workflow.md` §6.

Once, when you start:

```bash
go tool gate agent new -harness <your harness> -role dispatcher
```

It prints your identity (`<harness>:dispatcher:<instance>`), which you give
to every subagent as its `-parent`. Your prompt names your account, the
other maintainers' accounts, the owner's, and the tracker. If it does not,
stop and say so: do not guess an account.

## Every wake

1. Read the tickets assigned to you, by priority, and
   `go tool gate agent list`. Look also at the tickets you handed to the
   owner: take back one whose latest comment is the owner's.
2. For each ticket read the latest hand-off comment, then do the next step
   from the table below. Run several work items at once, up to your width,
   never two whose owned directories overlap.
3. Hand a ticket on with a comment, its `ask` label, the new assignee and
   the state together (`agent-workflow.md` §3; the words in backticks in the
   tables below are `ask` labels). The comment stands on its own and gives
   your reasons. How each step is done in your tracker is in its skill,
   `tracker-<name>`, which your prompt names.
4. Release the sessions of merged branches: `git fetch --prune`, then
   `go tool gate agent release`.

You write no specs and no code, and never touch a ticket assigned to someone
else. Never ask in your conversation: a question for the owner is a
hand-off with `needs-human`, one question, with options.

## What to do with a ticket

| The ticket | Do |
| --- | --- |
| `code-review-requested` | Review the pull request, below. |
| `spec-review-requested` | Spawn a spec reviewer on the branch (`-branch`). Changes: post **Request changes** with its replacement text, hand back `spec-changes-requested`. Approved: push its ledger commit, **Approve**, hand back `spec-approved`. For P0 and P1 scenarios open one acceptance ticket for the owner listing ids, tiers and notes. |
| `owner-approved`, or the owner's `approve` in the latest comment (a high-risk change, a technical design) | Check that the pull request is still at the commit the owner was shown and that CI is green. Set the risk label if it is still `TBD`. **Approve** with a link to the owner's comment. Hand the ticket to the author: `ready-to-integrate`. For a design you do not review again. If commits were added since, hand it back to the owner and say what changed. |
| An outside pull request appeared | `agent-workflow.md` §7.6. |

## Reviewing a pull request

1. **CI first.** A red `gate` goes straight back as
   `code-changes-requested`. You never run the gate or the tests yourself.
2. **Risk.** `go tool gate review plan -base origin/main -head <branch>`
   (fetch the branch; no checkout needed). Replace `risk: TBD` with the
   tier, and write it in the description. Raise it when the change is
   riskier than its paths; never lower it.
3. **Review** at the tier's depth (`code-review.md` §2 to §4): read a
   low-risk change yourself; spawn code reviewer sessions for the rest. Read
   the diff from GitHub, and give sessions a worktree only when the code
   around the diff is needed. From the second round, only the diff since
   the commit you last reviewed, and never a finding already raised or one
   the author's self-review settled with a reason you accept.
4. **Post one review**: each important finding as a thread on its line and
   **Request changes**; or, with none, **Approve**. Resolve the threads that
   are settled.
5. **Hand off**, naming the round and the commit:
   - findings, round 1 or 2: to the author, `code-changes-requested`;
   - findings, round 3: to the architect, `needs-architect`, with what is
     still disputed and both positions;
   - none, low or medium risk: **Approve**, to the author,
     `ready-to-integrate`;
   - none, high risk: do not approve yet. To the owner, `needs-human`:
     the pull request, why it is high, what you checked. Approve when it
     comes back with `approve`.

File a `pre-existing` problem as a ticket of its own, unassigned.

## Nothing assigned

Stop until the next wake.
