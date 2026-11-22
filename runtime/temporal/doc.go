// Package temporal is the Temporal-backed durable runtime (WP-L5-02).
//
// Pull request (a) delivers the data path: tenant payload codec, claim-check,
// flow-host workflow with Step/Sleep/Select, and step activity retries.
//
// Pull request (b) delivers gates as Temporal Updates (journal-driven Await),
// synchronous basamento-emit outbox activity, Child host workflows,
// Checkpoint/ContinueAsNew with continuation state in HostInput.Continuation,
// and Capabilities (durable, sync_gate_updates, continue_as_new, polyglot_queues).
// Flow code must replay durable calls in the same order; step results journal by
// name. DefaultCheckpointEventThreshold is 2000 events. ContinueAsNew carries the
// journal, not NewID/Rand counters.
//
// Pull request (c) implements runtime.Runtime: Start, Describe, Cancel,
// Resolve, List, Capabilities, and Serve. Options.Client supplies host,
// namespace, TLS and credentials; the runtime owns DataConverter and
// ContextPropagators. Tenant ids that are unsafe inside visibility query literals
// (quotes, backslash, controls, and the like) are rejected on every control call;
// see docs/architecture/runtime.md §8 (control plane, tenant filters).
//
// Failure messages in Temporal history are plaintext: only core error codes and
// messages cross the boundary (Details maps are dropped). Non-core errors become
// internal with a fixed message; the original text is logged only, bounded like
// panic values. The tenant header is plaintext by necessity. The workflow-bound
// converter fails transient codec errors as workflow tasks (sanitized panic);
// permanent errors are returned. On the workflow-bound path, cancelled means bound
// expiry (the blob layer may map a deadline that way) or store-reported
// cancellation—retried like unavailable, not a caller cancel. Run cancellation
// (REQ-RT-013) is separate. Workflow-bound I/O is capped at 30 s per call so a
// hung blob or key request cannot block a worker thread forever; if that bound
// expires the workflow task fails (sanitized panic) and the server retries, and the
// run outcome is unchanged once I/O succeeds. Client and activity converters treat
// caller-cancelled contexts as permanent errors.
// Workers must keep SDK default WorkflowPanicPolicy (BlockWorkflow); Serve pins
// that policy and a 30 s worker stop timeout.
//
// New requires Outbox at the composition root; production code includes no
// in-memory outbox.
//
// Status: WP-L5-02 data path, workflow features and control plane implemented;
// embedded server packaging is WP-L5-03.
package temporal
