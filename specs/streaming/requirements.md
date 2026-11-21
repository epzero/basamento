# Streaming: runs outlive connections

Status: **Partly implemented** (WP-L6-01, `stream/eventlog`; WP-L6-02 is planned). Design: ADR-0005, `docs/architecture/streaming.md`.

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

## Durable event log and relay (WP-L6-01)

The log writes every durable event of a run in one transaction with the state
change that produced it (the outbox), numbers them per run, and a relay
publishes them to the bus at least once.

### REQ-STRM-008 — Per-run sequence
When a state change of a run records a durable event, the system shall give it the run's next sequence number, which starts at 1 and increases by one per event.

### REQ-STRM-009 — One sequencer per run
While several state changes of one run are recorded at the same time, the system shall still give each event the run's next sequence number, with no gap and no duplicate.

### REQ-STRM-010 — A crash leaves whole or nothing
If the process stops while a state change of a run records its durable events, then the system shall leave either both that state change and all of its durable events in the log, or neither.

### REQ-STRM-011 — Atomic with the state change
When a state change of a run is recorded, the system shall persist the new state and all durable events it records together.

### REQ-STRM-012 — Atomic on failure
If a state change of a run is rejected in the writing transaction, then the system shall leave neither that state change nor its durable events in the log.

### REQ-STRM-013 — Relay in order
While a run has outbox entries not yet marked relayed, the relay shall publish them to the bus in the order of their sequence numbers.

### REQ-STRM-014 — Mark relayed
When the relay has sent an outbox entry to the bus, the relay shall mark that entry relayed.

### REQ-STRM-015 — At-least-once delivery
If the relay stops after sending an outbox entry to the bus and before marking it relayed, then the system shall send that entry to the bus again after the relay restarts.

### REQ-STRM-016 — Duplicates are identifiable
When a consumer receives a durable event more than once, the system shall carry the same run id and sequence number on each copy.

### REQ-STRM-017 — Bus unreachable
If the bus is unreachable, then the system shall keep every unrelayed outbox entry and shall not fail the state change of the run.

### REQ-STRM-018 — Relay waits, not drops
While the bus is unreachable, the relay shall keep the outbox entry at the head of its run's order unrelayed until the bus accepts it.

### REQ-STRM-019 — Relay resumes
When the bus becomes reachable, the relay shall publish every outbox entry held during the outage in the order of their sequence numbers.

### REQ-STRM-020 — Read from a cursor
When a caller reads the durable events of a run after a sequence number, the system shall return every event after it in sequence order with no gap and no duplicate.

### REQ-STRM-021 — Read past the end
When a caller reads the durable events of a run after a sequence number at or above the run's last one, the system shall return no events.

### REQ-STRM-022 — Reads stay in the tenant
If a caller reads the durable events of a run that belongs to another tenant, then the system shall fail the read with code `not_found`.

### REQ-STRM-024 — Event envelope
When the system publishes a durable event to the bus, the system shall publish an envelope carrying the run id, the sequence number, the event type, the schema version and the event data.

### REQ-STRM-025 — Tenant required on read
If a caller reads the durable events of a run without a tenant, then the system shall fail the read with code `invalid_argument`.
