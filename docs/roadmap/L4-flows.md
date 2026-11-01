# L4 — Flow engine

Flows as data, a pure interpreter, CEL expressions (ADR-0003, `specs/flow-engine`).

### WP-L4-01 — IR and compiler
Depends: WP-L2-01, WP-L2-02.
Owns: `flow/ir`, `flow/compile`.
Canonical IR, content hash, validation, cycle guards, CEL type-checking. Specs: `specs/flow-engine/compile.feature`.

### WP-L4-02 — Flow machine
Depends: WP-L4-01.
Owns: `flow/machine`.
Pure interpreter, supersteps, reducers, canonical merge order; property tests over completion orders. Specs: `specs/flow-engine/routing.feature`.

### WP-L4-03 — Go builder API
Depends: WP-L4-01.
Owns: `flow/builder`.
Typed builder producing IR.

### WP-L4-04 — Flow host
Depends: WP-L4-02.
Owns: `flow/host`.
The generic `runtime.FlowHost`: loads the pinned IR, drives the machine, turns its commands into `DurableContext` calls and dispatches effectful node kinds to registered node handlers (ADR-0011).
