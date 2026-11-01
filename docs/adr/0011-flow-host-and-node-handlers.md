# ADR-0011 — The flow host and node handlers are layers of their own

**Status:** accepted · **Date:** 2026-11-15

## Context
The flow machine is pure (ADR-0003) and flow code is written against
`runtime.DurableContext` (ADR-0002). Something has to join the two: load the
pinned IR, feed the machine events and turn its commands into `DurableContext`
calls. That host needs both package `flow` and package `runtime`, yet `flow`
may import only `core` and `expr`, and `runtime` only `core`. No layer could
hold it and no work package owned it.

The model node has the same problem. Reserving, starting with a stable client
ref, waiting and opening the `ambiguous_start` gate is flow code, but `model`
may not import `runtime`.

## Decision
- **`flow/host`** is a layer of its own that may import `core`, `expr`, `flow`
  and `runtime`. It provides the one `runtime.FlowHost`. Package `flow` (IR,
  compiler, machine, builder) keeps importing neither `runtime` nor any I/O,
  so its purity is enforced by `gate layering`, not by convention.
- **Node handlers.** The machine and the host handle `set`, `gate`, `wait`,
  `map`, `parallel` and `subflow` between them. Every other node kind is a
  handler:

  ```go
  type NodeHandler func(dc runtime.DurableContext, node flow.Node, input json.RawMessage) (json.RawMessage, error)
  ```

  A handler is deterministic flow code; its side effects are steps. Handlers
  are registered with the host by node kind at the composition root (`cmd/kit`,
  and the BDD harness). The host imports no handler, so adding a node kind
  never changes `flow/host`.
- **`model/node`** is a layer of its own that may import `core`, `platform`,
  `expr`, `model` and `runtime`. It holds the `model` node handler and its
  steps. The rest of `model` stays free of `runtime`, so an in-process caller
  (embeddings) never links a runtime.
- `agent` already may import `runtime` and provides the `agent` handler. A
  `tool` or `task` node is one step, registered with `Runtime.RegisterStep`.
- Neither new layer may import a durable-execution vendor (`deny_external`).

## Consequences
- `WP-L4-04` owns `flow/host`. Both runtimes depend on it, and the model node
  (`WP-L3-03`) depends on the local runtime, because its scenarios run runs.
- A work package whose owned directory has no layer adds the layer first, in a
  change of its own that cites or adds an ADR (`agent-workflow.md` §4).
