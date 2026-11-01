---
name: tracker-linear
description: "How a Basamento lead works its tickets in Linear (find its tickets, hand one on, open one, link a pull request) and how a Linear team is set up for the workflow. Use when your prompt names Linear as the tracker."
---

# The tracker is Linear

`docs/engineering/agent-workflow.md` §3 says what a lead does in the
tracker, in words that fit any tracker. This is how each of those things is
done in Linear. Your prompt names the workspace, the team (by its key, such
as `BAS`) and the project; never work outside them.

Use your harness's Linear tools (usually Linear's MCP server, signed in as
your maintainer's account). The tool names below are that server's; another
client has the same operations under other names. Subagents do not touch
Linear: only the lead does.

| In the workflow | In Linear |
| --- | --- |
| Ticket | Issue, in your team and project |
| Assignee: whose turn it is | The issue's assignee |
| State | The issue's status, by the names in `docs/engineering/tracker.json` |
| The `ask` label | One label of the group `ask`; a group holds one label per issue, so setting a new one replaces the old |
| Hand-off comment | A comment on the issue |
| Blocked by | A "blocked by" relation |
| The pull request | A link on the issue; the GitHub integration adds it when the pull request's title or branch names the issue key, otherwise attach the URL |

## On every wake

1. **Your tickets:** `list_issues` with `assignee: "me"`, your team and
   project, by priority; skip the ones whose status is completed or
   canceled. Page until `hasNextPage` is false.
2. **Tickets you handed to the owner** (to take back one the owner answered
   without reassigning): `list_issues` with the owner as assignee and label
   `needs-human`, then `list_comments` on each: yours to take back if the
   latest comment is the owner's and your maintainer wrote the one before.
3. The designer also lists unassigned issues (`assignee: null`) in the
   team that are not completed or canceled.
4. For each issue: `get_issue`, then `list_comments` (the description does
   not include them). The latest hand-off comment says what is asked; the
   `ask` label should say the same. When they differ, the comment wins:
   the owner hands tickets on without always changing the label.

## Handing a ticket on

Three things, in this order, so that a failure leaves the ticket still
yours:

1. `save_comment` on the issue: the hand-off comment. Its first line is the
   `ask` word (and `round N` for a review), then what was done, what is
   asked and the links. Mention nobody with `@`: the assignment notifies.
2. `save_issue` with the issue's id and, in the same call, `assignee` (the
   next maintainer's account from your prompt), `state` (the status the
   `ask` has in `tracker.json`; leave it as it is where that is `null`) and
   `addLabels: ["<ask word>"]`.
3. `get_issue` again and check the assignee, the status and that exactly
   one `ask` label is on it. If the second call failed, repeat it; never
   post the comment twice.

To the owner the `ask` is always `needs-human`, and the comment holds one
question with options, or one decision to take with what you checked.

## Opening a ticket

`list_issues` with `query` first, so there is no second ticket for the same
work. Then `save_issue` without an id: `team`, `project`, `title` (a work
package's ticket is titled with its id and name, such as `WP-L2-01 CEL
environment`), a short `description` for people, `assignee` and `state`.
For a work package add a blocked-by relation to the ticket of each
dependency that `go tool gate wp` lists and that has one. Details only
agents need go in a collapsible `+++ Agent notes` section at the end of the
description.

## Setting a team up

Once per team, by a workspace admin, and again whenever
`docs/engineering/tracker.json` changes:

```bash
LINEAR_API_KEY=… python3 .agents/skills/tracker-linear/scripts/setup.py --team <KEY>           # shows what is missing
LINEAR_API_KEY=… python3 .agents/skills/tracker-linear/scripts/setup.py --team <KEY> --apply   # creates it
```

It adds the missing statuses, the `ask` label group with its labels and the
`escaped` label to that one team, and changes or deletes nothing. `--check`
exits with 1 when something is missing, which a lead runs when it starts if
it has a key. Not done by the script, because they are choices: inviting the
maintainers' accounts to the team, turning on the GitHub integration, and
the project itself.

A lead that finds a status or an `ask` label missing does not create it by
hand: it hands one ticket to the owner asking for the setup to be run.
