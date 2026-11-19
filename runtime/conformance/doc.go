// Package conformance holds the runtime conformance suite: the golden flows
// every runtime must reproduce, the records of their final state and durable
// events, and the harness that runs them (WP-L5-04).
//
// A golden record is a JSON document under testdata/ and changes only with
// -update and a reason in the commit body (AGENTS.md, specs/runtime/design.md
// §What stays with conformance). The flows are written once against
// runtime.DurableContext, so runtime/local and runtime/temporal run the same
// code with only their clocks and journals differing.
//
// The suite uses the fake clock the caller supplies: a gate with a timeout is
// reached by advancing that clock, never by sleeping. Steps return deterministic
// JSON and record their calls, so two runs of one flow produce byte-identical
// records.
package conformance
