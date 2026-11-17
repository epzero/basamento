# L5 — Runtimes

One durable contract, two runtimes (ADR-0002, `specs/runtime`).

### WP-L5-01 — Local runtime
Depends: WP-L4-04.
Owns: `runtime/local`.
In-process `Runtime` and `DurableContext`; not durable by design; passes the conformance suite.

### WP-L5-02 — Temporal runtime
Depends: WP-L4-04, WP-L1-02, WP-L1-04.
Owns: `runtime/temporal`.
The workflow that runs the flow host, step activities, payload codec (blob claim-check and encryption), ContinueAsNew; server, API and SDK versions pinned from the matching `temporalio/cli` release.

### WP-L5-03 — Embedded Temporal server
Depends: WP-L5-02, WP-L1-05.
Owns: `runtime/temporal/embedded` (its own Go module).
In-process server on file-backed SQLite, started by `kit serve` and `kit run`, with the settings measured in `runtime/temporal/embedded/BENCHMARKS.md`. Specs: `specs/runtime/embedded.feature`.

### WP-L5-04 — Runtime conformance suite
Depends: WP-L5-01, WP-L5-02, WP-L5-03.
Owns: `runtime/conformance`.
Golden flows and records; crash resume; replay. Specs: `specs/runtime/conformance.feature`, `specs/flow-engine/runs.feature`.
