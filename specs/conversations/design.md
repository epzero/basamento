# Conversations: design

Design for WP-L7-03 (owner of `agent/conversation`). Decisions: ADR-0007,
ADR-0002, ADR-0005, `docs/architecture/runtime.md`. The requirements and
scenarios are the contract; this file says how the implementer meets them and
records what ADR-0007 leaves open.

## 1. Shape

One coordinator flow code (`agent/conversation`) registered with the flow host,
run on the configured `runtime.Runtime` as `conv-<id>`:

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
  and is refused with `not_found` / `permission_denied` / `conflict` /
  `invalid_argument` at the edge (REQ-CONV-017 to REQ-CONV-021, and gate
  validation as in `flow-engine` REQ-FLOW-006).
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
continuation.

The history a turn sees (REQ-CONV-012) is built from `messages`: completed,
failed and cancelled turns in order, then the starting user message. What a
failed or cancelled turn produced stays in `messages` (REQ-CONV-009,
REQ-CONV-013): its partial model message is marked `interrupted`, and its user
message and model messages are given to later turns so a subsequent turn can
see what happened. Steering messages are appended to `messages` as they are
delivered (REQ-CONV-007, CONV-012-03).

**Growth.** `messages` grows with the conversation. The coordinator stores
it in state, beyond the few-KB threshold of `docs/architecture/runtime.md` §3
only if it overflows, in which case it is stored by reference in blob storage
and the same change lands the reference in state. Trimming history to a token
budget is the agent loop's memory policy (WP-L7-01), not this work package's.

## 3. Modes

| Mode | While a turn runs | Between turns |
| --- | --- | --- |
| `queue` (default) | append to `queue` | start its turn at once (REQ-CONV-004-02) |
| `steer` | deliver to the running child run as its next steering input (REQ-CONV-006) | refused with `conflict`: nothing to redirect (REQ-CONV-020) |
| `cancel` | cancel the running child run (REQ-CONV-008) | refused with `conflict`: nothing to cancel (REQ-CONV-021) |

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

The coordinator counts the durable calls it made **itself** since it last
started: its gate awaits, gate selections, steps, child runs, sleeps and emits
(`dc.Await`, `dc.Select`, `dc.Step`, `dc.Child`, `dc.Sleep`, `dc.Emit`), taken
from the runtime's event count since the run started, not from a timer. The
durable calls a child run makes inside itself belong to the child run's own
history and are **not** counted (REQ-CONV-014). At each turn boundary, if the
count is at or above the threshold the coordinator calls `dc.Checkpoint()`; the
new run is `conv-<id>` with the same record, and its count starts again from
zero. The threshold is configuration with a default of 1000 durable calls
(chosen by this spec; `docs/architecture/runtime.md` §3 leaves the fixed event
threshold open). When `dc.Capabilities().ContinueAsNew` is true, the coordinator
checkpoints at the turn boundary; when it is false (on the `local` runtime), the
coordinator does not checkpoint (no fake semantics, ADR-0002; CONV-014-04).
A turn of the coordinator shape in §1 is four durable calls of the coordinator:
`Await`, `Select`, `Step` and `Child`; the model and tool calls of the child run
do not add to them.

## 5. Isolation (REQ-CONV-016 to REQ-CONV-018)

`tenant` and `user` are part of the conversation's identity: the coordinator
run id is derived from the conversation id *within* the tenant, and every
step's `tenant` context comes from the run state, never from the message.
Every read or write of the record carries that context (constitution §8). A
principal of another tenant therefore cannot address the run, and a step of
another tenant cannot read the record. Within a tenant, `user` must equal the
user in the record for any read, send or cancel (`resource.owner == principal.id`,
`docs/architecture/auth.md` §2); a mismatch is denied with `permission_denied`
(REQ-CONV-018, matching REQ-AUTH-004 and REQ-AUTH-005), whereas cross-tenant
access is `not_found` (REQ-CONV-017, matching REQ-AUTH-002).

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

## 8. Crash-resume

WP-L7-03 takes no dependency on the Temporal runtime (owner decision,
2026-11-17). Surviving a crash is the runtime's guarantee, proved by the
conformance suite (WP-L5-04, `specs/runtime/conformance.feature`); the
conversation logic here is specified on `@runtime:any` / local and does not
restate it. A conversation crash-resume scenario on Temporal (queued and
steering messages surviving a restart) is a tracked follow-up for a later work
package that depends on both WP-L7-03 and the Temporal runtime.

