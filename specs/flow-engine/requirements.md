# Flow engine: flows as data

Status: **Planned** (WP-L4-01, WP-L4-02). Design: ADR-0003, `docs/architecture/flow-engine.md`.

### REQ-FLOW-001 — Conditional edges
When a node completes, the flow machine shall follow the first outgoing edge whose condition holds, in declaration order, and otherwise the edge without a condition.

### REQ-FLOW-002 — Completion order does not matter
When parallel nodes complete, in whatever order, the flow machine shall merge their updates in node-id order, so that the final state does not depend on completion order.

### REQ-FLOW-003 — Guarded cycles
The flow compiler shall reject a flow that has a cycle with no guarded node.

### REQ-FLOW-004 — Type-checked expressions
The flow compiler shall reject a flow whose expressions do not type-check against the state schema, naming the node or edge and the expression.

### REQ-FLOW-005 — Pinned definitions
When a flow definition changes, runs that already started shall continue with the definition hash they started with.

### REQ-FLOW-006 — Gate payload validation
If a gate payload does not satisfy the gate's schema or acceptance condition, then the system shall reject it with code `invalid_argument` and keep the gate open.

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
(owner decision D2, 2026-11-17; `docs/architecture/flow-engine.md` §1). They are
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
