# Agents: design notes

Decisions the implementer of WP-L7-01 must follow, beyond the requirements and
scenarios. They follow ADR-0002, ADR-0006, ADR-0011,
`docs/architecture/flow-engine.md` §4 and `docs/architecture/model-layer.md`.
They introduce no behavior the scenarios do not show; where a number is given
below it is the one the scenarios use.

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
resolved as REQ-MODEL-005 says. A turn's text streams as one message per
`docs/architecture/streaming.md` (WP-L6-01/02); the step result is the
durable `message.completed`.

## 3. Agent node spec

`flow-engine.md` §1 shows `{ agent: planner }`. An agent is defined in that
spec with the fields below. The implementer encodes them as the node spec
schema of the `agent` handler; this is a decision of this work package, not a
change to package `flow`.

| Field | Meaning | Default |
| --- | --- | --- |
| `node` | the node id a handoff names (scenarios: `"triage"`, `"fixer"`) | — |
| `model` | the model id of every turn | — |
| `system` | the system messages, kept first by the memory policy | none |
| `tools` | the tools the agent allows (each tool's own access rules are WP-L7-02) | none |
| `checks` | model turns between checks of the time cap and cancellation: **1** | 1 |
| `caps.steps` | the step cap, in model turns | none = no cap |
| `caps.cost_mpts` | the cost cap, in milli-points | none = no cap |
| `caps.tokens` | the token cap, counted as input plus output tokens | none = no cap |
| `caps.time` | the time cap, a duration on the run clock | none = no cap |
| `input_tokens` | the input token limit of the model, in tokens | the model's descriptor |
| `out` | the state key of the run output | `agent` |

A cap that is absent is not applied. The scenarios are deterministic because
the scripted model answers with the usage and cost written in the scenario;
the reservation estimate is the price formula on estimated usage, as
REQ-MODEL-006 (cost.feature) already specifies, and `model` node metering is
not repeated here.

## 4. Handoff

The model requests a handoff the way its provider expresses a tool call, with
the tool name `handoff_to` and the argument naming a node id of the flow. The
agent loop treats that request specially: it is not a `tool-call step`
(REQ-AGENT-003 counts only the agent's allowed tools).

The agent run ends completed with state `handoff` and an output:

```json
{"state": "handoff", "target": "fixer", "turns": [ … model turns … ]}
```

The node's outgoing edge leads to the target node, which receives this output
as its input. When the target's handler sees `state: "handoff"`, it takes
`turns` as its message history (REQ-AGENT-017) and its **chain**: the ordered
list of node ids that ran, so that a second visit to one of them is a
`conflict` (REQ-AGENT-022). A node that starts without a handoff starts a new
chain with itself.

Caps (REQ-AGENT-018, REQ-AGENT-019): the target's `caps.steps` counts the
target's own model turns (the carried turns are not the target's turns); the
target's `caps.cost_mpts` and `caps.tokens` count the target's own spend and
usage, so carried turns never refuse a handoff (only a handoff into a node
that already ran does, REQ-AGENT-022); the target's **time cap is the value
the chain started with**, measured from the run's start on the run clock, so
a chain is bounded in time whatever its length. When the target has no time
cap and the first agent had one, the target runs under the first agent's time
cap.

A handoff is not a cap: if the target requests a handoff in its first turn,
the chain continues.

## 5. Memory policy

Before each model turn the agent loop builds the turn's messages: the
history, then the new tool results. If they exceed `input_tokens`, the policy
keeps:

1. every `system` message of the agent (REQ-AGENT-023);
2. the **first model turn** and the tool results that followed it (the run's
   opening request and its first answer, kept whole) (REQ-AGENT-023);
3. the most recent model turns that fit in the limit's remainder, latest
   first, a whole turn at a time: a model turn with the tool-call results it
   asked for (REQ-AGENT-024, REQ-AGENT-025).

Messages dropped are counted and recorded in the step result, not kept
elsewhere (REQ-AGENT-026): the run's messages stay complete for its output.
A kept set that still exceeds the limit fails the run with `invalid_argument`
(REQ-AGENT-027).

## 6. Order of checks at a turn boundary

At each boundary, in this order: cancellation (REQ-AGENT-028), then the time
cap (REQ-AGENT-012), then the step cap (REQ-AGENT-008), then the token cap
(REQ-AGENT-011), then the reservation (REQ-AGENT-009/010). The first check
that stops the run decides its state, so a run that meets two caps reports
the earlier one in this order.

## 7. Changes outside this directory

None needed to write this work package. The tool port's interface is landed by
WP-L7-02 in `tool`; the `agent` node spec schema belongs to the handler
registered by this work package. Should implementing the scenarios need a
change outside the directories this WP owns (`agent`, `specs/agents`,
`internal/bdd/steps/agent.go`), the implementer lands that change first, in a
pull request of its own.
