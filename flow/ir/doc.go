// Package ir is the canonical form of a flow definition: decoding, RFC 8785
// canonical JSON, the content hash runs pin, and the structural validation
// the compiler runs before it type-checks expressions.
//
// The IR types live in package flow; this package only reads and normalizes
// them, so flow stays free of runtime and I/O (ADR-0003, ADR-0011).
package ir
