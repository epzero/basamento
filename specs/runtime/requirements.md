# Runtime: one contract, several engines

Status: **Planned** (WP-L5-01 … WP-L5-04). Design: ADR-0002, `docs/architecture/runtime.md`.

### REQ-RT-001 — Conformance
Each runtime shall produce the same final state and the same durable events for every conformance flow.

### REQ-RT-002 — Crash resume
When a durable runtime restarts during a run, the runtime shall resume the run from its last completed step without executing completed steps again.

### REQ-RT-003 — Embedded server
Where the profile is `embedded`, the system shall run the Temporal server in-process on a file-backed SQLite database, without any external service.

### REQ-RT-004 — Replay
When a recorded run history is replayed against the current flow code, the runtime shall reproduce the recorded commands or report a determinism violation naming the first divergent event.
