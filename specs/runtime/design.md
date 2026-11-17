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

Decisions for `runtime/temporal` that leave `REQ-RT-026` … `REQ-RT-029` and
`REQ-RT-031` … `REQ-RT-037` in `temporal.feature` to the implementer. It
builds on the payload codec of WP-L1-04 (`specs/secrets/`) and the blob store
of WP-L1-02 (`specs/blob/`); it does not respectify either. This work package
changes no contract file.

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

The workflow and the activity worker share one context-aware data converter
(with the tenant of the run or activity in the context, as
`specs/secrets/design.md` §1 requires) that composes the encryption codec of
WP-L1-04 and the blob claim-check codec over WP-L1-02:

1. **On encode** (`REQ-RT-028`, `REQ-RT-031`): the payload is first encrypted
   with the tenant's encryption codec (`REQ-SECRET-002`). If the encrypted
   payload's size in bytes exceeds the claim-check threshold, the encrypted
   payload is written as a blob to the run's tenant in the blob store under
   its `sha256:` content key (`REQ-BLOB-001`), replaced by a small claim-check
   marker payload naming that content key, and the marker itself is encrypted
   with the tenant's encryption codec. Every payload written to the run
   history or an activity task — whether kept inline or replaced by a
   claim-check marker — therefore carries `encoding: binary/encrypted`
   (`REQ-RT-031`). A start without a tenant is rejected by `Start` itself with
   `invalid_argument` before anything is recorded (`REQ-RT-031`, `RT-031-02`).
2. **On decode** (`REQ-RT-029`, `REQ-RT-032`, `REQ-RT-033`): the history
   payload is first decrypted with the tenant's encryption codec; a payload
   without `encoding: binary/encrypted` is rejected with `invalid_argument`
   (`REQ-SECRET-006`, `REQ-RT-032`). If the decrypted payload is a claim-check
   marker, the codec reads the referenced blob from the blob store for the
   run's tenant and decrypts that blob with the tenant's encryption codec to
   restore the original payload's content and encoding (`REQ-RT-029`).

Encoding sits inside the data converter, so ciphertexts and blobs depend on
ambient state, not on deterministic workflow state. The workflow must not
branch on anything but the decoded payload: re-encoding a payload after
ContinueAsNew or replay produces a different ciphertext (`REQ-SECRET-003`)
but the same decoded content, so replay is unaffected. The claim-check
threshold applies to the encrypted form, which is a few bytes larger than the
plain form; the sizes in the scenarios (`1024`, `6000`, `65536`) are chosen
clear of the 5 KB boundary by more than that overhead.

When a claim-check marker in a run's history names a blob content key that the
run's tenant does not hold (including a blob stored by another tenant), the
blob store returns `not_found` (`REQ-BLOB-006`, `REQ-BLOB-010`) and the run
fails with `not_found` (`REQ-RT-033`). If a payload or blob ciphertext was
tampered with or encrypted for another tenant, the encryption codec rejects it
with `invalid_argument` (`REQ-SECRET-004`, `REQ-SECRET-005`).

### ContinueAsNew

The checkpoint event threshold is a configuration of the runtime.
`docs/architecture/runtime.md` (§2, §3) maps `Checkpoint` to ContinueAsNew at
a fixed event threshold and does not specify automatic ContinueAsNew outside
`Checkpoint`; in this spec ContinueAsNew is triggered only when `Checkpoint`
is called and the current history's event count has reached the threshold.

