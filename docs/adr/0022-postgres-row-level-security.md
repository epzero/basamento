# ADR-0022 — Postgres row-level security for the cloud profile

**Status:** accepted · **Date:** 2026-11-22 · **Decides:** WP-L8-02 part (b)
(EP0-214); amends the tenancy story in ADR-0008 for Postgres only.

## Context

ADR-0008 and `specs/auth/design.md` §8 name row-level security (RLS) as a
second line of defence behind repository tenant filters. Section 8 still says
“before each transaction” the store calls `set_config('app.tenant', $1, true)`.
In the tree today almost every repository statement runs on the pooled
`Execer` without `InTx`; the authorization hot path
(`auth/authz.SQLStore.BindingsForCheck`) is a single `Query`. Transaction-local
`set_config` does not apply there. Session-level `set_config` survives until
`RESET` or until a rolled-back transaction that set it inside the transaction
(reverts to empty string, probes EP0-214). A superuser connection bypasses RLS
unless `SET ROLE` is used (probes EP0-214). Shared goose migrations cannot
contain `ENABLE ROW LEVEL SECURITY` because SQLite has no RLS. Column names
differ (`tenant` on `authz_bindings`, `tenant_id` on credentials, audit and
stream tables).

This ADR closes those gaps so an implementer of WP-L8-02 part (b) needs no
further architecture choice.

## Decision

| Rule | Detail |
| --- | --- |
| **D1 — Tenant variable without mandating `InTx`** | On Postgres only, `platform/sql` applies `app.tenant` before every statement that touches a tenant table. **Outside** an `InTx` callback it uses `SELECT set_config('app.tenant', $1, false)` once per physical connection when the context tenant differs from the value already on that connection, then runs the statement. **Inside** `InTx` it uses `set_config(..., true)` once at transaction begin. When the context has no tenant, repositories already return `invalid_argument` before SQL; if a statement is attempted anyway, the store **rejects** it rather than running unscoped. On connection release to the pool, a driver `SessionResetter` runs `RESET app.tenant` so the next checkout never inherits a tenant. Implementation is a **pgx stdlib `driver.Connector` wrapper** registered only for the application pool (not for the short-lived migrate connection). The wrapper caches the last applied tenant on the connection and clears that cache on `Rollback` because a rolled-back session `set_config` leaves an empty string, not SQL NULL (probes EP0-214). |
| **D2 — Owner and application roles** | **Production (cloud profile):** configuration carries two libpq URLs: `sql.migrate_url` (owner role: owns tables, runs goose, may `CREATEROLE` when the operator grants it) and `sql.url` (application role: `LOGIN`, no `BYPASSRLS`, not table owner). Passwords come only from the secrets port (file or env substitution in URL), never from committed config. `platform/sql.Open` connects with `migrate_url`, applies migrations, then opens the long-lived pool on `sql.url`. When the managed service lacks `CREATEROLE`, an operator provisions `bas_owner` and `bas_app` once; the postgres-only migration assumes they exist and only grants and policies. **Gate / embedded Postgres:** migrations still run as the admin user (ADR-0015); the application pool uses the `bas_app` role via `sql.url` rewritten to `bas_app` after migrate, or equivalent `SET ROLE bas_app` on pool acquire documented in `specs/sql/design.md` §4. Scenarios prove `rolbypassrls = false` and `FORCE ROW LEVEL SECURITY` by opening the store as `bas_app`, not as the embedded superuser. |
| **D3 — Postgres-only migrations** | Shared goose SQL stays dialect-neutral. **Postgres-only** RLS, roles and grants live in a **second embedded migration set** per owning package (e.g. `migrations/postgres/*.sql`), applied only when `Store.Dialect() == "postgres"`, with goose table name `goose_db_version_postgres` so versions are independent of the shared set. `platform/sql.Open` runs the shared set first, then `Up` on the postgres set on one migrate connection. SQLite skips the second pass. Goose v3.24.1 has no per-file dialect filter in one FS; a second provider and table name is the supported mechanism (probes: module API). |
| **D4 — Which tables** | Every table that carries a tenant key column in production SQL gets `ENABLE` + `FORCE RLS` and one `tenant_isolation` policy comparing that column to `current_setting('app.tenant', true)`. **Initial set:** `credentials`, `audit_events`, `authz_bindings` (`specs/auth` postgres migrations); `eventlog_runs`, `eventlog_events`, `eventlog_outbox`, `relay_leases` (`stream/eventlog` postgres migrations); `telemetry_spans` (`platform/telemetry` postgres migrations, and the span store stops creating the table only on Postgres outside goose — table creation moves into the postgres migration set). Column name in the policy matches the table (`tenant` vs `tenant_id`). The package that owns the table owns its postgres migration fragment. **`go tool gate rls`** (new check, designer spec) fails if any migrated postgres table with column `tenant` or `tenant_id` lacks a `tenant_isolation` policy in catalog `pg_policies` after migrations in the Postgres lane fixture. |
| **D5 — Fail-closed** | Unset variable: `current_setting('app.tenant', true)` is SQL NULL → policy unknown → **no rows** (probes EP0-214). After `RESET` or rolled-back session set: empty string → still **no rows**. Failed `set_config` or missing role: statement **errors**, never runs unscoped. |

