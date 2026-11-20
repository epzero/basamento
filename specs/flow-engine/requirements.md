# Flow engine: flows as data

Status: **In progress** (IR and compiler implemented, WP-L4-01; machine, builder and host planned, WP-L4-02, WP-L4-03, WP-L4-04). Design: ADR-0003, `docs/architecture/flow-engine.md`.

### REQ-FLOW-001 — Conditional edges
When a node completes, the flow machine shall follow the first outgoing edge whose condition holds, in declaration order, and otherwise the edge without a condition.

### REQ-FLOW-002 — Completion order does not matter
When parallel nodes complete, in whatever order, the flow machine shall merge their updates in node-id order, so that the final state does not depend on completion order.

### REQ-FLOW-003 — Bounded cycles
The flow compiler shall reject a flow that has a cycle on which no node has a guard carrying a positive `max_visits` or `cost_mpts`, naming a node on the cycle and the missing bound. A `while` condition alone does not bound a cycle.

### REQ-FLOW-004 — Type-checked expressions
The flow compiler shall reject a flow whose expressions do not type-check against the state schema, naming the node or edge and the expression.

### REQ-FLOW-005 — Pinned definitions
When a flow definition changes, runs that already started shall continue with the definition hash they started with.

### REQ-FLOW-006 — Gate payload validation
If a gate payload does not satisfy the gate's schema or acceptance condition, then the system shall reject it with code `invalid_argument` and keep the gate open.

## Flow machine (WP-L4-02)

The machine enforces the bounds the compiler accepted (REQ-FLOW-003, REQ-FLOW-045) and the fan-out limit of `design.md` §2.5 at run time. Scenarios: `routing.feature`.

### REQ-FLOW-047 — Guard budgets at run time
While the run's settled cost is at least the positive `cost_mpts` of a node's guard, or the node has had at least the positive `max_visits` visits that guard allows, the flow machine shall not start that node and shall fail the run with code `budget_exceeded`, naming the node and the budget it reached.

### REQ-FLOW-048 — The map fan-out limit
If the collection a `map` node fans out over holds more items than the fan-out limit of `design.md` §2.5, then the flow machine shall fail the run with code `budget_exceeded` before it creates any fan-out for that node, naming the node, the item count and the limit, and the failure shall carry no state data.

### REQ-FLOW-049 — A finished node's settled cost accrues to the run
When the flow machine applies a `node_completed` or `node_failed` event carrying a node's settled cost, the flow machine shall add that cost, in milli-points, to the run's settled cost, which later expressions read as `run.cost`.

## Compiler limits (WP-L4-01)

The maxima in REQ-FLOW-045 and REQ-FLOW-046 are fixed properties of the
compiler, with their values and reasons in `design.md` §2.2 and §2.3. They are
requirements, not tuning knobs: changing a value is a spec change.

### REQ-FLOW-045 — Guard budgets within their maxima
If a node's guard gives `max_visits` or `cost_mpts` a negative value, or a value above its maximum in `design.md` §2.2, then the flow compiler shall reject the flow with code `invalid_argument`, naming the node, the field and the maximum.

### REQ-FLOW-046 — Definition size limits
If a flow definition's node count, edge count, size of any one expression or canonical JSON size exceeds its maximum in `design.md` §2.3, then the flow compiler shall reject the definition with code `invalid_argument`, naming the limit and its maximum.

## Go builder (WP-L4-03)

A flow is authored in Go with a typed builder, in YAML or by an LLM as data
(ADR-0003). The builder produces the IR of `flow/flow.go` and nothing more: it
checks what can be known from the declarations given to it, and the compiler
(WP-L4-01) checks the rest. Scenarios: `builder.feature`.

### REQ-FLOW-010 — The builder produces the IR of the equivalent data flow
The flow builder shall produce, for a flow built from a set of declarations, the IR that the compiler produces for the flow written as data with the same declarations.

### REQ-FLOW-011 — Deterministic building
The flow builder shall produce byte-identical IR every time it builds the same declarations, whatever the order in which the nodes and the reducers of the flow were declared.

### REQ-FLOW-012 — Every node kind
Where a flow declares a node of one of the kinds `set`, `task`, `model`, `agent`, `tool`, `map`, `parallel`, `subflow`, `gate` and `wait`, the flow builder shall include that node in the IR with its kind, its node spec, its input and output mapping and its guard as declared.

### REQ-FLOW-013 — Every edge form
Where a flow declares an edge between two nodes, from the entry point or to a failure, with or without a CEL condition, the flow builder shall include that edge in the IR with its source, its target and its condition as declared, in the order the edges were declared.

### REQ-FLOW-014 — Entry and failure edge ends
Where a flow declares an edge from `"START"` or to `"FAIL"`, or marks a node as the entry node, the flow builder shall write the entry edge with source `"START"` and the failure edge with target `"FAIL"` in the IR.

### REQ-FLOW-015 — A duplicate node name
If a flow declares two nodes with the same name, then the flow builder shall reject it with code `invalid_argument`, naming the duplicated node name.

### REQ-FLOW-016 — An edge to or from a node that does not exist
If a flow declares an edge whose source is neither a declared node nor `"START"`, or whose target is neither a declared node nor `"FAIL"`, then the flow builder shall reject it with code `invalid_argument`, naming the missing node name.

### REQ-FLOW-017 — A flow that does not say where it starts
If a flow declares no edge from the entry point and no node marked as the entry node, then the flow builder shall reject it with code `invalid_argument`, stating that the flow has no entry.

