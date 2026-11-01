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
