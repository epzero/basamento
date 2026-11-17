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
| Auditor | `Auditor`: one audit event per resolution. The SQL auditor ships here; it appends to `credential_audit`. |
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

CREATE TABLE credential_audit (
  tenant_id  TEXT NOT NULL,
  principal  TEXT NOT NULL,
  credential TEXT NOT NULL,
  run_id     TEXT NOT NULL,
  step_id    TEXT NOT NULL,
  outcome    TEXT NOT NULL,
  at         TIMESTAMP NOT NULL
);
```

The audit table has no value column. Audit rows are append-only and are not
deleted with the credential.

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
| `Put(ctx, name, value)` | Requires a tenant; validates the name; encrypts; upserts the row, setting `created_at` and `updated_at` to the clock's time. On an existing name the backend keeps the stored `created_at` and updates `ciphertext` and `updated_at`. No audit event. |
| `Resolve(ctx, ref)` | Requires a tenant; parses the reference; reads the row; decrypts; audits; returns the value. On a failure it audits and returns the failure. |
| `Delete(ctx, name)` | Requires a tenant; validates the name; deletes the tenant's row. No audit event. |
| `List(ctx)` | Requires a tenant; returns the tenant's rows as `platform.CredentialInfo`, sorted by name. |
| `Names(ctx)` | Requires a tenant; returns the names of `List` in the same order. It stays in the port for callers that need no metadata. |

`Resolve` is the only call that decrypts and the only call that returns a
value. `Put`, `Delete`, `List` and `Names` never call `Secrets.Decrypt`
(REQ-CRED-014). The resolved slice belongs to the caller; the store retains no
copy.

Resolution is called at the moment of use, inside a step. The step host sets
`Use` on the context; the store does not authenticate the caller — WP-L8 does —
and audits the principal, run and step it is given. Callers must not put a
resolved value into run state, events or step results (constitution article
7); those are WP-L3-01's and the runtimes' concern. This package proves the
store itself writes the plaintext nowhere.

## 5. Tenancy

The tenant comes from the context, through the accessor the SQL store uses for
every repository call (WP-L1-01). The credential store requires it at the
start of every public method, so no backend, Secrets call or audit write
happens without one (REQ-CRED-005). If the SQL store has not exported the
accessor, the implementer lands that small change in a pull request of its own
before this work package, as with the port change in §8.

Every query and every audit insert is scoped to that tenant by the SQL store;
an external backend scopes its rows by the same tenant. The row key is
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
```

For every resolution of a well-formed reference, successful or not, the store
calls `Auditor.Record` with the principal, the credential name, the run, the
step, the outcome and the clock's time (REQ-CRED-017, REQ-CRED-018). Outcomes
are `resolved`, `not_found`, `internal` and `unavailable`.

A malformed reference is rejected before a credential is named and is not
audited. A call without a tenant is rejected before any audit row can be
scoped, and is not audited.

The audit write is part of the use: if `Record` fails, the resolution fails
with code `internal` and returns no value, even when the value was already
decrypted (REQ-CRED-020). The value is never written to the event; the event
carries only the fields above (REQ-CRED-019).

The SQL auditor appends one row to `credential_audit`, using the tenant from
the context.

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

The SQL backend is `NewSQLBackend(store)`; the SQL auditor is
`NewSQLAuditor(store)`. The SQL backend writes the `credentials` table through
the SQL store. An external secret manager (HashiCorp Vault KV, a cloud secret
manager) implements `Backend` over its own store: it keeps the same `Record`
bytes under a tenant-scoped path and returns them; it never receives a
plaintext value because the store encrypts before `Upsert` and decrypts after
`Get` (REQ-CRED-026). Vendor adapters are a follow-up change under this
package and add no spec behavior; wiring (WP-L1-05) selects one. The gate
proves the port with a simulated external backend in `internal/bdd`; no live
vendor call runs in the gate.

## 8. Port change

The existing `platform.Credentials` port has `Put`, `Resolve`, `Delete` and
`Names`; `Names` cannot carry the creation and last-updated times the listing
requires (REQ-CRED-015). Add, additively, to `platform/platform.go`:

```go
// CredentialInfo describes a stored credential without its value.
type CredentialInfo struct {
    Name      string
    CreatedAt time.Time
    UpdatedAt time.Time
}

// In Credentials:
// List returns the tenant's credentials, ordered by name, with their metadata
// and never their values.
List(ctx context.Context) ([]CredentialInfo, error)
```

`Names` stays and means the names of `List`. `platform/platform.go` is outside
this work package's owned directory, so the implementer lands this change
first in a pull request of its own; the credentials implementation and the
scenarios follow on top of it.

## 9. Errors

Codes come from `core`; no new code is added.

| Case | Code | Message |
| --- | --- | --- |
| Call without a tenant (REQ-CRED-005) | `invalid_argument` | from the shared tenant accessor, `tenant is required` |
| Invalid credential name (REQ-CRED-007) | `invalid_argument` | `invalid credential name` |
| Malformed reference (REQ-CRED-009) | `invalid_argument` | `invalid credential reference` |
| Resolution names no credential of the tenant (REQ-CRED-010, REQ-CRED-011) | `not_found` | `credential not found` |
| Delete names no credential of the tenant (REQ-CRED-004) | `not_found` | `credential not found` |
| The Secrets port rejects the stored ciphertext (REQ-CRED-023) | `internal` | `stored credential cannot be decrypted` |
| The key service is unavailable | `unavailable` | the Secrets port's error, naming the scheme only |
| The audit event cannot be recorded (REQ-CRED-020) | `internal` | `credential audit cannot be recorded` |
| An external backend is unreachable | `unavailable` | the backend's error, naming the store only |

Missing and another tenant's credential share code and message, so a caller
cannot tell them apart. Errors use fixed messages and never echo the value,
the ciphertext or a raw reference. Log records may carry the tenant, the
credential name and the outcome, never the value or the ciphertext.

## 10. Test support

The BDD steps live in `internal/bdd/steps/credentials.go` and helpers beside
it, in the `internal/bdd` package:

- Each scenario gets a fresh SQLite file opened through the SQL store with
  this package's migration set, a simulated key service holding the named
  master key (reusing the secrets steps' helper), the fake clock from
  `core/coretest`, and the SQL auditor.
- Every resolution in the steps is made in a use context; the driver defaults
  it to principal `human:ana`, run `r1` and step `s1` unless the scenario
  names another. A scenario that asserts the audit names its fields.
- A **simulated external store** implements `credentials.Backend` in
  `internal/bdd`: it keeps records per tenant and exposes what it stored, so a
  scenario can assert ciphertext-only rows and that no SQL credential row
  exists. It never calls a vendor.
- A **gated Secrets wrapper** in `internal/bdd` signals when a resolution
  reaches `Decrypt` and waits until the scenario releases it; it produces the
  in-flight replacement of REQ-CRED-025 deterministically.
- A **failing auditor** in `internal/bdd` returns an error for REQ-CRED-020.
- Inspection steps read the `credentials` and `credential_audit` rows through
  the SQL store; they are test helpers, not a production API.

Unit tests in `platform/credentials` use fakes defined in their own `_test.go`
files. No test double enters production code, and no new test-only package is
added: `core/coretest` remains the only entry in `layering.yaml`'s
`test_only`, importable from `_test.go` files and from `internal/bdd`. The
gate uses no wall clock, no unseeded randomness and no live vendor call.
