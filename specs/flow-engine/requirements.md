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

## Reserved edge endpoints (WP-L4-01)

`START` and `FAIL` are reserved names for the entry and the failure exit of a flow
(owner decision D2, 2026-11-17; `docs/architecture/flow-engine.md` §1). They are
edge endpoints only: neither is declared as a node. Every rejection below fails
with code `invalid_argument` and names the node or the edge at fault, as
REQ-FLOW-003 and REQ-FLOW-004 name theirs.

### REQ-FLOW-039 — START and FAIL need no node
The flow compiler shall accept an edge from `START` and an edge to `FAIL` without `START` or `FAIL` being declared as a node.

### REQ-FLOW-040 — START and FAIL are not node names
If a flow declares a node named `START` or `FAIL`, then the flow compiler shall reject the flow with code `invalid_argument` and name the node.

### REQ-FLOW-041 — START is only an edge source
If an edge targets `START`, then the flow compiler shall reject the flow with code `invalid_argument` and name the edge.

### REQ-FLOW-042 — FAIL is only an edge target
If an edge starts at `FAIL`, then the flow compiler shall reject the flow with code `invalid_argument` and name the edge.

### REQ-FLOW-043 — Exactly one edge starts at START
If a flow has no edge from `START`, or more than one, then the flow compiler shall reject the flow with code `invalid_argument` and name the edges from `START`.
