# Credentials: design

Decisions the implementer of WP-L1-06 follows. Package:
`platform/credentials`. It sits in the `platform` layer of `layering.yaml`, so
it may import `core`, `platform` and the packages under `platform/` (including
the SQL store); it imports no runtime, model or vendor package. Decisions:
ADR-0004 and the owner decisions of 2026-11-17 on the audit port and the
tenant context. Key wrapping and payload encryption are WP-L1-04's
([`../secrets/design.md`](../secrets/design.md)); their terms (master key,
data key, keyset) apply here but are not respecified.

## 1. Parts

| Part | What it is |
| --- | --- |
| Store | Implements `platform.Credentials` over a backing store, the Secrets port, `platform.Audit` and the clock. |
| Backing store | `Backend`: where encrypted credential rows live. One SQL backend ships here; an external secret manager implements the same interface. It never sees a plaintext value. |
| Secrets port | `platform.Secrets`: `Encrypt` on put, `Decrypt` on resolve. The master key, data keys and keysets belong to WP-L1-04 and never to this package. |
| Audit port | `platform.Audit`: the audit sink the store is given, `Record(ctx, platform.AuditEvent) error`, called once per resolution of a well-formed reference with a tenant in context (owner decision, 2026-11-17). |
| Audit store | `AuditStore`: this package's SQL implementation of `platform.Audit`, an append-only, tenant-scoped table (REQ-CRED-029 to REQ-CRED-031). |
| Use context | `Use`: principal, run and step, carried by the context of a resolution. |

The constructor is `New(backend Backend, audit platform.Audit, secrets platform.Secrets, clock core.Clock) *Store`;
the composition root supplies non-nil dependencies. The package opens no
database and parses no URL itself: the SQL store (WP-L1-01) owns opening and
migrating, and wiring (WP-L1-05) selects the backend and passes the SQL audit
store as `platform.Audit`.

## 2. Values and rows

Every value is encrypted exactly once, by the Secrets port:

- Put: `ciphertext := secrets.Encrypt(ctx, value)`, then `backend.Upsert` with
  that ciphertext.
- Resolve: `record := backend.Get(ctx, name)`, then
  `value := secrets.Decrypt(ctx, record.Ciphertext)`.

The plaintext never reaches a backend, the audit store, a log record, an error
or any other stored field. The store holds no key and does no key wrapping or
payload encoding; it stores the opaque bytes the Secrets port returned
(REQ-CRED-021).

The SQL implementation ships its goose migrations, embedded from
`platform/credentials/migrations`; the package exposes them as `Migrations() fs.FS`
for the composition root and the BDD driver. The set holds the `credentials`
table below and the `audit_events` table of §6. The composition root includes
it in the SQL store's migration set.

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

- A credential name matches `^[A-Za-z0-9._/-]{1,128}$` and is made of
  `/`-separated segments, none empty, `.` or `..`: 1–128 ASCII letters, digits,
  `.`, `_`, `-` or `/`, so no leading or trailing `/`, no `//` and no `../`
  traversal. Names are case-sensitive and used verbatim as the row key, with
  no normalization.
- `Put` and `Delete` take a bare name. A reference is not a name.
- `Resolve` accepts exactly `cred://` followed by a valid name. The prefix is
  case-sensitive; no query, fragment, whitespace or other character is part of
  it. A malformed reference is rejected before any lookup or audit.
- Listing order is the byte order of the UTF-8 names; the store sorts, and
  does not rely on the backend's order.
- `platform.Credentials` keeps `Names(ctx) ([]string, error)` only: there is no
  listing metadata (`CreatedAt`, `UpdatedAt`) on the port. That is the owner
  decision of 2026-11-17; metadata may be added additively later, when needed.

## 4. Operations

