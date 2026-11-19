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
// (REQ-TELEM-010 through REQ-TELEM-014). The store creates its own
// `telemetry_spans` table idempotently on the platform/sql store it is given,
// so it needs no migration owned by another capability. In the cloud profile
// spans, log records and metric points are exported over OTLP to
// Config.CollectorURL without ever making the work wait, and an undeliverable
// batch is dropped without a retry (REQ-TELEM-015 through REQ-TELEM-019).
// Shutdown drains what was finished and then refuses new signals
// (REQ-TELEM-022, REQ-TELEM-023).
//
// The setup carries no package-level mutable state: pass one *Setup through
// dependency injection.
package telemetry
