# Runtime: design notes

## The request tenant follows the context

The rule binds every runtime on `Runtime.Start` (`StartRequest`), on the run
references of `Describe`, `Cancel` and `Resolve`, and on the `RunQuery` of
`List` (`REQ-RT-042`). The tenant of a call is the context's
(`core.TenantFrom(ctx)`); a tenant field the call carries
(`StartRequest.TenantID`, `RunRef.TenantID`, `RunQuery.TenantID`) may be empty
or equal to it, and nothing else. A field that names a different tenant is a
mismatch, not a request on that tenant: the call fails with
`invalid_argument` and nothing is started, changed or returned. The runtime
never falls back to the field, so a start that names `globex` on an `acme`
context starts no run in either tenant (`RT-042-01`), and an `acme` caller
cannot read, cancel or resolve through a reference that says `globex`
(`RT-042-02` … `RT-042-05`). This check comes before the same-tenant lookup,
so the `not_found` of `REQ-RT-025` keeps answering for a reference that agrees
with the context.

The context is the only source of the tenant for the same reason it is for
`platform.AuditEvent.Tenant`: one source, and no field a caller can set to
reach another tenant's runs. The rule does not trust the field over the
context, as `Record` does not trust the event over the context, and it fails
closed without a tenant.

Enforcing the context against the field also fails closed when the context has
no tenant; an empty or otherwise invalid context id counts as no tenant
(REQ-CORE-005, REQ-CORE-006), not a tenant that matches an empty field. On the local runtime every call without a context tenant is
rejected with `invalid_argument` (`REQ-RT-044`). On the temporal runtime,
`Describe`, `Cancel`, `Resolve` and `List` are rejected the same way
(`REQ-RT-045`), and `Start` is covered by `REQ-RT-040`.

The tenant a call is served under is the context's, so a returned `RunRef`
carries the run's own tenant (`REQ-RT-043`): the reference a start returns,
the `RunStatus.Ref` a description returns and each item of a listing name the
run's tenant, never an empty string or a field the caller passed.

## Local runtime (WP-L5-01)

Decisions for `runtime/local` that leave `REQ-RT-005` … `REQ-RT-019`,
`REQ-RT-021` … `REQ-RT-027`, `REQ-RT-042` … `REQ-RT-044` and
`REQ-RT-046` … `REQ-RT-054` in `local.feature` to the implementer. This
work package needs an additive core change: a timer capability on the clock
`New` accepts (`After`), implemented by `core.SystemClock` and
`coretest.FakeClock` (fires on Advance). No runtime contract file changes.

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
- **Select** (`REQ-RT-011`): returns the call-order index of the operation
  whose future became ready first. Operations already ready when the wait
  begins, and futures that complete in the same clock reading (one clock
  advance is one reading for `REQ-RT-008`), are tied and the lowest index
  among them wins. `RT-011-04` is the discriminating case: the higher-index
  `"1h"` timer completes in the first of two clock advances and wins, even
  though the lower-index `"2h"` timer is also ready by the time the wait is
  answered. `RT-011-02` is the tie case: one advance makes both timers ready
  and the lowest index wins. The alternative reading — lowest index among
  everything ready when the wait is answered — is rejected: it depends on how
  fast the run goroutine wakes, breaks gate determinism (`bdd-convention.md`
  §1.5, `spec-review.md` R5), and would make the flow host's first-failure
  rule (`REQ-FLOW-052`) non-deterministic.
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
listed in `StepOptions.NonRetryable` and `core.IsRetryable(err)` holds. As soon
as an attempt succeeds before the limit, retrying stops and that attempt's
result is the step's result (`REQ-RT-027`). When a step fails with a
non-retryable error (`REQ-RT-016`) or exhausts its attempt limit
(`REQ-RT-017`), `Future.Get` returns the step's error (preserving its
`*core.Error` and `core.CodeOf(err)`; for an exhausted limit, the last
attempt's error) and, if unhandled by the flow, the run moves to `failed`.
The flow's handling is what decides whether the run fails: the runtime delays
the run's `failed` state until the step error reaches the run goroutine
uncaught: only then does the run move to `failed`.

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
well as run id. Describing, cancelling or resolving a gate of a run of
another tenant returns `not_found`, the same answer as for a run that never
existed, rather than `permission_denied`, which would tell the caller that the
id exists (`REQ-RT-025`). `List` filters on tenant, so a tenant lists only its
own runs, even when another tenant has a run with the same id (`REQ-RT-026`).
`terminated` (a child closed by its parent under `terminate`, `REQ-RT-015`) is
one of the `Run` states in `specs/glossary.md`.

