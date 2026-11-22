# Auth

Status: **Implemented** for the `none` and `oidc` modes and local bearer-token
records (WP-L8-01, `auth/authn`). **Planned**: local accounts (argon2id
passwords, passkeys, cookie sessions), request wiring in the API, and
authorization (WP-L8-02). Decision: ADR-0008. Specs: `specs/auth`.

## 1. Authentication modes

| Mode | For | How |
| --- | --- | --- |
| `none` | Single user on their own machine | Listens on loopback only; refuses any other address (REQ-AUTH-001). |
| `local` | Small self-hosted teams | In-app accounts: argon2id passwords, passkeys (`go-webauthn`), cookie sessions, hashed scoped API tokens. Listens on loopback only until request authentication is wired into `kit serve`. |
| `oidc` | SaaS, enterprise self-hosting | Any OIDC issuer; **Logto** (self-hosted MPL-2.0 core or Logto Cloud) is the reference. JWT validation covers issuer keys (JWKS or configured set), audience and expiry (REQ-AUTH-003). Organization role mapping belongs to authorization (WP-L8-02). Logto runs as a separate service; it is never part of the embedded binary. Listens on loopback only until request authentication is wired into `kit serve`. |

`auth/authn` implements the modes. `kit serve` validates `listen.addr` against
`auth.mode`: every mode accepts loopback only until request authentication is
wired (REQ-AUTH-001 for `none`; the same loopback rule for `local` and `oidc`
while bearer verification on requests is planned in WP-L6-03). Verifying bearer
tokens on requests for `local` and `oidc` is planned in the API work (WP-L6-03);
authorization in WP-L8-02.

OIDC verification requires the issuer, the audience, a non-expired `exp` and a
signature by one of the issuer's keys. A `JWKS` key set is cached; fetches use
https (http only to loopback), and neither the default nor a supplied HTTP
client follows redirects,
and at most one refresh attempt per `MinRefresh` interval (30 s by default), so a
token signed with a brand-new key may be rejected until the next attempt. The JWKS
URL must not carry credentials. While the cache is empty, a recent fetch failure is
reported again inside the cooldown instead of `ErrUnknownKey`. A fetch response with
no usable signing key leaves the previous keys in place. Clock skew
is an explicit leeway capped at five minutes, zero by default. The token's
`tenant` claim (name configurable) must satisfy the tenant grammar when present.
When absent, `Principal.Tenant` is empty; request wiring must refuse such a
principal wherever a tenant is required. Bearer JWT verification returns
`cancelled` or `timeout` when the caller's context is done or a JWKS lookup
hits its fetch deadline; every other verification or fetch failure is
`unauthenticated`. Errors never carry the token, a claim, or the JWKS URL.

Local-mode bearer tokens are opaque 256-bit secrets. Only their SHA-256 hash is
stored; verification compares hashes in constant time and enforces a fixed
expiry. A token carries its tenant and scopes, and rotating it means minting a
new one and replacing the stored record. Every rejection is
`unauthenticated` with a message that carries no credential or claim detail.

## 2. Authorization

- Permissions are declared in Go. Roles are bound per project and stored in SQL.
- A single `authz.Check(principal, action, resource)` runs in a connect
  interceptor. Denials return `permission_denied` and write an audit event
  (REQ-AUTH-004).
- **Audit**: every producer, the credential store and this check among them,
  records through `platform.Audit`. There is one audit store, owned by the
  credential store (WP-L1-06): an append-only table with tenant-scoped reads.
  Authorization is another producer (denials), not the owner of a second
  store. An event never carries a secret value or a raw token: `Principal`
  is a user ID or service token ID, and pool and join tokens are logged
  only as hashed identifiers. An event's tenant comes from the context;
  `Record` rejects an event that names a different tenant, and a producer
  on a tenant path fails closed when it has no tenant. Events with no tenant
  are deployment-level: refused joins with an unknown pool token
  (REQ-MODEL-029), shared-pool and launch events (ADR-0012 §10, §12), and
  operator actions. Tenant-scoped reads never return them. See
  [`overview.md`](overview.md) §3.
- Optional **CEL conditions** for attribute rules (`resource.owner ==
  principal.id`), reusing the expression engine. No Casbin.
- **Tenancy**: every repository call takes a tenant context, set with
  `core.WithTenant` and read with `core.TenantFrom` (REQ-AUTH-002). Postgres
  row-level security is a second line of defense in the cloud profile
  ([ADR-0022](../adr/0022-postgres-row-level-security.md) D1–D8): `app.tenant`
  (and relay discovery `app.system`) set before each statement through a
  `platform/sql` connector; production uses `bas_app` on `sql.url` with an
  open-time check that the role is not superuser and does not bypass RLS; the
  gate fixture creates `bas_app` and opens the store on a real application URL.

### Postgres RLS operator setup (cloud)

Before the first migrate against an empty database (once per cluster):

1. Create role `bas_app` with `LOGIN`, not `SUPERUSER`, not `BYPASSRLS`; grant
   `CONNECT` on the application database.
2. On PostgreSQL 14 and earlier, also run
   `REVOKE CREATE ON SCHEMA public FROM PUBLIC;` so only the migration login
   can create objects.
3. Store `sql.migrate_url` (migration login) and `sql.url` (`bas_app`) in the
   secrets port; migrations never create roles or embed passwords (ADR D2).

The migration login owns tenant tables; postgres-only goose fragments grant DML
to `bas_app` and attach policies (ADR D3–D5). Backups need a separate role with
`BYPASSRLS` or superuser (ADR D7).
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
