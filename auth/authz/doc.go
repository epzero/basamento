// Package authz implements in-app RBAC for Basamento (WP-L8-02, specs/auth/design.md).
//
// A closed action catalog and three built-in roles (viewer, member, admin) feed a
// single Check decision. Callers supply Resource values; bindings live in SQL
// (authz_bindings) keyed by the context tenant. Grant and revoke require binding.grant
// or binding.revoke plus a superset of the target role's permissions.
//
// Grant validates identifiers, then grantor authority, then compiles conditions (at
// most 2048 bytes per condition), then writes (REQ-AUTH-014 ordering). Denials return permission denied with one authz.deny
// audit event; store failures on Check return unavailable without an audit record.
// When several bindings report condition errors, the audit reason is
// condition-cost-exceeded before condition-error before no-binding.
//
// Guard wires RPC procedures to catalog actions at startup: Authorize runs the
// resolver then Check. An undeclared procedure is rejected before the store or audit.
//
// Scope narrowing is a fail-closed seam until EP0-208 defines scope grammar.
package authz
