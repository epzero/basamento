# Runtime: one contract, several engines

Status: **In progress** (local runtime WP-L5-01 implemented; Temporal, embedded server and conformance WP-L5-02 … WP-L5-04 planned). Design: ADR-0002, `docs/architecture/runtime.md`.

### REQ-RT-001 — Conformance
Each runtime shall produce the same final state and the same durable events for every conformance flow.

### REQ-RT-002 — Crash resume
When a durable runtime restarts during a run, the runtime shall resume the run from its last completed step without executing completed steps again.

### REQ-RT-003 — Embedded server
Where the profile is `embedded`, the system shall run the Temporal server in-process on a file-backed SQLite database, without any external service.

### REQ-RT-004 — Replay
When a recorded run history is replayed against the current flow code, the runtime shall reproduce the recorded commands or report a determinism violation naming the first divergent event.

### REQ-RT-042 — The request tenant is the context tenant
If a start request, a run reference or a list query carries a non-empty tenant that differs from the calling context's tenant, then the runtime shall reject the call with code `invalid_argument`, starting no run, changing no run and returning no run.

### REQ-RT-043 — Returned run references carry the run's tenant
When the runtime returns a run reference from a start, a run description or a run listing, the runtime shall return the run's own tenant in it.

## Local runtime

WP-L5-01, `runtime/local`. The local runtime runs flows in-process with an
in-memory journal. It is not durable by design and says so (REQ-RT-006); every
requirement below binds the `local` runtime.

### REQ-RT-044 — Tenant required on every call on the local runtime
If a start, describe, cancel, gate resolution or list call reaches the local runtime without a tenant in its context, then the local runtime shall reject the call with code `invalid_argument`, starting no run, changing no run and returning no run.

### REQ-RT-005 — Runtime capabilities
The local runtime shall report capabilities `durable`, `continue_as_new`, and `polyglot_queues` as false and `sync_gate_updates` as true.

### REQ-RT-006 — Nothing survives a restart
When a local runtime starts, the local runtime shall have no record of any run from an earlier runtime instance.

### REQ-RT-007 — One execution per step name in a run
When a run on the local runtime calls a step with a name that already completed in that run, the local runtime shall return the recorded result without executing the step again.

### REQ-RT-008 — Timers move only with the runtime's clock
While the local runtime runs, the local runtime shall complete a timer of duration `d` only after the runtime's clock has advanced by at least `d`.

### REQ-RT-009 — Awaited gates
When a gate is resolved on a run waiting at that gate, the local runtime shall complete the run's gate wait with the resolved payload.

### REQ-RT-010 — A resolved gate reaches the run once
If a gate is resolved a second time on a run that already received the first resolution of that gate, then the local runtime shall reject the second resolution with code `conflict`.

### REQ-RT-011 — Waiting on the first of several operations
When a run waits on the first of several pending operations, the local runtime shall return the call-order index of the operation that became ready first; operations already ready when the wait begins, and operations that become ready in the same clock reading, are tied and the lowest such index wins.

### REQ-RT-012 — Cancellation
When a run is cancelled, the local runtime shall move the run to `cancelled` and deliver cancellation to its in-flight steps and timers.

### REQ-RT-013 — Waits end at cancellation
If a run is cancelled while a gate wait or a timer is pending, then the local runtime shall complete the pending wait with code `cancelled`.

### REQ-RT-014 — Child runs
When a parent run starts a child flow with a child run id, the local runtime shall execute the child flow as a run under that child run id in the parent's tenant and return the child run's output to the parent when the child run completes.

### REQ-RT-015 — Child close policy
When a parent run ends, the local runtime shall apply each in-flight child run's close policy: `terminate` moves the child run to `terminated`, `request_cancel` moves it to `cancelled`, and `abandon` leaves it `running`.

### REQ-RT-016 — Non-retryable step failures
When a step attempt fails with an error that is not marked retryable or whose code is listed as non-retryable for the step, the local runtime shall stop retrying and fail the step with that error, and shall fail the run if the error is unhandled by the flow.

### REQ-RT-017 — Step attempt limit
If every attempt of a step fails with a retryable error whose code is not listed as non-retryable, then the local runtime shall retry immediately without backoff up to the step's attempt limit, and when the last attempt also fails, the local runtime shall fail the step with the last attempt's error, and shall fail the run if the error is unhandled by the flow.

### REQ-RT-018 — Default attempt count
Where a step leaves its attempt limit at 0, the local runtime shall allow at most 3 attempts of that step.

### REQ-RT-019 — Independent steps overlap
While a run has several independent steps in flight, the local runtime shall execute their step functions concurrently.

### REQ-RT-021 — Run and step isolation
While multiple runs execute on the same local runtime in the same tenant or in different tenants, the local runtime shall keep each run's state and recorded step results separate from every other run.

### REQ-RT-022 — Shutdown leaves in-flight runs running
When the local runtime shuts down while runs are in flight, the local runtime shall leave those in-flight runs in state `running`.

### REQ-RT-023 — Shutdown stops in-flight steps before returning
When the local runtime shuts down while steps are in flight, the local runtime shall deliver cancellation to those steps and wait for them to stop before shutdown returns.

### REQ-RT-024 — No new runs after shutdown
If a caller starts a run after the local runtime has shut down, then the local runtime shall reject the start with code `unavailable`.

### REQ-RT-025 — Tenant isolation on run lookup, cancellation and gate resolution
If a tenant describes, cancels or resolves a gate of a run belonging to another tenant on the local runtime, then the local runtime shall fail the request with code `not_found`.

### REQ-RT-026 — Run listing is per tenant
When a tenant lists runs of a flow on the local runtime, the local runtime shall return only the runs that belong to that tenant.

