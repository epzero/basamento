// Package telemetry is the telemetry setup of the platform: one constructor
// wires the slog logger, the OpenTelemetry tracer and the OpenTelemetry meter
// used by every layer (REQ-TELEM-001, ADR-0004).
//
// The setup reads time only from Config.Clock (REQ-TELEM-003), attaches the
// run, tenant and active span to every log record (REQ-TELEM-005), and redacts
// credential values and the fields named password, secret, token or
// authorization before a signal is kept or exported (REQ-TELEM-006,
// REQ-TELEM-007).
//
// In the embedded profile ended spans are kept in a SQL span store
// (REQ-TELEM-010 through REQ-TELEM-014). The span table belongs to this
// package: NewSQLSpanStore creates `telemetry_spans` and its run index if they
// are absent on the SQLite store it is given. The table has one shape and is
// not part of the platform/sql migration set. In the cloud profile spans, log
// records and metric points are exported over OTLP to Config.CollectorURL
// without ever making the work wait, and an undeliverable batch is dropped
// without a retry (REQ-TELEM-015 through REQ-TELEM-019). Shutdown stops the
// owned exporter, drains what was accepted and only then returns
// (REQ-TELEM-022, REQ-TELEM-023).
//
// A setup carries no mutable package-level state: pass one *Setup through
// dependency injection.
package telemetry
