# Agents: design notes

Decisions the implementer of WP-L7-01 must follow, beyond the requirements and
scenarios. They follow ADR-0002, ADR-0006, ADR-0011, ADR-0020,
`docs/architecture/flow-engine.md` §4 and `docs/architecture/model-layer.md`.
They introduce no behavior the scenarios do not show; where a number is given
below it is the one the scenarios use.

## 0. Counting rules

Every number in a scenario follows these rules.

1. **When checks run.** Every check runs at the turn boundary before starting
   model turn `n` (`n = 1, 2, …`), never after a turn completes when no next
   turn is attempted. If model turn `n` answers without requesting a tool
   (REQ-AGENT-007), the run ends immediately as completed with that turn's
   output. If model turn `n` requests a handoff (REQ-AGENT-016), the run ends
   immediately as completed with state `handoff`. Only when model turn `n`
   requests one or more tools (REQ-AGENT-003) and those tool-call steps
   complete (REQ-AGENT-004) does the loop advance to the boundary before model
   turn `n+1`.
2. **Order of the checks** at the boundary before model turn `n`: cancellation
   (REQ-AGENT-028), the time cap (REQ-AGENT-012), the step cap (REQ-AGENT-008),
   the context-window policy (the memory policy of REQ-AGENT-023–025, REQ-AGENT-027), the token cap
   (REQ-AGENT-011), the cost cap and reservation (REQ-AGENT-009,
   REQ-AGENT-010) (see §6).
3. **Reaching a cap is within it.** Step, token and cost caps are inclusive:
   only a turn that would exceed (`>`) the cap is stopped. At equal values it
   depends on the next turn's figures: with `spent cost == cost cap`, turn
   `n+1` runs if its reservation is `0` and is stopped if the reservation is
   `> 0`; with `used tokens == token cap`, turn `n+1` runs if its input tokens
   are `0` and is stopped if they are `> 0`; turn `S` runs under a step cap
   `S`, and turn `S+1` is stopped.
4. **The time cap uses `>=`.** An elapsed time of at least `caps.time` at a
   boundary stops the loop before the next turn; `59 seconds` under a
   `1 minute` cap lets the run continue.
5. **Reservation and charge.** A turn's reservation is its estimated cost in
   whole milli-points, from the model's price formula on estimated usage
   (REQ-MODEL-006), held against the remaining cost cap and settled to the
   actual charge when the turn finishes. A scripted turn that writes
   `costing X milli-points` reserves `X` and charges `X`; a scenario that tests
   reservation against settlement states both the estimate and the actual cost
   in its `Given`.
6. **Token cap counts input plus output.** Each completed model turn adds its
   input tokens and its output tokens to the run's used tokens; before a turn
   starts only its input tokens are known, so the cap check uses used tokens
   plus that turn's input tokens. A summarising model call (§5.1) also adds its
   input and output tokens to used tokens and its charge to spent cost, but it
   is not a model turn for the step cap; a summary request rejected before the
   call adds nothing. Across a handoff the target's step count, spent cost and
   used tokens start at `0` and count only its own turns; the time cap is the
   chain's, measured from the run's start (§4).
7. **Context-window policy counts.** A completed model turn is one unit: its assistant
   message (with any tool-call requests) plus the tool-result messages that
   followed it; a turn with `k` tool calls is `1 + k` messages. Before model
   turn `n`, let `T_sys` be the system messages' tokens and `T_i` turn `i`'s
   tokens. If `T_sys + Σ T_i (i = 1…n-1) <= input_tokens`, nothing is dropped.
   Otherwise the policy keeps `T_sys` and, when `n > 1`, turn `1`; if that set
   alone exceeds `input_tokens` the run fails with `invalid_argument` before
   invoking the model (REQ-AGENT-027); else with remainder
   `R = input_tokens - (T_sys + T_1)` it keeps whole turns walking backward
   from `n-1` to `2` while each fits in `R`, and drops the others
   (REQ-AGENT-024, REQ-AGENT-025). With `input_summary: true`, `T_summary` is
   the active summary's recorded output tokens plus its envelope estimate
   (§5.1), and it is reserved in `R` before the tail is chosen.

