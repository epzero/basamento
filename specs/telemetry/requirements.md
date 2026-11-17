# Telemetry: one setup for logs, traces and metrics

Status: **Planned** (WP-L1-07). Design: ADR-0004, `docs/architecture/overview.md` §2.
Terms: [glossary](../glossary.md) (telemetry setup, span, log record, trace context,
metric label, redaction, span store, simulated collector, export batch).

### REQ-TELEM-001 — One setup
The telemetry setup shall provide the logger, tracer and meter that record log records, spans and metric points.

### REQ-TELEM-003 — The basis of the clock
The telemetry setup shall take the timestamp of every log record, span and metric point from the clock it is given.

### REQ-TELEM-005 — Correlation in every log record
When a log record is written, the telemetry setup shall attach the `run_id` and `tenant` from the context, together with the active span's `trace_id` and `span_id` when a span is active and empty `trace_id` and `span_id` otherwise.

### REQ-TELEM-006 — Secrets are never recorded
The telemetry setup shall replace with `[REDACTED]` any credential value it is given and the value of any field named `password`, `secret`, `token` or `authorization` at any depth in every log record, span attribute and metric label, and leave other fields unchanged.

### REQ-TELEM-007 — Records are redacted before they are kept or exported
While a log record, span attribute or metric label is being recorded, the telemetry setup shall redact its values before they are kept or handed to an exporter.

### REQ-TELEM-010 — Spans are kept in the span store
Where the profile is `embedded`, when a span ends, the telemetry setup shall keep it in the span store.

### REQ-TELEM-011 — What a kept span holds
A span kept in the span store shall hold the trace id, span id and parent span id of its trace, its name, start time, end time, status, run id, tenant and attributes.

### REQ-TELEM-012 — A run's spans
Where the profile is `embedded`, when a run's spans are read back, the telemetry setup shall return every span of that run ordered by start time and, for spans that start at the same time, by span id.

### REQ-TELEM-013 — No span of another tenant
If a tenant asks for the spans of a run of another tenant, then the telemetry setup shall return no span and fail the read with code `not_found`.

### REQ-TELEM-014 — Retention
Where the profile is `embedded`, when the span store purges, the telemetry setup shall delete the spans that ended before the configured retention period.

### REQ-TELEM-015 — Export in the cloud profile
Where the profile is `cloud`, the telemetry setup shall export its spans, metric points and log records over OTLP to the collector URL it is given.

### REQ-TELEM-016 — Work never waits on the exporter
Where the profile is `cloud`, while the collector is slow or unreachable, the telemetry setup shall record and return every span, metric point and log record without waiting for the export.

### REQ-TELEM-017 — Undelivered batches are dropped
If an export batch cannot be delivered, then the telemetry setup shall drop that batch rather than retrying it without bound.

### REQ-TELEM-019 — Work never sees an export failure
If an export batch cannot be delivered, then the telemetry setup shall complete the recording of the span, log record or metric point without error.

### REQ-TELEM-021 — Trace context crosses a boundary
When a trace context is written to a carrier and read back, the telemetry setup shall preserve its trace id and span id so that a span started from the restored context joins the parent trace.

### REQ-TELEM-022 — Shutdown flushes
When the telemetry setup shuts down, the telemetry setup shall deliver every span, log record and metric that the work has finished recording before it returns.

### REQ-TELEM-023 — Shutdown stops telemetry
While the telemetry setup is shut down, the telemetry setup shall record no new span, log record or metric point.
