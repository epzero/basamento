# Credentials: design

Decisions the implementer of WP-L1-06 follows. Package:
`platform/credentials`. It sits in the `platform` layer of `layering.yaml`, so
it may import `core`, `platform` and the packages under `platform/` (including
the SQL store); it imports no runtime, model or vendor package. Decisions:
ADR-0004. Key wrapping and payload encryption are WP-L1-04's
([`../secrets/design.md`](../secrets/design.md)); their terms (master key,
data key, keyset) apply here but are not respecified.

## 1. Parts

| Part | What it is |
| --- | --- |
| Store | Implements `platform.Credentials` over a backing store, the Secrets port, an auditor and the clock. |
| Backing store | `Backend`: where encrypted credential rows live. One SQL backend ships here; an external secret manager implements the same interface. It never sees a plaintext value. |
| Secrets port | `platform.Secrets`: `Encrypt` on put, `Decrypt` on resolve. The master key, data keys and keysets belong to WP-L1-04 and never to this package. |
| Auditor | `Auditor`: the audit sink interface the store is given; it receives one `AuditRecord` per resolution of a well-formed reference with a tenant in context. |
| Use context | `Use`: principal, run and step, carried by the context of a resolution. |

The constructor is `New(backend Backend, auditor Auditor, secrets platform.Secrets, clock core.Clock) *Store`;
the composition root supplies non-nil dependencies. The package opens no
database and parses no URL itself: the SQL store (WP-L1-01) owns opening and
migrating, and wiring (WP-L1-05) selects the backend.

## 2. Values and rows

Every value is encrypted exactly once, by the Secrets port:

- Put: `ciphertext := secrets.Encrypt(ctx, value)`, then `backend.Upsert` with
  that ciphertext.
- Resolve: `record := backend.Get(ctx, name)`, then
  `value := secrets.Decrypt(ctx, record.Ciphertext)`.

The plaintext never reaches a backend, the auditor, a log record, an error or
any other stored field. The store holds no key and does no key wrapping or
payload encoding; it stores the opaque bytes the Secrets port returned
(REQ-CRED-021).

The SQL backend ships its goose migration, embedded from
`platform/credentials/migrations`; the package exposes it as `Migrations() fs.FS`
for the composition root and the BDD driver. The composition root includes it
in the SQL store's migration set.

```sql
CREATE TABLE credentials (
  tenant_id  TEXT NOT NULL,
  name       TEXT NOT NULL,
  ciphertext BLOB NOT NULL,
  created_at TIMESTAMP NOT NULL,
  updated_at TIMESTAMP NOT NULL,
  PRIMARY KEY (tenant_id, name)
);
```

The store keeps no decrypted value and no ciphertext after a call returns.
Each resolution reads the row again, so a replacement is seen at once
(REQ-CRED-024) and a resolution already holding a record finishes with it
(REQ-CRED-025). The Secrets port may cache keysets (WP-L1-04); this package
caches nothing.

## 3. Names and references

- A credential name matches `^[A-Za-z0-9._/-]{1,128}$`: 1–128 ASCII letters,
  digits, `.`, `_`, `-` or `/`. Names are case-sensitive and used verbatim as
  the row key, with no normalization.
- `Put` and `Delete` take a bare name. A reference is not a name.
- `Resolve` accepts exactly `cred://` followed by a valid name. The prefix is
  case-sensitive; no query, fragment, whitespace or other character is part of
  it. A malformed reference is rejected before any lookup or audit.
- Listing order is the byte order of the UTF-8 names; the store sorts, and
  does not rely on the backend's order.

## 4. Operations

| Call | Does |
| --- | --- |
| `Put(ctx, name, value)` | Requires a tenant; validates the name; encrypts; upserts the row, setting `created_at` and `updated_at` to the clock's time. On an existing name the backend keeps the stored `created_at` and updates `ciphertext` and `updated_at`. No audit record. |
| `Resolve(ctx, ref)` | Requires a tenant; parses the reference; reads the row; decrypts; hands an audit record to the audit sink; returns the value. On a failure after reference parsing it hands a failure audit record to the audit sink and returns the failure. |
| `Delete(ctx, name)` | Requires a tenant; validates the name; deletes the tenant's row. No audit record. |
| `Names(ctx)` | Requires a tenant; returns the tenant's credential names sorted by the bytes of their UTF-8 names, and never a value (REQ-CRED-012, REQ-CRED-015, REQ-CRED-016). |