## 1. Where the agent loop lives

`agent` provides the `agent` node handler, registered with the flow host at the
composition root (ADR-0011). It may import `core`, `model`, `prompt`, `runtime`
and `tool`; it never imports a durable-execution vendor (constitution §2).
Everything in it is flow code against `runtime.DurableContext`: the clock is
`dc.Now()`, ids come from `dc.NewID`, order never depends on map iteration.

`tool` (the tool port) is WP-L7-02. This package depends on its interface
only; the tool-call step runs the registered tool. Tools, allowlists, MCP and
sandboxes are not specified here.

## 2. Steps

| Step name | Side effect | Result |
| --- | --- | --- |
| `turn-<n>` | one model turn: reserve, `model.Invoke` (blocking form), settle | the model output, the usage, the charge, the count of messages the context-window policy dropped (§5) |
| `tool-<n>-<k>` | tool call `k` of turn `n`; a hard loop-guard block does not run the tool and returns a synthetic result (§8) | the tool result |
| `summary-<from>-<to>` | one summarising model call over a chunk of the dropped turns `from..to`, when `input_summary` is true (§5.1); a failed call is a successful `summary.failed` result with a fixed reason code | the summary text, its covered range, the usage and charge |

`n` and `k` are 1-based. The step's `IdempotencyKey` is its name, stable for
the run. One model turn is one invocation with
`ClientRef = hash(run id, node id, "turn-<n>")` (REQ-MODEL-004); a resumed run
never invokes a completed turn again (REQ-AGENT-002), and an ambiguous start is
resolved as REQ-MODEL-005 says. WP-L7-01 invokes the model with the blocking
`model.Invoke` form only; streaming a turn's text as one message per
`docs/architecture/streaming.md` is added by WP-L6-02 and WP-L7-03 (`agent`
may not import `stream`). The step result is the durable `message.completed`. A `summary-<from>-<to>`
step is not a model turn: it does not increment `n`, does not appear in
`Output.Turns` and does not count against `caps.steps`. One summary step covers
one chunk; its name is the chunk's range, not the whole active range.

## 3. Agent node spec

`flow-engine.md` §1 shows `{ agent: planner }`. An agent is defined in that
spec with the fields below. The implementer encodes them as the node spec
schema of the `agent` handler; this is a decision of this work package, not a
change to package `flow`. The node id is the node's key in `flow.IR.Nodes`, and
`flow.Node.Out` names the state key receiving the run output.

| Field | Meaning | Default |
| --- | --- | --- |
| `model` | the model id of every turn | — |
| `system` | the system messages, kept first by the context-window policy | none |
| `tools` | the tools offered to the model (allowlists and tool access rules are WP-L7-02) | none |
| `checks` | accepted for compatibility; no effect — cancellation and the time cap are checked before every model turn (REQ-AGENT-012, REQ-AGENT-028) | 1 |
| `caps.steps` | the step cap, in model turns | none = no cap |
| `caps.cost_mpts` | the cost cap, in milli-points | none = no cap |
| `caps.tokens` | the token cap, counted as input plus output tokens | none = no cap |
| `caps.time` | the time cap, a duration on the run clock | none = no cap |
| `input_tokens` | the input token limit of the model, in tokens | `0` = no limit |
| `input_summary` | summarise the model turns the context-window policy drops, in a durable summary message (ADR-0020, §5.1) | `false` |

`input_tokens` is not derived from the model: `model.Descriptor` carries no
input-token limit, so an agent node that omits it has no limit and keeps the
whole message history.

A cap that is absent is not applied. For step, token and cost caps, reaching
the cap (`==`) is within the cap; only a turn that would exceed (`>`) the cap is
stopped before invoking the model. For the time cap, elapsed time on the run
clock of at least (`>=`) `caps.time` stops the loop before starting the next
turn. The scenarios are deterministic because the scripted model answers with
the usage and cost written in the scenario: the reservation estimate is the
price formula on estimated usage, as REQ-MODEL-006 (`cost.feature`) already
specifies, and when a scripted turn states only `costing X milli-points`, its
estimated reservation and actual charge are both `X` milli-points.