### Authorization hot path (measured)

Probes on embedded Postgres 16 (EP0-214, `go test -run RLSProbe214` in
`internal/bdd/sqltest`, since removed): steady-state autocommit
`set_config` + `Query` + `RESET` ≈ **45–59 µs per statement** on loopback;
`BEGIN` + `set_config(..., true)` + `Query` + `COMMIT` ≈ **49–57 µs per
statement** but **four** server round trips per `BindingsForCheck` vs **one**
today. With **D1**, a reused connection with the same tenant adds **zero**
extra round trips; a cold or rotated connection adds **one** `set_config`
(≈45 µs measured) before the binding query. Mandating `InTx` on every
repository call was rejected for this cost.

### `platform/sql` API and behaviour (summary)

See `specs/sql/design.md` §4 (D1–D5) for implementer detail. Highlights:

- `Open` accepts optional `OpenOptions{ MigrateURL, PostgresMigrations fs.FS }`
  (names TBD in implementation); migrate URL used only until both goose passes
  finish; application pool uses `rawURL`.
- Postgres application `sql.Open` uses the tenant-aware connector wrapper.
- `Execer` methods call the connector prelude; `InTx` sets transaction-local
  tenant at begin.
- SQLite: options ignored; no connector wrapper.

Cross-references: `specs/auth/design.md` §8 (RLS policy shape and roles),
`docs/architecture/auth.md` §2, ADR-0015 (Postgres lane unchanged except
store opens as `bas_app` in cloud scenarios).

## Alternatives considered

| Option | Cost | Verdict |
| --- | --- | --- |
| **A. Every tenant access through `InTx`** | Adds `BEGIN`/`COMMIT` to every repository method; **four** round trips on `BindingsForCheck` vs one today; ~54 µs/stmt measured but multiplied by pool sync. Simplest mentally. | Rejected. Authorization and most reads stay autocommit; cost on every check is too high. |
| **B. Session `set_config` only in repositories** | Each repository duplicates tenant prelude; easy to miss one table; no pool `RESET` → cross-tenant leak on connection reuse. | Rejected. Centralising in `platform/sql` keeps second-line defence uniform. |
| **C. Tenant-aware `driver.Connector` + pool `RESET` (D1)** | One wrapper package; implement `SessionResetter`, invalidate cache on `Rollback`; ~0–1 extra round trip per statement on hot path. | **Chosen.** |
| **D. Implicit transaction per statement** | Same round-trip cost as A; `Query` rows hold transaction until `Close` — fragile with `database/sql`. | Rejected. |
| **E. Single URL + `SET ROLE` for app** | One secret; migrate and app share login; `SET ROLE` after connect. Superuser migrate can forget `SET ROLE`. | Rejected for production. Acceptable only as BDD fallback when second URL is awkward. |
| **F. Dialect-suffixed files in one goose FS** | Goose 3.24.1 has no built-in `.postgres.sql` filter in one provider. | Rejected. Second provider + table name is explicit. |
| **G. Go migrations with `if postgres` branches** | Loses SQL reviewability; harder gate check. | Rejected for RLS DDL. |
| **H. SQL valid on both dialects** | No RLS statements exist on SQLite. | Impossible for policies. |

## Consequences

- WP-L8-02 part (b) implements the wrapper, dual URL open path, postgres
  migration sets, policies on the table list in D4, BDD steps that open as
  `bas_app`, and `gate rls`.
- Config work (designer): `sql.migrate_url` beside `sql.url` in cloud profile,
  both via secrets.
- `platform/telemetry` moves Postgres DDL into goose postgres migrations.
- Embedded profile unchanged (SQLite, no RLS).
- Layering: new `platform/sql/pgtenant` or unexported files in `platform/sql`;
  no new outward edges without `layering.yaml` update in implementation WP.

## Revert

Revert the merge commit; remove ADR-0022, README row, and design § deltas;
restore prior `specs/auth/design.md` §8 wording.

## Implementation changes these files

| Path | Change |
| --- | --- |
| `docs/adr/0022-postgres-row-level-security.md` | This record. |
| `docs/adr/README.md` | Index row. |
| `specs/auth/design.md` | §8 numbered D1–D5 aligned with this ADR. |
| `specs/sql/design.md` | §4 tenancy + postgres RLS mechanics. |
| `docs/architecture/auth.md` | RLS mechanism pointer. |
| `docs/roadmap/L8-auth.md` | Note ADR-0022. |