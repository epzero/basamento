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

## Open questions

1. **Default step retry limit (`REQ-RT-018`)**: `runtime/runtime.go:48` states `MaxAttempts int // 0 = runtime default`, without pinning a number in `ADR-0002` or `docs/architecture/runtime.md`. This spec chooses `3` attempts for `runtime/local` when `MaxAttempts` is `0`. Confirm whether `3` is the intended default for `local` (and whether `temporal` in `WP-L5-02` should share it).
2. **Run failure error propagation (`REQ-RT-016`, `REQ-RT-017`)**: `runtime.RunStatus` (`runtime/runtime.go:120-126`) exposes `State` (`running | completed | failed | cancelled | terminated`) and timestamps, but has no `Error` or `Code` field on `RunStatus` itself. This spec asserts the error code on the failed step (`Future.Get`) and asserts that the run's state becomes `"failed"`, without inventing an `internal` or `unavailable` run-level error code mapping. Confirm whether `RunStatus` should remain state-only or whether a contract change is wanted later.
3. **Shared `Run` row in `specs/glossary.md`**: `runtime/runtime.go:123` includes `terminated` in `RunStatus.State`, whereas the shared **Run** row in `specs/glossary.md:9` lists `running, completed, failed, cancelled`. To avoid touching shared glossary text that other work packages rely on, this draft defines `terminated` in the new **Child run** row and leaves the **Run** row untouched. Confirm whether the **Run** row in `specs/glossary.md` should be extended with `terminated`.
