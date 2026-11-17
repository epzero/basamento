# Runtime: design notes

## Local runtime (WP-L5-01)

Decisions for `runtime/local` that leave `REQ-RT-005` … `REQ-RT-019` and
`REQ-RT-021` … `REQ-RT-025` in `local.feature` to the implementer. This work
package changes no contract file.

### Ports the composition root supplies

`runtime/local` is production code, so it cannot import `core/coretest`. The
test helpers live in `internal/bdd` and `_test.go`, which pass the doubles
through the package's constructor in `runtime/local`:

- a `core.Clock` for `Now`, timers and `StartedAt`/`ClosedAt`: the
  `coretest.FakeClock` in the gate, `core.SystemClock` in the binary;
- an id source for run ids: a sequential source in the gate, `core.ULIDs` in
  the binary. `core.Ids` prefixes ids (`run_<26 chars>`); the gate's `"r1"` is
  the `StartRequest.RunID` the scenario passes.

The package never calls `time.Now` or `time.Sleep`; it waits on the injected
clock.

### One goroutine per run

Each run executes its `FlowHost` on a goroutine started by `Start`, with a
journal in memory: step results by name, gates received with their payloads,
child runs, and state. `DurableContext` calls hand back a `Future`; the run
goroutine goes on and reads futures through `Select` or `Get`.

- **Steps** (`REQ-RT-007`): the first `Step(N, …)` records a pending entry
  for `N` and runs the registered function on its own goroutine. Once `N`
  completes, every later `Step(N, …)` in the same run returns that recorded
  entry without running the step function again. Calling step functions on
  their own goroutines is what lets independent steps overlap (`REQ-RT-019`).
- **Timers** (`REQ-RT-008`): `Sleep(d)` registers a wake-up at `Now()+d` on the
  clock; it completes only when the clock reports that time.
- **Gates** (`REQ-RT-009`, `REQ-RT-010`): `Await` registers the run at the gate
  name. `Resolve` hands the payload to the run's journal and completes its
  future. `Resolve` for a gate the journal already holds returns a
  `*core.Error` with code `conflict` (the contract's code, not a new one); the
  run does not see it.
- **Select** (`REQ-RT-011`): returns the lowest index whose future is ready,
  at call time or when the first pending future completes.
- **Child runs** (`REQ-RT-014`, `REQ-RT-015`): `Child` starts a run of the
  child flow under `ChildOptions.ID` in the parent's tenant, and the future
  yields the child's output. When a parent closes, each of its children still
  `running` moves by policy: `terminate` → `terminated`, `request_cancel` →
  `cancelled`, `abandon` → stays `running`.
- **Other calls**: `Emit` appends to the run's ordered outbox and is kept for
  the process's life, like the journal (`REQ-RT-006`); `Checkpoint` is a no-op,
  which `Caps.ContinueAsNew == false` announces (`REQ-RT-005`).

### Steps and retries

`MaxAttempts == 0` means 3 attempts (`REQ-RT-018`). A failed attempt repeats
immediately without backoff on the local runtime while the error's code is not
listed in `StepOptions.NonRetryable` and `core.IsRetryable(err)` holds. When a
step fails with a non-retryable error (`REQ-RT-016`) or exhausts its attempt
limit (`REQ-RT-017`), `Future.Get` returns the step's error (preserving its
`*core.Error` and `core.CodeOf(err)`) and, if unhandled by the flow, the run
moves to `failed`.

### Cancellation and shutdown

