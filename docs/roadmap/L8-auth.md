# L8 — Auth

Authentication modes and in-app authorization (ADR-0008, `specs/auth`).

### WP-L8-01 — Authentication modes
Depends: WP-L1-05.
Owns: `auth/authn`.
`none` (loopback only), `local` (argon2id, passkeys, sessions, API tokens), `oidc` (Logto or any issuer; JWT validation). Specs: `specs/auth/access.feature`.

### WP-L8-02 — Authorization
Depends: WP-L8-01.
Owns: `auth/authz`.
In-app RBAC with CEL conditions, audit events for denials, through the `platform.Audit` port (store from WP-L1-06), Postgres row-level security in the cloud profile. Specs: `specs/auth/permissions.feature`, design in `specs/auth/design.md`.

Delivered in two parts: **(a)** the core — action catalog, built-in roles, caller-supplied resource, binding store and API, grant authority, decision procedure, CEL conditions and denial audit; **(b)** Postgres row-level security in the cloud profile ([ADR-0022](../adr/0022-postgres-row-level-security.md): tenant connector, dual URLs, postgres-only migrations, gate as `bas_app`). WP-L6-03 needs only the check, roles, bindings and audit from part (a), so the API surface does not wait for RLS.