### Run ids, start keys and child ids

All three identifiers are resolved under the context tenant, before any work
starts. A `StartRequest.RunID` that already names a run of that tenant fails
with `conflict` (`REQ-RT-046`); the same id in another tenant is a different
run, as `RT-021-01` already shows. A `StartRequest.IdempotencyKey` is mapped
per tenant too: a repeat of a non-empty key in the same tenant returns the first
run's reference and runs no step (`REQ-RT-047`), while the same key in two
tenants yields two runs (`REQ-RT-048`, `RT-048-01`). Empty keys are not stored
in the dedupe map, so each start with an empty key creates a new run
(`REQ-RT-047`, `RT-047-02`). The map is kept for the process's life, like
the journal (see Retention below).

A child id names a child of one parent and one flow. `Child` with the id of a
child already started by this parent for this flow returns that child's future
instead of starting another (`REQ-RT-049`, `RT-049-01`): this is how a parent
re-attaches after a retry, while the child is still in flight. An id that
already names any other run of the tenant — a child of a different parent
(`RT-050-02`), a child of another flow (`RT-050-01`) or a top-level run
(`RT-050-03`) — fails with `conflict` (`REQ-RT-050`); it never silently
replaces the existing run.

### List paging

`List` returns matching runs in the order they were started and, when the page
is short of the matches, a paging cursor naming that page's end. The cursor is
opaque: a client passes it back unchanged and does not interpret it; the
runtime is free to change its encoding. A cursor the runtime did not issue, or
one whose position no longer exists, fails with `invalid_argument`
(`REQ-RT-052`). A cursor carries the tenant it was issued to; a query under a
different tenant fails with `invalid_argument` rather than returning any run
(`REQ-RT-053`, `RT-053-01`), so paging never widens tenant visibility. The
denial happens before the page is read, like the tenant checks in
`REQ-RT-042` … `REQ-RT-044`.

### A local runtime needs a timer clock

`New` takes a clock and requires a timer clock — one that can schedule timers
(`After`), not just report time (`Now`). `New` type-asserts/accepts a clock with
`After`; a `core.Clock` with only `Now` fails construction with an error
(`REQ-RT-054`, `RT-054-01`): timers could not move and `REQ-RT-008` could not
hold. Passing nil selects `core.SystemClock`, which is a timer clock. The gate
injects `coretest.FakeClock`, which fires timers on `Advance`.

### Panics stay inside one run or step

The flow host and each step function run behind a recover at their boundary;
no panic escapes to bring down the process or another run. A panic in the flow
host fails that run, as an unhandled error would. A panic in a step function
fails the step with code `internal`, marked non-retryable, so no retry follows
(`REQ-RT-016`); if the flow does not handle it, the run fails as for any other
unhandled step error. The failure is recorded in the journal so `Describe` and
`Future.Get` report it. These are runtime boundary rules, not a contract
change. The recovered value is logged once per recovery through the runtime's
logger, bounded in size, and is not part of any error a caller receives.

### Retention is the process's life

Runs, their journals and outboxes, and the start-idempotency-key map stay in
memory for as long as the local runtime's process lives; nothing is evicted.
`REQ-RT-006` already says a new runtime has no record of an earlier one's
runs. A long-lived embedding therefore grows without bound and needs a
retention policy (for example evicting closed runs and their keys after an
age). No such policy is specified here; adding one is a new requirement and a
spec change.

### Changes outside `runtime/local`

An additive timer capability on the clock `New` accepts (`After`), implemented
by `core.SystemClock` and `coretest.FakeClock` (fires on Advance), as stated
above. `runtime/runtime.go` already has `Caps`, `StepOptions`,
`ChildOptions.ParentClose`, the `terminated` state and every code used, and
the package `runtime/local` exists in `layering.yaml`. No runtime contract
file changes. If the implementer finds that a further contract change is
needed, it lands first in a pull request of its own.

## Temporal runtime (WP-L5-02)

Decisions for `runtime/temporal` that leave `REQ-RT-028` … `REQ-RT-043` and
`REQ-RT-045` in `temporal.feature` to the implementer. It
builds on the payload codec of WP-L1-04 (`specs/secrets/`) and the blob store
of WP-L1-02 (`specs/blob/`); it does not respecify either. This work package
changes no contract file.

