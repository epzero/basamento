// Package codegen holds the codegen preset's deterministic test doubles: a
// scripted model, a simulated sandbox and a simulated git host. They live in
// internal/bdd, never in production code (AGENTS.md, constitution 11), and they
// are the only state the codegen scenarios read.
package codegen