That work package is WP-L7-04 (`crash-resume.feature`, `recording.feature`,
`REQ-CONV-022`, `REQ-CONV-023`). It adds no package. The coordinator and the
turn are already written against `runtime.Runtime`, and the runtime replays a
restarted run from the Temporal history (`REQ-RT-002`, `REQ-FLOW-037`): a
killed process leaves the coordinator run and its child run in the Temporal
history, and the restarted process's workers continue them. The queued
messages are in the coordinator's record (`queue`, §2), which is part of that
history, so the resumed coordinator runs them in arrival order after the
resumed turn ends (REQ-CONV-022). If `agent/conversation` does not already
deduplicate a message, WP-L7-04 updates it in place and says so in the pull
request's note on directories outside its own (AGENTS.md).

**Recorded once (REQ-CONV-023).** A replay or a retried step can execute a
turn's model call or its message-recording step again for the same turn. The
turn record must then not append the message a second time:

- a model message is identified by its turn and its position in that turn;
- recording a completed stream for a message that is already in `messages`
  is a no-op, the same guarantee `handled` gives user messages (REQ-CONV-005),
  while an `interrupted` message of the same turn and position is completed in
  place.

A replay of the turn that calls the model call's step again therefore cannot
append a second model message (REQ-CONV-023). In `CONV-022-01`, the client
reattaches with her last cursor after `kit` reports `ready` and before the
resumed attempt finishes streaming; the streaming model step emits
`reset{attempt}` immediately before forwarding the retried attempt's first delta
from the simulator (so `reset` is strictly ordered after the client reattaches
and before attempt 2's deltas regardless of when the heartbeat timeout fires),
and the client sees the `reset` (REQ-STRM-004) followed by the completed
message once (REQ-STRM-002).

### Harness (WP-L7-04)

- **Scenarios.** `CONV-022-01` and `CONV-022-02` are `@level:system`
  (`crash-resume.feature`). They run the built `kit` binary serving the
  `embedded` profile against the embedded Temporal server of WP-L5-03, whose
  SQLite database lives in the `data.dir` the scenario chooses, and they
  go through the public API of WP-L6-03 (`specs/config/design.md` §6 for the
  process; `specs/runtime/embedded.feature` for the embedded profile). The
  restart runs no Temporal test server: the server is the embedded one, on
  the pinned version, and its file-backed SQLite is what makes the run
  survive.
- **Scripted model.** The model is an HTTP simulator started by
  `internal/bdd` as its own process, reached by `kit` through a provider
  endpoint configured for the scenario (WP-L3-04, `model/providers`). It is
  not imported by production code (constitution) and makes no live call
  (`bdd-convention.md` §5).
- **Crash.** The harness sends `SIGKILL` to the process, not `SIGTERM`:
  `SIGTERM` drains the server gracefully (REQ-CONFIG-023) and would let the
  turn finish. It waits for the process to exit, starts the same binary on
  the same `data.dir`, and waits for the `ready` line before it continues.
  The kill is safe to send at any time, not only at one instant: a turn that
  has not yet started or has already ended resumes as the replay specifies,
  and a turn killed after its model call completed resumes from the history
  without a new attempt. The simulator streams `"Once upon"` to the running
  turn, so the turn is mid-model-call when the process is killed.
- **Component scenario.** `CONV-023-01` in `recording.feature` is
  `@level:component` and `@runtime:any`: it drives the coordinator in process
  and needs no process kill. It runs the turn's recording step a second time
  for the same model message and reads the history back.
- **Steps.** The steps go in `internal/bdd/steps/conversation.go` next to
  WP-L7-03's steps.

## Open questions

1. **Interrupted turns in subsequent turn history (`REQ-CONV-012`, `CONV-012-02`, `CONV-013-03`):** `ADR-0007` specifies that `cancel` keeps the partial message marked `interrupted`, and `REQ-STRM-005` records interrupted partial messages, but `ADR-0007` does not explicitly say whether failed and cancelled turns' user and interrupted model messages are passed to subsequent turns' child runs. This spec includes both `failed` and `cancelled` turns (with partial model messages marked `interrupted`) in the history later turns see so follow-up turns and retries have context. Confirm whether both should be included, only `failed`, or neither.
3. **Default checkpoint threshold of 1000 durable calls (`REQ-CONV-014`):** `docs/architecture/runtime.md` §3 says *"Histories are bounded with `Checkpoint` at a fixed event threshold"* without naming a number, and `ADR-0007` says *"The coordinator checkpoints regularly"*. This spec chooses `1000` durable calls as the default threshold. Confirm whether `1000` is the intended default.