## 4. Handoff

The model requests a handoff the way its provider expresses a tool call, with
the tool name `handoff_to` and the argument naming a node id of the flow. The
agent loop treats that request specially: it is not a `tool-call step`
(REQ-AGENT-003), and the tool `handoff_to` is never run by the tool port.

A turn's reply may mix tool calls with `handoff_to`. The loop reads the turn's
requested calls in order, before making any tool call of that turn:

- with no `handoff_to`, it makes one tool-call step per other call
  (REQ-AGENT-003), as usual;
- with one `handoff_to`, it makes no tool-call step for that turn, whether or
  not the turn also requests other tools, and ends the run as a handoff
  (REQ-AGENT-016). The other requests are not retried by the target;
- with two or more `handoff_to`, it fails the run with code `invalid_argument`.

The run's output:

```json
{"state": "handoff", "target": "fixer", "turns": [ … model turns … ],
 "summary": {"text": "…", "from": 2, "to": 4},
 "started_at": "2026-11-17T10:00:00Z", "time_cap": "1m"}
```

`turns` is every model turn of the first agent in order, the closing turn
included: that turn's assistant message, as recorded, holds the `handoff_to`
request, exactly as the requests of its other tool calls. `summary` is present
only when the first agent dropped turns with `input_summary`; it is the active
summary of §5.1, not a model turn. `started_at` is the
run's start time on the run clock (`dc.Now()`, design §1); `time_cap` is the
time cap in effect for the chain (REQ-AGENT-016, REQ-AGENT-019,
REQ-AGENT-029), written as a Go duration string (`"1m"`, `"90s"`) or `null`
when neither the incoming handoff nor this agent sets one (REQ-AGENT-016).

The node's outgoing edge leads to the target node, which receives this output
as its input and the same run clock. When the target's handler sees
`state: "handoff"`, it takes `turns` as its message history (REQ-AGENT-017):
the target's first turn has the system messages of the target agent, then the
carried turns each paired with its tool-result messages. Cycles between agent
nodes in a flow are bounded by `flow.Guard` (`REQ-FLOW-003`), not by the agent
handler.

