# Runtimes

Status: contract **Implemented** (`runtime/runtime.go`); the flow host
**Implemented** (`flow/host`, WP-L4-04); `runtime/local` **Implemented**
(WP-L5-01); `runtime/temporal` **Implemented** (WP-L5-02); the embedded
server **Implemented** (WP-L5-03). Decisions: ADR-0002, ADR-0011.

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
| `local` | tests, CLI, short interactive runs | Goroutines and an in-memory journal. Not durable by design. |
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
gate with timeout, and continue-as-new, which checkpoints forty steps) and
their expected final states and durable events. Every runtime must reproduce
them (`specs/runtime/conformance.feature`). The records live in
`runtime/conformance/testdata/<flow>.golden.json`; they change only with
`-update` and a reason in the commit body.

The suite also owns crash resume and replay. `Runtime.Kill` and
`Runtime.Resume` on `runtime/temporal` stop a run as a killed process would
(in-flight work cancelled, completed steps kept) and re-execute it from its
journal, so `RT-002-01` can assert that steps 1 and 2 ran once. A recorded run
replays through `Runtime.Replay`; a changed flow reports `determinism_violation`
naming the first divergent durable call (`RT-004-01`). The gate runs both with
the fake clock and no wall-clock waits.

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
| Go | ≥ 1.26.10 | Required by server v1.32. |

~50 ms per sequential step adds about 1 s to a 20-step agent loop. Interactive
work that needs lower latency runs on the `local` runtime.

### How `kit serve` starts it

On the embedded profile, `kit serve` starts the server after the ports open
and before it binds its listener: the database is `$(data.dir)/temporal.db`,
its frontend takes a free loopback port, and `Start` returns only once a
health check answers. The `ready` line names it (`temporal=embedded`,
`temporal.addr=127.0.0.1:<port>`); shutdown stops it after the ports close.
A server that cannot start closes the ports in reverse order and exits 1. The
cloud profile starts no server: it talks to the Temporal service the operator
runs. `RT-003-01` starts `kit serve` on an empty directory, reads
`temporal.addr` from the ready line, and runs the `linear` conformance flow on
that server through an SDK worker, so the run proves the started server, not
the in-memory simulation.

The module is linked into the binary through a local compatibility build of
`go.opentelemetry.io/otel/log` in `third_party/otel-log`. Server v1.32.2 still
uses the pre-v1 `log.KeyValue` / `log.String` helpers, which
`go.opentelemetry.io/otel/log` v1.47.0 (selected by `gocloud.dev` v0.49.0)
removed; a module graph has one version per module, so the server does not
compile against v1.47.0. The local build is upstream v1.47.0 plus those
helpers; its `README.md` says when to remove it.
