// Package compile turns a flow IR into its validated, canonical form: it
// checks the structural rules and limits of flow/ir, rejects a canonical
// definition above 1 MiB, and type-checks every CEL expression against the
// state schema through package expr (ADR-0003, ADR-0011).
//
// Compiling is deterministic and reads no clock, randomness or instance
// identity: the same declarations always give the same canonical bytes and
// the same content hash (REQ-FLOW-004, REQ-FLOW-005).
package compile
