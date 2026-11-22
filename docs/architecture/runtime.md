# Runtimes

Status: contract **Implemented** (`runtime/runtime.go`); the flow host
**Implemented** (`flow/host`, WP-L4-04); `local` **Implemented**
(`runtime/local`, WP-L5-01); `temporal` **In progress** — data path and workflow features
(`runtime/temporal`, WP-L5-02 pull requests (a) and (b); control plane still
planned in pull request (c)).
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

## 5. Embedded Temporal server (WP-L5-03)

The embedded profile runs a pinned **Temporal server v1.32** in-process on a
file-backed SQLite database under `data.dir`, started by `kit serve` with no
external Temporal service (REQ-RT-003). The server stays in nested module
`runtime/temporal/embedded` (ADR-0002); **linking into `kit`** is ADR-0021.
Library packages never import it; only `cmd/kit` and, if needed, `internal/bdd`
may. WP-L5-03 wires the server in `kit serve`; it does **not** register a flow
host or worker in the `kit` process (that lands in a later work package).

### Measured cost

Runtime behaviour (server-only bench, `runtime/temporal/embedded/BENCHMARKS.md`):

- a **116 MB** stripped static binary when `bench` links server, SDK and worker
  (not the `kit` binary size; see below);
- a 0.3–0.55 s cold start and a sub-100 ms start on an existing database;
- about 210 MB of RSS;
- about 50 ms per sequential step (the same as the official dev server);
- correct recovery after `kill -9` in 10 of 10 runs;
- deterministic replay.

**Linking cost** (measured **2026-11-22** on darwin/arm64, Go 1.26.11;
`CGO_ENABLED=0 go build -o kit ./cmd/kit`; **MB = 10⁶ bytes**; CI will differ —
ADR-0021): unstripped `kit` **106 MB → 196 MB**; stripped with
`-ldflags='-s -w'` (as `BENCHMARKS.md`) **72 MB → 134 MB**. The **116 MB** in
ADR-0002 and `BENCHMARKS.md` is the stripped **bench** binary, not `kit`.
`go build ./cmd/kit` takes about **20–70 s** wall with a warm cache and about
**four CPU-minutes** cold; root `go.mod` **199 → 310** lines (+111, 96
`// indirect`); `go.sum` **737 → 1073** (+336); `go list -m all` **426 → 563**
(+137); `go list -deps ./cmd/kit` **1127 → 1729** packages.

~50 ms per sequential step adds about 1 s to a 20-step agent loop. Interactive
work that needs lower latency runs on the `local` runtime.

### Linking into one `kit` binary (D12)

See **ADR-0021** for the full decision. Summary:

- `cmd/kit` imports `runtime/temporal/embedded`; root `go.mod` **`require`s
  `v0.1.0`**, **`replace`s** to `./runtime/temporal/embedded`, and **`replace`s
  the OpenTelemetry family** at v1.45.0 / `otel/log` v0.20.0 so
  `go.temporal.io/server` v1.32.2 compiles in the same graph as `gocloud.dev`
  v0.49.0. A `replace` cap overrides every required version; raising a
  `require` above a cap has no effect while the cap stands. The gate's
  `go build ./cmd/kit` fails when a cap is removed or weakened while the server
  still needs it; delete the six caps and rebuild to learn whether they can go.
  Caps apply only to the root module's builds (consumers ignore them).
- **Tag prerequisite (owner):** `runtime/temporal/embedded/v0.1.0` on `main`
  before the implementation merges; order: nested tag → root `require` bump →
  root release tag.
- **`go install …/cmd/kit@version` is not supported** (root `replace`); releases
  build from a checkout (`ADR-0021`).
- **Parity:** raise the nested `go.mod` to the root's versions for shared
  modules (except the pinned OTel family); unified `modernc.org/sqlite` is
  **v1.57.0**; re-run embedded **crash + replay** after alignment.
- **Module graph (measured 2026-11-22):** a consumer that `require`s only the
  root and imports only `runtime`: `go list -m -e all` **185 → 282** (+97); one
  that also imports `runtime/temporal`, `platform/blob` and `platform/sql`:
  **384 → 450** (+66). No `go.temporal.io/server` packages in `go list -deps`;
  consumers resolve `otel` v1.47.0 (caps invisible). Listing grows ~40–50%;
  nothing from the server tree is compiled (ADR-0002 amended).

