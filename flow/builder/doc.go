// Package builder authors flow IR in Go with typed declarations.
//
// A Builder collects nodes, edges, reducers and a state schema; Build returns
// the same *flow.IR the compiler accepts for the equivalent flow written as
// data (REQ-FLOW-010). It checks only what the declarations can tell it: the
// state schema, reducer and node output keys, node names, edge endpoints and
// the entry point. Node kind and spec shape, expression typing and guarded
// cycles stay the compiler's checks (ADR-0003, specs/flow-engine/design.md §1).
//
// Building is deterministic: the canonical JSON of the IR sorts node ids and
// reducer keys, edges keep declaration order, and Build reads no clock,
// randomness or instance identity (REQ-FLOW-011).
package builder
