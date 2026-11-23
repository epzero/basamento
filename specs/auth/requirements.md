# Auth: identity, tenancy and permissions

Status: **Partially implemented**: the `none` and `oidc` authentication modes
(WP-L8-01); the authorization core in `auth/authz` (WP-L8-02 part (a)), including
active permission scenarios with real SQL stores. **Planned**: local accounts,
row-level security (WP-L8-02 part (b)), API wiring (WP-L6-03), and the
requirements proved through the API in WP-L9-01. Scope narrowing recognizes
`session:` scopes against `Resource.Session`; unrecognized scope lists fail closed
(REQ-AUTH-030). Design: ADR-0008, `docs/architecture/auth.md`.

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

### REQ-AUTH-006 — Declared actions only
The system shall recognize only `<area>.<verb>` actions declared in the Go action catalog and shall deny any action that is not declared.

### REQ-AUTH-007 — RPC action and resource declarations
The system shall refuse to serve an RPC that does not declare both its action and how its resource is resolved.

### REQ-AUTH-008 — Built-in role permission sets
The system shall grant each built-in role only the fixed permission set declared for it in Go, where `viewer` holds read actions, `member` adds the run lifecycle and converse actions, and `admin` adds bindings and credential names but no credential value.

### REQ-AUTH-009 — Caller-supplied resource
The system shall take the resource of a permission check from the caller's per-RPC resolver and shall not read the runs table while deciding the check.

### REQ-AUTH-010 — Binding project resolution
The system shall match a resource against the tenant-wide bindings and the bindings of the resource's project, and shall match only tenant-wide bindings when the resource has no project.

### REQ-AUTH-011 — Binding store and API
The system shall store bindings in a tenant-scoped SQL table keyed by (tenant, project, principal, role) on SQLite and Postgres through `platform/sql`, with a Go API to grant, revoke and list them.

### REQ-AUTH-012 — Grant authority
The system shall allow a grant or revoke only to a principal that holds a distinct grant action and every permission held by the role being granted or revoked.

### REQ-AUTH-013 — Unknown role
If a grant or revoke names a role that is not declared, then the system shall reject it with `invalid_argument`.

### REQ-AUTH-014 — Identifier grammar
If a binding names a project or principal id that does not match its declared grammar, then the system shall reject it with `invalid_argument`.

### REQ-AUTH-015 — Default deny
The system shall deny a permission check for which no applicable binding grants the action.

### REQ-AUTH-016 — Tenant mismatch
If the context tenant differs from the principal's tenant, then the system shall deny the permission check.

### REQ-AUTH-017 — Effective permissions
The system shall grant a permission when any role binding applicable to the principal and resource holds it and the binding's condition holds.

### REQ-AUTH-018 — Scopes narrow only
The system shall allow a principal's non-empty scopes only to narrow the permissions that applicable bindings grant.

### REQ-AUTH-030 — Scope grammar and fail-closed narrowing
When a principal carries a `session:<id>` scope, the system shall keep a granted permission only for a resource whose resolver-supplied session equals `<id>`; a scope string in any other form shall grant nothing; and a non-empty scope list with no recognized form shall deny the action with `permission_denied` and audit reason `scope-narrowed`. An empty scope list shall not narrow the granted permissions.

### REQ-AUTH-031 — Authorization scopes only
The system shall hold in `Principal.Scopes` only authorization scopes minted by the system for scoped worker or service tokens (REQ-AUTH-005), and shall not copy an identity provider's OAuth `scope` claim into `Principal.Scopes`.

### REQ-AUTH-019 — Denial message holds no detail
When a policy check denies an action, the system shall return `permission_denied` with a message that names neither the required action nor any role.

### REQ-AUTH-020 — Store failure is unavailable
If an internal failure prevents a permission check from completing, then the system shall deny the action, reporting `unavailable` when the binding store cannot answer.

### REQ-AUTH-021 — Condition type-check at grant
When a binding is granted with a condition, the system shall compile and type-check it with `expr` against the declared principal and resource variables, and reject an invalid condition or one that uses a clock or random function with `invalid_argument`.

### REQ-AUTH-022 — Condition cost limit
The system shall evaluate a binding's condition within a cost limit and shall not grant a permission when the condition exceeds that limit.

### REQ-AUTH-023 — Condition error denies
If a binding's condition errors at check time, then the system shall not grant the permission and shall record a denial with a safe reason category.

### REQ-AUTH-024 — Denial audit event
When the system denies a permission check, it shall record an audit event with action `authz.deny`, outcome `denied`, the principal id, the resource kind and id without attributes, and a safe reason category, carrying no condition text and no token.

### REQ-AUTH-025 — Record failure still denies
If recording the denial audit event fails, then the denial shall stand and the system shall log and count the failure.

### REQ-AUTH-026 — Allowed checks are not audited
The system shall not record an audit event for a permitted permission check.

### REQ-AUTH-027 — Row-level security enforcement
While in the cloud profile, the system shall enforce row-level security with `FORCE ROW LEVEL SECURITY` on every tenant table using an application role that lacks `BYPASSRLS` and is not the table owner.

### REQ-AUTH-028 — Tenant session variable
While in the cloud profile, the system shall set a tenant session variable for every transaction through `platform/sql` and shall return no rows when the variable is unset.

### REQ-AUTH-029 — RLS policies in migrations
The system shall create the row-level security policies for the credentials, audit and authz tables in the migrations.