### Shape

The flow host runs as one Temporal workflow; the workflow type and task queue
are private to `runtime/temporal` and the scenarios do not name them.

| Contract call | On the Temporal runtime |
| --- | --- |
| `Step` | activity, on `StepOptions.Queue` or the default queue; `StartToClose`, `Heartbeat` and `IdempotencyKey` map onto the activity options, and `MaxAttempts` and `NonRetryable` map onto its retry policy (`REQ-RT-041`) |
| `Await` | Update, validated synchronously (`sync_gate_updates`) |
| `Checkpoint` | ContinueAsNew, at `Checkpoint` only, when the current run history's length as the Temporal SDK reports it reaches the threshold; first waits until no step of the run is in flight (`REQ-RT-034` … `REQ-RT-036`, `REQ-RT-038`) |
| `Emit` | durable outbox write, ordered per run as on the local runtime |
| `Child`, `Sleep`, `Select`, `Cancel` | as the local runtime (`REQ-RT-009` … `REQ-RT-015`); the conformance suite checks they match |

### What stays with conformance (WP-L5-04)

`REQ-RT-002` (crash resume) has its scenario `RT-002-01` in
`conformance.feature`, at `@level:system`, and the conformance flows own the
equivalence of final states and durable events (`RT-001-01`), replay
(`RT-004-01`) and several golden flows, one of which exercises ContinueAsNew.
This work package does not restate them: `temporal.feature` only says what
conformance cannot observe — the run history's payloads, blobs, history length
and server version. Restart is not simulated here: killing a worker process
mid-run is the conformance suite's concurrency test.

The contract's retry rules that are already specified — `REQ-RT-016` (a
non-retryable error or a listed code stops retrying) and `REQ-RT-018`
(`MaxAttempts` 0 allows 3 attempts) — bind every runtime on paper, but
`conformance.feature` has only the `linear`, `parallel-merge` and
`gate-and-timeout` flows, which exercise none of them, so their cross-runtime
parity on the temporal runtime is deferred to WP-L5-04 with its flows and
golden records; `temporal.feature` does not restate them. `REQ-RT-041` covers
only the `MaxAttempts > 0` case, where the retry can be delayed.

### Payload codec: claim-check and encryption

The workflow and the activity worker share one context-aware data converter
that composes the encryption codec of WP-L1-04 and the blob claim-check codec
over WP-L1-02. The tenant of the run or activity is read from the context
through `core.TenantFrom(ctx)` set by `core.WithTenant(ctx, id)`, as
`specs/secrets/design.md` §1 requires; an empty or otherwise invalid id
counts as no tenant (REQ-CORE-005, REQ-CORE-006).
The codec composes them in this order:

1. **On encode** (`REQ-RT-028`, `REQ-RT-031`): the payload is first encrypted
   with the tenant's encryption codec (`REQ-SECRET-002`). If the encrypted
   payload's size in bytes exceeds the claim-check threshold, the encrypted
   payload is written as a blob to the run's tenant in the blob store under
   its `sha256:` content key (`REQ-BLOB-001`), replaced by a small claim-check
   marker payload naming that content key, and the marker itself is encrypted
   with the tenant's encryption codec. Every payload written to the run
   history or an activity task — whether kept inline or replaced by a
   claim-check marker — therefore carries `encoding: binary/encrypted`
   (`REQ-RT-031`). A start without a tenant is rejected by `Runtime.Start`
   itself with `invalid_argument` before any step runs or any run history is
   recorded (`REQ-RT-040`, `RT-031-02`); the API checks as well.
2. **On decode** (`REQ-RT-029`, `REQ-RT-032`, `REQ-RT-033`): the history
   payload is first decrypted with the tenant's encryption codec; a payload
   without `encoding: binary/encrypted` is rejected with `invalid_argument`
   (`REQ-SECRET-006`, `REQ-RT-032`). If the decrypted payload is a claim-check
   marker, the codec reads the referenced blob from the blob store for the
   run's tenant and decrypts that blob with the tenant's encryption codec to
   restore the original payload's content and encoding (`REQ-RT-029`).
   Decoding happens when the workflow reads a payload back — an activity's
   result through its `Future.Get`, a gate Update's payload, or a replay of
   either — so a decode error fails that step or the decoding replay, not
   the run as a whole: `Future.Get` fails the step with `invalid_argument`
   (`REQ-RT-032`) or `not_found` (`REQ-RT-033`), and when the flow does not
   handle the error the step's failure moves the run to `failed`, as for any
   other unhandled step error (`REQ-RT-017`). `RunStatus` has no error code
   of its own, so the scenarios assert the code on the step and the state on
   the run.

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
blob store returns `not_found` (`REQ-BLOB-006`, `REQ-BLOB-010`) and the step
fails with `not_found` and, unhandled, the run fails (`REQ-RT-033`). If a
payload or blob ciphertext was tampered with or encrypted for another tenant,
the encryption codec rejects it with `invalid_argument` (`REQ-SECRET-004`,
`REQ-SECRET-005`).

