# Auth

Status: **Planned** (WP-L8-01, WP-L8-02). Decision: ADR-0008. Specs: `specs/auth`.

## 1. Authentication modes

| Mode | For | How |
| --- | --- | --- |
| `none` | Single user on their own machine | Listens on loopback only; refuses any other address (REQ-AUTH-001). |
| `local` | Small self-hosted teams | In-app accounts: argon2id passwords, passkeys (`go-webauthn`), cookie sessions, hashed scoped API tokens. |
| `oidc` | SaaS, enterprise self-hosting | Any OIDC issuer; **Logto** (self-hosted MPL-2.0 core or Logto Cloud) is the reference. We only validate JWTs (issuer keys via JWKS, audience, expiry) and map organization roles from claims (REQ-AUTH-003). Logto runs as a separate service; it is never part of the embedded binary. |

## 2. Authorization

- Permissions are declared in Go. Roles are bound per project and stored in SQL.
- A single `authz.Check(principal, action, resource)` runs in a connect
  interceptor. Denials return `permission_denied` and write an audit event
  (REQ-AUTH-004).
- **Audit**: every producer, the credential store and this check among them,
  records through `platform.Audit`. There is one audit store, owned by the
  credential store (WP-L1-06): an append-only table with tenant-scoped reads.
  Authorization is another producer (denials), not the owner of a second
  store. An event never carries a secret value.
- Optional **CEL conditions** for attribute rules (`resource.owner ==
  principal.id`), reusing the expression engine. No Casbin.
- **Tenancy**: every repository call takes a tenant context, set with
  `core.WithTenant` and read with `core.TenantFrom` (REQ-AUTH-002). Postgres row-level security is a second line of defense in
  the cloud profile.
- **Worker tokens**: scoped to one session's resources with a fixed expiry,
  for the browser runner and other external services (REQ-AUTH-005).
- **Pool tokens**: let a `kit worker` join one pool, and nothing else.
  Joining yields a worker id and a short-lived credential, renewed by
  heartbeats and revocable per worker, with which it declares what it
  serves and leases that work. A worker started on demand joins with a
  single-use token made for that launch (ADR-0012).
- **Credentials**: vendor keys live in the vault (ADR-0004) and are used by
  reference; no role can read a value back.
- The `Authorizer` port leaves room for OpenFGA if sharing ever becomes
  graph-shaped.
