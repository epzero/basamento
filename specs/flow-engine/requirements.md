# Flow engine: flows as data

Status: **Planned** (WP-L4-01, WP-L4-02, WP-L4-03). Design: ADR-0003, `docs/architecture/flow-engine.md`.

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
The flow builder shall produce byte-identical IR every time it builds the same declarations, whatever the order in which the nodes and the edges of the flow were declared.

### REQ-FLOW-012 — Every node kind
Where a flow declares a node of one of the kinds `set`, `task`, `model`, `agent`, `tool`, `map`, `parallel`, `subflow`, `gate` and `wait`, the flow builder shall include that node in the IR with its kind, its node spec, its input and output mapping and its guard as declared.

### REQ-FLOW-013 — Every edge form
Where a flow declares an edge between two nodes, from the entry point or to a failure, with or without a CEL condition, the flow builder shall include that edge in the IR with its source, its target and its condition as declared, in the order the edges were declared.

### REQ-FLOW-014 — The entry point
When a flow built with the flow builder is compiled, the flow compiler shall accept node "START" as a source of edges and node "FAIL" as a target of edges.

### REQ-FLOW-015 — A duplicate node name
If a flow declares two nodes with the same name, then the flow builder shall reject it with code `conflict`, naming the duplicated node name.

### REQ-FLOW-016 — An edge to a node that does not exist
If a flow declares an edge whose source or target is neither a declared node nor the entry point or the failure target, then the flow builder shall reject it with code `not_found`, naming the missing node name.

### REQ-FLOW-017 — A flow that does not say where it starts
If a flow declares no edge from the entry point and no node marked as the entry node, then the flow builder shall reject it with code `invalid_argument`, stating that the flow has no entry.

### REQ-FLOW-018 — More than one marked entry node
If a flow marks more than one node as the entry node, then the flow builder shall reject it with code `invalid_argument`, naming the marked nodes.

### REQ-FLOW-019 — An out value that is not a state key
If a flow declares a node whose out value is not a key of the flow's state schema, then the flow builder shall reject it with code `invalid_argument`, naming the node and the key.

### REQ-FLOW-020 — A condition of the wrong type
If a flow declares an edge condition whose CEL expression has a result type other than `bool`, then the flow builder shall reject it with code `invalid_argument`, naming the edge and the expression.

### REQ-FLOW-025 — A condition the builder leaves to the compiler
If a flow declares an edge condition that is a boolean-valued CEL expression that does not type-check, then the flow builder shall keep the condition in the IR so that the flow compiler rejects it.

### REQ-FLOW-021 — State schema and reducers from Go declarations
The flow builder shall write into the IR, as the flow's state schema, the JSON Schema declared for the flow's state and, as the flow's reducers, the reducer of each state key as declared.

### REQ-FLOW-022 — A flow built in Go compiles like the same flow written as data
When a flow built with the flow builder is compiled, the flow compiler shall accept or reject it exactly as the flow written as data with the same declarations, with the same error.

### REQ-FLOW-023 — The compiler's checks stay with the compiler
The flow builder shall hash and compile nothing: a built flow's content hash and the compiler's verdict come from the build result passed to the compiler.

### REQ-FLOW-024 — A declaration that contradicts itself
If a flow declares a state schema or a reducer that the flow builder cannot put into the IR, then the flow builder shall reject it with the code the schema compiler returns, naming the state key or reducer.
