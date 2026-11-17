# Conversations: design

Design for WP-L7-03 (owner of `agent/conversation`). Decisions: ADR-0007,
ADR-0002, ADR-0005, `docs/architecture/runtime.md`. The requirements and
scenarios are the contract; this file says how the implementer meets them and
records what ADR-0007 leaves open.

## 1. Shape

One coordinator flow code (`agent/conversation`) registered with the flow host,
run on the `temporal` runtime as `conv-<id>`:

```
dc.Await("inbox")            validate the user message, fold it into "inbox"
  dc.Select(                 wait for the next user message or the end of the running turn
    inbox,                   routed by mode: queue | steer | cancel
    turn-done)               the running turn's child run
  dc.Step("record-turn")     one step per state change of the turn record
  dc.Child(turn, history)    exactly one child run per turn
  dc.Checkpoint()            when the checkpoint threshold was reached at this boundary
```

- **One child run per turn (REQ-CONV-002).** The coordinator never starts turn
  `n+1` before the child run of turn `n` has ended. Child runs run on the
  same task queue and use the turn flow of WP-L7-01.
- **Gate resolutions.** A user message reaches the coordinator as one gate
  resolution carrying `{tenant, user, message_id, text, mode}`; the coordinator
  is always waiting at an open gate, so a resolution validates synchronously
  and is refused with `not_found` / `invalid_argument` at the edge (REQ-CONV-017,
  REQ-CONV-018, REQ-STRM-style validation as in `flow-engine` REQ-FLOW-006).
- **Transport.** Steps speak of user messages only. The gate/update mechanics
  of the runtime never appear in scenarios.

## 2. The turn record

The coordinator's durable state is a single record:

| Field | Keeps |
| --- | --- |
| `turns` | per turn: state (`completed`, `failed`, `cancelled`), code, child run id |
| `messages` | user and model messages in order, each with its turn, message id and status (`complete`, `interrupted`) |
| `queue` | user messages waiting for a turn, in arrival order |
| `handled` | ids of every user message already accepted |
| `running` | turn id of the running child run, if any |

`handled` is what makes REQ-CONV-005 hold: the same `message_id` twice is a
no-op, while the same text with a different id is two messages. Because
`handled` lives in the run's state, it also survives a restart and a
continuation (REQ-CONV-015-03).

The history a turn sees (REQ-CONV-012) is built from `messages`: completed
turns in order, then the starting user message. What a failed turn produced
stays in `messages` (REQ-CONV-013): it is marked `interrupted` and its user
message and model messages are given to later turns, so a retry can see what
went wrong. Steering messages are appended to `messages` as they are delivered
(REQ-CONV-007, REQ-CONV-012-03).

**Growth.** `messages` grows with the conversation. The coordinator stores
it in state, beyond the few-KB threshold of `docs/architecture/runtime.md` §3
only if it overflows, in which case it is stored by reference in blob storage
and the same change lands the reference in state. Trimming history to a token
budget is the agent loop's memory policy (WP-L7-01), not this work package's.

## 3. Modes

| Mode | While a turn runs | Between turns |
| --- | --- | --- |
| `queue` (default) | append to `queue` | start its turn at once (REQ-CONV-004-02) |
| `steer` | deliver to the running child run as its next steering input (REQ-CONV-006) | refused with `conflict`: nothing to redirect |
| `cancel` | cancel the running child run (REQ-CONV-008) | refused with `conflict`: nothing to cancel |

The running child run receives steering inputs through the same typed input
channel it uses for cancellation of a turn (flow-engine gate).

**Steering (ADR-0007: "delivered at the next step boundary").** The child run
reads its input channel at each step boundary of the agent loop: between one
model call (and its tool calls) and the next, never during a model call. The
message written so far is kept as a complete history message, the steering
text is appended as a user message, and the loop continues in the same child
run, so nothing scheduled by the turn is cancelled (REQ-CONV-007). The partial
text stays visible to the stream as the message it was (streaming spec).

**Cancel of a turn (REQ-CONV-009).** The coordinator cancels the child run and
records the turn as `cancelled`; the model message the turn was writing is
`interrupted` with its text so far, as REQ-STRM-005 records it for a run cancel.
The person receives the turn result with code `cancelled`. Queued messages stay
and run in order (REQ-CONV-010).

**Cancel of a conversation (REQ-CONV-011).** The coordinator cancels the child
run, drops `queue`, and returns; the coordinator run ends `cancelled`. A
message sent afterwards is refused with `conflict` (REQ-CONV-019).

## 4. Checkpoint (REQ-CONV-014)

The coordinator counts the durable calls it made since it last started
(`dc.Capabilities()` and the runtime's event count, not a timer). At each turn
boundary, if the count is at or above the threshold it calls
`dc.Checkpoint()`; the new run is `conv-<id>` with the same record. The
threshold is configuration with the default of 1000. Only the `temporal`
runtime has `ContinueAsNew` (`Caps`); on a runtime without it the coordinator
does not checkpoint (no fake semantics, ADR-0002), and the long-conversation
scenarios target `temporal`.

## 5. Isolation (REQ-CONV-016 to REQ-CONV-018)

`tenant` and `user` are part of the conversation's identity: the coordinator
run id is derived from the conversation id *within* the tenant, and every
step's `tenant` context comes from the run state, never from the message.
Every read or write of the record carries that context (constitution §8). A
principal of another tenant therefore cannot address the run, and a step of
another tenant cannot read the record. Within a tenant, `user` must equal the
user in the record for any read or write; a mismatch is `not_found`, not
`permission_denied`, so the existence of another user's conversation is not
disclosed (REQ-CONV-018; the tenant rule itself is REQ-AUTH-002).

## 6. Changes outside this work package

- **Turn flow and its steering input (WP-L7-01, `agent`).** The turn's input
  channel is owned by the agent loop. If it does not accept steering and
  cancellation inputs, the implementer lands that change in its own pull
  request first, naming WP-L7-01.
- **Streaming (WP-L6-02, `stream`).** The interrupted message and the turn
  result are recorded as that work package records a cancelled run. This work
  package does not duplicate it.
- **API (WP-L6-03, `api`).** Tenant and user come from the API's principal;
  nothing outside `agent/conversation` is changed here.

## 7. Test helpers

Step definitions go in `internal/bdd/steps/conversation.go`. The scripted
model, the runtime and the tenant context are built in `internal/bdd` and
test-only packages; no test double is imported by `agent/conversation`.
