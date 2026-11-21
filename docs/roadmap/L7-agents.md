# L7 — Agents and tools

### WP-L7-01 — Agent loop
Depends: WP-L3-02, WP-L5-01.
Owns: `agent`.
One model turn per step; caps on steps, cost, tokens and time; handoff; context-window policy (memory policy).

### WP-L7-02 — Tools and MCP
Depends: WP-L7-01.
Owns: `tool`.
Tool port, allowlists, MCP client (`modelcontextprotocol/go-sdk`), sandbox port. Tools on other machines are served by workers (WP-L3-07).

### WP-L7-03 — Conversations
Depends: WP-L7-01, WP-L6-02, WP-L7-05, WP-L7-06.
Owns: `agent/conversation`.
Coordinator run per conversation with a child run per turn; queue, steer and cancel (ADR-0007).

### WP-L7-04 — Conversations on a durable runtime
Depends: WP-L7-03, WP-L5-03, WP-L6-03, WP-L3-04.
Owns: none.
Proves at `@level:system` that a conversation on the `temporal` runtime, served by `kit serve --profile embedded`, resumes its turn and queued messages after the process is killed and restarted on the same data directory, with every user and model message recorded and streamed once. Specs: `specs/conversations/crash-resume.feature`, `specs/conversations/recording.feature`.

### WP-L7-05 — Dropped-turn summary
Depends: WP-L7-01.
Owns: `agent`, `specs/agents`, `internal/bdd/steps/agent.go`.
Opt-in summary of the model turns the context-window policy drops: bounded chunked durable `summary-<from>-<to>` steps, replay-stable messages, cap accounting, the failed-chunk barrier and handoff carry (ADR-0020).

### WP-L7-06 — Loop guard
Depends: WP-L7-01, WP-L7-05.
Owns: `agent`, `specs/agents`, `internal/bdd/steps/agent.go`.
Soft marker on the 3rd–5th call and hard block on the 6th and later for repeated non-handoff tool calls, with exact-bytes canonical arguments and a 100-entry LRU rebuilt from the recorded turns; adds a test asserting the unchanged `turnResult` JSON (ADR-0020).
