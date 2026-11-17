# Telemetry: design notes for the implementer

Optional. The requirements and scenarios are the contract; this file only
records the interface and decisions the scenarios need, so the steps and the
code agree. Where this file and a requirement disagree, the requirement wins.

## Package

`platform/telemetry`, owned by WP-L1-07, a layer `platform/telemetry` that may
import `core` and `platform` only. It is the OpenTelemetry SDK wiring; every
log record, span and metric of the process comes from it (REQ-TELEM-001).

```go
type Config struct {
    Profile      string        // "embedded" or "cloud"
    CollectorURL string        // OTLP/gRPC endpoint; cloud only
    Clock        core.Clock    // the only source of time (REQ-TELEM-003)
    Secrets      SecretsSource // credential values to redact, by tenant
    Runs         SpanReader    // the SQL table the span store lives in (WP-L1-01)
    IDs          core.Ids      // event_id (REQ-TELEM-004)
}

type Telemetry interface {
    Logger() *slog.Logger
    Tracer() trace.Tracer
    Meter() metric.Meter
    Redactor() Redactor
    Flush(ctx context.Context) error
    Shutdown(ctx context.Context) error // returns once drained (REQ-TELEM-022)
    DropCounts() Dropped                // by signal (REQ-TELEM-017)
}
```

- One setup per process: the construction function returns `conflict` from
  `core` when one already exists (REQ-TELEM-002).
- Every OTel provider is built with a `WithClock`-style option that reads
  `Config.Clock`; nothing calls `time.Now`, and no span or metric sleeps. A
  scenario that says "without the clock advancing" checks that the call
  returns while the fake clock still reads the same instant.

## Profiles

| | `embedded` | `cloud` |
| --- | --- | --- |
| Span processor | one that writes kept spans to the SQL store when the span ends | batch exporter, no span store |
| Span exporter | none, unless a `CollectorURL` is given | OTLP/gRPC to `CollectorURL` |
| Span store rows | `trace_id, span_id, parent_span_id, name, start, end, status, run_id, tenant, attributes` (REQ-TELEM-011) | none (REQ-TELEM-018) |
| Purge | `DELETE` on rows whose end is older than 24 h, run by a clock-driven ticker so the fake clock triggers it | none |

A scenario tagged `@profile:embedded` in a `@profile:cloud` feature sets up
the embedded profile with an unreachable collector; that is the only way the
console batch exporter's failure path is observable today. The implementer
lands that exporter for both profiles in this work package; no other package
changes.

## Redaction

`Redactor.Scrub(field string, v any) any` and `Redactor.String(s string) string`.
The scrubber walks strings, arrays (`[]any`) and objects (`map[string]any`):

1. A field whose key is `password`, `secret`, `token` or `authorization`
   (case-insensitive, at any depth) becomes `[REDACTED]`; `token_count` does
   not match (REQ-TELEM-006, TELEM-006-04).
2. A string that contains a credential value of the tenant in context or any
   whole-word occurrence (case-insensitive) of payload text or input text of
   a run of that tenant becomes `[REDACTED]` (REQ-TELEM-006, TELEM-006-02).
   Numbers are compared as their decimal text; a number is never payload data.
3. A string longer than 512 characters: the first 512 characters are scanned
   again for any of the values above; if one is found the whole value becomes
   `[REDACTED]`, otherwise it is kept as recorded (REQ-TELEM-008).
4. Anything else is returned unchanged (REQ-TELEM-009).

The log handler, the span processor and the metric view call the redactor
**before** storing or exporting (REQ-TELEM-007). `[REDACTED]` is replaced,
not trimmed.

## Correlation

A log handler adds, on every record: `event_id` (a `core.Ids` id, kind
`evt`), and `trace_id`, `span_id` from the active span (empty when none),
`run_id` and `tenant` from the context the run set (REQ-TELEM-004,
REQ-TELEM-005).

## Export

OTLP/gRPC over plaintext to the collector URL, one exporter per signal
(traces, logs, metrics). Batches are exported with bounded concurrency and a
bounded queue; an attempt that fails drops its batch, adds its size per
signal to the drop counts, and records the counts as metric points. Host work
never blocks on the exporter and never sees its error (REQ-TELEM-016,
REQ-TELEM-017, REQ-TELEM-019). Metric points carry no labels (REQ-TELEM-020).

## Steps

A step's trace context is saved with the step's durable record and restored
into the step's `context.Context` before every attempt, after restarts too
(REQ-TELEM-021; the only durable record exists today on the temporal runtime,
so the scenario is `@runtime:any` and both runtimes must satisfy it).

## Gate helpers

The SQLite store for the span table comes from WP-L1-01. The
simulated collector and the in-process setup helpers live in `internal/bdd`
(and `_test.go` files), never in `platform/telemetry`. The OTLP receiver the
simulated collector stands on is new test support inside `internal/bdd`;
`platform/telemetry` itself only gains the `opentelemetry-go` exporters,
which need a dependency reason in the commit body. `gate layering` does not
know `platform/telemetry` yet: adding the layer line needs an ADR owned by
the architect, and `platform/telemetry` imports nothing outside `core` and
`platform` until that ADR lands, so the change passes `gate layering` as the
graph stands.
