# Runtimes

Status: contract **Implemented** (`runtime/runtime.go`); the flow host
**Implemented** (`flow/host`, WP-L4-04); `local` **Implemented**
(`runtime/local`, WP-L5-01); `temporal` **In progress** — data path
(`runtime/temporal`, WP-L5-02 pull request (a); control plane and Await still
planned in the same WP).
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

## 6. Temporal runtime: data path (WP-L5-02, pull request (a))

`runtime/temporal` registers the flow host as a Temporal workflow and each step
as an activity. This change delivers:

- A **payload data converter** shared by workflow and activity workers: tenant
  encryption (`platform/secrets`), blob claim-check over the threshold (default
  5 KB on encrypted size), markers use encoding `basamento/claim-check` and only
  history ever holds `binary/encrypted`.
- A **host workflow** with `DurableContext` for `Context`, `Now`, `NewID`,
  `Rand`, `Step`, `Sleep`, `Select`, and `Capabilities`; `Child`, `Await`,
  `Emit` and `Checkpoint` return `unsupported` until the next changes of
  WP-L5-02.
- **Step activity** retries: `MaxAttempts` 0 → 3, listed `NonRetryable` codes and
  non-retryable `core.Error` values stop after one attempt; Temporal backoff is
  1 s initial, ×2, cap 100 s (the local runtime retries immediately). Codes and
  the `Retryable` flag cross the activity boundary on `Future.Get`.
- **Registration** (`RegisterFlowHost`, `RegisterStep`, `Register`,
  `DataConverter`) without control-plane `runtime.Runtime` (pull request (c)).
- **`Capabilities()`** on `DurableContext` reports only `durable` and
  `polyglot_queues` until pull requests (b) and (c) add gates and ContinueAsNew.

**Plain text in history:** Temporal stores failure messages and the tenant
header unencrypted; payload bytes go through the codec only. Step authors must
not put secrets in `core.Error` messages; non-`core.Error` failures become
`internal` with fixed messages and details go to the logger only.

The BDD harness **decorates** `DataConverter` (records payloads, models the
client tenant context, injects RT-032/033 faults on activity results). Production
code has no test hooks. Real-server version pinning is pull request (c) and
WP-L5-03.

## 7. Local runtime: behaviours the specs leave open

These are implementation choices for `runtime/local` (WP-L5-01). Where a row
names a requirement or scenario id, the gate exercises it; otherwise the
behaviour is spec-silent and covered only by unit tests or this document.

| Behaviour | Spec coverage |
| --- | --- |
| Timers due at one clock reading complete as one batch sharing one completion seq; `Select` snapshots run-owned futures under the run lock so a batch is visible all-or-nothing (same as `fireDue`), then breaks ties by lowest index within a batch and otherwise prefers the earliest completion seq | RT-008 / RT-011 (incl. RT-011-04) |
| Timer wakes before `Now()` reaches the deadline re-arm for the remaining duration; a timer never completes until `Now() >= deadline` | RT-008 (unit tests) |
| Duplicate `Start` with the same `RunID` in one tenant → `conflict` | RT-046-01 |
| Non-empty `StartRequest.IdempotencyKey` deduplicates within a tenant; empty key does not; keys do not cross tenants | RT-047 / RT-048 |
| Panic in the flow host → failed run, process survives; panic value logged once per recovery, bounded in size (not in errors returned to callers) | spec-silent (unit tests) |
| Panic in a registered step → `internal` step error, not retried, process survives; panic value and stack logged once per recovery, bounded | spec-silent (unit tests) |
| `Cancel` truncates the caller's reason to 256 runes in the cancellation message | spec-silent (unit tests) |
| `Resolve` accepts any payload; `GateSpec.Schema` and `AcceptIf` validation (FLOW-006-01) arrives with WP-L5-04 | spec-silent here; FLOW-006-01 planned (WP-L5-04) |
| `Child` re-attach: same parent run and flow (name and hash) returns the existing child; otherwise `conflict` | RT-049 / RT-050 |
| `Child` re-attach to an already-linked child returns the first call's future; the first close policy wins, later policies are ignored | spec-silent (unit tests) |
| Parent close on a child applies that child's close policies to its own children (top-down); `abandon` on the middle link leaves descendants running | spec-silent (unit tests) |
| `Child` on a parent that is not `running` returns `cancelled` and starts no run; an already-linked child still returns its recorded future | spec-silent (unit tests) |
| `List` cursor paging: opaque version-1 token (`base64.RawURLEncoding` of JSON `{"v":1,"tenant":"<id>","pos":<n>}`), tenant-bound, `pos` is that tenant's start-order ordinal; clients must not parse it (BDD steps craft one token in this form for RT-052-01); invalid, out-of-range or foreign-tenant cursor → `invalid_argument` before any run is read | RT-051…053, RT-026 lists runs |
| Every started run stays in memory (no retention) | RT-006 / design §Local |
| `New` requires a timer clock (`After`); failure message names timer scheduling | RT-054-01 |
| `Resolve` on a gate whose wait already ended (run cancelled, gate timeout, runtime shut down) → `conflict`, payload not delivered | spec-silent (unit tests) |