`Resolve` is the only call that decrypts and the only call that returns a
value. `Put`, `Delete` and `Names` never call `Secrets.Decrypt`
(REQ-CRED-014). The resolved slice belongs to the caller; the store retains no
copy.

Resolution is called at the moment of use, inside a step. The step host sets
`Use` on the context; the store does not authenticate the caller — WP-L8 does —
and audits the principal, run and step it is given. Callers must not put a
resolved value into run state, events or step results (constitution article
7); those are WP-L3-01's and the runtimes' concern. This package proves the
store itself writes the plaintext nowhere.

## 5. Tenancy

The tenant comes from the context, through the shared tenant context accessor
used across `platform` ports. The credential store requires it at the start of
every public method, so no backend, Secrets call or audit record happens
without one (REQ-CRED-005). If the shared tenant context accessor has not yet
landed in `core` or `platform`, the implementer lands that small prerequisite
change first in a pull request of its own before this work package; where it
lives (`core` vs `platform`) is an open question for the architect.

Every SQL query and external-backend operation is scoped to that tenant. The
row key is
`(tenant_id, name)`. Another tenant's credential is not found, with the same
code and message as one that was never stored (REQ-CRED-010, REQ-CRED-011);
delete, list and resolve never reveal that it exists.

## 6. Audit

`Use` carries the principal, run and step; the store reads it from the context:

```go
type Use struct {
    Principal string // human:<login> or <harness>:<role>:<instance>
    RunID     string
    StepID    string
}
func WithUse(ctx context.Context, u Use) context.Context
func UseFrom(ctx context.Context) Use

type AuditRecord struct {
    Tenant     string
    Principal  string
    Credential string
    RunID      string
    StepID     string
    Outcome    string
    At         time.Time
}

type Auditor interface {
    Record(ctx context.Context, r AuditRecord)
}
```

For every resolution of a well-formed reference with a tenant in context,
successful or not, the store hands `Auditor.Record` an `AuditRecord` with the
tenant, the principal, the credential name, the run, the step, the outcome and
the clock's time (REQ-CRED-017, REQ-CRED-018). Outcomes are `resolved`,
`not_found`, `internal` and `unavailable`. When the context carries no `Use`
(or omits any of principal, run or step), `UseFrom(ctx)` yields empty strings
for those fields and the resolution still proceeds and audits those empty
fields (REQ-CRED-017).

A malformed reference is rejected before a credential is named and is not
audited. A call without a tenant is rejected before any audit record can be
scoped, and is not audited (REQ-CRED-018).

The value is never written to the audit record; the record carries only the
fields above (REQ-CRED-019). This package defines no audit SQL table and no
shared `platform` audit port: where audit records are finally persisted is
WP-L8-02's and the architect's.

## 7. Backing stores

```go
// Record is one encrypted credential row.
type Record struct {
    Name       string
    Ciphertext []byte
    CreatedAt  time.Time
    UpdatedAt  time.Time
}

// Backend keeps encrypted credential rows for the tenant in ctx. It never
// receives or returns a plaintext value.
type Backend interface {
    Upsert(ctx context.Context, r Record) error      // insert, or replace ciphertext and updated_at; keep created_at
    Get(ctx context.Context, name string) (Record, error) // not_found when absent
    Delete(ctx context.Context, name string) error   // not_found when absent
    List(ctx context.Context) ([]Record, error)
}
```

The SQL backend is `NewSQLBackend(store)`. The SQL backend writes the
`credentials` table through the SQL store. An external secret manager (HashiCorp Vault KV, a cloud secret
manager) implements `Backend` over its own store: it keeps the same `Record`
bytes under a tenant-scoped path and returns them; it never receives a
plaintext value because the store encrypts before `Upsert` and decrypts after
`Get` (REQ-CRED-026). Vendor adapters are a follow-up change under this
package and add no spec behavior; wiring (WP-L1-05) selects one. The gate
proves the port with a simulated external backend in `internal/bdd`; no live
vendor call runs in the gate.

## 8. Errors

Codes come from `core`; no new code is added.

