# Telemetry: design notes for the implementer

Optional. The requirements and scenarios are the contract; this file only
records the interface and decisions the scenarios need, so the steps and the
code agree. Where this file and a requirement disagree, the requirement wins.

## Package

`platform/telemetry`, owned by WP-L1-07, falls under the `platform: [core]`
layer in `layering.yaml` and may import `core` and `platform` packages only.
It is the `slog` and OpenTelemetry SDK wiring constructed and passed to
consumers via dependency injection (no package-level mutable state;
`docs/engineering/coding-guidelines.md` §2, REQ-TELEM-001).

```go
type Config struct {
    Profile         string        // "embedded" or "cloud"
    CollectorURL    string        // OTLP endpoint; cloud only
    Clock           core.Clock    // the only source of time (REQ-TELEM-003)
    RetentionPeriod time.Duration // embedded span store retention (REQ-TELEM-014)
    Secrets         SecretsSource // credential values to redact
    Spans           SpanStore     // SQLite span store in the embedded profile (NewSQLSpanStore)
}

type Telemetry interface {
    Logger() *slog.Logger
    Tracer() trace.Tracer
    Meter() metric.Meter
    Inject(ctx context.Context, carrier propagation.TextMapCarrier)
    Extract(ctx context.Context, carrier propagation.TextMapCarrier) context.Context
    Flush(ctx context.Context) error
    Shutdown(ctx context.Context) error // returns once drained (REQ-TELEM-022)
}
```

- Every provider and handler reads timestamps from `Config.Clock`
  (REQ-TELEM-003); nothing calls `time.Now`, and no span, log record or metric
  sleeps. A scenario that says "without the clock advancing" checks that the
  call returns while the fake clock still reads the same instant.

## Profiles

| | `embedded` | `cloud` |
| --- | --- | --- |
| Span processor | writes ended spans to the span store (`REQ-TELEM-010`, `REQ-TELEM-011`) | batch exporter to `CollectorURL` (`REQ-TELEM-015`) |
| Metrics & logs | held in-process / `slog` handler | OTLP batch export to `CollectorURL` (`REQ-TELEM-015`) |
| Purge | deletes rows whose end time is older than the effective retention period: `Config.RetentionPeriod`, or 24 hours when it is zero (`REQ-TELEM-014`) | none |

The effective retention period is the setup's: `New` calls `SetRetention` on a
store that implements `RetentionApplier` (the shipped `SQLSpanStore` does), so
`Config.RetentionPeriod` wins over the value the store was built with. A store
that does not implement it owns its own window.

## Redaction

Before any log record, span attribute or metric label is kept or handed to an
exporter (`REQ-TELEM-006`, `REQ-TELEM-007`):

1. A field whose key is `password`, `secret`, `token` or `authorization`
   (case-insensitive exact key match, at any depth in nested maps/slices/groups)
   has its value replaced with `[REDACTED]`. Keys that merely contain one of
   those words as a substring (such as `token_count`) are not matched
   (`TELEM-006-04`).
2. Any string value containing a credential value supplied by `Config.Secrets`
   is replaced with `[REDACTED]` (`TELEM-006-01`).
3. Other fields and values are kept unchanged (`TELEM-006-04`).

## Correlation

The `slog.Handler` attaches `run_id` and `tenant` from the `context.Context`,
and `trace_id` and `span_id` from the active span in context (or empty strings
when no span is active) on every log record (`REQ-TELEM-005`).

## Export and Propagation

In the `cloud` profile, OTLP exporters send batches to `CollectorURL` with
bounded queues and without blocking the caller (`REQ-TELEM-015`,
`REQ-TELEM-016`). An export batch that cannot be delivered is dropped
without retrying it, and the observed span, log record or metric point
completes without error (`REQ-TELEM-017`, `REQ-TELEM-019`).

- **Bounded queues.** Each per-type queue holds at most
  `exportQueueCap` (4096) signals. Past that bound the oldest pending signal of
  that type is dropped, `Setup.DroppedExports` counts it, and recording still
  returns without error. This is the design's bounded-queue decision; where
  `REQ-TELEM-016`'s "record and return every signal" meets a stalled collector,
  the bound wins and the drop is observable rather than memory growing without
  limit.
- **Delivery triggers.** The background exporter wakes when 512 signals are
  pending, on `Flush`, `ExportOnce` and `Shutdown`, and on the first enqueue
  after the oldest pending signal has waited `exportInterval` (one second) on
  `Config.Clock`. The age check runs on enqueue and reads the injected clock,
  never the wall clock: a test advances the fake clock, and a long-lived
  process that records small volumes exports its backlog with its next signal,
  or at shutdown when no further signal arrives.
- **Shutdown.** `Shutdown(ctx)` stops new spans, log records and metric points
  immediately, but a span started before the call and ended while it runs is
  still recorded and delivered (`REQ-TELEM-022`). It repeats the drain until
  nothing is in flight and no signal is pending. The context bounds each wait
  and each request: when it expires, `Shutdown` refuses later recordings,
  drops the remainder and returns `ctx.Err()`; an in-flight request is
  cancelled and its goroutine exits on its own (`REQ-TELEM-017` permits the
  drop).

`Inject` and `Extract` serialize and restore W3C TraceContext across process or
worker lease boundaries (`REQ-TELEM-021`, ADR-0012 §11).

## Gate and Ownership Notes

- `platform/telemetry` falls under `platform: [core]` in `layering.yaml`. If
  `platform/telemetry` ever needs its own layer entry in `layering.yaml`, that
  change must land first in its own pull request with an ADR
  (`docs/engineering/agent-workflow.md` §4).
- The span table belongs to `platform/telemetry`, not to `platform/sql`.
  `NewSQLSpanStore` creates the table `telemetry_spans` if it is absent, on
  the `platform/sql` store it is given; WP-L1-01 ships no span table. The span
  store is embedded only, so `NewSQLSpanStore` accepts a SQLite store and
  refuses any other dialect with `invalid_argument`.
- **What owning the table outside the migration set gives up.** The table is
  not in the product's versioned migration set, so the schema version of the
  SQL store does not cover it, and REQ-SQL-005, REQ-SQL-008 and REQ-SQL-009
  give it no guarantee. Create-if-absent never changes an existing table, so
  the table has one shape. A change to that shape is a spec change of its own,
  and it must say how spans still inside the retention period are kept, since
  REQ-TELEM-012 returns them and REQ-TELEM-014 deletes only older ones.
- **Default retention.** `New` applies the effective period to the span
  store: when `Config.RetentionPeriod` is zero it keeps spans for 24 hours
  (`DefaultRetentionPeriod`, REQ-TELEM-014), and a negative period is refused
  with `invalid_argument` by both `New` and the store constructor. Spans serve
  debugging of
  recent runs; a day covers that without letting a local database grow
  without bound. `specs/config` has no retention key; adding one is a change
  to that spec.
- The statements about `NewSQLSpanStore` describe the work package's
  implementation before it is reviewed; the implementer and the code reviewer
  check them against the code, including the SQLite DDL.
- The simulated OTLP collector and test harness helpers live in `internal/bdd`
  (and `_test.go` files), never in `platform/telemetry`.