- **Event counts** (one rule, applied to every scenario). In the current
  history (since the run started or since its last ContinueAsNew, which resets
  the current history's event count to 0), the runtime counts **2 events per
  step** (activity scheduled and activity result; a retried step adds 1 event
  per retry, and no scenario retries before a checkpoint) and **1 event per
  emitted durable event** (`Emit`), excluding the checkpoint request itself.
  When `Checkpoint` is called, the runtime continues the run as new if and
  only if `count >= threshold` (`REQ-RT-034`, `REQ-RT-035`):
  - `RT-034-01` (threshold `8`): 5 steps `s1`–`s5` with a checkpoint after
    each give counts `2`, `4`, `6`, `8` at `s1`–`s4`; `s4`'s checkpoint hits
    `8 >= 8` and continues as new (resetting the new history's count to `0`),
    and `s5` records `2 < 8` in the new history. Continued as new `1` time.
  - `RT-035-01` (threshold `8`): 3 steps `s1`–`s3` give `6 < 8` events at the
    checkpoint; the run stays in its history (continued as new `0` times,
    checkpoint recorded at event count `6`) and completes when gate `"approve"`
    is resolved.
  - `RT-035-02` (threshold `13`): 6 steps `s1`–`s6` give `12 < 13` events (one
    short of the threshold) at the only checkpoint. Continued as new `0`
    times.
  - `RT-036-01` (threshold `8`): 9 steps `s1`–`s9` give `18 >= 8` events at
    the checkpoint, so the run continues as new `1` time and waits at gate
    `"approve"`; the run id (`"r1"`), the 9 recorded step results and the
    gate wait survive, so resolving `"approve"` on `"r1"` completes the run
    without re-executing `s1`–`s9`.
  - `RT-036-02` (threshold `6`): 6 emitted events `"a"`–`"f"` give `6 >= 6`
    events at the checkpoint, so the run continues as new `1` time (resetting
    the new history's count to `0`), then runs 5 steps (`10` events, with no
    further checkpoint) and returns `"done"` with `"a"`–`"f"` in its outbox
    once, in order.
- The continued run keeps its id (`StartRequest.RunID`, `"r1"` in
  `RT-036-01`), its recorded step results, its outbox and its pending gate
  waits (`REQ-RT-036`). `Describe` and `List` report one run under that id;
  the chain of workflow executions Temporal keeps under the hood does not
  appear in `List`. The gate Update that arrives after ContinueAsNew addresses
  the run by id, so the continued run receives it.
- Because the runtime always gives a started run a run id (a generated one
  when `StartRequest.RunID` is empty), ContinueAsNew never hits Temporal's
  same-run-id collision.

### Version pinning

The `temporalio/cli` release fixes server, API and SDK versions; at this
writing the pinned release ships server v1.32.2, API v1.63.5 and SDK v1.47.0
(`runtime/temporal/embedded/BENCHMARKS.md`). The runtime pins the server major
and minor version — 1.32 — and "outside the pinned range" means a different
major or minor (`1.31.9` and `1.33.0` in `RT-037-02`). On `Serve`, the
runtime asks the server for its version and refuses to serve, with code
`unsupported`, when it is outside the range (`REQ-RT-037`). This check uses no
new method on `runtime.Runtime` or `runtime.Caps`.

The gate has no way to run an older or newer server build. The Temporal **test
server** — the SDK's dev server whose features the scenarios use through
time skipping — runs at the pinned version (`1.32.2` in `Background` and
`RT-037-01`). `RT-037-02` passes the runtime a **simulated Temporal server
version** for the version check `Serve` makes, which fails with `unsupported`
when outside `1.32.x`.

### How the scenarios run deterministically

In `runtime/temporal` tests (`_test.go` and `internal/bdd`), the harness starts
an in-process Temporal test server on an **ephemeral loopback port
(`127.0.0.1:0`)** with time skipping enabled; the embedded profile's server
(WP-L5-03) and fixed port `7233` are never used. The scenarios speak test time
only (`RT-027-01`'s `1 day of test time`) and time skipping advances the
workflow clock to the next timer or retry at once: no wall clock, no sleep, no
timing assumption.

Histories are read from the test server's client (`GetWorkflowHistory`,
including payload metadata and `ContinuedAsNew` events). The tenant, the blob
store, the payload codec and the simulated key service come from the gate: the
payload codec and its simulated key service are WP-L1-04's
(`internal/bdd/steps/secrets.go`), the blob store is WP-L1-02's store, and the
two are composed here as the data converter this package takes from the
composition root. The documents of other work packages this work package needs
(`specs/secrets/`, `specs/blob/`) were in review on their own branches when
this draft was written; the implementer uses their step library, not a copy.

### Changed outside `runtime/temporal`

None. `layering.yaml` already lists `runtime/temporal`
(`[core, runtime, flow, platform]`, and `go.temporal.io/` denied in every
other layer), so the runtime may import the payload codec of WP-L1-04 and the
blob store of WP-L1-02 through `platform`.

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
   for steps, as `runtime/temporal/embedded/BENCHMARKS.md` item 5 already does;
   or (b) require the runtime to complete or cancel in-flight steps before
   ContinueAsNew.
5. **Payload codec composition and claim-check threshold (`REQ-RT-028` … `REQ-RT-032`)**:
   `docs/architecture/runtime.md` §3 says "On Temporal this is a payload codec,
   which also encrypts", without prescribing the internal ordering of
   encryption and claim-check. This spec encrypts the payload first, stores the
   encrypted payload in the blob store when it exceeds the threshold, and
   encrypts the resulting claim-check marker so that both the blob store and
   every payload in Temporal's run history hold only `binary/encrypted`
   ciphertext. Confirm this composition (and counting the threshold on the
   encrypted size), or say if the architect prefers another codec arrangement.
6. **ContinueAsNew trigger and event counting (`REQ-RT-034`, `REQ-RT-035`)**:
   `docs/architecture/runtime.md` (§2, §3) states "`Checkpoint` =
   ContinueAsNew" and "Histories are bounded with `Checkpoint` at a fixed
   event threshold", without specifying whether ContinueAsNew can ever trigger
   outside an explicit `Checkpoint` call or whether the threshold counts
   logical runtime events (2 per step, 1 per emitted event) versus raw
   Temporal `HistoryEvent` records. Confirm the `Checkpoint`-only trigger and
   the 2-per-step / 1-per-emit event count model used in `RT-034-01` …
   `RT-036-02`.
7. **Who rejects a start without a tenant (`REQ-RT-031`, `RT-031-02`)**:
   `Start` rejects it with `invalid_argument` because the payload codec needs a
   tenant (`REQ-SECRET-022`). Nothing specifies error codes for `Start` and
   `Runtime` has no "unauthenticated" tenant input. Confirm the code, or move
   the check to the API.