| Call | Does |
| --- | --- |
| `Put(ctx, name, value)` | Requires a tenant; validates the name; encrypts; upserts the row, setting `created_at` and `updated_at` to the clock's time. On an existing name the backend keeps the stored `created_at` and updates `ciphertext` and `updated_at`. No audit event. |
| `Resolve(ctx, ref)` | Requires a tenant; parses the reference; reads the row; decrypts; records one `platform.AuditEvent` through `platform.Audit`; returns the value. On a failure after reference parsing it records a failure event and returns the failure. If the event cannot be written, it returns no value and fails with `unavailable` (REQ-CRED-028). |
| `Delete(ctx, name)` | Requires a tenant; validates the name; deletes the tenant's row. No audit event. |
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

The tenant comes from the context through `core.TenantFrom(ctx)`:
`core.TenantID`, set with `core.WithTenant`, and an empty id counts as no
tenant. Every layer reads the tenant through that helper (owner decision,
2026-11-17). The credential store requires it at the start of every public
method, so no backend, Secrets call or audit event happens without one
(REQ-CRED-005). The helper lands first, in its own `feat(core)` contract pull
request, before this work package.

Every SQL query and external-backend operation is scoped to that tenant. The
row key is `(tenant_id, name)`. Another tenant's credential is not found, with
the same code and message as one that was never stored (REQ-CRED-010,
REQ-CRED-011); delete, list and resolve never reveal that it exists.

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
```

The audit sink is the `platform.Audit` port; `platform.AuditEvent` is declared
in `platform/platform.go` (owner decision, 2026-11-17), and lands first in its
own `feat(platform)` contract pull request before this work package:

```go
type AuditEvent struct {
    Time      time.Time     // from core.Clock
    Tenant    core.TenantID // from core.TenantFrom(ctx)
    Principal string
    Action    string
    Resource  string
    Outcome   string // "allowed", "denied" or "failed"
    RunID     string
    Step      string
    Reason    string
}

