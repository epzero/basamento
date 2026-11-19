// Package local is the in-process Runtime: goroutines and an in-memory journal.
// It is not durable by design and says so (REQ-RT-005, REQ-RT-006). It exists
// for tests, the CLI and short interactive runs, and must pass the runtime
// conformance suite.
//
// One goroutine runs the registered FlowHost of each run, steps run on their own
// goroutines, and every timer waits on the core.Clock the composition root
// supplies; the package never reads the wall clock or sleeps. See
// specs/runtime/design.md §Local runtime and ADR-0002.
package local
