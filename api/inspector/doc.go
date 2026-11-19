// Package inspector serves the run inspector: the runs a principal may see,
// one run's durable events as they arrive, the run's open gates and the replay
// of its rrweb recording (specs/inspector).
//
// The inspector shows; it does not decide. Authorization, tenant isolation and
// a gate's payload validation stay behind Source, which is the tenant-scoped
// API of WP-L6-03 (REQ-INSP-002, REQ-INSP-006, REQ-INSP-007). The handler adds
// one rule of its own that it never delegates: a run whose Tenant is not the
// principal's tenant is never rendered, even if a Source returns it.
//
// The UI is server-rendered HTML with embedded CSS and JavaScript, so the
// single binary of ADR-0001 needs no separate asset server. The page carries
// the same data as a JSON island (script type="application/json"), which the
// embedded script hydrates and which the journey driver asserts on.
package inspector
