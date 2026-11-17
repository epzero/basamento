# Agents: the bounded tool loop

Status: **Planned** (WP-L7-01; tools and MCP in WP-L7-02, conversations in
WP-L7-03). Design: ADR-0002, ADR-0006, ADR-0007, ADR-0011,
`docs/architecture/flow-engine.md`, `docs/architecture/model-layer.md`.

An **agent run** is a run of an `agent` flow node. The node's flow code (§4 of
`docs/architecture/flow-engine.md`; package `agent`) is the **agent loop**:
every **model turn** is one durable step, so each model output is recorded,
retried and metered. A **tool call** is one durable step too. The loop ends
when the model answers without requesting a tool, or when a **cap** (steps,
cost, tokens, time on the run clock) stops it. A **handoff** starts the agent
node named by the model: the model asks for it with a `handoff_to` request,
which the loop reads before it makes any tool call of that turn. What goes
into the next turn is decided by the **memory policy**.

### REQ-AGENT-001 — One model turn per step
While an agent run is running, the agent loop shall make each model turn one step of the run whose result holds that turn's model output.

### REQ-AGENT-002 — A finished turn is not paid for twice
When an agent run resumes, the agent loop shall feed every model turn recorded as completed from its step result.

### REQ-AGENT-003 — A request for a tool becomes a tool call
When a model turn requests `n` non-handoff tools and no handoff, the agent loop shall make `n` tool-call steps for those requests, one for each.

### REQ-AGENT-004 — A tool result is fed to the next turn
When a tool-call step completes, the agent loop shall include its result in the next model turn's messages.

### REQ-AGENT-005 — A failed tool call fails the run
If a tool-call step fails, then the agent run shall fail with that step's error code.

### REQ-AGENT-007 — An answer ends the loop
When a model turn answers without requesting a tool, the agent loop shall end the agent run as completed with that turn's output as the run output.

### REQ-AGENT-008 — The step cap
If starting a model turn would make the agent run's model turns exceed the step cap, then the agent loop shall end the agent run as completed with state `step_cap_reached` and its completed model turns in order without invoking the model.

### REQ-AGENT-009 — The cost cap reserves first
When starting a model turn, the agent loop shall reserve the turn's estimated cost against a budget of the cost cap in milli-points before invoking the model.

### REQ-AGENT-010 — The cost cap
If the agent run's spent cost plus a model turn's reservation would exceed the cost cap, then the agent loop shall end the agent run as completed with state `cost_cap_reached` and its completed model turns in order without invoking the model.

### REQ-AGENT-011 — The token cap
If the agent run's used tokens plus a model turn's input tokens would exceed the token cap, then the agent loop shall end the agent run as completed with state `token_cap_reached` and its completed model turns in order without invoking the model.

### REQ-AGENT-012 — The time cap
If the agent run's elapsed time from the start of the run on the run clock is at least the time cap when starting a model turn, then the agent loop shall end the agent run as completed with state `time_cap_reached` and its completed model turns in order without invoking the model.

### REQ-AGENT-016 — A handoff carries the turns
When a model turn requests a handoff to an agent whose node the flow defines, the agent loop shall end the agent run as completed with state `handoff`, the target node, every model turn of the run in order, the run's start time on the run clock and the time cap in effect for the run: that agent's own time cap, or, when it has none, the time cap its input carries, or `null` when neither exists.

### REQ-AGENT-017 — A handoff starts the target with the turns
When an `agent` node's input carries a handoff with the target node, the agent loop shall make the target agent's first model turn with the carried turns in the message history.

### REQ-AGENT-018 — The first agent's caps do not bind the target
When an `agent` node's input carries a handoff with the target node, the agent loop shall take the target agent's step cap, cost cap and token cap in place of the first agent's.

### REQ-AGENT-019 — The time cap spans the handoff
When an `agent` node's input carries a handoff with the target node and the first agent has a time cap, the agent loop shall measure the target agent's elapsed time on the run clock from the run's start against that time cap.

### REQ-AGENT-029 — The target's time cap when the first agent had none
When an `agent` node's input carries a handoff with the target node, the first agent has no time cap and the target agent has one, the agent loop shall measure the target agent's elapsed time on the run clock from the run's start against the target agent's time cap.

### REQ-AGENT-020 — A handoff to an agent the flow does not define
If a model turn requests a handoff to an agent whose node the flow does not define, then the agent run shall fail with code `not_found` naming the agent.

### REQ-AGENT-030 — Multiple handoffs in one turn fail
If a model turn requests more than one handoff, then the agent run shall fail with code `invalid_argument` without making a tool-call step.

### REQ-AGENT-023 — The memory policy keeps the start
If the messages of a model turn exceed the agent's input token limit when starting the turn, then the memory policy shall keep the agent's system messages and the agent run's first model turn.

### REQ-AGENT-024 — The memory policy keeps the end
If the messages of a model turn exceed the agent's input token limit when starting the turn, then the memory policy shall keep the most recent model turns that fit in the remaining tokens, latest first, dropping whole turns.

### REQ-AGENT-025 — A dropped request takes its result with it
When the memory policy drops a model turn, the memory policy shall drop the tool-call requests it contains and those tools' results.

### REQ-AGENT-027 — Messages over the limit even after the policy
If the messages the memory policy keeps exceed the agent's input token limit, then the agent loop shall fail the agent run with code `invalid_argument` without invoking the model.

### REQ-AGENT-028 — Cancellation between turns
When an agent run is cancelled while it waits to start a model turn, the agent loop shall fail the agent run with code `cancelled` without starting another model turn.
