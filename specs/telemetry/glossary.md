# Telemetry terms

Added to [`specs/glossary.md`](../glossary.md) by this capability. See also the
glossary's run, step, tenant, credential, profile and SQL store terms.

| Term | Meaning |
| --- | --- |
| **Telemetry setup** | The one value REQ-TELEM-001 provides per process: the `slog` log records, the OpenTelemetry traces and metrics, the redaction and the exporter, built from a profile, a collector URL, a clock and the values it must redact. Implemented by `platform/telemetry`, so logs, spans and metrics share it rather than three global handlers. |
| **Telemetry port** | The seam that returns a telemetry setup for a configuration. A process has at most one. |
| **Span** | One timed unit of work: the run span around a run, a step span around each attempt of a step, and span attributes a parent span may add. It has a trace id, a span id, an optional parent span id, a run id, a tenant, attributes and a status (`ok`, `error`, `unset`). |
| **Trace context** | A trace id and a span id, carried in a `context.Context`: from the span that starts a step, to the step, across a restart and into every attempt. |
| **Log record** | One `slog` record with structured attributes. |
| **Metric label** | Any key/value pair attached to a metric besides the metric's name. |
| **Redaction** | Replacing a value by the literal `[REDACTED]` when it holds a credential value, run payload data or the value of a field named `password`, `secret`, `token` or `authorization`. It runs on strings, arrays and objects; a number is compared as its decimal text. |
| **Span store** | The SQL store table of kept spans in the embedded profile: written by the telemetry setup, read by a run's reader ordered by start time then span id, and purged 24 hours after a span ends. |
| **Simulated collector** | A deterministic OTLP gRPC receiver used by the gate in place of a real collector. It records every batch it receives and can be told to be slow or unreachable. |
| **Export batch** | The spans or metrics one export attempt sends. If it cannot be delivered, it is dropped rather than retried forever. |
| **Dropped telemetry** | The log records, spans and metric points of an export batch that could not be delivered, counted per signal and readable as the metric points `telemetry.dropped.log_records`, `telemetry.dropped.spans` and `telemetry.dropped.metric_points`. |
| **Flush, drain** | Flushing sends every recorded span and metric now. Draining waits until an in-flight export batch ends. Shutting down does both. |