Cancellation (from `Cancel`, or from `Serve`'s context at shutdown):

- cancels the run's `context.Context`, which every step function receives, so
  an in-flight step sees `ctx.Done` (`REQ-RT-012`, `REQ-RT-023`);
- completes every pending `Await` and `Sleep` future with `cancelled`
  (`REQ-RT-013`), so the run goroutine unwinds and the step function's
  goroutine is not left blocked;
- only for `Cancel`, moves the run to `cancelled`. Shutdown leaves in-flight
  runs `running` (`REQ-RT-022`).

`Serve` records that it is serving and cancels that state when its context is
cancelled. `Start` called after `Serve` has shut down fails with code
`unavailable` (`REQ-RT-024`), distinct from the `cancelled` a run cut short
reports on its pending calls. `Serve` returns when every goroutine it
supervises has stopped (`REQ-RT-023`): it waits on them, with no sleep and no
fixed grace period.

### Tenancy

Each run's state and step journal are isolated per `(TenantID, RunID)`
(`REQ-RT-021`). `Describe`, `Cancel`, `Resolve` and `List` match on tenant as
well as run id. Describing or cancelling a run of another tenant returns
`not_found`, the same answer as for a run that never existed, rather than
`permission_denied`, which would tell the caller that the id exists
(`REQ-RT-025`).

### Changes outside `runtime/local`

None are needed: `runtime/runtime.go` already has `Caps`, `StepOptions`,
`ChildOptions.ParentClose`, the `terminated` state and every code used, and
the package `runtime/local` exists in `layering.yaml`. If the implementer
finds that a contract change is needed, it lands first in a pull request of
its own.

## Temporal runtime (WP-L5-02)

Decisions for `runtime/temporal` that leave `REQ-RT-026` … `REQ-RT-037` in
`temporal.feature` to the implementer. It builds on the payload codec of
WP-L1-04 (`specs/secrets/`) and the blob store of WP-L1-02 (`specs/blob/`); it
does not respectify either. This work package changes no contract file unless
open question 7 is answered yes, in which case the contract change lands first
in a pull request of its own.

### Shape

The flow host runs as one Temporal workflow; the workflow type and task queue
are private to `runtime/temporal` and the scenarios do not name them.

| Contract call | On the Temporal runtime |
| --- | --- |
| `Step` | activity, on `StepOptions.Queue` or the default queue; `MaxAttempts`, `NonRetryable`, `StartToClose`, `Heartbeat` and `IdempotencyKey` map onto the activity's retry policy (`REQ-RT-027`) |
| `Await` | Update, validated synchronously (`sync_gate_updates`) |
| `Checkpoint` | ContinueAsNew when the run history reaches the threshold (`REQ-RT-034` … `REQ-RT-036`) |
| `Emit` | durable outbox write, ordered per run as on the local runtime |
| `Child`, `Sleep`, `Select`, `Cancel` | as the local runtime (`REQ-RT-009` … `REQ-RT-015`); the conformance suite checks they match |

### What stays with conformance (WP-L5-04)

`REQ-RT-002` (crash resume) has its scenario `RT-002-01` in
`conformance.feature`, at `@level:system`, and the conformance flows own the
equivalence of final states and durable events (`RT-001-01`), replay
(`RT-004-01`) and several golden flows, one of which exercises ContinueAsNew.
This work package does not restate them: `temporal.feature` only says what
conformance cannot observe — the run history's payloads, blobs, event counts
and server version. Restart is not simulated here: killing a worker process
mid-run is the conformance suite's concurrency test.

The contract's retry rules that are already specified — `REQ-RT-016` (a
non-retryable error or a listed code stops retrying) and `REQ-RT-018`
(`MaxAttempts` 0 allows 3 attempts) — bind every runtime, and the conformance
flows show them on the temporal runtime; `REQ-RT-027`
adds only the `MaxAttempts > 0` case where retries can be delayed.

### Payload codec: claim-check and encryption

The workflow and the activity worker share one data converter that wraps two
codecs, in this order:

1. **Encryption** (`REQ-RT-031`): the payload codec of WP-L1-04, with the
   tenant of the run or activity in the context (a context-aware converter, as
   `specs/secrets/design.md` §1 requires). It runs for every payload the runtime
   hands to Temporal, so every payload in the run history and in the activity
   task carries `encoding: binary/encrypted` (`REQ-SECRET-002`). The codec's
   own failures (a payload without that encoding, `REQ-SECRET-006`) are what
   `REQ-RT-032` reports: a run that must load such a payload from its history
   fails it with `invalid_argument`. A start without a tenant is rejected by `Start` itself
   with `invalid_argument` before anything is recorded (`REQ-RT-031`,
   `RT-031-02`); this is the runtime's check, not a new payload codec rule.
2. **Claim-check** (`REQ-RT-028` … `REQ-RT-030`): a payload whose encoded form
   is larger than the threshold is written to the blob store of the run's tenant
   and replaced in the history by a small marker payload whose content, and
   so whose `sha256:` content key (`REQ-BLOB-001`), is the content key of that
   blob. The bytes of a content key and its marker contain no `0x00`
   (`REQ-RT-030`), so the blob's bytes are not the content key of anything
   else and two payloads with the same content key hold the same payload only
   (`REQ-RT-029`). The marker itself is encrypted by step 1 and passes the
   codec untouched on decode, as a payload already encoded by a codec.

Encoding sits inside the workflow, so its ciphertexts and blobs depend on
ambient state, not on the run. The workflow must not branch on anything but
the decoded payload: re-encoding a payload after ContinueAsNew or replay
produces a different ciphertext (`REQ-SECRET-003`) but the same decoded
content, so replay is unaffected. The claim-check threshold applies to the
encrypted form, which is a few bytes larger than the plain form; the sizes in
the scenarios are chosen clear of the boundary by more than that overhead.