Caps (REQ-AGENT-018, REQ-AGENT-019, REQ-AGENT-029): the target's `caps.steps`
counts the target's own model turns (the carried turns are not the target's
turns); the target's `caps.cost_mpts` and `caps.tokens` count the target's own
spend and usage, so carried turns never refuse a handoff; the chain's **time
cap** is the incoming `time_cap` when present, otherwise this agent's own
`caps.time` (REQ-AGENT-019, REQ-AGENT-029), and it is measured from
`started_at` on the run clock, so a chain is bounded in time whatever its
length. So the incoming cap governs the target even if the target sets its
own, and the target's own cap applies only when the handoff carries none
(REQ-AGENT-019, REQ-AGENT-029). Each agent's handoff output carries the time
cap in effect for that agent (`null` when neither an incoming handoff nor this
agent sets one, or the active duration string otherwise), and each further
handoff carries that active value on: a middle agent's handoff output repeats
the carried cap, not its own (with triage capped at `1m`, B at `10m` and C,
B's output has time cap `1m`). The target's first turn boundary is checked
like any other (design §0.2), the time cap included, so a target that starts
at or past the cap runs no turn.

A handoff is not a cap: if the target requests a handoff in its first turn,
the chain continues and the next agent receives the target's turns too.

## 5. Memory policy

Despite the section title, `buildMessages` and the rules below are the agent
node's context-window policy: they keep or drop the turns of one model turn's
working memory so it fits `input_tokens`, and estimate the weight of messages
that declare none. Apart from the opt-in summary step (§5.1), which is a
durable step in the run's own journal, the policy stores nothing and persists
nothing between runs. The summary is not cross-run memory: it is read back from
the same run's journal on replay and carried in the handoff output to the
target of that chain, and no run outside the chain reads it. The name "memory
policy" is kept for compatibility with REQ-AGENT-023–027, but it is not
cross-run memory. Memory that outlives a run — facts about the codebase, what
earlier runs did, how to do a thing — and permission gating and lifecycle
policy (run the formatter after every edit, log every tool call) belong to the
host: the composition root and the flow placed around the agent node, which
takes System messages and an `input_tokens` limit and does the rest by replay.
An in-loop policy engine for these concerns would add a non-deterministic
second policy engine, which the constitution (article 3, determinism/replay)
rejects.

Before each model turn the agent loop builds the turn's messages: the system
messages, the completed model turns (each turn's assistant message paired with
the tool-result messages that followed it; a turn with `k` tool calls is `1 + k`
messages), and any new tool results. If total message tokens exceed
`input_tokens`, the policy keeps:

1. every `system` message of the agent (REQ-AGENT-023);
2. when starting model turn 2 or later, the **first model turn** and the tool
   results that followed it, kept whole (REQ-AGENT-023);
3. the most recent model turns that fit in the limit's remainder, walking
   backward from the latest turn (`n-1` down to `2`) a whole turn at a time: a
   model turn with the tool-call results it asked for (REQ-AGENT-024,
   REQ-AGENT-025).

When counting tokens, a tool-result message reports no usage of its own: its
input tokens are estimated from its content at four bytes per token, rounded up,
plus one token for the message envelope. With `input_tokens > 0`, the same
content-based estimate also applies to a system message that declares no token
count and to a turn that reports no usage, so no message is free; otherwise a
turn's weight is its recorded usage tokens plus the estimated tokens of its tool
results. The kept set is measured once its messages are assembled.

The count of dropped messages is recorded in the turn's step result (`turn-<n>`),
not kept elsewhere: the run's messages stay complete for its output. A kept set
that still exceeds `input_tokens` fails the run with `invalid_argument` before
invoking the model (REQ-AGENT-027).

### 5.1 Dropped-turn summary (ADR-0020)

With `input_summary: true`, the policy also keeps the active summary of the
dropped turns when the loop has one covering one or more of them (a failure
still continues without it, §5.1 below). The summary is a synthetic user message
`<untrusted source="conversation-summary">…</untrusted>` placed after the kept
first turn and before the kept recent tail; it has no `Name` and is not a
`Turn`. The summary text is untrusted: any substring that reads as an
untrusted-content delimiter is neutralised (REQ-PROMPT-015) before wrapping.
The active summary covers turns `2..T`, with `T` at most the frontier, the last
dropped turn.

The summary is produced by durable steps named
`<node>/<runID>/summary-<from>-<to>`, one per chunk of the dropped range. A
chunk is the largest whole-turn prefix of `previous.To+1..frontier` whose
request fits in `input_tokens` after the summariser instructions and the
previous summary; a turn is never split, and no chunk starts at or after the
first failed chunk's `from`. The step input carries the model, the
summarising request, the chunk's `from` and `to`, and the previous summary
(wrapped and neutralised like the summary message). The step result holds the
updated summary (`from: 2`, `to: <chunk to>`), its usage and its charge. Replay
calls the same step names and reads the recorded text, so the replayed messages
are the first execution's messages.

As the frontier advances, the loop extends the summary from
`previous.To+1..frontier` in chunks using the previous summary and the newly
dropped raw turns; it does not re-summarise the whole prefix. It recomputes the
tail with the summary's measured weight: the step's recorded
`usage.output_tokens` plus the envelope's content-based estimate. A handoff
target whose model differs from the producing model re-measures the carried
summary with the host `Estimator` over a `model.Request` that contains the
summary message (the estimator and request assembly the loop already uses for a
turn); the carried `output_tokens` is a lower bound, and before invoking it
re-checks the assembled request and drops tail turns, never the run, if it
does not fit. If `T_sys + T_1 + T_summary` exceeds `input_tokens`, the loop drops
tail turns; if no tail turn can be dropped, it skips the summary and falls back
to the pre-change tail. The run never fails because the summary is too large;
if `T_sys + T_1` alone exceeds the limit, REQ-AGENT-027 still fails the run.

After every tail recomputation, not only after a handoff, a summary whose `To`
is above the recomputed frontier is discarded and, if turns are still dropped,
the loop restarts from turn 2. The loop is bounded: at most 8 summary steps per
boundary, at most 2 restarts per boundary, and at most 16 summarising model
calls per run, counted from the journaled summary steps that record a model
call. A restart re-derives the summary from the chunks: a chunk whose range and
input were already journaled replays that step, and a new range is a new step.
A failed chunk is recorded as `summary.failed` with a fixed reason code and is
not retried; it is a barrier for the run — no chunk starts at or after its
`from`, and the summary's `To` freezes at the last successful chunk's `to` — so
a later boundary with a larger frontier makes no new summary call for the
failed range or beyond. The fixed point terminates because each successful
chunk increases `To`, the frontier is
bounded by the number of turns, a failure stops the extension, and a restart
strictly shrinks the covered range.

The summary call is reserved and metered like a turn: its estimated cost is
checked against `caps.cost_mpts` and its input tokens against `caps.tokens`
before the call; when it runs, its input and output tokens add to the run's
used tokens and its charge to the run's spent cost and `Result.CostMpts`. It is
not a model turn for `caps.steps`. A summary that would exceed a token or cost
cap is skipped without a step or a model call; the turn's own checks follow. A
request over `input_tokens` records `summary.failed` with reason
`input_too_large` and zero usage and zero cost; `used`, `spent` and
`Result.CostMpts` grow only for a model invocation that ran. A model failure
records `summary.failed` with a fixed reason code, and the run continues
without a summary for the affected turns.

A handoff carries the active summary in the output's `summary` field. The
target uses it only when the target agent's `input_summary` is true and its own
dropped range is the same or larger; when `input_summary` is false it ignores
the carried summary and applies its ordinary policy to the complete carried
turns. With `input_summary` true it discards the carried summary after a tail
recomputation that keeps a turn the summary covers, and restarts from turn 2 if
turns are still dropped. The target's summary steps use its own `node/runID`
prefix and its own caps; a different model re-measures the carried summary as
above.

## 6. Order of checks at a turn boundary

At each turn boundary before model turn `n`, in this order:
1. cancellation (REQ-AGENT-028);
2. the time cap (REQ-AGENT-012);
3. the step cap (REQ-AGENT-008);
4. the context-window policy (the memory policy of REQ-AGENT-023–025, REQ-AGENT-027)
   and input token limit, which determines turn `n`'s messages and input tokens;
   this step may make one or more summary calls (§5.1), each reserved and
   checked against the time, token and cost caps before it runs; the summary
   loop is bounded (at most 8 steps per boundary, 2 restarts per boundary and
   16 summarising model calls per run);
5. the token cap (REQ-AGENT-011);
6. the cost reservation (REQ-AGENT-009, REQ-AGENT-010).

The first check that stops the run decides its state, so a run that meets two
caps reports the earlier one in this order. A summary that would exceed a token
or cost cap is skipped, not a reason to end the run; the time cap is checked
again after the summary and before the turn, so a summary that runs past it
does not start the turn.

## 7. Changes outside this directory

None needed to write this work package. The tool port's interface is landed by
WP-L7-02 in `tool`; the `agent` node spec schema belongs to the handler
registered by this work package. Should implementing the scenarios need a
change outside the directories this WP owns (`agent`, `specs/agents`,
`internal/bdd/steps/agent.go`), the implementer lands that change first, in a
pull request of its own.

## 8. Loop guard (ADR-0020)

The loop guard applies to every non-handoff tool call. It hashes the tool name
and the canonical arguments: JSON object keys sorted recursively, only
structural whitespace removed, string values byte-exact, arrays in order,
numbers kept as their JSON literals (`json.Decoder.UseNumber()`), and raw bytes
when the arguments are not valid JSON or contain a duplicate object key. Empty
arguments, `null` and `{}` all canonicalise to `{}`. A 100-entry LRU holds the
counts; a new key evicts the least-recently-used entry, but an entry at or
above the soft threshold is pinned and is evicted only when no lower-count
entry exists. If all 100 entries are pinned, the guard evicts the
least-recently-used anyway; it is best-effort and the caps bound the cost of a
missed repeat. The post-increment count, including the current call, decides:

| Request number | Behavior |
| ---: | --- |
| 1st, 2nd | run the tool |
| 3rd–5th | run the tool; append a `<loop-guard>` warning to its result |
| 6th and later | do not run the tool; return a synthetic successful tool result that says the call was blocked |

The `tool-<n>-<k>` step still exists for a blocked call (REQ-AGENT-003): its
input carries `blocked: true` and the count, and its result is the synthetic
tool result. The run continues; a block is not a tool failure and has no error
code. The model sees the warning or the synthetic result and can change its
arguments or approach.

The guard state is rebuilt from the journal, not from a durable counter. At
each turn boundary it is rebuilt from every journaled turn in order: each
non-handoff call whose `tool-<n>-<k>` step is journaled is observed once, in
order, matched to its result in `Turn.Results` by tool-call ID (positional
fallback for a result without an ID). When the loop processes the current
turn's calls, it observes a call only while its tool-call step is not yet
journaled, and then journals the step. On a resume, the loop first rebuilds
from the current turn's journaled `tool-<n>-<k>` prefix before processing its
remaining calls, then observes only calls whose steps are not yet journaled.
Tool steps of a turn are made in order,
so the journaled calls are a prefix and the observations stay in order. A
handoff turn's other calls have no tool step and are not observed; a blocked
call has its step and synthetic result and is observed. On a handoff the target
seeds the guard from the carried turns. No counter is stored in `turnResult`,
`Turn`, `Output` or a new step; the `tool-<n>-<k>` step input gains `blocked`
and `count`, and the `turnResult` JSON is unchanged; WP-L7-06 adds a test
asserting its exact JSON.

Assistant-text repetition is not part of this guard; it has no tool side effect
and is a candidate for a later ADR. The guard is always on and has no agent
spec field.

## Design choices

1. **Handoff mechanism (`design.md` §4):** Expressing a model handoff as a reserved `handoff_to` tool call intercepted by the agent loop (rather than a `model.Result` field or structured output convention) is a design choice not fixed by `docs/architecture/flow-engine.md` or `model-layer.md`.
2. **Caps across a handoff (`REQ-AGENT-016`, `REQ-AGENT-018`, `REQ-AGENT-019`, `REQ-AGENT-029`, `design.md` §4):** Resetting step, cost, and token caps to the target agent's own caps while keeping the chain's `started_at` and one time cap for the whole chain is a design choice for WP-L7-01: the incoming handoff's `time_cap` governs the target even when the target sets its own (REQ-AGENT-019), a target's own `caps.time` applies only when the handoff carries none (REQ-AGENT-029), and each handoff output carries the cap in effect for its run, or `null` when there is none (REQ-AGENT-016).
3. **Context-window (memory) policy retention rule (`REQ-AGENT-023`–`025`, `design.md` §5):** Keeping `system` messages + whole model turn 1 (with its tool results) + the most recent whole turns that fit in `input_tokens` is a design choice for WP-L7-01 (`docs/roadmap/L7-agents.md` names "memory policy" without prescribing the algorithm).
4. **Turn-boundary check order (`design.md` §6):** Checking `cancellation → time cap → step cap → context-window policy (input token limit) → token cap → cost reservation` is a design choice that determines which terminal state wins when two caps are reached at the same boundary.
5. **Dropped-turn summary (`ADR-0020`, `design.md` §5.1):** An opt-in, incrementally extended, chunked and bounded durable summary between the kept first turn and the kept tail, metered as a model call but not a model turn, is a design choice for WP-L7-05. The alternatives and their costs are in ADR-0020 §3.1.
6. **Loop guard (`ADR-0020`, `design.md` §8):** A soft marker on the 3rd–5th call and a synthetic-result block on the 6th and later, with exact-bytes canonical arguments, a 100-entry pinned LRU rebuilt from the recorded turns and no new durable counter, is a design choice for WP-L7-06. The alternatives and their costs are in ADR-0020 §3.2.

---