| Case | Code | Message |
| --- | --- | --- |
| Call without a tenant (REQ-CRED-005) | `invalid_argument` | from the shared tenant accessor, `tenant is required` |
| Invalid credential name (REQ-CRED-007) | `invalid_argument` | `invalid credential name` |
| Malformed reference (REQ-CRED-009) | `invalid_argument` | `invalid credential reference` |
| Resolution names no credential of the tenant (REQ-CRED-010, REQ-CRED-011) | `not_found` | `credential not found` |
| Delete names no credential of the tenant (REQ-CRED-004) | `not_found` | `credential not found` |
| The Secrets port rejects the stored ciphertext with `invalid_argument` (REQ-CRED-023) | `internal` | `stored credential cannot be decrypted` |
| The key service is unavailable during put or resolve (REQ-CRED-027) | `unavailable` | the Secrets port's error, naming the scheme only |
| An external backend is unreachable | `unavailable` | the backend's error, naming the store only |

Missing and another tenant's credential share code and message, so a caller
cannot tell them apart. Errors use fixed messages and never echo the value,
the ciphertext or a raw reference. Log records may carry the tenant, the
credential name and the outcome, never the value or the ciphertext.

## 9. Test support

The BDD steps live in `internal/bdd/steps/credentials.go` and helpers beside
it, in the `internal/bdd` package:

- Each scenario gets a fresh SQLite file opened through the SQL store with
  this package's migration set, a simulated key service holding the named
  master key (reusing the secrets steps' helper), the fake clock from
  `core/coretest`, and a **recording `Auditor` sink** in `internal/bdd` that
  stores the `AuditRecord` values per tenant for the audit assertions.
- When a step names a principal, run and step, the driver attaches `Use` to
  the context; when a step does not name them, the driver attaches no `Use`,
  so `UseFrom(ctx)` returns empty strings.
- A **simulated external store** implements `credentials.Backend` in
  `internal/bdd`: it keeps records per tenant and exposes what it stored, so a
  scenario can assert ciphertext-only rows and that no SQL credential row
  exists. It never calls a vendor.
- A **gated Secrets wrapper** in `internal/bdd` signals when a resolution
  reaches `Decrypt` and waits until the scenario releases it; it produces the
  in-flight replacement of REQ-CRED-025 deterministically.
- Inspection steps read the `credentials` rows through the SQL store and the
  recorded audit records from the recording `Auditor` sink; they are test
  helpers, not a production API.

Unit tests in `platform/credentials` use fakes defined in their own `_test.go`
files. No test double enters production code, and no new test-only package is
added: `core/coretest` remains the only entry in `layering.yaml`'s
`test_only`, importable from `_test.go` files and from `internal/bdd`. The
gate uses no wall clock, no unseeded randomness and no live vendor call.

## Open questions

1. **Richer listing metadata on `platform.Credentials` (architect):** `platform.Credentials` in `platform/platform.go` currently declares `Names(ctx context.Context) ([]string, error)` (names only). Should the port stay names-only, or should the architect add a `List(ctx context.Context) ([]CredentialInfo, error)` method carrying `CreatedAt` and `UpdatedAt` timestamps in a separate contract change?
2. **Final audit destination and shared audit port (WP-L8-02 / architect):** WP-L1-06 hands each credential-use `AuditRecord` to an injected `Auditor` sink (`platform/credentials`), tested with a recording sink in `internal/bdd`. Where audit records are finally persisted (and whether a shared `Auditor` port belongs in `platform` or `auth` under WP-L8-02) is left to WP-L8-02 and the architect.
3. **Audit-write failure policy (owner / architect):** If the production audit store introduced by WP-L8-02 can fail when recording a credential resolution, should `Resolve` fail closed with code `internal` and return no secret value, or return the value and report the audit failure? `REQ-CRED-020` and `CRED-020-01` have been removed pending that policy decision.
4. **Package home of the shared tenant context accessor (architect):** `platform/sql` (WP-L1-01), `platform/secrets` (WP-L1-04), `platform/credentials` (WP-L1-06), and `stream/eventlog` (WP-L6-01) all read the calling tenant from `context.Context` and reject missing-tenant calls with `invalid_argument`. Should the shared `Tenant` context key and accessor live in `core` or in `platform`?
