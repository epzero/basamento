# Agents: design notes

Decisions the implementer of WP-L7-01 must follow, beyond the requirements and
scenarios. They follow ADR-0002, ADR-0006, ADR-0011,
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
   the memory policy (REQ-AGENT-023–025, REQ-AGENT-027), the token cap
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
   plus that turn's input tokens. Across a handoff the target's step count,
   spent cost and used tokens start at `0` and count only its own turns; the
   time cap is the chain's, measured from the run's start (§4).
7. **Memory policy counts.** A completed model turn is one unit: its assistant
   message (with any tool-call requests) plus the tool-result messages that
   followed it; a turn with `k` tool calls is `1 + k` messages. Before model
   turn `n`, let `T_sys` be the system messages' tokens and `T_i` turn `i`'s
   tokens. If `T_sys + Σ T_i (i = 1…n-1) <= input_tokens`, nothing is dropped.
   Otherwise the policy keeps `T_sys` and, when `n > 1`, turn `1`; if that set
   alone exceeds `input_tokens` the run fails with `invalid_argument` before
   invoking the model (REQ-AGENT-027); else with remainder
   `R = input_tokens - (T_sys + T_1)` it keeps whole turns walking backward
   from `n-1` to `2` while each fits in `R`, and drops the others
   (REQ-AGENT-024, REQ-AGENT-025).

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
| `turn-<n>` | one model turn: reserve, `model.Invoke` (blocking form), settle | the model output, the usage, the charge, the count of messages the memory policy dropped (§5) |
| `tool-<n>-<k>` | tool call `k` of turn `n` | the tool result |

`n` and `k` are 1-based. The step's `IdempotencyKey` is its name, stable for
the run. One model turn is one invocation with
`ClientRef = hash(run id, node id, "turn-<n>")` (REQ-MODEL-004); a resumed run
never invokes a completed turn again (REQ-AGENT-002), and an ambiguous start is
resolved as REQ-MODEL-005 says. WP-L7-01 invokes the model with the blocking
`model.Invoke` form only; streaming a turn's text as one message per
`docs/architecture/streaming.md` is added by WP-L6-02 and WP-L7-03 (`agent`
may not import `stream`). The step result is the durable `message.completed`.

## 3. Agent node spec

`flow-engine.md` §1 shows `{ agent: planner }`. An agent is defined in that
spec with the fields below. The implementer encodes them as the node spec
schema of the `agent` handler; this is a decision of this work package, not a
change to package `flow`. The node id is the node's key in `flow.IR.Nodes`, and
`flow.Node.Out` names the state key receiving the run output.

| Field | Meaning | Default |
| --- | --- | --- |
| `model` | the model id of every turn | — |
| `system` | the system messages, kept first by the memory policy | none |
| `tools` | the tools offered to the model (allowlists and tool access rules are WP-L7-02) | none |
| `checks` | accepted for compatibility; no effect — cancellation and the time cap are checked before every model turn (REQ-AGENT-012, REQ-AGENT-028) | 1 |
| `caps.steps` | the step cap, in model turns | none = no cap |
| `caps.cost_mpts` | the cost cap, in milli-points | none = no cap |
| `caps.tokens` | the token cap, counted as input plus output tokens | none = no cap |
| `caps.time` | the time cap, a duration on the run clock | none = no cap |
| `input_tokens` | the input token limit of the model, in tokens | `0` = no limit |

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
 "started_at": "2026-11-17T10:00:00Z", "time_cap": "1m"}
```

`turns` is every model turn of the first agent in order, the closing turn
included: that turn's assistant message, as recorded, holds the `handoff_to`
request, exactly as the requests of its other tool calls. `started_at` is the
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
that declare none, storing nothing and persisting nothing between runs. The
name "memory policy" is kept for compatibility with REQ-AGENT-023–027, but it
is not cross-run memory. Memory that outlives a run — facts about the codebase,
what earlier runs did, how to do a thing — and permission gating and lifecycle
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

## 6. Order of checks at a turn boundary

At each turn boundary before model turn `n`, in this order:
1. cancellation (REQ-AGENT-028);
2. the time cap (REQ-AGENT-012);
3. the step cap (REQ-AGENT-008);
4. the memory policy and input token limit (REQ-AGENT-023–025, REQ-AGENT-027),
   which determines turn `n`'s messages and input tokens;
5. the token cap (REQ-AGENT-011);
6. the cost reservation (REQ-AGENT-009, REQ-AGENT-010).

The first check that stops the run decides its state, so a run that meets two
caps reports the earlier one in this order.

## 7. Changes outside this directory

None needed to write this work package. The tool port's interface is landed by
WP-L7-02 in `tool`; the `agent` node spec schema belongs to the handler
registered by this work package. Should implementing the scenarios need a
change outside the directories this WP owns (`agent`, `specs/agents`,
`internal/bdd/steps/agent.go`), the implementer lands that change first, in a
pull request of its own.

## Design choices

1. **Handoff mechanism (`design.md` §4):** Expressing a model handoff as a reserved `handoff_to` tool call intercepted by the agent loop (rather than a `model.Result` field or structured output convention) is a design choice not fixed by `docs/architecture/flow-engine.md` or `model-layer.md`.
2. **Caps across a handoff (`REQ-AGENT-016`, `REQ-AGENT-018`, `REQ-AGENT-019`, `REQ-AGENT-029`, `design.md` §4):** Resetting step, cost, and token caps to the target agent's own caps while keeping the chain's `started_at` and one time cap for the whole chain is a design choice for WP-L7-01: the incoming handoff's `time_cap` governs the target even when the target sets its own (REQ-AGENT-019), a target's own `caps.time` applies only when the handoff carries none (REQ-AGENT-029), and each handoff output carries the cap in effect for its run, or `null` when there is none (REQ-AGENT-016).
3. **Memory policy retention rule (`REQ-AGENT-023`–`025`, `design.md` §5):** Keeping `system` messages + whole model turn 1 (with its tool results) + the most recent whole turns that fit in `input_tokens` is a design choice for WP-L7-01 (`docs/roadmap/L7-agents.md` names "memory policy" without prescribing the algorithm).
4. **Turn-boundary check order (`design.md` §6):** Checking `cancellation → time cap → step cap → memory policy (input token limit) → token cap → cost reservation` is a design choice that determines which terminal state wins when two caps are reached at the same boundary.

---
