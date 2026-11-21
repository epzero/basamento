// Package local is the in-process Runtime: goroutines and an in-memory journal.
// It is not durable by design and says so (REQ-RT-005, REQ-RT-006). Finished
// runs and idempotency keys stay in memory for the life of the process; nothing
// is evicted. It is a development runtime for tests, the CLI and short
// interactive runs, and must pass the runtime conformance suite.
//
// One goroutine runs the registered FlowHost of each run, steps run on their own
// goroutines, and every timer waits on the core.Clock the composition root
// supplies; the package never reads the wall clock or sleeps. Recovered panics
// are written once to Options.Logger (default slog.Default()) with tenant, run,
// step, the panic value cut at 4,096 runes and the stack cut at 16 KiB; error
// values returned to callers omit the panic text. See specs/runtime/design.md
// §Local runtime and ADR-0002.
package local