Reading a blob the run's tenant does not hold, or that decodes for another
tenant or fails authentication, is the payload codec's or blob store's error
(`REQ-BLOB-010`, `REQ-SECRET-004`): the activity that reads it fails with
`invalid_argument` and the run fails (`REQ-RT-033`).

### ContinueAsNew

The checkpoint event threshold is a configuration of the runtime. The
event count a checkpoint request compares is the number of history events the
run already recorded, not counting the request itself.

- **Event counts** (the rule, then each scenario). The runtime records exactly
  two events per step — the schedule of its activity and its result (a
  retried activity adds events; no scenario retries before a checkpoint) —
  and one event per emitted durable event.
  - `RT-034-01`: after `s8`'s result there are 8 events; the count excludes
    the checkpoint's own event, so `s8`'s checkpoint is at 8 ≥ 8 and
    continues as new; the new run starts with the 8 recorded events and `s9`
    runs there. 1 time.
  - `RT-035-01`: 3 steps give 6 events < 8; the run stays in its history and
    waits at the gate.
  - `RT-035-02`: 6 steps give 12 events, one short of 13; the only checkpoint
    finds 12 < 13. 0 times.
  - `RT-036-01`: `s9`'s checkpoint follows 18 events ≥ 8 and continues as new
    while the run waits at `approve`; the gate wait, the 9 recorded results
    and the run id survive, so no step runs again and the Update completes the
    continued run's wait.
  - `RT-036-02`: 6 emitted events before the only checkpoint give 6 events <
    8. The checkpoint continues the run because the flow explicitly continues
    as new there, as `RT-036-01` does at its gate; the continued run keeps
    the 6 events, runs the 5 steps (30 events) and returns.
- The continued run keeps its id (`StartRequest.RunID`, `r1` in `RT-036-01`),
  its recorded step results, its outbox and its pending gate waits
  (`REQ-RT-036`). `Describe` and `List` report one run under that id; the
  chain of runs Temporal keeps in its place does not appear in `List`. The
  gate Update that arrives after ContinueAsNew addresses the run by id, so the
  continued run receives it.
- Because the runtime always gives a started run a run id (design: a generated
  one when `StartRequest.RunID` is empty), ContinueAsNew never hits Temporal's
  same-run-id collision.
- `Checkpoint` only takes effect at a workflow task boundary: on a run waiting
  at a gate, it is a request on the continued run.

### Version pinning

The `temporalio/cli` release fixes server, API and SDK versions; at this
writing the pinned release ships server v1.32.2, API v1.63.5 and SDK v1.47.0
(`runtime/temporal/embedded/BENCHMARKS.md`). The runtime pins the server major
and minor version — 1.32 — and "outside the pinned range" means a different
major or minor (`1.31.9` and `1.33.0` in `RT-037-02`). On `Serve`, the
runtime asks the server for its version and refuses to serve, with code
`unsupported`, when it is outside the range (`REQ-RT-037`); it reports the
server version it saw through `DescribeServerVersion` (open question 7).

The gate has no way to run an older or newer server build. The Temporal **test
server** — the SDK's dev server whose features the scenarios use through
time skipping — runs exactly one version, the pinned one. `RT-037-02` passes
the runtime a **simulated version of the server it would ask**: a stand-in for
the one version call `Serve` makes, seen through the runtime's connection
parameter, which fails like a server refusing the request. The test server
reports its real version, which `RT-037-01` checks is `1.32.2`; a pin change
updates the Background and that example in the same change.

### How the scenarios run deterministically

