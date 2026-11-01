# Auth: identity, tenancy and permissions

Status: **Planned** (WP-L8-01, WP-L8-02; through the API in WP-L6-03 and WP-L9-01). Design: ADR-0008, `docs/architecture/auth.md`.

### REQ-AUTH-001 — No auth means loopback only
While the auth mode is `none`, the server shall refuse to listen on any non-loopback address.

### REQ-AUTH-002 — Tenant isolation
The system shall not return any record of another tenant to a principal.

### REQ-AUTH-003 — OIDC tokens
While the auth mode is `oidc`, the server shall accept only tokens signed by the configured issuer's keys, for the configured audience, and not expired.

### REQ-AUTH-004 — Permission checks
When a principal lacks the permission for an action, the system shall deny the action with code `permission_denied` and record an audit event.

### REQ-AUTH-005 — Scoped worker tokens
When the system issues a token to an external worker, the system shall limit it to one session's resources and a fixed expiry.
