# L3 — Models

One invocation contract for every model (ADR-0006, `specs/models`).

### WP-L3-01 — Invocation contract and chat adapters
Depends: WP-L1-06, WP-L2-02.
Owns: `model`, `model/chat`, `contracts/basamento/invoke`.
The `Invoker` contract and `Invoke`; chat and embedding adapters over `eino/schema` (`eino-ext`, official SDKs, OpenAI-compatible mode for MiniMax and GLM); streaming normalized into events. Specs: `specs/models/contract.feature`.

### WP-L3-02 — Structured output and middleware
Depends: WP-L3-01.
Owns: `model/structured`, `model/middleware`.
One attempt budget across native schema → JSON mode → forced tool → local repair; retry with jitter and Retry-After; first-chunk and idle timeouts; metering; call log.

### WP-L3-03 — Model node
Depends: WP-L3-01, WP-L3-06, WP-L1-01, WP-L1-02, WP-L5-01.
Owns: `model/node`.
The `model` node handler (ADR-0011): validate, reserve, start with a stable client ref, follow events, wait (poll, webhook, socket), ingest with provenance, settle; ambiguous-start handling. Specs: `specs/models/starts.feature`, `specs/models/cost.feature`.

### WP-L3-04 — Providers and model routing
Depends: WP-L3-03.
Owns: `model/providers`.
Generic HTTP API and CLI providers driven by manifests, then one task per vendor manifest (or adapter where a manifest is not enough), each with a simulator and contract tests; routing by capability and locality. Specs: `specs/models/outputs-and-routing.feature`.

### WP-L3-05 — Prompt templates
Depends: WP-L0-01.
Owns: `prompt`.
Versioned templates, segments with budgets and cache hints, untrusted-input wrapping, prompt lint.

### WP-L3-06 — Model catalog and price table
Depends: WP-L3-01, WP-L1-01.
Owns: `model/catalog`.
Descriptors by model id; the price table with its shipped snapshot, deployment and tenant overrides; the default token formula; estimates for reservations (`model-layer.md` §7).

### WP-L3-07 — Workers
Depends: WP-L3-04, WP-L1-03, WP-L8-01, WP-L1-08.
Owns: `model/worker`, `contracts/basamento/worker`.
`kit worker` and the worker contract (ADR-0012 §1 to §11): joining with a pool token, descriptors kept by the pool, the queue in the SQL store with classes, fairness and ageing, leases by free capacity, draining and pausing, the worker's journal, restart-safe redelivery and `worker_lost`, spill-over as a tenant setting, queue metrics and worker administration; routing by labels. Serves models and tools. Specs: `specs/models/workers.feature`.

### WP-L3-08 — Workers on demand
Depends: WP-L3-07.
Owns: `model/worker/scale`, `platform/launcher`.
Worker templates, the `Launcher` port with its conformance suite and the `http` and `process` launchers, single-use join tokens, the scaler, `kit worker describe` and the worker's exit flags (ADR-0012 §12). Native launchers for Cloud Run Jobs, Kubernetes Jobs and AWS are later work packages. Specs: `specs/models/workers-on-demand.feature`.
