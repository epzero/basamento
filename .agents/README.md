# Agent instructions, for every harness

`AGENTS.md` at the repository root holds the rules. `skills/` holds what one
kind of session does, as [Agent Skills](https://agentskills.io): one folder
per skill with a `SKILL.md`, which many coding agents discover on their own
and any agent can simply be told to read.

- `skills/lead-<maintainer>/`: the playbook of a maintainer's long-running
  lead (developer, reviewer, architect, designer).
- `skills/role-<role>/`: the instructions of one subagent session.
- `skills/tracker-<name>/`: how a lead works in one tracker, and the script
  that sets it up. `docs/engineering/tracker.json` says what any tracker
  must hold.

A harness that looks for skills elsewhere gets a symbolic link to `skills/`
(`.claude/skills`). A harness with its own format for agent definitions
(`.claude/agents/`) gets one thin file per role that names the harness and
loads the skill. The instructions are never copied. Setup and prompts:
`docs/engineering/agent-workflow.md` §6.
