// Package ir holds the codegen preset's flows as data (ADR-0003). They are
// compiled to the same canonical IR as any other flow (flow/ir), and their
// SHA-256 identity is what a run pins.
//
// Both run flows share the loop plan → implement each planned module → verify in
// a sandbox → repair under a guard → self-review → commit. They differ in the
// first stage and in the commit: codegen.bootstrap commits the first commit of a
// new repository on its default branch, and codegen.iterate commits on a new
// codegen/<run id> branch. codegen.version is the separate flow that proposes
// and reviews a new preset version; it is never composed into a run, and it
// states both review outcomes: approved reaches the accepted node, rejected
// reaches FAIL.
//
// The runner implements this loop; the IR is its declarative mirror, and
// Compile cross-checks the order, edges, guards and sequential max_parallel
// against it.
package ir
