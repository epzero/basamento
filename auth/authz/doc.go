// Package authz implements in-app RBAC for Basamento (WP-L8-02, specs/auth/design.md).
//
// A closed action catalog and three built-in roles (viewer, member, admin) feed a
// single Check decision. Callers supply Resource values, including an optional
// Session id set only by the per-RPC resolver; Session is not audited, stored, or
// passed to CEL. Bindings live in SQL (authz_bindings) keyed by the context tenant.
// Grant and revoke require binding.grant or binding.revoke plus a superset of the
// target role's permissions.
//
// Grant validates identifiers, then grantor authority, then compiles conditions (at
// most 2048 bytes per condition), then writes (REQ-AUTH-014 ordering). Denials return
// permission denied with one authz.deny audit event; store failures on Check return
// unavailable without an audit record. When several bindings report condition errors,
// the audit reason is condition-cost-exceeded before condition-error before no-binding.
//
// Scopes narrow only through recognized session scopes (`session:` plus a non-empty id,
// byte-exact). Unrecognized scope lists fail closed. Grant, Revoke and List use the
// same check with an empty resource session, so session-scoped principals cannot
// administer bindings.
//
// Guard wires RPC procedures to catalog actions at startup: Authorize runs the
// resolver then Check. Resolvers return the Resource for the request; they must not
// turn a missing or foreign resource into a distinguishable error before the check
// (handlers report not_found only after Check allows the action). A resolver's
// *core.Error is returned unchanged; other errors become invalid_argument.
// An undeclared procedure is rejected before the store or audit.
package authz
