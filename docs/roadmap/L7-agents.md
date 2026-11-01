# L7 — Agents and tools

### WP-L7-01 — Agent loop
Depends: WP-L3-02, WP-L5-01.
Owns: `agent`.
One model turn per step; caps on steps, cost, tokens and time; handoff; memory policy.

### WP-L7-02 — Tools and MCP
Depends: WP-L7-01.
Owns: `tool`.
Tool port, allowlists, MCP client (`modelcontextprotocol/go-sdk`), sandbox port. Tools on other machines are served by workers (WP-L3-07).

### WP-L7-03 — Conversations
Depends: WP-L7-01, WP-L6-02.
Owns: `agent/conversation`.
Coordinator run per conversation with a child run per turn; queue, steer and cancel (ADR-0007).
