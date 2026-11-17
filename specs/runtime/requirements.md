# Runtime: one contract, several engines

Status: **Planned** (WP-L5-01 … WP-L5-04). Design: ADR-0002, `docs/architecture/runtime.md`.

### REQ-RT-001 — Conformance
Each runtime shall produce the same final state and the same durable events for every conformance flow.

### REQ-RT-002 — Crash resume
When a durable runtime restarts during a run, the runtime shall resume the run from its last completed step without executing completed steps again.

### REQ-RT-003 — Embedded server
Where the profile is `embedded`, the system shall run the Temporal server in-process on a file-backed SQLite database, without any external service.

### REQ-RT-004 — Replay
When a recorded run history is replayed against the current flow code, the runtime shall reproduce the recorded commands or report a determinism violation naming the first divergent event.

## Local runtime

WP-L5-01, `runtime/local`. The local runtime runs flows in-process with an
in-memory journal. It is not durable by design and says so (REQ-RT-006); every
requirement below binds the `local` runtime.

### REQ-RT-005 — Runtime capabilities
The local runtime shall report capabilities `durable`, `continue_as_new`, and `polyglot_queues` as false and `sync_gate_updates` as true.

### REQ-RT-006 — Nothing survives a restart
When a local runtime starts, the local runtime shall have no record of any run from an earlier runtime instance.

### REQ-RT-007 — One execution per step name in a run
When a run on the local runtime calls a step with a name that already completed in that run, the local runtime shall return the recorded result without executing the step again.

### REQ-RT-008 — Timers move only with the clock
While the local runtime runs on a fake clock, the local runtime shall complete a timer of duration `d` only after the clock has advanced by at least `d`.

### REQ-RT-009 — Awaited gates
When a gate is resolved on a run waiting at that gate, the local runtime shall complete the run's gate wait with the resolved payload.

### REQ-RT-010 — A resolved gate reaches the run once
If a gate is resolved a second time on a run that already received the first resolution of that gate, then the local runtime shall reject the second resolution with code `conflict`.

### REQ-RT-011 — Waiting on the first of several operations
When a run waits on the first of several pending operations and one or more become ready, the local runtime shall return the lowest index in call order among the ready operations.

### REQ-RT-012 — Cancellation
When a run is cancelled, the local runtime shall move the run to `cancelled` and deliver cancellation to its in-flight steps and timers.

### REQ-RT-013 — Waits end at cancellation
If a run is cancelled while a gate wait or a timer is pending, then the local runtime shall complete the pending wait with code `cancelled`.

### REQ-RT-014 — Child runs
When a parent run starts a child flow with a child run id, the local runtime shall execute the child flow as a run under that child run id in the parent's tenant and return the child run's output to the parent when the child run completes.

### REQ-RT-015 — Child close policy
When a parent run ends, the local runtime shall apply each in-flight child run's close policy: `terminate` moves the child run to `terminated`, `request_cancel` moves it to `cancelled`, and `abandon` leaves it `running`.

### REQ-RT-016 — Non-retryable step failures
When a step attempt fails with an error that is not marked retryable or whose code is listed as non-retryable for the step, the local runtime shall stop retrying, fail the step with that error, and fail the run.

### REQ-RT-017 — Step attempt limit
If every attempt of a step fails with a retryable error whose code is not listed as non-retryable, then the local runtime shall retry immediately without backoff up to the step's attempt limit, fail the step with the last attempt's error, and fail the run.

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

### REQ-RT-025 — Tenant isolation on run lookup and cancellation
If a tenant describes or cancels a run belonging to another tenant on the local runtime, then the local runtime shall fail the request with code `not_found`.

## Temporal runtime

WP-L5-02, `runtime/temporal`. The Temporal runtime runs the flow host as a
Temporal workflow, each step as an activity, with a payload codec for blob
claim-check and encryption, and `Checkpoint` as ContinueAsNew
(`docs/architecture/runtime.md`). It is the default runtime of both profiles.
Every requirement below binds the `temporal` runtime. The embedded server is
WP-L5-03 and the conformance suite WP-L5-04.

### REQ-RT-026 — Temporal runtime capabilities
The temporal runtime shall report capabilities `durable`, `sync_gate_updates`, `continue_as_new` and `polyglot_queues` as true.

### REQ-RT-027 — Steps keep their retry settings
When a step with `MaxAttempts` greater than 0 fails with a retryable error whose code is not listed as non-retryable, the temporal runtime shall retry the step until an attempt succeeds or `MaxAttempts` attempts have failed, and if all `MaxAttempts` attempts fail, fail the step with the last attempt's error and fail the run.

### REQ-RT-028 — Claim-check threshold
Where a claim-check threshold is configured, the temporal runtime shall store a run history payload larger than the threshold as a blob.

### REQ-RT-029 — Claim-checked payloads round-trip unchanged
Where a run history payload is stored as a blob, the temporal runtime shall read it back as the payload that was written, content and encoding.

### REQ-RT-031 — History holds only encrypted payloads
The temporal runtime shall encode every run history payload with the payload codec for the run's tenant.

### REQ-RT-032 — Readable payloads are refused
If a run history payload without the encoding `binary/encrypted` is loaded into a run, then the temporal runtime shall reject it with code `invalid_argument`.

### REQ-RT-033 — Claim-checked payloads stay bound to their tenant
If a run reads a run history payload whose blob is not held by the run's tenant, then the temporal runtime shall fail the run with code `not_found`.

### REQ-RT-034 — Checkpoint threshold
Where a checkpoint event threshold is configured, the temporal runtime shall continue a run as new once a checkpoint is requested and the run history holds at least that many events.

### REQ-RT-035 — Checkpoint before the threshold
If a checkpoint is requested while the run history holds fewer events than the checkpoint event threshold, then the temporal runtime shall leave the run in its current history.

### REQ-RT-036 — ContinueAsNew keeps the run
When the temporal runtime continues a run as new, the temporal runtime shall keep the run's id, recorded results, emitted events and awaited gates.

### REQ-RT-037 — Pinned server versions
If the temporal runtime starts against a server whose version is outside the pinned range, then the temporal runtime shall refuse to start with code `unsupported`.
