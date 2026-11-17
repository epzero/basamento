# Streaming designs

Decisions the implementer must follow, on top of the requirements. Scope here:
the durable event log and relay (WP-L6-01, REQ-STRM-008 … 022, 024, 025); deltas,
snapshots and reattach are REQ-STRM-001 … 007 and `docs/architecture/streaming.md` §2–3.

## Durable event log and relay (WP-L6-01)

### Location

`stream/eventlog`, which may import only `core` and `platform` (`layering.yaml`).
The relay runs in-process in every profile: it starts with the composition root,
not as a separate service.

### Two pieces

- **Writer.** Takes the state change and the events it records
  (`AppendChange(ctx, ...)`), numbers them with the run's next `seq`
  (REQ-STRM-008, REQ-STRM-009), and commits both in one SQL transaction
  (REQ-STRM-010 … 012).
- **Relay.** Reads unrelayed outbox entries per run in `seq` order, publishes
  each with `platform.Bus.Publish` (the `Bus` port, from WP-L1-03; the relay is
  written against the interface, so it compiles and is tested with a failing
  substitute bus before there is a real one), and marks it relayed only after the bus has
  accepted it (REQ-STRM-013, REQ-STRM-014). A failure leaves the entry
  unrelayed (REQ-STRM-015 … 019). It is a loop driven by the `core.Clock` and a
  fixed retry step; tests inject bus failures through a failing `Bus` and
  advance the fake clock, never by timing (REQ-STRM-018, REQ-STRM-019).

### Subject

One subject per run, `run.<run_id>.events`, matching `specs/bus` (where the
bus delivers exact subjects without wildcards and orders messages per
subject) and the two-stream layout in `docs/architecture/streaming.md` §2;
the event type travels in the envelope's `type` field (REQ-STRM-024). The
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
   the context and never from an argument, so a caller cannot omit the
   tenant or name another tenant (REQ-STRM-022, REQ-STRM-025). The shared
   `Tenant` context key and accessor belong in `core`, not in
   `stream/eventlog`; if not yet present in `core`, the implementer lands
   them first in their own pull request and cites this design.

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

## Open questions

1. **Roadmap dependency (`docs/roadmap/L6-streaming-and-api.md` line 6):** `WP-L6-01` currently lists `Depends: WP-L1-01, WP-L5-01.` and omits `WP-L1-03` (Bus and KV), whereas `WP-L6-02` includes `WP-L1-03`. Should a separate roadmap/design change add `WP-L1-03` to `WP-L6-01`'s `Depends:` line, or should `WP-L6-01` continue to build against `platform.Bus` and its test double until `WP-L1-03` lands?
2. **Shared SQL transaction and tenant-context helpers (`WP-L1-01` vs `WP-L6-01`):** `specs/sql` (`WP-L1-01`) specifies all-or-nothing transactions (`REQ-SQL-013`) and tenant-from-context scoping (`REQ-SQL-010` .. `012`), while `specs/streaming/design.md` notes that the implementer lands the `core` tenant-context accessor and any missing cross-repository transaction entry point in small prerequisite pull requests if `WP-L1-01` does not export them first.
