// Package machine is the pure flow interpreter of ADR-0003.
//
// A Machine runs one compiled flow definition (flow.IR). It reads no clock,
// does no I/O and imports no runtime: Start and Apply take the machine's own
// snapshot and an event and return the next snapshot and the commands the flow
// host must carry out.
//
// # Supersteps
//
// A run advances in supersteps. Every node the machine starts in one superstep
// receives its command together, and the next superstep starts only when all
// of them have reported. At the barrier their updates reach the state through
// the key's reducer in canonical order: node id, then map attempt. That order
// makes the final state independent of the order in which parallel nodes
// completed (REQ-FLOW-002).
//
// # Routing
//
// When a node completes the machine follows its first outgoing edge whose CEL
// condition holds, in declaration order, and otherwise its edge without a
// condition (REQ-FLOW-001). A node with no outgoing edge ends its branch; a
// branch that reaches the reserved FAIL endpoint fails the run.
//
// # Node kinds
//
// The machine itself runs set nodes: a set node's spec is either a CEL
// expression whose value is written to the node's Out key, or a JSON object of
// literal state assignments. Edge routing is likewise evaluated here, with no
// durable call (REQ-FLOW-036). Every other kind becomes a command:
// run_node for task, model, agent, tool and subflow; await_gate for gate;
// start_timer for wait.
//
// A parallel node starts every node named by its outgoing edges in one
// superstep; its edges are an unconditional fan-out. A map node evaluates its
// spec's over expression to an array and runs the node named by its node field
// once per element, carrying the element as the command input and the
// one-based index as the attempt, up to DefaultMaxFanout (10 000) items per
// superstep; the host may lower the limit with WithMaxFanout. max_parallel is
// the host's throttle on how many map children run at once, not a cap on fan-out.
//
// # Guards
//
// Before a node runs its guard is checked. A guard While condition that does
// not hold skips the node and ends its branch. A visit or cost bound that is
// reached fails the run with code budget_exceeded (specs/glossary.md, "Repair
// guard").
package machine
