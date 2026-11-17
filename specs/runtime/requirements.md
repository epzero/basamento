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
requirement below binds the `local` runtime and none of the `temporal` one.

### REQ-RT-005 — Observably non-durable
The local runtime shall report `Durable` as false in `Caps`.

### REQ-RT-006 — Nothing survives a restart
When a local runtime starts, the runtime shall have no record of any run of an earlier process.

### REQ-RT-007 — One execution per step in a run
When a run of the `local` runtime calls a step with the name `N`, the local runtime shall execute the step function registered as `N` exactly once for that run and return its recorded result on every later call with that name.

### REQ-RT-008 — Timers move only with the clock
While the `local` runtime runs on a fake clock, the local runtime shall complete a `Sleep(d)` only after the clock has advanced by `d`.

### REQ-RT-009 — Awaited gates
When a gate is resolved on a run waiting at it, the local runtime shall complete that run's `Await` future for the gate with the resolved payload.

### REQ-RT-010 — A resolved gate reaches the run once
If a gate is resolved a second time on a run that already received the first resolution, then the local runtime shall reject the second resolution with code `conflict`.

### REQ-RT-011 — Waiting on the first of several futures
When one of the futures passed to `Select` becomes ready, the local runtime shall return the index of the first ready future in the order of the call.

### REQ-RT-012 — Cancellation
When a run is cancelled, the local runtime shall move it to `cancelled` and pass cancellation to the steps and timers it has in flight.

### REQ-RT-013 — Waits end at cancellation
If a run is cancelled while a gate `Await` or a `Sleep` is pending, then the pending call shall complete with code `cancelled`.

### REQ-RT-014 — Child runs
When the local runtime runs a child flow for a `Child` call, the child run shall run under the run id in the call's `ChildOptions.ID` and be `completed` when the flow returns.

### REQ-RT-015 — Child close policy
When a parent run ends and the parent's `ParentClose` policy is `terminate`, the local runtime shall move the parent's child runs still in flight to `terminated`.

### REQ-RT-016 — Retryable failures
When a step attempt fails with an error that is not marked retryable and is not listed in `NonRetryable`, the local runtime shall make the step future fail with that error and the run fail with code `internal`.

### REQ-RT-017 — Attempt limit
If every attempt of a step fails with a retryable error, then the local runtime shall make the run fail with code `unavailable` after exactly `MaxAttempts` attempts.

### REQ-RT-018 — Default attempt count
Where a step's options leave `MaxAttempts` at 0, the local runtime shall run at most 3 attempts of that step.

### REQ-RT-019 — Independent steps overlap
While a run has several independent steps in flight, the local runtime shall run their step functions concurrently, one execution per step name.

### REQ-RT-020 — Results in call order
While a run has several independent steps in flight, the local runtime shall hand their results to `Select` in the order of the `Select` call, whatever order the steps finish in.

### REQ-RT-021 — Isolation
While runs in different tenants or in the same tenant run at once on one local runtime, the local runtime shall keep each run's run id and step results separate from every other run.

### REQ-RT-022 — Shutdown with runs in flight
If the `Serve` context is cancelled while runs are in flight, then the local runtime shall leave those runs `running`, cancel their in-flight steps, cease accepting new runs, and have `Serve` return only after its workers have stopped.
