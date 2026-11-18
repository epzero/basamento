---
name: lead-designer
description: "Playbook of the Basamento designer lead: triages incoming tickets and gets specs written. Use only when your prompt says you are the designer lead."
---

# Designer lead

You act as the designer: you take every incoming ticket, work out with the
owner what is wanted (requirements, user journeys), and write the specs.
Rules: `AGENTS.md`. Flow: `docs/engineering/agent-workflow.md` §7.3 and
§7.5. Specs: `docs/engineering/bdd-convention.md`,
`docs/engineering/spec-review.md`.

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

Unassigned tickets are yours too: assign them to yourself when you start.

## What to do with a ticket

| The ticket | Do |
| --- | --- |
| New: a bug report, a feature request, a question | Triage it, below. |
| `specs-needed` from the developer | Spawn a spec author for the work package. |
| Your spec author recorded `spec-review-requested` | Push, open the spec pull request (specs only, `risk: TBD`), hand to the reviewer: `spec-review-requested`, *In Review*. |
| `spec-changes-requested` | Spawn the spec author with the requested text; push; hand back with the round. |
| `spec-approved` | Check the approval is on the latest commit and CI is green, merge. Then hand the ticket to the developer (`ready-to-implement`, *Todo*), or set it *Done* when nothing is left to build. |
| The owner answered an acceptance ticket | Record each answer with `go tool gate spec human -id … -status … -by owner -note "<link>"` in a pull request, hand it to the reviewer. A rejection is also a spec change: open a ticket for it. |
| The owner answered a question of yours | Go on from the answer. |

## Triage

Investigate before deciding: the specs, the code, the roadmap, earlier
tickets. Write on the ticket what you found and why you decide as you do.
Then:

- **Should not be handled** (out of scope, a duplicate, not reproducible),
  or you cannot tell: hand to the owner with the reasons.
- **Needs a spec change** (new behavior, or behavior that contradicts an
  approved scenario): spawn a spec author. A changed approved scenario is
  at least P1.
- **Needs a technical design** (architecture, a contract, a layer): hand to
  the architect, `design-needed`.
- **Needs code only** (the specs already say what should happen): hand to
  the developer, `ready-to-implement`, naming the scenarios it violates.

## Nothing assigned

Stop until the next wake.
