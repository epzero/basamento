# Architecture decision records

An ADR is amended only by a later ADR or a dated status note, never silently.
Changing `layering.yaml` or the constitution, or breaking a contract, needs an
ADR. An ADR is proposed as a design pull request, written as it will read once
merged; the owner's approval lets it merge, and merging is what accepts it
(`../engineering/agent-workflow.md` §7.4). An arbiter's proposal for another
direction is the exception: its ADR is written as **Proposed** (§5).

| ADR | Decision | Status |
| --- | --- | --- |
| [0001](0001-pure-go-single-binary.md) | Pure Go, one static binary, two profiles | Accepted |
| [0002](0002-runtime-neutral-durable-execution.md) | Runtime-neutral durable execution on Temporal | Accepted |
| [0003](0003-flows-as-data-with-cel.md) | Flows as data, a pure interpreter, CEL | Accepted |
| [0004](0004-ports-with-embedded-and-cloud-implementations.md) | Ports with embedded and cloud implementations | Accepted |
| [0005](0005-two-layer-streaming.md) | Two-layer streaming: SQL event log plus NATS JetStream | Accepted |
| [0006](0006-capability-typed-model-layer.md) | Capability-typed model layer with one invocation contract | Accepted |
| [0007](0007-conversation-coordinator.md) | Conversations: a coordinator run with one child run per turn | Accepted |
| [0008](0008-auth-modes-and-in-app-authorization.md) | Auth modes none/local/oidc; in-app authorization | Accepted |
| [0009](0009-browser-runner-service.md) | Browser use and recording as a separate service | Accepted |
| [0010](0010-bdd-specs-agent-review-and-gate.md) | BDD specs, agent-first review with tiers, deterministic gate | Accepted |
| [0011](0011-flow-host-and-node-handlers.md) | The flow host and node handlers are layers of their own | Accepted |
| [0012](0012-workers.md) | Workers declare what they serve and pull invocations; the queue's order, capacity, lost workers, workers on demand | Accepted |
| [0013](0013-blob-upload-urls.md) | Blob uploads: a staged upload URL scoped to the tenant, then a commit that names the content key | Accepted |
| [0015](0015-how-the-pr-gate-runs-postgres.md) | How the PR gate gets Postgres: `BDD_POSTGRES_URL`, else embedded-postgres v2 on major 16; strict CI fails instead of skipping | Accepted |