**Rejected:** child/helper binary (constitution, ADR-0001, REQ-RT-003);
downgrading `gocloud.dev` instead of OTel caps (blob tests break); forking
server; waiting for upstream near term; nested `cmd/kit` module (ambiguous
import when the root tree still ships `cmd/kit`, or high churn to retarget
build, BDD, layering — owner may still choose it; caps in the root graph are
the reason caps stay on the root `go.mod`, per ADR-0021).

`make gate` **`build`** is already `go build ./...`; once `cmd/kit` imports
embedded, that step links the server — no separate gate target.

### Module API and lifecycle (D13–D19)

**D13 — `Start(ctx, Options) (*Server, error)` and `(*Server) Stop(ctx)
error`.** Replace the spike's sync API and dropped `Stop` error. Export plain
Go errors the module cannot wrap as `core.Error`: `ErrInUse` when the
data-directory lock cannot be acquired (`cmd/kit` → `unavailable`,
`temporal: embedded data directory is in use`); `ErrUnsupportedPlatform` on
non-Unix builds (`cmd/kit` → `unsupported`,
`temporal: the embedded server needs a Unix host`). No `panic` in library code.

**D14 — Loopback listeners and trust boundary.** The spike opens five loopback
listeners (`embedded.go`: frontend gRPC and HTTP, history, matching, worker), all
on `127.0.0.1` with ephemeral ports unless tests set `Options.Port` (D18). The
OSS Temporal server has no authentication. The frontend HTTP listener stays
only if the server cannot start with `HTTPPort` 0 — the implementation pull
request must try `HTTPPort` 0 and disable HTTP when that works; record the
result. Trust domain: every local process that can reach loopback, the same as
`kit serve` under `auth.mode: none` (REQ-AUTH-001 requires loopback). Payloads
are encrypted by the data converter, so a hostile local process cannot read
them, but it can cancel, terminate or signal runs. On a multi-user host the
embedded profile is not safe against other local users; use the cloud profile
or OS-level restriction. Mode `0600` on `frontend.grpc` is hygiene, not
protection (ports can be scanned). Accepted risk (ADR-0021).

**D15 — Ready.** `Start` returns after `temporal.Server.Start()` succeeds.
Readiness: dial the frontend with the SDK (`client.Dial`; it already calls
`GetSystemInfo` via `loadCapabilities`); retry after **50 ms**, doubling up to
**1 s**, using `select` on a timer and `ctx.Done()` (no `time.Sleep`). The caller
supplies the bound: `cmd/kit` passes `context.WithTimeout(ctx, 30*time.Second)`
(constant, not a setting). On expiry `Start` stops the server, releases the lock
and returns an error wrapping `ctx.Err()` and the last dial error; `cmd/kit`
maps it to `unavailable` (`temporal: the embedded server did not become ready`).
The **1.32** major.minor version check stays in `runtime/temporal.Serve`
(WP-L5-02 part (c) D7), not in the embedded module.

**D16 — Data directory layout.** Caller passes absolute paths; the module does
not import `internal/config`:

| Path | Purpose |
| --- | --- |
| `temporal/sqlite.db` | Temporal persistence (`modernc.org/sqlite`) |
| `temporal/sqlite.db.lock` | Exclusive non-blocking advisory lock for one server instance (see D17) |
| `temporal/frontend.grpc` | One line `host:port`, mode `0600`, write temp file then rename after ready |

Parent directory mode `0700` when missing. SQLite pragmas as today unless
`TemporalDefaults`.

**D17 — One server per data directory and upgrade.** WAL SQLite **allows** a
second `Start` on the same file without an advisory lock — two servers would
corrupt history ownership; unlike SQL migrations, that is not a harmless race.
**Unix:** before opening the DB, take `flock(LOCK_EX|LOCK_NB)` on
`<DBFile>.lock`, hold until `Stop` (kernel releases on crash — no stale lock);
match `platform/sql/migrate_lock_unix.go` (`O_NOFOLLOW`, regular file).
**Non-Unix:** `embedded.Start` returns `ErrUnsupportedPlatform`; do not start
unlocked (`platform/sql/migrate_lock_other.go` runs migrations without a lock;
that pattern must not apply here). CI runs on Linux; developers also build on
macOS; nothing in the specs promises Windows. **Owner:** non-Unix embedded
Temporal support.

