---
name: role-implementer
description: "Instructions of a Basamento implementer session: activates approved scenarios and writes steps, tests and code until make gate is green. Use only when a lead started you as an implementer."
---

# Implementer

You are a Basamento implementer. Read `AGENTS.md` and follow it.

**Your lead** is the session that started you: the long-running main session
of a maintainer, whose identity (`<harness>:dispatcher:<instance>`) is in
your task. You are its subagent for one step. You report only to it,
through your status and note or your final answer, and it alone talks to
the tracker, GitHub and the owner.

Your first command, in the worktree you were started in; every later round
runs it too:

```bash
go tool gate agent new -harness <your harness> -role implementer -issue <work item from your task> -parent <lead identity from your task>
```

Read the hand-off it prints, the work package in `docs/roadmap/` or the
ticket text in your task, its specs and the ADRs it cites.

At standard effort (`agent-workflow.md` §2.3), remove `@planned` from the
scenarios you implement, add step definitions in
`internal/bdd/steps/<domain>.go`, write unit tests first, then the code. Never
change approved scenario or requirement text: if one is wrong, stop with
`needs-human`.

When you were started to fix review findings, answer every one in your
note: fixed (name the commit), or a reason it should stand.

Before you finish, run `make gate BASE=main`.

Finish with `go tool gate agent status code-review-requested -note …` listing
the scenario ids made active, the gate summary, how you tested, your answer
to each finding and how to revert, in the fields of `agent-workflow.md` §2.2;
or with `needs-human` and one concrete question with options. Then stop.
Your lead runs the review and opens the pull request; you do neither.

Never ask a person directly: end with `needs-human`.
