// Package host is the one durable entrypoint of the flow engine
// (runtime.FlowHost, ADR-0011). It loads the flow definition pinned by a run,
// drives the pure machine in flow/machine, turns every command the machine
// returns into a call on the run's runtime.DurableContext, and dispatches an
// effectful node whose kind has a registered NodeHandler to that handler.
//
// The package is runtime-neutral: it may import core, flow, flow/ir, flow/machine
// and runtime,
// never a durable-execution vendor (ADR-0002, ADR-0011, layering.yaml). It
// imports no node handler either; the composition root registers them by kind
// with RegisterHandler before it registers the host with a runtime.
//
// Node kinds are split between the machine and the host
// (docs/architecture/flow-engine.md §4): the machine evaluates set nodes and
// routing with no I/O; the host executes tool and task nodes as one step,
// wait nodes as a timer, gate nodes as a durable gate wait, subflow nodes as a
// child run, and every kind with a registered handler (model, agent today)
// through that handler.
//
// After loading, a non-empty ref.Hash must match ir.Hash of the definition.
// Step idempotency keys include the superstep sequence as well as node and
// attempt. When several futures in one superstep fail, the first one
// DurableContext.Select reports ends the run. A failed node handler ends the
// superstep without running later handlers in the batch. Node handlers report
// settled milli-points on NodeResult; other node kinds accrue no cost through
// the host. WithMaxFanout lowers the map fan-out limit for runs the host starts.
//
// Status: implemented (WP-L4-04).
package host
