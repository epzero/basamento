# L8 — Auth

Authentication modes and in-app authorization (ADR-0008, `specs/auth`).

### WP-L8-01 — Authentication modes
Depends: WP-L1-05.
Owns: `auth/authn`.
`none` (loopback only), `local` (argon2id, passkeys, sessions, API tokens), `oidc` (Logto or any issuer; JWT validation). Specs: `specs/auth/access.feature`.

### WP-L8-02 — Authorization
Depends: WP-L8-01.
Owns: `auth/authz`.
In-app RBAC with CEL conditions, audit events, Postgres row-level security in the cloud profile. Specs: `specs/auth/permissions.feature`.