**Upgrade test (default A, ADR-0021):** golden database from the previous pin
plus a nested-module test that opens it under the current pin and completes a
workflow; refresh at every pin bump (`-update`, reason in commit body); size to
be measured in the implementation pull request. Alternative B: manual backup
only.

Schema: empty file → `SetupSchema` + namespace; existing file → server-managed
`schema_version` on open. Same **1.32** patch opens older DBs; newer
major.minor refuses at `Serve` version check.

**D18 — Ports.** The spike calls `freePort()` five times (frontend gRPC when
`Port` is 0, frontend HTTP, history, matching, worker). Each use of
`net.Listen("127.0.0.1:0")` returns a port and **closes the listener** before
the server binds. That is a loopback time-of-check race for **every** listener:
if a port is taken before bind, `Start` returns the bind error and `cmd/kit`
fails startup — no retry loop. Production uses ephemeral ports; tests may set a
fixed frontend gRPC port.

**D19 — Tuning and logging.** Partitions = 1, archival off, one history shard,
noop DC redirection; `Options.Logger *slog.Logger` is required (nil is an error;
bench may pass a noop logger). Create the database directory with mode **0700**
(not the spike's `0755`).
**Workflow-task timeout 2–3 s** (BENCHMARKS.md) is set on
`client.StartWorkflowOptions.WorkflowTaskTimeout` when starting workflows (as in
`embedded/bench/main.go`), not in embedded `dynConfig` and not a worker option.
A small additive field on `temporal.Options` belongs to whichever work package
first constructs `temporal.Runtime` inside `kit` (not WP-L5-03).

### Composition in `cmd/kit` (D20–D22)

Read `cmd/kit/serve.go` (`wire`, `wireEmbedded`, `startPorts`, `runServe`) and
`internal/config/serve.go` (`Port` is `interface{ Close() error }`;
`Server.Serve` drains HTTP, then closes `ports` in reverse).

**D20 — Embedded profile startup (WP-L5-03 only).** The embedded server is **not**
a `wirePort` (the ready line prints `name=implementation` for every `wirePort`;
REQ-CONFIG-020 keeps the text:
`ready profile=embedded runtime=temporal listen=… sql=… blob=… bus=… secrets=…`;
`wire` already returns `runtime=temporal` for both profiles). **`startPorts`**
gains a runtime step after the last platform port and before `net.Listen`: call
`embedded.Start` with `DBFile` = `data.dir/temporal/sqlite.db`, write
`temporal/frontend.grpc` (temp + rename), return a `config.Port` adapter whose
`Close` runs D22; append it to the `opened` list passed to `config.NewServer` so
`Server.Serve` closes it first on shutdown (reverse order). **Placement:** after
the four ports (a later `temporal.Runtime` needs blob and secrets for its data
converter; the server starts at the same hook so the step can grow from
server-only into the full runtime). **No** `temporal.Runtime`, **no** `Serve`,
**no** flow host in WP-L5-03. On `embedded.Start` failure: map D13 errors to
stderr and exit 1; `startPorts` closes opened ports in reverse (existing code).
On termination before ready: cancel `Start`'s `ctx`, `Start` stops a
half-started server, releases the lock, returns `ctx.Err()`, exit 0 as today.

**D21 — Cloud profile.** No in-process server; external address via a future
config key (out of WP-L5-03).

**D22 — Shutdown.** Order: HTTP drain (`config.Server.Serve`) → remove
`frontend.grpc` → `embedded.Server.Stop(ctx)` (releases the lock last) → other
ports close in reverse via `Server.Serve`. Treat `frontend.grpc` as a hint (stale
after crash until overwritten; a failed dial means stale). `Stop` uses
`shutdown.timeout` from the end of the HTTP drain; a late or failed `Stop` is
reported on stderr and does not change `Server.Serve`'s exit code; the kernel
releases the lock on process exit. **Future `kit run`:** try the lock — acquired
means start a server; refused means attach using `frontend.grpc` (not WP-L5-03).

### Proof and conformance (D23–D24)

**D23 — `RT-003-01`.** Register these four step phrases (scenario wording):

1. `Given a machine with no database, Temporal or NATS server running`
2. `When "kit serve --profile embedded" starts with an empty data directory`
3. `Then the server reports ready`
4. `And a run of conformance flow "linear" completes`

**Driver (today's specs):** (a) subprocess `kit serve --profile embedded` with an
empty `data.dir` and env that does not point at external SQL/Temporal/NATS
(that is all the Given step can assert; it does not scan the host); (b) wait for
the ready line; (c) read `temporal/frontend.grpc`; (d) build an in-process `runtime/temporal.Runtime` (PR #169 API) with
`Options.Client.HostPort` from the file, `Options.TaskQueue` =
`bdd-conformance-linear` (not `DefaultTaskQueue` `basamento-flow`, so no future
`kit` worker competes), plus opened blob and secrets stores for `New`; register a
**linear placeholder** flow host in `internal/bdd` that runs three steps named
`s1`–`s3`, each returning its numeric argument plus one through `DurableContext.Step`;
register matching step handlers; call `Runtime.Start` with JSON input `1` and
tenant in context; run `Serve` on a worker goroutine. Waits use blocking calls
only: the ready line is already a subprocess stdout read; completion waits on
the Temporal SDK long poll
`client.GetWorkflow(ctx, workflowID, "").Get(ctx, nil)` on a harness client
dialed to the same `HostPort` and namespace (a nil result pointer decodes
nothing, so the tenant payload converter is not needed) or on channels — no poll
loop on `Describe` (`go tool gate hygiene` forbids `time.Sleep`, `time.After`,
`time.Now` and friends in tests; every harness wait is blocking or channel-
driven, and the scenario context carries the overall deadline). After the wait,
assert once that `Runtime.Describe` reports state `completed`. Step handlers
record the steps they ran in order; the Then step asserts the recorded sequence
`s1`, `s2`, `s3` (no encrypted payload decode). Names follow PR #169:
`RegisterFlowHost`, `HostWorkflowName`, `Start(ctx, StartRequest)`. `RunRef`
carries `RunID` and `TenantID`; the SDK workflow id is `<tenant>/<runID>` when
the tenant is non-empty, else the bare `RunID` (PR #169 `workflowID` in
`ids.go`), which is what `GetWorkflow` needs. Lives in `internal/bdd` until
WP-L5-04 moves it to `runtime/conformance` with goldens. This proves the server
inside `kit` and a harness worker on the cluster — not a worker inside `kit`.

**Lane:** PR gate G3, not `@slow`, given measured link cost above; implementer
records G3 duration delta; if **>3 minutes** over the previous green run, move to
`@slow` + nightly (`redgreen` still runs the scenario by id). Owner decides.

**Red→green:** base without embedded wiring fails (no server in `kit serve`;
undefined steps count as fail on base).

**D24 — Goldens.** WP-L5-04 owns golden `linear` (`RT-001-01`); WP-L5-03 runs
the flow only.

### Settings reference (from measurements)

| Setting | Value | Why |
| --- | --- | --- |
| Task-queue partitions | 1 | One worker process; four partitions only add forwarding latency (fan-out 2–3× faster). |
| SQLite | `journal_mode=wal`, `synchronous=normal`, `busy_timeout=10000`, 4 connections | Double the fan-out throughput; survives process crashes. |
| Workflow-task timeout | 2–3 s | `StartWorkflowOptions.WorkflowTaskTimeout` when starting runs (see D19). |
| Module pins | server, API and SDK versions from the matching `temporalio/cli` release | Newer API modules do not compile against a given server release. |
| Go | ≥ 1.26.11 | Required by server v1.32. |

## 6. Temporal runtime: data path (WP-L5-02, pull request (a))

`runtime/temporal` registers the flow host as a Temporal workflow and each step
as an activity. This change delivers:

- A **payload data converter** shared by workflow and activity workers: tenant
  encryption (`platform/secrets`), blob claim-check over the threshold (default
  5 KB on encrypted size), markers use encoding `basamento/claim-check` and only
  history ever holds `binary/encrypted`. The converter may perform blob I/O inside
  workflow code; `New` wraps it with `workflow.DataConverterWithoutDeadlockDetection`
  so the SDK worker deadlock detector does not count that I/O (a slow store delays
  the workflow task subject to server task timeout; pull request (c) will document
  worker `DeadlockDetectionTimeout`).
- **Payload codec errors:** only the **workflow-bound** data converter (the clone
  from `WithWorkflowContext`) applies the transient policy: `unavailable`,
  `timeout`, `rate_limited`, `cancelled` on this path (bound expiry—the blob layer
  may map the deadline to `cancelled`—or store-reported cancellation; not a caller
  decision), or retryable `core.Error` values are logged (bounded message, code,
  tenant, workflow id) and panic as a sanitized `taskFailure`, so the **workflow
  task** fails and the server retries (the run is not failed). Permanent errors
  are returned. That covers step argument and result encode/decode, workflow input
  decode, workflow result encode, and in pull request (b) the continuation journal
  (`HostInput.Continuation` on ContinueAsNew), `Emit` activity arguments, gate
  Update payloads (`GateResolution`), and child workflow inputs and results
  (`HostInput` on `ExecuteChildWorkflow`). Client
  and activity converters (`WithContext`) return errors without panicking.
  Permanent step-argument encode failures are still surfaced to the flow as a failed
  `Step` future (`Step` recovers the SDK panic). Workflow I/O is bounded at 30 s
  per converter call so a hung store cannot block a worker thread; those
  `cancelled` cases and bound expiry are transient (workflow task failure, server
  retry, bounded by the run timeout), never a permanent flow error. Run-level
  cancellation (`REQ-RT-013`) is unrelated. Workers must keep the SDK default
  `WorkflowPanicPolicy` (`BlockWorkflow`); `FailWorkflow` would turn task failures
  into permanent run failures (pull request (c) pins worker options). User panics
  in the flow host still fail the run once with `temporal: the flow host panicked`;
  SDK non-determinism and illegal-state panics are out of scope here.
- A **host workflow** with `DurableContext` for `Context`, `Now`, `NewID`,
  `Rand`, `Step`, `Sleep`, `Select`, and `Capabilities` (pull request (a)).
- **Step activity** retries: `MaxAttempts` 0 → 3, listed `NonRetryable` codes and
  non-retryable `core.Error` values stop after one attempt; Temporal backoff is
  1 s initial, ×2, cap 100 s (the local runtime retries immediately). Codes and
  the `Retryable` flag cross the activity boundary on `Future.Get`.
- **Registration** (`RegisterFlowHost`, `RegisterStep`, `Register`,
  `DataConverter`) without control-plane `runtime.Runtime` (pull request (c)).

**Plain text in history:** Temporal stores failure messages and the tenant
header unencrypted; payload bytes go through the codec only. History carries only
a core error’s code and message (no Details, no chained cause text). Step authors
must not put secrets in `core.Error` messages; other failures become `internal`
with a fixed message and the original text goes to the logger only (bounded).

**Claim-check blobs** are content-addressed. A replayed workflow task or retried
activity may store new ciphertext under new keys; unreferenced blobs are not
reclaimed in this phase.

The BDD harness **decorates** `DataConverter` (records payloads, models the
client tenant context, injects RT-032/033 faults on activity results). Production
code has no test hooks. Real-server version pinning is pull request (c) and
WP-L5-03.

## 7. Temporal runtime: workflow features (WP-L5-02, pull request (b))

Pull request (b) adds **gates** (`Await`, Temporal Updates), **Emit** (outbox
activity), **Child** workflows, **Checkpoint** / ContinueAsNew, and the
**continuation journal** in `HostInput.Continuation`. After ContinueAsNew the
host workflow runs the flow function again from its first line; values from
earlier executions come from `HostInput.Continuation`, not from re-running side
effects.

**No Temporal patch markers.** Part (a) ships no way to start a run: its only
exported surface is `Register`, `HostWorkflowName` and `HostInput`; `Start`,
`Resolve`, `Cancel`, `Describe`, `List` and `Serve` belong to pull request (c)
and are not merged. No part-(a) run can be in flight in any deployment, so this
pull request adds no `workflow.GetVersion` markers. `HostInput` still decodes
both the `run_id` field and the `RunID` spelling, as cheap insurance for any
history recorded meanwhile. Revisit this ruling if pull request (c)'s start
path ships before this branch merges.

### Continuation journal

The journal holds only what flow code cannot recompute on replay. It grows with
the run (every finished step outcome stays in it) and serializes through the
shared data converter (encrypted per tenant, claim-checked over the threshold;
see section 6 for blob retention).

| JSON field | Meaning | Replay rule |
| --- | --- | --- |
| `steps` | Finished step outcomes | Key = step name; return journaled result or error |
| `children` | Finished child outcomes | Key = child run id |
| `gates` | `GateJournal` per gate name | Pending, resolved, timed out, or cancelled |
| `timers` | `Sleep(d>0)` calls | Match by call order; deadline and `fired` |
| `selects` | `Select` branch indices | Match by call order |
| `nows` | `Now()` samples | Match by call order |
| `emitted` | Count of stored Emit events | Next seq = `emitted + len(emit_failures) + 1` |
| `emit_failures` | Terminal Emit failures | Key = 1-based Emit call ordinal |
| `checkpoints` | Honored Checkpoint calls | Ordinal already continued returns nil |

`NewID` and `Rand` counters are **not** carried: they restart per execution and
reproduce the same values when the flow calls them in the same order
(`NewID(kind)` is `kind_N`, not run-scoped).

### Replay-stable flow code

Durable calls (`Step`, `Sleep`, `Select`, `Child`, `Await`, `Emit`, `Now`,
`Checkpoint`) must happen in the same order on every execution. Timers,
`Select`, and `Now` journal entries match by **call order**; steps by **name**;
children by **id**; gates by **name**. A flow that branches on `Future.Ready()`
across a `Checkpoint` can diverge (a replayed future is ready at once); use
`Select`.

**Step memoization:** step outcomes are keyed by name (same as `runtime/local`,
REQ-RT-007). Inside one execution, `Step` memoizes by name in `stepFutures`, so
a second call with the same name returns the first call's future regardless of
input. After ContinueAsNew, finished outcomes live in the journal field `steps`; a
second call with the same name returns the journaled result or error and does
not run the activity again.

### Checkpoint (REQ-RT-034, REQ-RT-035, REQ-RT-036, REQ-RT-038)

`Checkpoint` compares `GetCurrentHistoryLength()` to
`Options.CheckpointEventThreshold`, default **2000** (`DefaultCheckpointEventThreshold`);
negative disables ContinueAsNew. Below the threshold it returns nil and does
nothing. At or above it the runtime waits for in-flight steps and children
(including children with parent-close `abandon`) and for Update handlers to
finish without re-running them, snapshots the journal, and continues as new.
The deciding `Checkpoint` returns the ContinueAsNew error; **the flow must
return that error immediately** (the host workflow still continues as new if
the flow ignores it; blocking on a future created before the decision keeps
the run alive until that wait ends). After the decision, side-effecting durable
calls (`Step`, `Sleep`, `Select`, `Child`, `Await`, `Emit`, `Checkpoint`) fail
fast with code `cancelled` and message `temporal: the run is continuing as new`.
`Now`, `NewID` and `Rand` keep returning their deterministic values (journaled
time, per-execution counter, run-id seed) and schedule nothing; the host
continues as new regardless.
A Checkpoint whose ordinal was already honored in an earlier execution returns
nil on replay. `SnapshotRef` is unused. See the spec-silent table below for a
cancelled run at threshold.

### Gates (Temporal Updates)

Resolution is Update `basamento-resolve-gate` with payload `{gate, payload}`.
The validator rejects, in order: run continuing as new → `unavailable`
`temporal: the run is continuing as new; retry`; gate not in the journal →
`not_found` `temporal: no gate with that name on the run`; already resolved →
`conflict` `temporal: the gate was already resolved`; wait already ended by
timeout or cancel → `conflict` `temporal: the gate wait already ended`. Gates
are journal-driven: the journal entry is created when the flow first calls
`Await` (no create-on-miss). After ContinueAsNew, pending gates stay in the
journal; when replay reaches `Await`, the wait is re-armed from stored
deadline (absolute time, so remaining time carries over; a past deadline times
out at once). A resolve Update that passes validation before the flow calls
`Await` again still records the payload in the journal so the next `Await`
returns it at once (no create-on-miss in the handler; the validator requires a
journal entry). Run cancellation settles pending gates as
`cancelled` (`temporal: gate cancelled`) and journals that. `GateSpec.Schema`
and `AcceptIf` are not evaluated (FLOW-006-01, WP-L5-04).

### Emit

A synchronous outbox activity `basamento-emit` calls
`Outbox.Append(ctx, run, seq, event)` with the tenant in context and
`RunRef.TenantID`. The activity derives the run id from the scheduling
workflow id (`<tenant>/<run id>`, or a bare run id for a top-level run started
with one) and rejects an activity input whose run id differs, so another
workflow on the queue cannot emit for the run. `seq` is the 1-based Emit call ordinal
(`emitted + len(emit_failures) + 1`): a terminal failure consumes its seq, so a
later event never reuses one that a store-then-error sink may already hold.
Replay and activity retries are idempotent by `seq`. Retryable errors retry
without attempt limit (1 s initial, ×2, cap 1 min, 1 min
`StartToCloseTimeout` per attempt). A non-retryable error fails that `Emit` once,
is journaled in `emit_failures`, and is returned identically on replay after
ContinueAsNew (the activity does not run again). `New` requires `Outbox` at the
composition root; production ships no in-memory sink.

### Child

A child is another host workflow with Temporal workflow id `<tenant>/<child run
id>` on the same task queue. `ChildOptions.ID` empty → `NewID("child")`
(`child_N`). Parent-close policies map like the local runtime: `terminate`
(default), `request_cancel`, `abandon` → Temporal `PARENT_CLOSE_POLICY_*`.
Finished child results are journaled by child id; a call that reuses an id for
a different flow (name or hash) returns `conflict` and starts no child.
**Caveat:** default child ids
(`child_N`) are per-execution counters, not scoped to the parent run; two
concurrent parent runs in one tenant that both use default ids target the same
workflow id and the second start fails with the SDK's already-started error.
Callers running many parents concurrently should set `ChildOptions.ID`.

### Test environment limits

A child's own ContinueAsNew is not exercised in the SDK test environment (child
environments do not inherit parent history length); child ContinueAsNew rests on
the same code path as the parent and on review. Claim-check blobs are not
garbage-collected (section 6).

In SDK v1.47.0 the test environment hands the workflow-bound data converter to
the activity side. With `workflow.DataConverterWithoutDeadlockDetection` that can
deadlock the environment (lock inversion between the deadlock detector's pause and
a coroutine ticker's end), so test converters derive the activity-side converter
from the unbound one, as `clientConverter` and the BDD `recordingConverter` do.
Production workers are not affected.

### Capabilities

`Capabilities()` reports `durable`, `sync_gate_updates`, `continue_as_new`, and
`polyglot_queues`.

### Behaviours the specs leave open (Temporal, pull request (b))

| Behaviour | Spec coverage |
| --- | --- |
| `Checkpoint` on a cancelled run at threshold → `cancelled` `temporal: the run was cancelled`, no ContinueAsNew | spec-silent (`TestCheckpointOnCancelledRunReturnsCancelledAndDoesNotContinue`) |
| Non-retryable `Emit` failure journaled in `emit_failures` and replayed identically | spec-silent (`TestEmitFailureIsJournaledAndReplayedAsFailure`) |
| Default child id `child_N` not parent-scoped; concurrent parents can collide on workflow id | spec-silent (no unit test: Temporal workflow-id uniqueness; SDK test env runs one workflow) |
| Step keyed by name; second call with same name returns first outcome (`stepFutures` or journal `steps`) | spec-silent (`TestStepMemoizedByName`, `TestContinuationDoesNotExecuteFinishedStepsAgain`; same as local REQ-RT-007) |
| Flow should return ContinueAsNew error from `Checkpoint`; host continues as new even if the flow ignores it; `Checkpoint` at threshold waits for pre-decision in-flight work | spec-silent (`TestCheckpointContinuesWhenHistoryAtThreshold`, `TestCheckpointErrorIgnoredByFlowStillContinuesAsNew`, `TestCheckpointAtThresholdWaitsForInFlightStepAndDoesNotRunItAgain`) |

## 8. Local runtime: behaviours the specs leave open

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
| `Child` whose parent closes after the child run starts but before it is linked is never linked, never delivers its result, and is stopped (terminate) regardless of close policy; the returned future is `cancelled` | spec-silent (unit tests) |
| `List` cursor paging: opaque version-1 token (`base64.RawURLEncoding` of JSON `{"v":1,"tenant":"<id>","pos":<n>}`), tenant-bound, `pos` is that tenant's start-order ordinal; clients must not parse it (BDD steps craft one token in this form for RT-052-01); invalid, out-of-range or foreign-tenant cursor → `invalid_argument` before any run is read | RT-051…053, RT-026 lists runs |
| Every started run stays in memory (no retention) | RT-006 / design §Local |
| `New` requires a timer clock (`After`); failure message names timer scheduling | RT-054-01 |
| `Resolve` on a gate whose wait already ended (run cancelled, gate timeout, runtime shut down) → `conflict`, payload not delivered | spec-silent (unit tests) |