### REQ-FLOW-018 — More than one marked entry node
If a flow marks more than one node as the entry node, then the flow builder shall reject it with code `invalid_argument`, naming the marked nodes.

### REQ-FLOW-019 — An out value that is not a state key
If a flow declares a node whose out value is not a key of the flow's state schema, then the flow builder shall reject it with code `invalid_argument`, naming the node and the key.

### REQ-FLOW-021 — State schema and reducers from Go declarations
The flow builder shall write into the IR, as the flow's state schema, the JSON Schema declared for the flow's state and, as the flow's reducers, the reducer of each state key as declared.

### REQ-FLOW-024 — A reducer for a key that is not in the state schema
If a flow declares a reducer for a key that is not a key of the flow's state schema, then the flow builder shall reject it with code `invalid_argument`, naming the state key.

### REQ-FLOW-026 — An invalid state schema
If a flow declares a state schema that is not a valid JSON Schema, then the flow builder shall reject it with code `invalid_argument`, naming the location in the schema of the violation.
## Reserved edge endpoints (WP-L4-01)

`START` and `FAIL` are reserved names for the entry and the failure exit of a flow
(`docs/architecture/flow-engine.md` §1). They are
edge endpoints only: neither is declared as a node. Every rejection below fails
with code `invalid_argument`, naming the node or edge at fault (REQ-FLOW-040 to
REQ-FLOW-042) or stating that the flow needs exactly one edge from `START`
(REQ-FLOW-043).

### REQ-FLOW-039 — START and FAIL need no node
The flow compiler shall accept an edge from `START` and an edge to `FAIL` without `START` or `FAIL` being declared as a node.

### REQ-FLOW-040 — START and FAIL are not node names
If a flow declares a node named `START` or `FAIL`, then the flow compiler shall reject the flow with code `invalid_argument` and name the node.

### REQ-FLOW-041 — START is only an edge source
If an edge targets `START`, then the flow compiler shall reject the flow with code `invalid_argument` and name the edge.

### REQ-FLOW-042 — FAIL is only an edge target
If an edge starts at `FAIL`, then the flow compiler shall reject the flow with code `invalid_argument` and name the edge.

### REQ-FLOW-043 — Exactly one edge starts at START
If a flow has no edge from `START`, or more than one, then the flow compiler shall reject the flow with code `invalid_argument`, stating that the flow needs exactly one edge from `START`.

## Flow host (WP-L4-04)

The flow host is the one durable entrypoint, `runtime.FlowHost` (ADR-0011): it
loads the flow definition pinned by a run, drives the pure flow machine, turns
the machine's commands into durable calls on the run's `DurableContext`, and
dispatches the nodes of an effectful kind whose kind has a registered node
handler to that handler. Scenarios: `host.feature`.

### REQ-FLOW-027 — The pinned definition
While a run is in progress, the flow host shall run the flow definition the run was started with, even if a different version of the flow is registered while the run is in progress.

### REQ-FLOW-028 — A node kind without a node handler
If a flow has a node whose kind is `model` or `agent` and no node handler is registered for that kind, then the flow host shall fail the run with code `unsupported` naming the node, and run no node of the run.

### REQ-FLOW-029 — Effectful nodes become steps
When the flow machine runs a node of kind `tool` or `task`, the flow host shall execute it as one step named by the node's specification with the node's input, and take the step's result as the node's output.

### REQ-FLOW-030 — Nodes of a kind with a node handler
When the flow machine runs a node of a kind that has a registered node handler, the flow host shall run that node by calling the node handler registered for its kind with the node and the node's input, and take the handler's output as the node's output.

### REQ-FLOW-031 — A waiting node settles through the run
When the flow machine runs a node of kind `wait` whose specification gives a duration, the flow host shall wait that duration before the node completes, and complete the node with no result.

### REQ-FLOW-032 — Gates open on a durable wait
When the flow machine runs a node of kind `gate`, the flow host shall wait for the gate named after the node with the schema, acceptance condition and timeout of the node's specification, and complete the node with the accepted payload.

### REQ-FLOW-033 — A subflow runs as a child run
When the flow machine runs a node of kind `subflow`, the flow host shall start a child run of the flow named in the node's specification with the node's input, and complete the node with the child run's result.

### REQ-FLOW-035 — Failed durable nodes report their failure
If a node's handler, step, gate or child run returns an error while running a node, then the flow host shall report the node as failed to the flow machine with the error's platform code (or `internal` when unclassified) and fail the run with that code when the machine fails the run.

### REQ-FLOW-036 — Pure nodes make no durable calls
The flow host shall make no durable call for a node of kind `set`, and none for the routing of the flow.

### REQ-FLOW-037 — Replay makes the same durable calls
While a runtime replays a run from its recorded history, the flow host shall make the same durable calls in the same order as when the run first made them, and take their results from the history.

### REQ-FLOW-038 — A run finishes with its result
When the flow machine completes a run, the flow host shall complete the run with the run's final state and make no further durable call for it.

### REQ-FLOW-044 — Concurrent superstep commands
When the flow machine returns multiple commands in one superstep, the flow host shall start every durable call in that superstep before waiting for any of them to complete.

### REQ-FLOW-050 — The cost is part of the node's recorded result
When the flow host reports a finished node to the flow machine, the flow host shall report the node settled cost in milli-points, taken from the result the node step, node handler or child run returned for this run; a gate or wait node completion carries no cost.
