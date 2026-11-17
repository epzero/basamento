# Streaming designs

Decisions the implementer must follow, on top of the requirements. Scope here:
the durable event log and relay (WP-L6-01, REQ-STRM-008 … 024); deltas,
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

One subject per event, `runs.<run_id>.<type>` (STRM-013-01 pins it).
The bus sequence (`Message.Seq`, `platform.Bus`) is not used for ordering:
consumers order by the envelope's `seq` (REQ-STRM-016, REQ-STRM-024). This
leaves any choice of bus subject layout to the implementer.

### Changes this work package lands first, in pull requests of their own

Both are outside `stream/eventlog` and are prerequisites; land them before the
log itself, each reviewed as its own change:

1. **SQL port.** `platform/platform.go` names an `SQL` port
   (`sqlite://…` ↔ `postgres://…`) that does not exist yet. It is owned by
   WP-L1-01; WP-L6-01 depends on it. The writer needs one transaction handle
   it can carry across the state change and the outbox insert — a `Tx` it
   opens itself, or a callback the store runs inside its own transaction. The
   port must say which; until it does, neither REQ-STRM-010 nor REQ-STRM-011
   is implementable as written.
2. **Tenant context.** The constitution (article 8) requires every repository
   call to carry a tenant context, but `core` has no tenant context or helper.
   `Read` takes its tenant from the context and never from an argument, so a
   caller cannot name another tenant (REQ-STRM-022). This belongs in `core`
   (a small `Tenant` on `context.Context` plus its accessor), a change in
   `core`, not in `stream/eventlog`; the implementer lands it first and cites
   this design in that pull request.

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