`memory` (`runtime/temporal` test setup, `_test.go` and `internal/bdd`): the
Temporal SDK's **test server** (`temporal.NewTestServer`, which embeds the CLI
dev server) with its **test environment** and time skipping. The scenarios
speak test time only (`RT-027-01`'s `1 day of test time`) and time skipping
advances the workflow clock to the next timer or retry at once: no wall clock,
no sleep, no timing assumption. The server is in-process on port 7233 (dev server
default) of a fresh test server, and the embedded profile's server (WP-L5-03) is not
involved.

The gate environment (`TestEnvironment`) does not expose Temporal's history;
histories are read from the test server's client (`GetWorkflowHistory`) and
from its metadata client (the run's event history and the `ContinuedAsNew`
close event). The tenant, the blob store, the payload codec and the key
service come from the gate: the payload codec and its simulated key service
are WP-L1-04's (`internal/bdd/steps/secrets.go`), the blob store is WP-L1-02's
in-memory or file-backed store, and the two are composed here as the
development converter this package takes from the composition root. The
documents of other work packages this work package needs (`specs/secrets/`,
`specs/blob/`) were in review on their own branches when this draft was
written; the implementer uses their step library, not a copy.

### Changed outside `runtime/temporal`

None are expected. `layering.yaml` already lists `runtime/temporal`
(`[core, runtime, flow, platform]`, and `go.temporal.io/` denied in every
other layer), so the runtime may import the payload codec of WP-L1-04 and the
blob store of WP-L1-02 through `platform`. If open question 7 changes
`runtime/runtime.go`, that change lands first in a pull request of its own.

## Open questions

1. **Default step retry limit (`REQ-RT-018`)**: `runtime/runtime.go:48` states `MaxAttempts int // 0 = runtime default`, without pinning a number in `ADR-0002` or `docs/architecture/runtime.md`. This spec chooses `3` attempts for `runtime/local` when `MaxAttempts` is `0`. Confirm whether `3` is the intended default for `local` (and whether `temporal` in `WP-L5-02` should share it).
2. **Run failure error propagation (`REQ-RT-016`, `REQ-RT-017`)**: `runtime.RunStatus` (`runtime/runtime.go:120-126`) exposes `State` (`running | completed | failed | cancelled | terminated`) and timestamps, but has no `Error` or `Code` field on `RunStatus` itself. This spec asserts the error code on the failed step (`Future.Get`) and asserts that the run's state becomes `"failed"`, without inventing an `internal` or `unavailable` run-level error code mapping. Confirm whether `RunStatus` should remain state-only or whether a contract change is wanted later.
3. **Shared `Run` row in `specs/glossary.md`**: `runtime/runtime.go:123` includes `terminated` in `RunStatus.State`, whereas the shared **Run** row in `specs/glossary.md:9` lists `running, completed, failed, cancelled`. To avoid touching shared glossary text that other work packages rely on, this draft defines `terminated` in the new **Child run** row and leaves the **Run** row untouched. Confirm whether the **Run** row in `specs/glossary.md` should be extended with `terminated`.
4. **Acknowledging a step attempt before ContinueAsNew (WP-L5-02)**: if a run
   continues as new while an activity attempt is in flight after a timeout,
   Temporal can ask for the activity again, so `Future.Get` may be a result
   that was not the first attempt's, even though the run's history records
   one result (`REQ-RT-036`). The docs do not say how the runtime should
   acknowledge the in-flight attempt before continuing as new, and no scenario
   can check what the history does not show. Decide: (a) accept at-least-once
   for steps, as `runtime/temporal/embedded/BENCHMARKS.md` item 5 already does,
   and drop the claim from the requirements; or (b) require the runtime to
   complete or cancel in-flight steps before ContinueAsNew.
5. **Counted into the claim-check threshold (`REQ-RT-028`, `REQ-RT-029`)**: "larger than the threshold"
   is counted on the encrypted form, because the runtime encodes before it
   claim-checks. Alternatively it could be counted on the plain payload; that
   needs the encryption codec to expose the plain size to the claim-check
   codec.
6. **Wording of `REQ-RT-030`**: the claim-check marker's exact shape (metadata key,
   payload) is left to the implementer as long as it holds no `0x00` byte.
   Confirm whether the owner wants the marker specified instead, as
   `specs/secrets/design.md` does for its encoded payload.
7. **Reporting the server version (`REQ-RT-037`)**: `Runtime` and `Caps` have
   no way to report the server version or to report the version the runtime
   refused, which `RT-037-01` and `RT-037-02` read. Decide: (a) add
   `DescribeServerVersion` to the runtime contract in its own pull request,
   with or without a versioned `Capabilities` field; or (b) drop those Then
   steps and let the version check report only through the start's error.
   The simulated server version of `RT-037-02` is a test stand-in for the one
   version call; confirm that it is acceptable that no scenario runs an
   actual server of another version.
8. **Who rejects a start without a tenant (`REQ-RT-031`, `RT-031-02`)**:
   `Start` rejects it with `invalid_argument` because the payload codec needs a
   tenant (`REQ-SECRET-022`). Nothing specifies error codes for `Start` and
   `Runtime` has no "unauthenticated" tenant input. Confirm the code, or move
   the check to the API.
9. **`RT-031-02`'s count**: "the Temporal test server holds 0 workloads for
   `fetch`" needs a way to list workloads; the SDK's visibility count query is
   the candidate. Confirm it, or replace the step with a count of started
   activities.
10. **A result that survives a continue-as-new despite a retried attempt**:
    the scenario considered for it (threshold 8, step "charge" whose attempt 1
    times out and attempt 2 completes, then a checkpoint) is not written: the
    run history would hold 3 step events, not the 2 the count assumes, and the
    outcome is the question of open question 4. Once 4 is decided, decide
    whether it needs a scenario.
