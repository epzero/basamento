// Package node implements the `model` node handler (ADR-0011).
//
// The handler turns one model node into flow code: it validates the node's
// parameters against the model's schema, prices the invocation with
// model/catalog, reserves the estimate against the run's budget, starts the
// invocation with a client ref that is stable across every attempt, follows
// its events, waits for its result, ingests output files with provenance and
// settles the reservation. An ambiguous start is looked up before anything is
// sent again; when the provider cannot answer, the run waits at gate
// "ambiguous_start" (REQ-MODEL-004, REQ-MODEL-005, REQ-MODEL-006,
// REQ-MODEL-007, REQ-MODEL-008, REQ-MODEL-009, REQ-MODEL-010).
//
// The package is the only part of model that imports package runtime: the
// handler's side effects are durable steps, while pricing, validation and the
// client ref stay deterministic flow code (ADR-0011, docs/architecture/
// model-layer.md §4).
//
// A Handler is bound to one composition: the invoker, the catalog, the blob
// store and the run's budget. It is safe for concurrent runs. The budget is
// read from the run context (WithBudget) so a run never spends another run's
// account. Waiting (poll, webhook, socket, command) is the adapter's business:
// the node only follows events and waits for the result through
// model.Invoker.
package node
