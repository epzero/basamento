// Package temporal is the Temporal-backed durable runtime (WP-L5-02).
//
// This change delivers the data path: a context-aware payload data converter
// (tenant encryption and blob claim-check), the flow-host workflow with
// DurableContext (Context, Now, NewID, Rand, Step, Sleep, Select, Capabilities),
// the step activity with retry mapping, and registration helpers for workers and
// the in-process SDK test harness.
//
// Child, Await, Emit and Checkpoint return code unsupported until the next
// WP-L5-02 changes. The control plane (runtime.Runtime Start, Resolve, Cancel,
// Describe, List, Serve) arrives in a later change of the same work package.
//
// Status: in progress (WP-L5-02, EP0-31); embedded server packaging is WP-L5-03.
package temporal
