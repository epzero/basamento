# Runtime: design notes

## Local runtime (WP-L5-01)

Decisions for `runtime/local` that leave `REQ-RT-005` … `REQ-RT-022` and
`local.feature` to the implementer. This work package changes no contract file.

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
  for `N` and runs the registered function on its own goroutine. Every `Step(N)`
  returns that entry. Calling step functions on their own goroutines is what
  lets independent steps overlap (`REQ-RT-018`).
- **Timers** (`REQ-RT-008`): `Sleep(d)` registers a wake-up at `Now()+d` on the
  clock; it completes only when the clock reports that time.
- **Gates** (`REQ-RT-009`, `REQ-RT-010`): `Await` registers the run at the gate
  name. `Resolve` hands the payload to the run's journal and completes its
  future. `Resolve` for a gate the journal already holds returns a
  `*core.Error` with code `conflict` (the contract's code, not a new one); the
  run does not see it.
- **Select** (`REQ-RT-011`): returns the lowest index whose future is ready,
  at call time or later.
- **Child runs** (`REQ-RT-014`, `REQ-RT-015`): `Child` starts a run of the
  child flow under `ChildOptions.ID` in the parent's tenant, and the future
  yields the child's output. In `RT-014-01` the parent observes the child as
  the run `child-1`, which is the one its `Child` call started, once
  `flow "child"` has returned. When a parent closes, each of its children still `running` moves by
  policy: `terminate` → `terminated`, `request_cancel` → `cancelled`,
  `abandon` → stays `running`.
- **Other calls**: `Emit` appends to the run's ordered outbox and is kept for
  the process's life, like the journal (`REQ-RT-006`); `Checkpoint` is a no-op,
  which `Caps.ContinueAsNew == false` announces (`REQ-RT-005`).

### Steps and retries

`MaxAttempts == 0` means 3 attempts (`REQ-RT-018`). A failed attempt repeats only while the
error is not listed in `StepOptions.NonRetryable` and `core.IsRetryable`
holds. Otherwise `Get` fails with the step's error and the run fails with
`internal` (`REQ-RT-016`). A retryable failure after the last attempt fails the
run with `unavailable` (`REQ-RT-017`); its `Get` reports the last attempt's
error. The backoff waits on the injected clock, so a fake clock drives the
retries.

Why the two codes differ: a caller can tell "this will not succeed as sent"
(`internal`) from "it could not be done here" (`unavailable`), and the
scenarios can assert which rule stopped the step.

### Cancellation and shutdown

Cancellation (from `Cancel`, or from `Serve`'s context at shutdown):

- cancels the run's `context.Context`, which every step function receives, so
  an in-flight step sees `ctx.Done` (`REQ-RT-012`);
- completes every pending `Await` and `Sleep` future with `cancelled`
  (`REQ-RT-013`), so the run goroutine unwinds and the step function's
  goroutine is not left blocked;
- only for `Cancel`, moves the run to `cancelled`. Shutdown leaves it
  `running` (`REQ-RT-021`).

`Serve` records that it is serving and cancels that state when its context is
cancelled. `Start` called while the runtime is not serving fails with code
`unavailable`, distinct from the `cancelled` a run cut short reports on its
pending calls. `Serve` returns when every goroutine it supervises has
stopped: it waits on them, with no sleep and no fixed grace period.

### Tenancy

`Describe` and `List` match on tenant as well as run id. A run of another
tenant is `not_found`, the same answer as for a run that never existed, rather
than `permission_denied`, which would tell the caller that the id exists
(`REQ-RT-020`).

### Changes outside `runtime/local`

None are needed: `runtime/runtime.go` already has `Caps`, `StepOptions`,
`ChildOptions.ParentClose`, the `terminated` state and every code used, and
the package `runtime/local` exists in `layering.yaml`. If the implementer
finds that a contract change is needed, it lands first in a pull request of
its own.
