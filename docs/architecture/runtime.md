# Runtimes

Status: contract **Implemented** (`runtime/runtime.go`); the flow host
**Implemented** (`flow/host`, WP-L4-04); `local` **Implemented**
(`runtime/local`, WP-L5-01); `temporal` **Planned** (WP-L5-02).
Decisions: ADR-0002, ADR-0011.

## 1. Two ports

- **`DurableContext`**: the only API flow code may use: `Now`, `NewID`,
  `Rand`, `Step`, `Child`, `Sleep`, `Await`, `Select`, `Emit`, `Checkpoint`,
  `Capabilities`. Every call is replay-safe.
- **`Runtime`**: the control plane: register the flow host and steps,
  `Start`, `Resolve` (gates), `Cancel`, `Describe`, `List`, `Serve`.

The tenant of a `Runtime` call comes from its context (`core.TenantFrom`),
never from the request, as with `AuditEvent.Tenant` in `platform/platform.go`.
`StartRequest`, `RunRef` and `RunQuery` carry a `core.TenantID` that must be
empty or equal to the context tenant; a Runtime rejects a different one, and a
call whose context carries no tenant, with code `invalid_argument`. The field
never overrides the context. A Runtime always fills the tenant on the run refs
it returns (`Start`, `RunStatus.Ref`). `specs/runtime/design.md` §The request
tenant follows the context has the rest of the isolation rules.

There is exactly one flow entrypoint, a `FlowHost(dc, flowRef, input)`,
provided by `flow/host` and registered with each runtime at the composition
root (ADR-0011). It loads the IR by hash, runs `flow.Machine`, turns commands
into `DurableContext` calls and feeds results back as events.

## 2. Implementations

| Runtime | Use | Notes |
| --- | --- | --- |
| `local` | tests, CLI, short interactive runs | Goroutines and an in-memory journal. Not durable by design; finished runs and idempotency keys are never evicted—the process is expected to be short-lived. |
| `temporal` | default for both profiles | `Step` = activity, `Await` = Update (validated synchronously), `Checkpoint` = ContinueAsNew, polyglot task queues. The embedded server follows `temporalio/cli` `internal/devserver`: `temporal.NewServer`, SQLite via `modernc.org/sqlite`, one history shard. It lives in its own Go module so library users do not inherit the server's dependencies. |

`Caps` (durable, sync gate updates, ContinueAsNew, polyglot queues) lets callers
adapt instead of faking semantics.

## 3. Rules for flow code

- No wall clock, no global randomness, no Go map iteration order. Use `dc.Now`,
  `dc.Rand`, and sorted keys.
- All I/O happens in steps. Steps are idempotent per `IdempotencyKey`.
- Long or streaming steps send heartbeats; Temporal delivers cancellation through them.
- Large payloads (over a few KB) go to blob storage by reference. On Temporal this
  is a payload codec, which also encrypts.
- Histories are bounded with `Checkpoint` at a fixed event threshold.

## 4. Conformance

`runtime/conformance` (WP-L5-04) holds golden flows (linear, parallel merge,
gate with timeout, cancel mid-step, continue-as-new) and their expected final
states and durable events. Every runtime must reproduce them
(`specs/runtime/conformance.feature`). Temporal histories recorded by the suite
are replayed on every change to catch nondeterminism.

## 5. Embedded server: measured

Measured with `runtime/temporal/embedded/bench` (details in `runtime/temporal/embedded/BENCHMARKS.md`):

- a 116 MB static binary;
- a 0.3–0.55 s cold start and a sub-100 ms start on an existing database;
- about 210 MB of memory;
- about 50 ms per sequential step (the same as the official dev server);
- correct recovery after `kill -9` in 10 of 10 runs;
- deterministic replay.

Settings for the embedded profile:

| Setting | Value | Why |
| --- | --- | --- |
| Task-queue partitions | 1 | One worker process; four partitions only add forwarding latency (fan-out 2–3× faster). |
| SQLite | `journal_mode=wal`, `synchronous=normal`, `busy_timeout=10000`, 4 connections | Double the fan-out throughput; survives process crashes. |
| Workflow-task timeout | 2–3 s | Bounds recovery when a task was handed to a dead process (default 10 s). |
| Module pins | server, API and SDK versions from the matching `temporalio/cli` release | Newer API modules do not compile against a given server release. |
| Go | ≥ 1.26.11 | Required by server v1.32. |

~50 ms per sequential step adds about 1 s to a 20-step agent loop. Interactive
work that needs lower latency runs on the `local` runtime.

## 6. Local runtime: behaviours the specs leave open

These are implementation choices for `runtime/local` (WP-L5-01). Where a row
names a requirement or scenario id, the gate exercises it; otherwise the
behaviour is spec-silent and covered only by unit tests or this document.

| Behaviour | Spec coverage |
| --- | --- |
| Timers due at one clock reading complete as one batch sharing one completion seq; `Select` breaks ties by lowest index | RT-008 / RT-011 scenarios (timer batching via flow stubs) |
| Duplicate `Start` with the same `RunID` in one tenant → `conflict` | spec-silent (unit tests) |
| `IdempotencyKey` deduplication scoped per tenant | RT-044 (tenant isolation); per-tenant key scope spec-silent |
| Panic in the flow host → failed run, process survives | spec-silent (unit tests) |
| Panic in a registered step → `internal` step error, not retried, process survives | spec-silent (unit tests) |
| `Child` re-attach: same parent run and flow returns the existing child; otherwise `conflict` | partial (child scenarios); re-attach rule spec-silent |
| `List` cursor paging: opaque decimal index into start order; invalid or out-of-range cursor → `invalid_argument`; cursor does not widen tenant visibility | RT-026 lists runs; paging semantics spec-silent |
| Every started run stays in memory (no retention) | RT-006 / design §Local |
| `New` requires a clock that implements `After` | spec-silent (unit test) |
