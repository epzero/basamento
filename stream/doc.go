// Package stream is the live layer of a run: clients attach, detach and
// reattach with a cursor while the run keeps going (WP-L6-02).
//
// Durable events come from stream/eventlog, numbered per run by seq; attaching
// replays those after the cursor from SQL and then follows the run live. Deltas
// are ephemeral and numbered per message by dseq; a reattaching client gets a
// snapshot of every message still in flight built from the deltas seen so far,
// then the deltas after it, so there is no gap and no duplicate
// (REQ-STRM-001 … 003, 007). A retried attempt starts with a reset
// (REQ-STRM-004). A producer heartbeats, which is how cancellation reaches it
// within one heartbeat interval, and the partial message is recorded as
// interrupted (REQ-STRM-005). A declared `on_detach: cancel_after(<d>)` stops a
// run that no client has attached for that long (REQ-STRM-006).
//
// Every call takes its tenant from core.TenantFrom and never from an argument,
// so a caller cannot read or stop another tenant's run (constitution article 8,
// REQ-STRM-022, REQ-STRM-025). Attachments are served in process; deltas are
// also published on the run's bus subject run.<hex(tenant)>.<run_id>.deltas,
// which is the
// retention the cloud profile shares between servers (ADR-0005).
//
// Layer: stream (core, platform and their sub-packages only).
package stream
