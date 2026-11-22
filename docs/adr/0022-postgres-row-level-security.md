# ADR-0022 — Postgres row-level security for the cloud profile

**Status:** accepted · **Date:** 2026-11-22  
**Decides:** WP-L8-02 part (b) (EP0-214); amends the tenancy story in ADR-0008
for Postgres only.

## Context

ADR-0008 and `specs/auth/design.md` §8 name row-level security (RLS) as a
second line of defence behind repository tenant filters. Almost every
repository statement—including `auth/authz.SQLStore.BindingsForCheck`—runs
autocommit on the pooled `Execer`. Shared goose migrations must stay valid on
SQLite. Column names differ (`tenant` vs `tenant_id`). Multi-package migration
composition is decided in **ADR-0023** (EP0-216): today `cmd/kit` ships only a
baseline migration and the BDD harness merges the sets with ad hoc renumbering.

## Decision

This ADR is the **only** numbered list **D1**–**D8**. Other docs cite these
numbers without redefining them.

### D1 — Tenant scope per statement

On Postgres with **RLS mode** enabled on `Open`, the application pool uses a
pgx stdlib `driver.Connector` wrapper (not the migrate connection). Before
every statement the wrapper reads `core.TenantFrom(ctx)` and D6 system scope
when present. With no tenant and no allowed system scope, return
`invalid_argument` before SQL. When the cached pair `(tenant, scope)` differs
from the statement's, the wrapper runs **one** SQL statement:
`SELECT set_config('app.tenant', $1, false), set_config('app.system', $2, false)`.
Tenant scope uses `(tenant, '')`; system scope uses `('', scope)`. Both GUCs are
always written together so a stale `app.system` cannot leave the relay discovery
policy enabled during a tenant statement (measured on PostgreSQL 16: with
`relay` left set, a tenant-only `set_config` still shows all pending outbox
rows). Then run the application statement. Inside `Store.InTx` each statement
uses the same rule—there is no pin at transaction begin.

**Cache invariants:**

- Cache holds the last successfully applied `(tenant, system scope)` pair.
- Set cache to unknown after any error on the connection, after `Rollback`, and
  after a failed `Commit`.
- A rolled-back session `set_config` leaves `current_setting('app.tenant',
  true)` as empty string (measured on PostgreSQL 16), not SQL NULL—treat empty
  as mismatch and re-apply.

**No `RESET app.tenant` on pool reuse:** `database/sql` may call
`ResetSession` before the next checkout; unconditional `RESET` would force
`set_config` on nearly every pooled statement (measured 10000/10000 with RESET
vs 10/10000 for two tenants on ten connections without RESET). A leftover GUC is
harmless because no application statement runs without naming its tenant. The
wrapper's `ResetSession` forwards to pgx only.

Intercept every statement path (`ExecContext`, `QueryContext`, `PrepareContext`,
`BeginTx`, …) and embed-forward optional interfaces on pgx's `stdlib.Conn`
(`ResetSession`, `CheckNamedValue`, `Ping`, …). Nothing tenant-related at
`Connect` time (`database/sql` opens background connections with
`context.Background()`).

A `database/sql.Tx` does **not** pin the tenant: a transaction begun with one
tenant's context applies, to each later statement, the tenant of **that
statement's** context. Callers pass one context per unit of work; repositories
must not assume the tenant is fixed at `BeginTx`.

**Topology:** `sql.url` must reach PostgreSQL with session affinity (direct
server or session-mode pooler). Transaction-mode or statement-mode poolers are
unsupported—session GUCs follow whichever client last used the backend.

### D2 — Roles, credentials, URLs

One application role: **`bas_app`** (`LOGIN`, not superuser, no `BYPASSRLS`,
not table owner). Migrations never `CREATE ROLE` and never embed passwords. The
operator or gate fixture creates `bas_app` before the first migrate. Tables are
owned by whichever login runs migrations (`sql.migrate_url`).

**`bas_app` grants** (postgres-only fragments):

| Object | Grants |
| --- | --- |
| `credentials`, `audit_events`, `authz_bindings`, `eventlog_runs`, `eventlog_events`, `eventlog_outbox`, `eventlog_relay_leases` | `SELECT`, `INSERT`, `UPDATE`, `DELETE` |
| `audit_seq` | `SELECT`, `UPDATE` |
| goose version table | none |

Each fragment first checks that `bas_app` exists and fails with a message that
names the role if it does not (for example a `DO` block inside
`-- +goose StatementBegin`). Operator steps: `docs/architecture/auth.md`.

Two URLs: `sql.migrate_url` (goose) and `sql.url` (`bas_app` pool). `Open` in
RLS mode migrates on the migrate URL, closes that handle, opens the pool on
`sql.url`. Both URLs redact like today.