### ContinueAsNew

The checkpoint event threshold is a configuration of the runtime.
`docs/architecture/runtime.md` (§2, §3) maps `Checkpoint` to ContinueAsNew at
a fixed event threshold. ContinueAsNew happens **only** at an explicit
`Checkpoint` call; nothing else the runtime does continues a run as new.

- **The measure is Temporal's history length.** At a `Checkpoint`, the
  workflow compares the threshold with the current run history's length in
  events as the Temporal SDK reports it at that moment
  (`workflow.GetInfo(ctx).GetCurrentHistoryLength()`, read inside the
  workflow). The runtime models no event count: it does not assume a number
  of events per step, per emitted event or per anything else. Those events are
  the SDK's and server's to decide, and the runtime treats the number as
  opaque except for the comparison. The runtime continues the run as new if
  and only if that length is at least the threshold (`REQ-RT-034`), and leaves
  the run in its current history otherwise (`REQ-RT-035`). A new history
  (after ContinueAsNew) starts its length again from the few events that begin
  it.
- **Scenarios stay away from the boundary.** Because no scenario knows how
  many events a step records, a scenario never places a threshold at the
  boundary of a run's history length; it chooses thresholds far from it, so
  the verdict is the same for any plausible history: a short run (a few steps
  and emitted events) far below the threshold continues as new `0` times
  (`RT-035-01`, `RT-035-02`: threshold `1000` with 3 and 6 steps), and a long
  run far above it continues as new at least once, without naming the exact
  number of times (`RT-034-01`: threshold `20` with 40 steps). A scenario that
  needs an exact count takes it from the structure of its flow instead: one
  checkpoint above the threshold and no later checkpoint that can reach it
  again gives exactly `1`. `RT-036-02` (threshold `20`) checkpoints once,
  after 40 steps; `RT-038-01` (threshold `20`) checkpoints once, after 5
  steps `s1` to `s5` and while one of two parallel steps is still in flight,
  and no later checkpoint exists.
- **Checkpoint drains in-flight steps first.** When `Checkpoint` is called
  while a step of the run is in flight, the runtime waits until no step of the
  run is in flight and only then continues the run as new if its history has
  reached the threshold (`REQ-RT-038`). In
  `RT-038-01` the checkpoint comes after 5 steps and meets the threshold
  `20` by itself with the events those 5 steps record, while step `b` of the
  parallel pair is still in flight, so the wait is observable: the run
  continues as new only once `b` has completed, and `b` is not run again
  (`step "b" completed before the continue-as-new`). A step in flight is a
  `Step` call of the run whose activity has not yet returned a result; a step
  the flow started and does not await still counts. Created activities that are still running would make Temporal
  re-dispatch them on the new execution, so draining is what keeps a
  continue-as-new from running a step again. Steps remain at-least-once in
  general — a crash or a failed attempt may run a step again — and idempotent
  per `IdempotencyKey`; what draining guarantees is that the continue-as-new
  itself is never the cause (`RT-038-01`).
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

The `temporalio/cli` release fixes server, API and SDK versions; the
pinned release ships server v1.32.2, API v1.63.5 and SDK v1.47.0
(`runtime/temporal/embedded/BENCHMARKS.md`). The runtime pins the server major
and minor version — 1.32 — and "outside the pinned range" means a different
major or minor (`1.31.9` and `1.33.0` in `RT-037-02`). On `Serve`, the
runtime asks the server for its version and refuses to serve, with code
`unsupported`, when it is outside the range (`REQ-RT-037`). This check uses no
new method on `runtime.Runtime` or `runtime.Caps`.

The gate downloads no server binary. The harness runs `runtime/temporal`
workflow and activity code on the SDK's `TestWorkflowEnvironment`, which is
in-process and owns its own skipping clock; no Temporal dev server is
downloaded or started.

### How the scenarios run deterministically

