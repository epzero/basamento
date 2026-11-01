// Package local is the in-process Runtime: goroutines and an in-memory journal.
// It is not durable by design. It exists for tests, the CLI and short
// interactive runs, and must pass the runtime conformance suite.
//
// Status: planned (WP-L5-01).
package local