**RLS mode refuses `Open`** with `invalid_argument` (message names the
condition, never a URL) when:

1. `rawURL` is not a Postgres URL.
2. `MigrateURL` is missing.
3. The migrate login and application login are the same role (`current_user` on
   a migrate handle vs a short-lived check on `sql.url`—see below).
4. The application role has `rolsuper` or `rolbypassrls` (self-check query
   below).

**Role self-check:** before registering the tenant wrapper, open a short-lived
plain `pgx` connection on `sql.url`, run
`SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`,
refuse if either is true, then close the handle. The wrapper's “no tenant, no
SQL” rule does not apply to this pre-wrapper check.

**RLS mode** affects only the tenant wrapper, the two URLs, and the refusal
checks above. A Postgres open **without** RLS mode is still used for the
conformance lane and administration (admin login, ADR-0015); that login bypasses
RLS. Policy fragments still run on every Postgres migrate (D3).

**Gate fixture:** `internal/bdd/sqltest` creates `bas_app` on the cluster once
(idempotently, before any scenario database is migrated), with a random password
from the seeded generator; migrates on the admin URL; opens the store on a real
`bas_app` URL in RLS scenarios—the same path as production.

Config surface for `sql.migrate_url`: **EP0-217**.

### D3 — One goose track; exclude `.postgres.sql` on SQLite

On **every Postgres open** (RLS mode or not), goose applies the shared
migrations and every `.postgres.sql` fragment. SQLite opens exclude those files.
Reasons: one schema per dialect—a Postgres database always carries its policies,
never a mode-dependent schema; goose refuses later `Up` when an unapplied version
is lower than one already recorded, so skipping fragments on first migrate would
block applying them without out-of-order handling.

Postgres-only files are `NNNNN_<name>.postgres.sql` in the same `fs.FS` as
shared migrations, versions interleaved in apply order. Goose
`WithExcludeNames` matches **exact basenames**, not globs: `platform/sql` walks
the FS, collects every base name ending in `.postgres.sql`, and passes that list
on SQLite. `maxMigrationVersion` and `checkSchemaVersion` skip the same names—a
naive walk that counts `00003_x.postgres.sql` as version 3 on SQLite would
wrongly trip “database schema newer than binary” otherwise. One
`goose_db_version` table, one advisory lock. `Down` fragments drop policies and
`NO FORCE` / `DISABLE`.

Stable version bands and one merged goose track: **ADR-0023** (EP0-216).

### D4 — Tables, exemptions, schema rule

**Tenant tables (policies):**

| Table | Tenant column | Owner package |
| --- | --- | --- |
| `credentials`, `audit_events` | `tenant_id` | `platform/credentials` |
| `authz_bindings` | `tenant` | `auth/authz` |
| `eventlog_runs`, `eventlog_events`, `eventlog_outbox`, `eventlog_relay_leases` | `tenant_id` | `stream/eventlog` |

`telemetry_spans` is **not** in scope: the span store refuses non-SQLite today; a
Postgres span store is **EP0-219**.

**Exempt (no tenant policy):** `audit_seq` (global row; residual channel:
monotonic ids reveal aggregate audit volume—accepted for now); goose version
table; BDD `lists`, `items`.

**Schema rule:** primary keys and foreign keys on tenant tables include the
tenant column; policy `WITH CHECK` pins written rows to the session tenant. A
foreign key without the tenant column on a tenant table would break isolation
(future **`go tool gate rls`**, **EP0-218**, will enforce this).

With composite keys and `WITH CHECK`, a `23503` on insert references only keys
within the caller's tenant—the error text is the same whether the referenced
run exists for another tenant or does not exist at all (measured on PostgreSQL
16 with `eventlog_events` composite FK).

`go tool gate rls` is **not** part of WP-L8-02 part (b); use the schema rule as a
review rule until **EP0-218**.

### D5 — Fail closed

Never set: `current_setting('app.tenant', true)` is SQL NULL → no rows. After
`RESET` or rolled-back session `set_config`: empty string → no rows (measured on
PostgreSQL 16). Failed `set_config` or missing grants: statement errors, never
unscoped execution. Policies have no `TO` clause; `ENABLE` + `FORCE` on every D4
tenant table.

### D6 — System scope (relay discovery only)

The only cross-tenant read on a tenant table in production today is
`SELECT DISTINCT tenant_id, run_id FROM eventlog_outbox WHERE relayed_at IS
NULL` (`stream/eventlog/relay.go`). Slice **D** changes the relay's lease
statements to run under `core.WithTenant(ctx, run.tenant)` (today they pass the
tenant only as a SQL parameter).

`platform/sql` exports a context marker (e.g. `WithSystemScope(ctx,
ScopeRelayDiscovery)`), setting `app.system` with `app.tenant` cleared via the
paired `set_config` (D1); cache tracks the pair; unknown scopes rejected.

