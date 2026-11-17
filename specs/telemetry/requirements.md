# Telemetry: one setup for logs, traces and metrics

Status: **Planned** (WP-L1-07). Design: ADR-0004, `docs/architecture/overview.md` §2.
Terms: [glossary](../glossary.md) (telemetry setup, span, log record, trace context,
metric label, redaction, span store, simulated collector, export batch, dropped
telemetry, drain).

### REQ-TELEM-001 — One setup
The telemetry setup shall provide log records, spans and metrics from the same process.

### REQ-TELEM-002 — One telemetry setup per process
If a process that already has a telemetry setup asks for another one, then the telemetry port shall reject the request with code `conflict`.

### REQ-TELEM-003 — The basis of the clock
The telemetry setup shall take the time of every span and metric from the clock it is given.

### REQ-TELEM-004 — Identifier in every log record
Each log record shall carry an `event_id` attribute that is unique within the process.

### REQ-TELEM-005 — Correlation in every log record
Each log record shall carry `trace_id`, `span_id`, `run_id` and `tenant` attributes, with `trace_id` and `span_id` empty when no span is active.

### REQ-TELEM-006 — Values that are never recorded
The telemetry setup shall remove from every log record, span attribute and metric label the credential values it holds, the payload data of runs, and the values of fields named `password`, `secret`, `token` or `authorization` at any depth.

### REQ-TELEM-007 — Records are redacted before they are kept or exported
While a log record, span attribute or metric label is being recorded, the telemetry setup shall redact its values before they are kept or handed to an exporter.

### REQ-TELEM-008 — Redaction that leaves no secret behind
The telemetry setup shall record a value as the literal string `[REDACTED]` if, after redaction, the value shortened to 512 characters still contains any of the values REQ-TELEM-006 names, and shall otherwise keep the value as recorded.

### REQ-TELEM-009 — Redaction outside telemetry
If a value that reaches the redactor does not name a field to redact, then the redactor shall return it unchanged.

### REQ-TELEM-010 — Spans are kept in the span store
Where the profile is `embedded`, when a span ends, the telemetry setup shall keep it in the span store.

### REQ-TELEM-011 — What a kept span holds
A span kept in the span store shall hold the trace id, span id and parent span id of its trace, its name, start time, end time, status, run id, tenant and attributes.

### REQ-TELEM-012 — A run's spans
Where the profile is `embedded`, when a run's spans are read back, the telemetry setup shall return every span of that run ordered by start time and, for spans that start at the same time, by span id.

### REQ-TELEM-013 — No span of another tenant
If a tenant asks for the spans of a run of another tenant, then the telemetry setup shall return no span and fail the read with code `not_found`.

### REQ-TELEM-014 — Retention
Where the profile is `embedded`, when the span store purges, the telemetry setup shall delete the spans that ended more than 24 hours before the purge.

### REQ-TELEM-015 — Export in the cloud profile
Where the profile is `cloud`, the telemetry setup shall export its spans and metrics over OTLP to the collector URL it is given.

### REQ-TELEM-016 — Work never waits on the exporter
While a simulated collector is slow or unreachable, the telemetry setup shall record, end and return every span, metric and log record without waiting for the export.

### REQ-TELEM-017 — Counting what was dropped
If an export batch cannot be delivered, then the telemetry setup shall drop that batch, count its log records, spans and metric points as dropped telemetry, and make the counts readable by signal.

### REQ-TELEM-018 — A dropped span is not kept
If an export batch cannot be delivered under the `cloud` profile, then the telemetry setup shall not keep a span of that batch in the span store.

### REQ-TELEM-019 — Work never sees an export failure
If an export batch cannot be delivered, then the span, log record, metric or run that was being recorded shall complete with the same result as when export succeeds.

### REQ-TELEM-020 — No metric labels
An exported metric point shall carry only its name, its numeric value and its point attributes, and no metric label.

### REQ-TELEM-021 — Trace across a step boundary
When a step is recorded, the telemetry setup shall save its trace context so that the span created in the step, after a restart and in every attempt, has the trace id of that trace context.

### REQ-TELEM-022 — Shutdown flushes
When the telemetry setup shuts down, the telemetry setup shall deliver every span, log record and metric that the work has finished recording before it returns.

### REQ-TELEM-023 — Shutdown stops telemetry
After the telemetry setup shuts down, it shall accept no new span, log record or metric.
