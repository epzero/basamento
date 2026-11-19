// Package credentials is the credential vault: named secrets per tenant,
// envelope-encrypted in SQL through the Secrets port and resolved inside a
// step at the moment of use. It implements platform.Credentials and, through
// AuditStore, the SQL implementation of platform.Audit (WP-L1-06).
//
// A value is written or rotated through Put, deleted through Delete, and never
// read back except as the return of Resolve. Callers hold only a reference,
// "cred://<name>"; Names exposes names, never values. The tenant comes from
// core.TenantFrom(ctx) for every operation, and the (tenant, name) row key is
// the isolation boundary.
//
// Resolve hands one platform.AuditEvent to the Audit port for every
// well-formed reference resolved with a tenant in context, successful or not.
// It fails closed with code unavailable when that event cannot be written and
// returns no value. The event, errors and logs never carry the value or the
// ciphertext (REQ-CRED-019, REQ-CRED-028).
//
// The SQL backend and its migrations live in this package; the composition
// root includes Migrations in the SQL store's migration set and wires
// NewSQLBackend and NewSQLAudit (ADR-0004). An external secret manager
// implements Backend and sees only ciphertext.
package credentials
