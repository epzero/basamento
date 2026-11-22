# ADR-0002 — Runtime-neutral durable execution on Temporal

**Status:** accepted · **Date:** 2026-11-01 · **Amended 2026-11-22:** ADR-0021
records how the root module links the nested server and amends the consequence
about library users and the module graph.

## Context
Flows run for seconds to days, call paid vendors and wait for people. The
embedded profile must start without external services; the cloud profile must
scale. Temporal's server is written in Go and can run in-process on a SQLite
file (`temporal.NewServer`, as in the `temporalio/cli` dev server), and the same
workflows run unchanged on Temporal Cloud or a self-hosted cluster.

## Decision
- Flow code depends only on `runtime.DurableContext`; control goes through
  `runtime.Runtime`. Flow, agent and model packages may not import a
  durable-execution vendor (`layering.yaml` `deny_external`).
- Runtimes:
  - `temporal`: an embedded server for the embedded profile, Temporal Cloud or a cluster for the cloud profile;
  - `local`: tests, the CLI and low-latency interactive runs; not durable.
- `Caps` declares what a runtime cannot do; no runtime fakes semantics.
- The embedded server (`runtime/temporal/embedded`) is its own Go module, so
  library users never inherit the server's dependencies. Its settings and
  measurements are in `runtime/temporal/embedded/BENCHMARKS.md`.

## Consequences
- A conformance suite (WP-L5-04) keeps the runtimes equivalent.
- Temporal features (Updates, ContinueAsNew, polyglot queues) are used through
  the contract.
- File-backed SQLite is outside Temporal's production envelope. It suits the
  single-user embedded profile, with backups and a tested upgrade path.
  Measured costs: about 50 ms per sequential step, a 116 MB stripped **bench**
  binary, a stripped linked `kit` with the server of **134 MB** (ADR-0021), and
  about 210 MB of memory for the server process.
- Linking the server into `kit` lists the nested module in the root `go.mod`;
  a consumer that imports only `runtime` grows from 185 to 282 modules in
  `go list -m -e all` (+97) without compiling the server tree (ADR-0021).
