# Streaming designs

Decisions the implementer must follow, on top of the requirements. Scope: the
durable event log and relay (WP-L6-01, REQ-STRM-008 … 022, 024, 025) and live
streaming with reattach (WP-L6-02, REQ-STRM-001 … 007); deltas, snapshots and
reattach are `docs/architecture/streaming.md` §2–3.

## Durable event log and relay (WP-L6-01)

### Location

`stream/eventlog`, which may import only `core` and `platform` (`layering.yaml`).
The relay runs in-process in every profile: it starts with the composition root,
not as a separate service.

### Dependencies

WP-L6-01 depends on WP-L1-01 (SQL store), WP-L1-03 (Bus and KV) and WP-L5-01
(runtime). The relay publishes through the
`platform.Bus` port from WP-L1-03.

### Two pieces

- **Writer.** Takes the state change and the events it records
  (`AppendChange(ctx, ...)`), numbers them with the run's next `seq`
  (REQ-STRM-008, REQ-STRM-009), and commits both in one SQL transaction
  (REQ-STRM-010 … 012).
- **Relay.** Reads unrelayed outbox entries per run in `seq` order, publishes
  each with `platform.Bus.Publish` (WP-L6-01 depends on WP-L1-03 for the
  `platform.Bus` port), and marks it relayed only
  after the bus has accepted it (REQ-STRM-013, REQ-STRM-014). A failure leaves
  the entry unrelayed (REQ-STRM-015 … 019). It is a loop driven by the
  `core.Clock` and a fixed retry step; tests inject bus failures through a
  fake `platform.Bus` in `internal/bdd` and advance the fake clock, never by
  timing (REQ-STRM-018, REQ-STRM-019).

### Subject

One subject per tenant and run, `run.<hex(tenant)>.<run_id>.events`, where
`<hex(tenant)>` is the hexadecimal encoding of the tenant id's bytes (tenant
"acme" → `61636d65`, so run "r1" of "acme" → `run.61636d65.r1.events`). The
tenant token is needed because a run id is unique only within a tenant
(REQ-RT-046): it keeps same-named runs of different tenants (e.g. acme/r1
and globex/r1) on different subjects. This still matches `specs/bus` (exact
subjects, per-subject ordering) and the two-stream layout in
`docs/architecture/streaming.md` §2; the event type stays in the envelope's
`type` field (REQ-STRM-024). Because the subject is an internal relay↔bus
detail and not caller-observable, tenant isolation is pinned at unit level:
`stream/eventlog.RunSubject(tenant, runID)` builds the subject, and the relay
unit test asserts that acme/r1 and globex/r1 publish to different subjects
(implementation on EP0-32-79ed63, commit 2395464). The
bus sequence (`Message.Seq`, `platform.Bus`) is not used for durable-event
ordering or deduplication: consumers order and deduplicate by the
envelope's `seq` (REQ-STRM-016, REQ-STRM-024).

### Changes this work package lands first, in pull requests of their own

Both are outside `stream/eventlog` and are prerequisites; if WP-L1-01 has
not already landed them, the implementer lands them first in pull requests
of their own before the log itself:

1. **SQL transaction handle.** WP-L1-01 (`specs/sql`, `platform/sql`, on
   which WP-L6-01 depends) specifies the SQL store and all-or-nothing
   transactions (REQ-SQL-013, `specs/sql/design.md` §1). The writer needs
   one transaction handle or callback (`InTx`) it can carry across the
   state change and the outbox insert (REQ-STRM-010 … 012); if `platform` /
   `platform/sql` does not yet expose that entry point, the implementer
   lands it first in its own pull request.
2. **Tenant context.** The constitution (article 8) and `specs/sql`
   (REQ-SQL-010 … 012, `specs/sql/design.md` §4) require every repository
   call to read its tenant from `context.Context` and fail with
   `invalid_argument` when none is present. `Read` takes its tenant from
   `core.TenantFrom(ctx)` and never from an argument, so a caller cannot omit
   the tenant or name another tenant (REQ-STRM-022, REQ-STRM-025). `core`
   provides `core.TenantID`, `core.WithTenant(ctx, id)` and
   `core.TenantFrom(ctx) (core.TenantID, bool)`;
   an empty or otherwise invalid id counts as no tenant (REQ-CORE-005,
   REQ-CORE-006). The helper
   lands first, in its own contract pull request (`feat(core)`), before this
   work package.

### Data

- `events`: `tenant_id`, `run_id`, `seq`, `type`, `v`, `data`, `created_at`;
  primary key (`tenant_id`, `run_id`, `seq`).
- `runs`: the run's state, in the same row set the writer updates; the next
  `seq` is `max(events.seq) + 1` for the run, read under the same transaction,
  so two writers serialise on the run (REQ-STRM-009).
- `outbox`: one row per event, `relayed_at` set on marking (REQ-STRM-014).

### Fault injection in the gate

The scenarios stop the writer and the relay at named points (REQ-STRM-010,
REQ-STRM-015). The test-only store wrapper and bus in `_test.go` /
`internal/bdd` call a hook when the point is reached and return an error or
drop the process, so every point is hit deterministically. There is no
timing in the gate.

## Live streaming and reattach (WP-L6-02)

### Location

`stream`, which may import only `core` and `platform` (`layering.yaml`). It
uses `stream/eventlog` for durable events and `platform.Bus` for the deltas'
retention and cross-server publication.

### Attach

`Broker.Attach(ctx, runID, cursor)` replays `eventlog.Read` after `cursor.Seq`
under the run's lock and registers the attachment before releasing it, so no
durable event can fall between the replay and the live stream (REQ-STRM-002,
REQ-STRM-007). It then sends one `Snapshot` per message still in flight, with
the text received so far and the last `dseq`, so the live deltas start at
`dseq + 1` (REQ-STRM-003). A cursor past the end replays nothing
(REQ-STRM-021) and the tenant always comes from `core.TenantFrom`
(REQ-STRM-022, REQ-STRM-025).

### Producers

`Publish` numbers the delta per message, keeps its text for snapshots and
publishes it on `run.<run_id>.deltas`. `Reset` clears the partial text, takes
the message's next `dseq` and reaches the client before the new attempt's
deltas (REQ-STRM-004). `CompleteMessage` writes `message.completed` with the
final text and `dseq`, so a reattach after completion still sees the text once
(REQ-STRM-002).

### Cancellation and detach

`Cancel` closes the run's cancellation channel, records every in-flight message
as interrupted with the text received so far and moves the run to `cancelled`
(REQ-STRM-005). A producer learns of it on `Heartbeat`, which is why a step
stops within one heartbeat interval; publishing after it fails with `cancelled`.
`EnforceDetachPolicies` runs on the `core.Clock`: when the last attachment
detaches, it starts `on_detach`'s duration and cancels the run once the clock
passes it, unless a client reattaches first (REQ-STRM-006).

### Fault injection in the gate

The scenarios move the fake clock and drive the broker through the scripted
model and the `platform.Bus` fake in `internal/bdd`; no test sleeps or reads
the wall clock.

## Open questions

1. **Shared SQL transaction handle (`WP-L1-01` vs `WP-L6-01`):** `specs/sql`
   (`WP-L1-01`) specifies all-or-nothing transactions (`REQ-SQL-013`), while
   this design notes that the implementer lands any missing cross-repository
   transaction entry point in a small prerequisite pull request if `WP-L1-01`
   does not export it first.
