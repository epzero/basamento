// Package eventlog is the durable event log of a run (WP-L6-01).
//
// It writes the state change of a run and the durable events that change
// records in one SQL transaction (the outbox, REQ-STRM-010 … 012), numbers the
// events with the run's next sequence (REQ-STRM-008, REQ-STRM-009), and relays
// them to the bus at least once and in sequence order (REQ-STRM-013 … 019).
// Reads replay durable events after a cursor, scoped to the caller's tenant
// (REQ-STRM-020 … 022, 025).
//
// One subject per tenant and run, run.<hex(tenant)>.<run_id>.events, carries
// the published envelopes; the envelope holds the run id, sequence, type,
// schema version and data (REQ-STRM-024). Consumers order and deduplicate by
// the envelope's seq, never by the bus sequence (REQ-STRM-016). See ADR-0005
// and docs/architecture/streaming.md §2.
//
// The relay is a system component: it scans the outbox of every tenant and
// publishes each run on its own subject, so it takes no tenant from a caller
// and returns no row to one. Every caller-facing read and write takes its
// tenant from core.TenantFrom and fails closed without one.
//
// Layer: stream (core, platform and their sub-packages only).
package eventlog
