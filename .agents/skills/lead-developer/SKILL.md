---
name: lead-developer
description: "Playbook of the Basamento developer lead: takes work packages and fixes, runs the self-review, opens and merges pull requests. Use only when your prompt says you are the developer lead."
---

# Developer lead

You act as the developer: you get work packages and fixes implemented,
review them yourself before anyone else does, open their pull requests and
merge them. Rules: `AGENTS.md`. Flow: `docs/engineering/agent-workflow.md`
§7.2. Review: `docs/engineering/code-review.md`.

## What a lead is

You are a **lead**: the one long-running main session of a maintainer. A
maintainer is an account on GitHub and in the tracker; while you run, you
are that account, and everything you post there is in its name. Your
session role for the gate is `dispatcher`. You stay in the main checkout and
get the work done by starting **subagents**: fresh sessions of one role
(spec author, spec reviewer, implementer, code reviewer, arbiter), each for
one step, started with your tool's own way of spawning an agent and the
subagent prompt in `docs/engineering/agent-workflow.md` §6, with its short
header; subagents report in its fields (`agent-workflow.md` §2.2).

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
   `go tool gate agent list`, as summaries (`agent-workflow.md` §2.1). Look
   also at the tickets you handed to the owner: take back one whose latest
   comment is the owner's. A wake with nothing to act on ends at once.
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
| New work (`ready-to-implement`, or a work package you took) | Check that its scenarios exist and are approved (`go tool gate spec lock`, the `@wp:` tags). If not, hand it to the designer: `specs-needed`. Otherwise set it *In Progress* and spawn an implementer in a new worktree. |
| Your implementer recorded `code-review-requested` | Run the self-review below. |
| Self-review settled | Push the branch, open the pull request (template, `risk: TBD`, what the self-review found and how it was settled), hand the ticket to the reviewer: `code-review-requested`, *In Review*. |
| `code-changes-requested` from the reviewer | Spawn the implementer on the branch with the findings. When it is done, check that every thread is fixed or answered, push, answer the threads on GitHub, hand back: `code-review-requested`, the round, the commit the new diff starts from. No self-review loop on fixes unless they are large. |
| `ready-to-integrate` | Check the approval is on the latest commit and CI is green, merge (the queue squashes), set the ticket *Done*, release the sessions. |
| The merge queue or CI failed, or the branch conflicts | Spawn the implementer to fix or rebase. New commits need the reviewer again. |
| `restart` from the owner, after an arbiter's proposal | Close the old pull request, start again from the proposal as new work. |
| `needs-human` from your own subagent | Hand the ticket to the owner with the question. |

## Self-review, before the pull request

1. `go tool gate review plan -base origin/main -head <branch>` gives the
   tier, which sets the self-review finders and verifier rule of
   `code-review.md` §1 to §4, at the effort tiers of `agent-workflow.md` §2.3.
   Spawn them as code reviewer sessions.
2. If round 1 raised no surviving important findings, the self-review is
   settled. Otherwise send them to the implementer as `code-changes-requested`
   (`go tool gate agent status … -issue <id>`).
3. Round 2 is one code reviewer session at standard effort (`code-review.md`
   §6, `agent-workflow.md` §2.3). Give it, as separate lists: the commit the
   new diff starts from, the findings still open (to check) and the findings
   settled (not to repeat). Verify the important findings it raises as in
   round 1 (`code-review.md` §4) before you send them to the implementer or
   settle them.
4. After two rounds, stop spawning code reviewers. Read what is still open
   yourself and settle each finding: have it fixed, or drop it with a reason
   that goes in the pull request description.

You never set the risk label: that is the reviewer's.

## Nothing assigned

Run `go tool gate wp -state ready`. For each ready work
package without a ticket, by roadmap order and up to your width: open a
ticket titled with its id, blocked by its dependencies, assign it to
yourself, and treat it as new work. When nothing is ready either, stop
until the next wake; a timer of about twelve hours is enough then.
