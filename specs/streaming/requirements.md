# Streaming: runs outlive connections

Status: **Planned** (WP-L6-01, WP-L6-02). Design: ADR-0005, `docs/architecture/streaming.md`.

### REQ-STRM-001 — Runs outlive connections
When a client detaches from a run stream, the system shall continue the run until it completes, fails, or is cancelled explicitly.

### REQ-STRM-002 — Durable events resume exactly once
When a client reattaches with a cursor, the system shall deliver every durable event after the cursor's sequence number exactly once and in order.

### REQ-STRM-003 — In-flight messages resume without gaps
When a client reattaches while a message is in flight, the system shall send a snapshot of that message followed by later deltas, with no gap and no duplicate.

### REQ-STRM-004 — Reset on retry
If a step that streams a message is retried, then the system shall emit a reset for that message before the deltas of the new attempt.

### REQ-STRM-005 — Explicit cancellation
When a principal cancels a run, the system shall stop the run's in-flight steps within one heartbeat interval and record the partial message as interrupted.

### REQ-STRM-006 — Detach policy
Where a flow declares `on_detach: cancel_after(<duration>)`, when no client has been attached for that duration, the system shall cancel the run.

### REQ-STRM-007 — Same order for every viewer
While several clients are attached to one run, the system shall deliver durable events to each of them in the same order.
