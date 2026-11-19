# Auth

Status: **Implemented** for the `none` and `oidc` modes and local bearer-token
records (WP-L8-01, `auth/authn`), and for the permission check, the role and
scope mapping and the denial audit (WP-L8-02, `auth/authz`). **Planned**: local
accounts (argon2id passwords, passkeys, cookie sessions), the role-binding store,
request wiring in the API, CEL conditions, and PostgreSQL row-level security.
Decision: ADR-0008. Specs: `specs/auth`.

## 1. Authentication modes

| Mode | For | How |
| --- | --- | --- |
| `none` | Single user on their own machine | Listens on loopback only; refuses any other address (REQ-AUTH-001). |
| `local` | Small self-hosted teams | In-app accounts: argon2id passwords, passkeys (`go-webauthn`), cookie sessions, hashed scoped API tokens. |
| `oidc` | SaaS, enterprise self-hosting | Any OIDC issuer; **Logto** (self-hosted MPL-2.0 core or Logto Cloud) is the reference. We only validate JWTs (issuer keys via JWKS, audience, expiry) and map organization roles from claims (REQ-AUTH-003). Logto runs as a separate service; it is never part of the embedded binary. |

`auth/authn` implements the modes and the `auth.mode` setting selects one.
`kit serve` refuses a non-loopback `listen.addr` in `none` mode before it opens
a port or binds the listener; only an IP loopback address or `localhost` is
accepted (REQ-AUTH-001).

OIDC verification requires the issuer, the audience, a non-expired `exp` and a
signature by one of the issuer's keys. A `JWKS` key set is cached and refreshed
when a token names an unknown key id, so issuer key rotation needs no restart.
Clock skew is an explicit, small leeway, zero by default. The token's `tenant`
claim (name configurable) must satisfy the tenant grammar when present; a token
without it is tenantless and cannot read tenant data.

Local-mode bearer tokens are opaque 256-bit secrets. Only their SHA-256 hash is
stored; verification compares hashes in constant time and enforces a fixed
expiry. A token carries its tenant and scopes, and rotating it means minting a
new one and replacing the stored record. Every rejection is
`unauthenticated` with a message that carries no credential or claim detail.

## 2. Authorization

- Permissions are declared in Go. Roles are bound per project and stored in SQL.
  `viewer` reads a project and its runs, `editor` also creates and cancels runs,
  and `admin` also manages the project. The binding store is behind
  `authz.Bindings` and is read for the tenant in the request context, never for
  a tenant named by the caller.
- A single `authz.Check(principal, action, resource)` runs in a connect
  interceptor. It denies by default: an absent binding, an unknown role and an
  unknown action grant nothing, and a token scope grants only its exact action.
  A denial returns `permission_denied` and writes an audit event (REQ-AUTH-004).
- The check maps its failures so tenancy never leaks: a missing or invalid
  principal or request tenant is `unauthenticated`; a principal or resource of
  another tenant is `not_found`, the same answer as an object that does not
  exist (REQ-AUTH-002); a binding lookup error fails closed. A denial whose
  audit event cannot be stored fails with `unavailable`; no failure path becomes
  an allow.
- **Audit**: every producer, the credential store and this check among them,
  records through `platform.Audit`. There is one audit store, owned by the
  credential store (WP-L1-06): an append-only table with tenant-scoped reads.
  Authorization is another producer (denials), not the owner of a second
  store. An event never carries a secret value or a raw token: `Principal`
  is a user ID or service token ID, and pool and join tokens are logged
  only as hashed identifiers. An event's tenant comes from the context;
  `Record` rejects an event that names a different tenant, and a producer
  on a tenant path fails closed when it has no tenant. A denied authorization
  names the attempted permission as its action, the project as its resource
  and the mapped code as its reason. Events with no tenant
  are deployment-level: refused joins with an unknown pool token
  (REQ-MODEL-029), shared-pool and launch events (ADR-0012 §10, §12), and
  operator actions. Tenant-scoped reads never return them. See
  [`overview.md`](overview.md) §3.
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
  reference, `cred://<name>`; no role can read a value back. A name is 1 to
  128 characters from A–Z, a–z, 0–9, `.`, `_`, `-` and `/`, made of
  `/`-separated segments none of which is empty, `.` or `..`, so it cannot
  walk out of the tenant's entries (REQ-CRED-006, REQ-CRED-007).
- The `Authorizer` port leaves room for OpenFGA if sharing ever becomes
  graph-shaped.