On `eventlog_outbox`:

```sql
CREATE POLICY relay_discover ON eventlog_outbox FOR SELECT
  USING (relayed_at IS NULL AND current_setting('app.system', true) = 'relay');
```

(beside `tenant_isolation`; permissive OR for `SELECT` only.) Allowed call
sites pinned by a test in `platform/sql`. The GUC is not a capability—raw SQL
on `bas_app` could set it; this guards repository mistakes.

### D7 — Errors, operations, rollout

**(a)** SQLSTATE `42501` stays `core.CodeInternal` with `Details["sqlstate"]`;
message must not name table, policy, or key (`specs/sql/design.md`).

**(b)** Backups: under `FORCE`, `pg_dump` as a **non-superuser table owner**
fails loudly (“query would be affected by row-level security policy”). Dump as
**superuser** or a role with **`BYPASSRLS`** reads every row. Backups run
deliberately as such a role, not as `bas_app`.

**(c)** Release **N**: connector + RLS mode, no policy fragments. Release
**N+1**: policy fragments. Rollback past N+1 runs `Down` under change control.
Old binary without connector against forced tables sees no rows.

**(d)** `ENABLE` / `FORCE` / `CREATE POLICY` hold `ACCESS EXCLUSIVE` briefly.

**(e)** FK violations use `23503` within the caller's tenant only (D4 schema
rule).

### D8 — Threat model and proof

**Defends:** a repository that forgets its tenant filter while the process holds
a legitimate `bas_app` session.

**Does not defend:** raw SQL setting GUCs, `SET ROLE`, bypass login (D2
self-check), transaction-mode poolers, `audit_seq` volume channel (D4).

**Proof REQ-AUTH-027–029** (scenario text unchanged): BDD seeds two tenants via
the public store; a second raw `database/sql` pool on the same `bas_app` URL
without the wrapper runs the SQL transactions in AUTH-027-01 and AUTH-028-01.
REQ-AUTH-028 “every transaction” means every statement through the wrapper sets
the variable (designer interpretation only). No `permissions.feature` change.

Kit cloud wiring: **EP0-220**.

### Authorization hot path

Measured on embedded PostgreSQL 16 over loopback: `set_config` + query ≈ 56 µs;
query when tenant already on connection ≈ 46 µs; `BEGIN` + set + query +
`COMMIT` ≈ 96 µs. Hit rate depends on traffic (worst case one extra round trip
per statement). Mandating `InTx` on every call is rejected because
`BindingsForCheck` goes from one round trip to four on real network latency.

### `platform/sql` API

See `specs/sql/design.md` §4 by reference to D1–D7.

## Alternatives considered

| Option | Cost | Verdict |
| --- | --- | --- |
| **A. Every tenant access through `InTx`** | Four round trips per autocommit check | Rejected. |
| **B. Session `set_config` only in repositories** | Leak, duplication | Rejected. |
| **C. Connector + cache, no unconditional RESET (D1)** | 0–1 `set_config` per statement by traffic | Chosen. |
| **D. Implicit transaction per statement** | `Rows` lifetime; 4 RT | Rejected. |
| **E. Single URL + `SET ROLE`** | Superuser leak risk | Rejected for production. |
| **F. `*.postgres.sql` excluded on SQLite (exact names)** | `migrate.go` must exclude in version math | Chosen (D3). |
| **G. Second provider + second version table** | Two tracks; version policy gap | Rejected. |
| **H. Go `if postgres` migrations** | Reviewability | Rejected for RLS DDL. |

## Consequences

Part (b) implementation slices (ticketed after this ADR is accepted):

- **(A)** `platform/sql` connector, RLS `Open` option, fixture, conformance on
  test tables.
- **(B)** config `sql.migrate_url` (**EP0-217**).
- **(C)** auth postgres fragments + AUTH-027–029 steps.
- **(D)** eventlog fragments + relay D6.

Migration composition: **ADR-0023** (EP0-216). Follow-ups: **EP0-218** (`gate
rls`), **EP0-219** (Postgres span store), **EP0-220** (kit cloud wiring).

Embedded profile unchanged (SQLite). Wrapper stays in `platform/sql`.

## Revert

Revert the merge commit; remove ADR-0022 and cross-references; restore prior
design wording.

## Implementation changes these files

| Path | Change |
| --- | --- |
| `docs/adr/0022-postgres-row-level-security.md` | This record. |
| `docs/adr/README.md` | Index row. |
| `specs/auth/design.md` | §8 policy + proof pointers. |
| `specs/sql/design.md` | §4 `platform/sql` mechanics. |
| `docs/architecture/auth.md` | RLS + operator provisioning. |
| `docs/roadmap/L8-auth.md` | ADR-0022 pointer. |