The harness runs the workflow and activities on the SDK's
`TestWorkflowEnvironment`, whose clock skips to the next timer or retry at
once. "1 day of test time" in `RT-041-01`/`RT-041-02` is the environment clock
skipping straight to that point; there is no wall clock and no sleep.

The runtime's data converter is the payload codec of WP-L1-04 composed with the
blob claim-check codec of WP-L1-02; the harness wraps that converter in a
recorder. Every payload the runtime hands to the SDK on encode passes through
the recorder, so "the run history holds ...", "every payload in the run
history ..." and "the Temporal test server holds no run history for that start"
(`RT-028-01`, `RT-029-01`, `RT-031-01`, `RT-031-02`, `RT-032-01`, `RT-033-01`,
`RT-040-01`, `RT-042-07`) are read from what the recorder saw — the payloads that would be
written to history. An empty recorder is "no run history". The tenant, the blob
store, the payload codec and the simulated key service still come from the
gate: the codec and key service are WP-L1-04's (`internal/bdd/steps/secrets.go`),
the blob store is WP-L1-02's, and the two are composed here as the data
converter the package takes from the composition root. The implementer uses the
step library of the other work packages this work package needs
(`specs/secrets/`, `specs/blob/`), not a copy.

ContinueAsNew (`RT-034-01` … `RT-038-01`): at a `Checkpoint` past the threshold,
the workflow returns the SDK's continue-as-new error with the continuation
input; the harness then starts the next execution under the same run id, as the
server does, so "continued as new N times" counts those restarts. Before each Checkpoint, the harness sets `SetCurrentHistoryLength` to a value
clearly on the scenario's intended side of the threshold (well above for
`RT-034-01`/`RT-036-*`/`RT-038-01`, well below for `RT-035-*`), so the
comparison is deterministic without depending on how many events the SDK would
count.

Serve version check (`RT-037-01`/`02`): a minimal in-process gRPC
`WorkflowService`, test-only code in `internal/bdd`, listens on `127.0.0.1:0`
and answers `GetSystemInfo` with the scenario's version — `1.32.2` in
`Background` and `RT-037-01`, and `1.31.9` and `1.33.0` in `RT-037-02`. The
Background step "Given a Temporal test server at version …" and "the Temporal
test server" in `RT-037-01` name that stub (and the harness world), never
`StartDevServer` or a downloaded binary. The runtime dials it through the real
client path; no server binary is downloaded.

Against a real server, WP-L5-03 (embedded server) and WP-L5-04 (conformance,
crash resume, replay) run the same workflows in real time, in the slow lane.

### Changed outside `runtime/temporal`

None. `layering.yaml` already lists `runtime/temporal`
(`[core, runtime, flow, platform]`, and `go.temporal.io/` denied in every
other layer), so the runtime may import the payload codec of WP-L1-04 and the
blob store of WP-L1-02 through `platform`.

## Open questions

1. **Default step retry limit (`REQ-RT-018`)**: `runtime/runtime.go:48` states `MaxAttempts int // 0 = runtime default`, without pinning a number in `ADR-0002` or `docs/architecture/runtime.md`. This spec chooses `3` attempts for `runtime/local` when `MaxAttempts` is `0`. Confirm whether `3` is the intended default for `local` (and whether `temporal` in `WP-L5-02` should share it).
2. **Run failure error propagation (`REQ-RT-016`, `REQ-RT-017`)**: `runtime.RunStatus` (`runtime/runtime.go:120-126`) exposes `State` (`running | completed | failed | cancelled | terminated`) and timestamps, but has no `Error` or `Code` field on `RunStatus` itself. This spec asserts the error code on the failed step (`Future.Get`) and asserts that the run's state becomes `"failed"`, without inventing an `internal` or `unavailable` run-level error code mapping. Confirm whether `RunStatus` should remain state-only or whether a contract change is wanted later.
3. **(WP-L5-02) Payload codec composition and claim-check threshold** (`REQ-RT-028` … `REQ-RT-032`):
   `docs/architecture/runtime.md` §3 says "On Temporal this is a payload codec,
   which also encrypts", without prescribing the internal ordering of
   encryption and claim-check. This spec encrypts the payload first, stores the
   encrypted payload in the blob store when it exceeds the threshold, and
   encrypts the resulting claim-check marker so that both the blob store and
   every payload in Temporal's run history hold only `binary/encrypted`
   ciphertext. Confirm this composition (and counting the threshold on the
   encrypted size), or state another codec arrangement.
