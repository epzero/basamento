# Streaming: runs outlive connections

Status: **Partly implemented** (WP-L6-01, WP-L6-02, the API surface of
WP-L6-03 and WP-L7-03; WP-L6-04 is planned). Decisions: ADR-0005, ADR-0007.
Specs: `specs/streaming`, `specs/conversations`.

## 1. Two layers

1. **Execution.** A run lives in the durable runtime. No connection,
   disconnect or reconnect affects it. Only an explicit cancel or a declared
   policy (`on_detach: cancel_after(<d>)`) stops it.
2. **Observation.** Any number of clients attach, detach and reattach with a
   cursor, from any device.

## 2. Two streams

| Stream | Ordering | Store | Contents |
| --- | --- | --- | --- |
| Durable events | `seq` per run | SQL table, written in the transaction of the state change, then relayed to the bus | `run.*`, `node.*`, `message.started`, `message.completed{content_ref}`, `tool.*` summaries, `gate.*` |
| Deltas | `dseq` per message | Bus only, retained while the message is in flight and dropped shortly after it completes | `text`, `audio`, `progress`, `reset{attempt}` |

Wire shapes: `contracts/basamento/stream/v1/stream.proto`, Go mirror in
`stream/stream.go`.

## 3. Reattach protocol

1. The client sends `Cursor{seq, open: {message_id: dseq}}`. Over SSE it is
   encoded in `Last-Event-ID`.
2. The server replays durable events after `seq` from SQL.
3. For each message started but not completed, it sends
   `Snapshot{text_so_far, dseq}`, built from the bus's retained deltas. They
   are kept for as long as the message is in flight, so a snapshot is complete
   however long the message takes.
4. It then streams live deltas from `dseq + 1`. Ordering by `dseq` means no
   gap and no duplicate (REQ-STRM-003).

The Go implementation is `stream.Broker` (WP-L6-02). `Attach` replays through
`stream/eventlog` under the run's lock, so a durable event cannot slip between
the replay and the live stream; it then sends one `Snapshot` per message still
in flight and follows the deltas. Deltas are numbered per message, kept for the
message's lifetime, published on `run.<run_id>.deltas` and fanned out to the
attachments of the server that owns them. `EnforceDetachPolicies` is the clock-
driven loop that applies `on_detach`; `Heartbeat` is how a producer learns of a
cancellation, which records the in-flight messages as interrupted.

## 4. Producers

- A streaming model step publishes deltas and **sends a heartbeat at least every
  few seconds**, because cancellation reaches activities through heartbeats
  (REQ-STRM-005).
- A retried attempt publishes `reset{attempt}` first; clients discard the
  partial text (REQ-STRM-004).
- The final message is the step's result: recorded, replayable, and written
  as `message.completed`.

## 5. Why not Temporal Workflow Streams

It is in Public Preview. Each publish batch is a Signal and each subscriber
poll is an Update in workflow history, which competes with history limits.
Subscribers must be Temporal clients. Producers outside workflows (webhooks,
the browser runner) cannot publish. It is also not runtime-neutral. It may later
back the `Bus` port for low-rate topics.

## 6. Conversations (ADR-0007)

A conversation is a thin **coordinator** run (`conv-<id>`) that receives user
messages as gate resolutions (Updates on Temporal) and runs **one child run per
turn**. A message that arrives during a turn is handled by policy:

- `queue` (run next),
- `steer` (delivered at the next step boundary of the running turn),
- `cancel` (stop the turn, keep the partial message marked interrupted).

The coordinator is `agent/conversation` (WP-L7-03). Its durable record holds
the turns, the messages, the queue and the accepted message ids, so a restart
or a continuation keeps them; the record counts only the coordinator's own
durable calls and checkpoints at a turn boundary on a runtime that supports
continuing as new. Isolation is per tenant and per user: another tenant's
conversation is `not_found`, another user's is `permission_denied`. The turn
itself is the agent loop of WP-L7-01, run as a child of the coordinator.

## 7. Client protocols

The native stream (§2, §3) is the contract. Other protocols are adapters in
`api` that translate it at the edge, as the OpenAI-compatible endpoint does
for model calls.

**AG-UI** (`/agui`, SSE) lets AG-UI clients such as CopilotKit attach to a run:

| Ours | AG-UI |
| --- | --- |
| `run.started`, `run.completed`, `run.failed` | `RUN_STARTED`, `RUN_FINISHED`, `RUN_ERROR` |
| `node.started`, `node.completed` | `STEP_STARTED`, `STEP_FINISHED` |
| `message.started`, `text` deltas, `message.completed` | `TEXT_MESSAGE_START`, `_CONTENT`, `_END` |
| reasoning deltas | `REASONING_START`, `REASONING_MESSAGE_*`, `REASONING_END` |
| `tool.*` | `TOOL_CALL_START`, `_ARGS`, `_END`, `_RESULT` |
| state after a superstep | `STATE_SNAPSHOT`, then `STATE_DELTA` (JSON Patch) |
| `Snapshot` on reattach | `MESSAGES_SNAPSHOT` |
| `progress` deltas | `ACTIVITY_SNAPSHOT`, `ACTIVITY_DELTA` |
| child runs | `SUBAGENT_STARTED`, `_FINISHED`, `_ERROR` |
| `gate.opened` | `RUN_FINISHED` with an interrupt outcome (a draft in AG-UI; `CUSTOM` until it is final) |
| `reset`, `audio` | `CUSTOM` |

The API (`package api`) serves the native stream as SSE at
`GET /v1/runs/{id}/stream` and the AG-UI adapter at `GET /agui?run_id=…`. Both
read the same attachment, so a client switches protocol without losing its
cursor: the SSE id carries `{seq, open}`, which comes back in `Last-Event-ID`
on reattach. The native events are named `durable`, `snapshot` and `delta`; a
stream ends after a terminal `run.*` event.

Run discovery is `GET /v1/runs` and `GET /v1/runs/{id}`, and cancellation is
`POST /v1/runs/{id}/cancel`. The request's tenant comes from its authenticated
principal, never from a request field; a run of another tenant is `not_found`,
the same answer as a run that does not exist (REQ-AUTH-002). Errors are the
platform codes with an HTTP status summary, and the body carries the code only.

AG-UI is not the native format because its events carry no sequence number,
so it has no cursor or reattach; it has no reset on retry and no media deltas;
and interrupts are still a draft. The adapter keeps reattach by putting our
cursor in the SSE event id.