### REQ-RT-027 — A successful retry ends the step
When an attempt of a step succeeds before its attempt limit, the local runtime shall stop retrying and return that attempt's result to the flow as the step's result.

### REQ-RT-046 — A run id is unique within a tenant
If a run is started on the local runtime with a run id that already names a run of the same tenant, then the local runtime shall reject the start with code `conflict`, starting no run and changing no run.

### REQ-RT-047 — A start idempotency key deduplicates within a tenant
When a run is started on the local runtime with a non-empty start idempotency key already used by a run of the same tenant, the local runtime shall return the reference of that existing run instead of starting a new one. An empty start idempotency key does not deduplicate: each start with an empty key creates a new run.

### REQ-RT-048 — Start idempotency keys are scoped to a tenant
The local runtime shall scope start idempotency key deduplication to a tenant, so that the same key in two tenants starts two runs.

### REQ-RT-049 — A child run can be re-attached by id
When a parent run on the local runtime starts a child run with a child run id that already names a child run started by that parent for that flow, the local runtime shall return that existing child run instead of starting a second child run.

### REQ-RT-050 — A child run id cannot name a different run of the tenant
If a parent run on the local runtime starts a child run with a child run id that already names a run of the same tenant that is not a child started by that parent for that flow, then the local runtime shall reject the child start with code `conflict`, starting no child run and changing no run.

### REQ-RT-051 — A paging cursor resumes a listing
When a List query on the local runtime has more matching runs than its limit, the local runtime shall return that page of matching runs in start order and a paging cursor that resumes the listing at the next run without skipping or repeating a run.

### REQ-RT-052 — An unusable paging cursor fails
If a List query on the local runtime carries a paging cursor that the local runtime did not issue or that no longer names a position in the listing, then the local runtime shall reject the query with code `invalid_argument`, returning no run.

### REQ-RT-053 — A paging cursor stays within its tenant
If a List query on the local runtime carries a paging cursor issued to a tenant other than the query's tenant, then the local runtime shall reject the query with code `invalid_argument`, returning no run.

### REQ-RT-054 — A local runtime needs a timer clock
If the local runtime is constructed with a clock that is not a timer clock, then construction shall fail with an error, because a run's timers could not move.

## Temporal runtime

WP-L5-02, `runtime/temporal`. The Temporal runtime runs the flow host as a
Temporal workflow, each step as an activity, with a payload codec for blob
claim-check and encryption, and `Checkpoint` as ContinueAsNew
(`docs/architecture/runtime.md`). It is the default runtime of both profiles.
Every requirement below binds the `temporal` runtime. The embedded server is
WP-L5-03 and the conformance suite WP-L5-04.

### REQ-RT-045 — Tenant required on describe, cancel, gate resolution and list on the temporal runtime
If a describe, cancel, gate resolution or list call reaches the temporal runtime without a tenant in its context, then the temporal runtime shall reject the call with code `invalid_argument`, changing no run and returning no run.

### REQ-RT-039 — Temporal runtime capabilities
The temporal runtime shall report capabilities `durable`, `sync_gate_updates`, `continue_as_new` and `polyglot_queues` as true.

### REQ-RT-040 — Tenant required on start
If a run is started on the temporal runtime without a tenant, then the temporal runtime shall reject the start with code `invalid_argument` before executing any step or recording any run history.

### REQ-RT-041 — Steps keep their retry settings
When a step with `MaxAttempts` greater than 0 fails with a retryable error whose code is not listed as non-retryable, the temporal runtime shall retry the step until an attempt succeeds or `MaxAttempts` attempts have failed; if all `MaxAttempts` attempts fail, the temporal runtime shall fail the step with the last attempt's error and, when the error is unhandled by the flow, fail the run.

### REQ-RT-028 — Claim-check threshold
Where a claim-check threshold is configured, the temporal runtime shall store a run history payload larger than the threshold as a blob.

### REQ-RT-029 — Claim-checked payloads round-trip unchanged
Where a run history payload is stored as a blob, the temporal runtime shall read it back as the payload that was written, content and encoding.

### REQ-RT-031 — History holds only encrypted payloads
The temporal runtime shall encode every run history payload with the payload codec for the run's tenant.

### REQ-RT-032 — Readable payloads are refused
If a step or replay reads a run history payload without the encoding `binary/encrypted`, then the temporal runtime shall reject it with code `invalid_argument` and fail the step or replay.

### REQ-RT-033 — Claim-checked payloads stay bound to their tenant
If a step reads a claim-checked run history payload whose blob is not held by the run's tenant, then the temporal runtime shall fail the step with code `not_found` and, when unhandled by the flow, fail the run.

### REQ-RT-034 — Checkpoint threshold
Where a checkpoint event threshold is configured, the temporal runtime shall continue a run as new when a checkpoint is requested and the current run history's length in events, as the Temporal SDK reports it, is at least the threshold.

### REQ-RT-035 — Checkpoint before the threshold
If a checkpoint is requested and the current run history's length in events, as the Temporal SDK reports it, is fewer than the checkpoint event threshold, then the temporal runtime shall leave the run in its current history.

### REQ-RT-036 — ContinueAsNew keeps the run
When the temporal runtime continues a run as new, the temporal runtime shall keep the run's id, recorded results, emitted events and awaited gates.

### REQ-RT-037 — Pinned server versions
If the temporal runtime starts against a server whose version is outside the pinned range, then the temporal runtime shall refuse to start with code `unsupported`.

### REQ-RT-038 — Checkpoint waits for in-flight steps
When a checkpoint is requested while a step of the run is in flight, the temporal runtime shall wait until no step of the run is in flight before continuing the run as new, and shall not execute a step of the run again because of the continue-as-new.