type Audit interface {
    Record(ctx context.Context, e AuditEvent) error
}
```

A credential resolution fills one event as follows:

| Field | Value |
| --- | --- |
| `Time` | the clock's time at the resolution |
| `Tenant` | the tenant from `core.TenantFrom(ctx)` |
| `Principal` | `Use.Principal`, empty when absent |
| `Action` | `credential.resolve` |
| `Resource` | the credential name, never the value or the ciphertext |
| `Outcome` | `allowed` when the resolution returns a value, `failed` on any failure |
| `RunID`, `Step` | `Use.RunID`, `Use.StepID`, empty when absent |
| `Reason` | empty on success; on failure the error code (`not_found`, `internal` or `unavailable`) |

For every resolution of a well-formed reference with a tenant in context,
successful or not, the store calls `Audit.Record` exactly once (REQ-CRED-017,
REQ-CRED-018). If `Record` returns an error, the store returns no value and
fails the resolution with code `unavailable` (REQ-CRED-028); that failure is
logged, because the audit store is what failed (owner decision, 2026-11-17).
When the context carries no `Use` (or omits any of principal, run or step),
`UseFrom(ctx)` yields empty strings for those fields and the resolution still
proceeds and audits those empty fields (REQ-CRED-017).

A malformed reference is rejected before a credential is named and is not
audited. A call without a tenant is rejected before any audit event can be
scoped, and is not audited (REQ-CRED-032).

The value is never written to the audit event; the event carries only the
fields above (REQ-CRED-019).

### The SQL audit store

`AuditStore` is this package's implementation of `platform.Audit`, constructed
with `NewSQLAudit(store)` over the SQL store. It keeps one table for every
producer, this package and WP-L8-02's denials among them; there is no second
audit store (owner decision, 2026-11-17).

```sql
CREATE TABLE audit_events (
  id         INTEGER PRIMARY KEY, -- dialect-specific identity/auto-increment
  tenant_id  TEXT NOT NULL,
  at         TIMESTAMP NOT NULL,
  principal  TEXT NOT NULL,
  action     TEXT NOT NULL,
  resource   TEXT NOT NULL,
  outcome    TEXT NOT NULL,
  run_id     TEXT NOT NULL,
  step       TEXT NOT NULL,
  reason     TEXT NOT NULL
);
CREATE INDEX audit_events_tenant_at ON audit_events (tenant_id, at, id);
```

`Record` takes `Tenant` from `core.TenantFrom(ctx)` and appends one row
(REQ-CRED-029); optional fields are stored as empty strings, never `NULL`, so
a read returns the event as recorded. A `Record` call with no tenant in its
context is rejected with `invalid_argument` (REQ-CRED-033): this is the
credentials store's fail-closed rule (REQ-CRED-005), stricter than the port,
whose contract (PR #25, `platform/platform.go`) lets a producer write a
deployment-level event with no tenant. `AuditStore` is that contract's store
for every producer, and its table keys rows by `tenant_id`, so it admits
only tenant audit events; a future producer needs deployment-level events,
it is added then, additively, with its own review. The table is
append-only: `AuditStore` exposes `Record` and `Read` and no update or delete,
and the package's SQL uses only `INSERT` and `SELECT` on it (REQ-CRED-030).

```go
// Read returns the calling tenant's events, ordered by time then id.
func (a *AuditStore) Read(ctx context.Context) ([]platform.AuditEvent, error)
```

`Read` takes the tenant from `core.TenantFrom(ctx)` and returns only that
tenant's rows (REQ-CRED-031); a context without a tenant fails with
`invalid_argument` (REQ-CRED-033). `Read` is not on `platform.Audit`, whose only operation is
`Record`; it is the package's inspection API for the console and the BDD
driver.

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
| The audit event of a resolution cannot be written (REQ-CRED-028) | `unavailable` | the audit store's error, naming the store only |
| An external backend is unreachable | `unavailable` | the backend's error, naming the store only |
| An audit record or read without a tenant (REQ-CRED-033) | `invalid_argument` | from the shared tenant accessor, tenant is required |

Missing and another tenant's credential share code and message, so a caller
cannot tell them apart. Errors use fixed messages and never echo the value,
the ciphertext or a raw reference. Log records may carry the tenant, the
credential name and the outcome, never the value or the ciphertext.

## 9. Test support

The BDD steps live in `internal/bdd/steps/credentials.go` and helpers beside
it, in the `internal/bdd` package:

- Each scenario gets a fresh SQLite file opened through the SQL store with
  this package's migration set (credentials and audit events), a simulated key
  service holding the named master key (reusing the secrets steps' helper),
  the fake clock from `core/coretest`, and the SQL audit store wired as
  `platform.Audit` through a **counting `platform.Audit` wrapper** in
  `internal/bdd` that passes every `Record` on to the SQL audit store and
  counts the calls, so "the audit port received 0 events" holds for
  `CRED-032-01` and `CRED-032-02`.
- Audit inspection reopens `AuditStore` on the same SQL store, so
  `CRED-029-01` reads back events recorded before the restart.
- When a step names a principal, run and step, the driver attaches `Use` to
  the context; when a step does not name them, the driver attaches no `Use`,
  so `UseFrom(ctx)` returns empty strings.
- A **failing `platform.Audit` wrapper** in `internal/bdd` implements `Record`
  by returning an error, for `CRED-028-01`; `CRED-019-03` uses the same
  wrapper to prove the failed resolution's error and log carry no value.
- A **simulated external store** implements `credentials.Backend` in
  `internal/bdd`: it keeps records per tenant and exposes what it stored, so a
  scenario can assert ciphertext-only rows and that no SQL credential row
  exists. It never calls a vendor.
- A **gated Secrets wrapper** in `internal/bdd` signals when a resolution
  reaches `Decrypt` and waits until the scenario releases it; it produces the
  in-flight replacement of REQ-CRED-025 deterministically.
- Inspection steps read the `credentials` rows through the SQL store and the
  audit events through `AuditStore.Read` for the tenant in the step; they are
  test helpers, not a production API.

Unit tests in `platform/credentials` use fakes defined in their own `_test.go`
files. No test double enters production code, and no new test-only package is
added: `core/coretest` remains the only entry in `layering.yaml`'s
`test_only`, importable from `_test.go` files and from `internal/bdd`. The
gate uses no wall clock, no unseeded randomness and no live vendor call.
