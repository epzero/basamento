# ADR-0022 — Postgres row-level security for the cloud profile

**Status:** accepted · **Date:** 2026-11-22 · **Amended 2026-11-22:** D1–D8,
single goose track, `bas_app` only (revision EP0-214 r2).  
**Decides:** WP-L8-02 part (b) (EP0-214); amends the tenancy story in ADR-0008
for Postgres only.

## Context

ADR-0008 and `specs/auth/design.md` §8 name row-level security (RLS) as a
second line of defence behind repository tenant filters. The first draft said
“before each transaction” with transaction-local `set_config`; almost every
repository statement—including `auth/authz.SQLStore.BindingsForCheck`—runs
autocommit on the pooled `Execer`. Shared goose migrations must stay valid on
SQLite. Column names differ (`tenant` vs `tenant_id`). Composition of migration
sets from several packages is unset in the tree today (`cmd/kit` ships a
baseline migration only; BDD merges with ad hoc renumbering). Three reviewer
passes (P, R, S) and revision r2 refined mechanics below.

## Decision

This ADR is the **only** numbered list **D1**–**D8**. Other docs cite these
numbers without redefining them.

| Rule | Detail |
| --- | --- |
| **D1 — Tenant scope per statement** | On Postgres with **RLS mode** enabled on `Open`, the application pool uses a **pgx stdlib `driver.Connector` wrapper** (not the migrate connection). Before **every** statement on that connection the wrapper reads `core.TenantFrom(ctx)` (and D6 system scope when present). If the context has **no** tenant and no allowed system scope, return `invalid_argument` **before SQL** (repositories already do this; the wrapper repeats it for paths that bypass `Execer`). If the tenant differs from the value **cached for this physical connection**, run `SELECT set_config('app.tenant', $1, false)` with a bound parameter, then run the statement. **Inside `Store.InTx` each statement is treated the same**—there is no “pin at begin” transaction-local variable (avoids S-1). **Cache invariants:** cache holds the last **successfully** applied `(tenant, system scope)` pair; set cache to **unknown** after any error on the connection, after `Rollback`, and after a failed `Commit`; a rolled-back **session** `set_config` leaves `current_setting('app.tenant', true)` as empty string (probed), not SQL NULL—treat empty as mismatch and re-apply. **No `RESET app.tenant` on pool reuse or return:** `database/sql` may call `ResetSession` before the next checkout (P-2); unconditional `RESET` would force `set_config` on nearly every statement (P-3: 10000/10000 with RESET vs 10/10000 for two tenants on ten conns). A leftover GUC is harmless because **no** application statement runs without naming its tenant. The wrapper's `ResetSession` **forwards to pgx only** (does not clear `app.tenant`). Intercept **every** statement path (`ExecContext`, `QueryContext`, `PrepareContext`, `BeginTx`, …) and **embed-forward** optional `database/sql/driver` interfaces implemented by pgx's `stdlib.Conn` (`ResetSession`, `CheckNamedValue`, `Ping`, …). Nothing tenant-related at `Connect` time (`database/sql` uses `context.Background()` for background opens, P report). **Topology:** `sql.url` must reach Postgres with **session** affinity (direct server or **session-mode** pooler). Transaction-mode or statement-mode PgBouncer is **unsupported**—session GUCs belong to whichever client last used the backend → empty/wrong results or cross-tenant bleed while repository filters still apply; a filter bug plus pooler bleed could expose another tenant's rows. |
| **D2 — Roles, credentials, URLs** | One application role name: **`bas_app`** (`LOGIN`, not superuser, no `BYPASSRLS`, not table owner). **Migrations never `CREATE ROLE` and never embed passwords.** The operator (or gate fixture) creates `bas_app` before the first migrate. Tables are owned by **whichever login runs migrations** (`sql.migrate_url`). Postgres-only fragments `GRANT` least privilege to `bas_app` (`SELECT, INSERT, UPDATE, DELETE` on tenant tables; `UPDATE` on `audit_seq`; no DDL; nothing on goose metadata). Each fragment **precondition:** fail with a clear message if `bas_app` is missing (e.g. `DO $$ … $$` in `-- +goose StatementBegin`, probed acceptable to goose). Operator provisioning SQL is documented in `docs/architecture/auth.md` (including `REVOKE CREATE ON SCHEMA public FROM PUBLIC` on PostgreSQL before 15). Two URLs: `sql.migrate_url` (runs goose) and `sql.url` (`bas_app` pool). `Open` in RLS mode migrates on migrate URL, closes that handle, opens the pool on `sql.url`. Both URLs redacted like today. **Startup self-check (RLS mode):** on the application pool, `SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`; refuse `Open` when either is true with **`invalid_argument`**, message names the attribute (`rolsuper` / `rolbypassrls`), never a URL. RLS mode is an explicit `Open` option the cloud profile sets; without it, behaviour stays as today (admin Postgres in ADR-0015 conformance). **Gate fixture:** `internal/bdd/sqltest` creates `bas_app` with a **random password** from the seeded generator, migrates on admin URL, opens the store on a real `bas_app` URL—the same path as production. |
| **D3 — One goose track; exclude `.postgres.sql` on SQLite** | Postgres-only fragments are files `NNNNN_<name>.postgres.sql` in the **same** `fs.FS` as shared migrations, versions interleaved in apply order (policy after table create). `platform/sql` passes `goose.WithExcludeNames` for `*.postgres.sql` when dialect is SQLite; `maxMigrationVersion` and `checkSchemaVersion` in `migrate.go` apply the same exclusion (P-8). One `goose_db_version` table, one advisory lock. `Down` fragments drop policies and `NO FORCE` / `DISABLE`. **Composition** of several packages' FS into one ordered set is **not** decided here: today there is no kit merge (`cmd/kit` baseline only; BDD uses `combinedAuthMigrations`-style renumbering). A **follow-up (tracked by the lead)** decides stable version bands and merge rules before cloud rollout. |
| **D4 — Tables, exemptions, schema rule** | **Tenant tables (policies):** `credentials`, `audit_events` (`tenant_id`, `platform/credentials`); `authz_bindings` (`tenant`, `auth/authz`); `eventlog_runs`, `eventlog_events`, `eventlog_outbox`, `eventlog_relay_leases` (`tenant_id`, `stream/eventlog`). **`telemetry_spans` is not in scope:** the span store refuses non-SQLite today (`platform/telemetry/spanstore.go`); a Postgres span store needs its own decision (purge and cross-run probe are cross-tenant). **Exempt (no tenant policy):** `audit_seq` (global row; `bas_app` needs `UPDATE`; residual channel: monotonic ids reveal aggregate audit volume across tenants—accepted for now); goose version table (migrate login only); BDD conformance tables `lists`, `items` (`internal/bdd/sqltest`, test schema). **Schema rule:** tenant tables use composite keys/FKs that include the tenant column; policy `WITH CHECK` pins written `tenant`/`tenant_id` to the session. **Residual:** PostgreSQL may still raise **`23503`** on cross-tenant FK attempts because FK checks can see referenced rows under RLS (probed EP0-214 r2; standard PG semantics)—that can leak that a key exists in another tenant; repositories must not attempt cross-tenant references. **`go tool gate rls`** is **not** part of part (b); use the schema rule as a review rule until a follow-up (tracked by the lead). |
| **D5 — Fail closed** | Never set: `current_setting('app.tenant', true)` is SQL **NULL** → no rows. After `RESET` or rolled-back session `set_config`: **empty string** → no rows (both probed). Failed `set_config` or missing grants: statement errors, never unscoped execution. Policies have **no `TO` clause**; `ENABLE` + `FORCE` on every D4 tenant table. |
| **D6 — System scope (relay discovery only)** | The only cross-tenant **read** on a tenant table today is `SELECT DISTINCT tenant_id, run_id FROM eventlog_outbox WHERE relayed_at IS NULL` (`stream/eventlog/relay.go`). Lease paths use per-run tenant in SQL and should run under `core.WithTenant` (no extra policy). `platform/sql` exports a context marker, e.g. `WithSystemScope(ctx, ScopeRelayDiscovery)` (names finalised in implementation), setting **`app.system`** instead of `app.tenant`; cache tracks the pair; unknown scope values rejected. `eventlog_outbox` gains a **second permissive `FOR SELECT` policy:** `USING (relayed_at IS NULL AND current_setting('app.system', true) = 'relay')` beside `tenant_isolation`; no write path, no other table. Allowed call sites pinned by a test in `platform/sql` (today one file in `stream/eventlog`). Not a capability—any `bas_app` session could set the GUC in raw SQL; this guards repository mistakes. Rejected: `BYPASSRLS` relay pool, per-tenant iteration (no registry), `SECURITY DEFINER` under `FORCE` (owner subject to policies; no bypass). Probed: OR policies; `app.system=relay` returns two tenants' pending rows; `INSERT` under relay scope → **`42501`** (SQLSTATE in report). |
| **D7 — Errors, operations, rollout** | **(a)** SQLSTATE **`42501`** (RLS / grant) stays **`core.CodeInternal`** with `Details["sqlstate"]`; message must not name table, policy, or key—defect signal, not user authorization (`specs/sql/design.md`). **(b)** Backups: under `FORCE`, `pg_dump` as table owner fails loudly (P-6); backups need a role with `BYPASSRLS` or superuser plus a deliberate process. **(c)** Rollout: release **N** ships connector + RLS mode **without** policy fragments; release **N+1** ships fragments; rollback past N+1 runs `Down` under change control—never ad hoc disable. Old binary without connector against forced tables sees no rows (fail closed). **(d)** `ENABLE`/`FORCE`/`CREATE POLICY` take `ACCESS EXCLUSIVE` briefly. **(e)** FK behaviour per D4; cross-tenant insert probe returned **`23503`** (report). |
| **D8 — Threat model and proof** | **Defends:** repository forgets tenant filter while `bas_app` is legitimate. **Does not defend:** raw SQL setting GUCs, `SET ROLE`, bypass login (D2 self-check), transaction-mode poolers, covert channels in D4 (`audit_seq`, FK oracle). **Proof REQ-AUTH-027–029** (scenario text unchanged): BDD seeds two tenants via the **public store**; a second raw `database/sql` pool on the same `bas_app` URL **without** the wrapper runs `BEGIN; SELECT set_config('app.tenant', 'acme', true); SELECT …; COMMIT` for AUTH-027-01 and a transaction **without** `set_config` for AUTH-028-01 (the store rejects tenant-less app SQL; scenarios prove **database** policy). REQ-AUTH-028 “every transaction” read as every statement through the wrapper setting the variable, hence in every SQL transaction that runs—**no** requirement text change (designer note only). Designer still adds `sql.migrate_url` to `specs/config` (R-5). |

### Authorization hot path (measured)

Embedded Postgres 16, revision r2 / finder P probes (loopback): **`set_config` + query** ≈ **56 µs**; query when already set ≈ **46 µs**; **`BEGIN` + local set + query + `COMMIT`** ≈ **96 µs** on this host. Hit rate depends on traffic: consecutive statements of one request mostly reuse the connection cache; P-3 measured **10/10000** extra `set_config` trips for two tenants alternating on ten connections **without** unconditional RESET (worst case: **one extra round trip per statement**). Reject **mandate `InTx` everywhere** primarily because **`BindingsForCheck` goes from one round trip to four** on non-loopback latency, not because of the µs ratio.

### `platform/sql` API (pointer)

`specs/sql/design.md` §4 describes `Open` options, wrapper invariants, error mapping, and migration naming—by reference to **D1**–**D7** here.

## Alternatives considered

| Option | Cost | Verdict |
| --- | --- | --- |
| **A. Every tenant access through `InTx`** | Four round trips per autocommit check | **Rejected** (D1, cost section). |
| **B. Session `set_config` only in repositories** | Leak, duplication | **Rejected**. |
| **C. Connector + cache, no unconditional RESET (D1)** | 0–1 `set_config` per statement by traffic | **Chosen.** |
| **D. Implicit transaction per statement** | `Rows` lifetime; 4 RT | **Rejected**. |
| **E. Single URL + `SET ROLE`** | Superuser leak risk | **Rejected** for production. |
| **F. `*.postgres.sql` excluded on SQLite (goose `WithExcludeNames`)** | `migrate.go` must exclude in version math | **Chosen** (D3; probed API in goose v3.24.1). |
| **G. Second provider + `goose_db_version_postgres`** | Two version tracks; `checkSchemaVersion` gap; cross-package ordering unclear | **Rejected** (was first draft D3). |
| **H. Go `if postgres` migrations** | Reviewability | **Rejected** for RLS DDL. |

## Consequences

- Part (b) slices: **(A)** `platform/sql` connector, RLS `Open` option, fixture, conformance on test tables; **(B)** config `sql.migrate_url`; **(C)** auth postgres fragments + AUTH-027–029 steps; **(D)** eventlog fragments + relay D6; follow-ups: **migration composition ADR**, **kit cloud wiring**, **`gate rls`**, Postgres span store.
- Embedded profile unchanged (SQLite).
- Layering: wrapper stays in `platform/sql`; no outward edge without `layering.yaml` update in implementation WP.

## Revert

Revert the merge commit; remove ADR-0022 and cross-references; restore prior design wording.

## Implementation changes these files

| Path | Change |
| --- | --- |
| `docs/adr/0022-postgres-row-level-security.md` | This record. |
| `docs/adr/README.md` | Index row. |
| `specs/auth/design.md` | §8 policy + proof pointers. |
| `specs/sql/design.md` | §4 `platform/sql` mechanics. |
| `docs/architecture/auth.md` | RLS + operator provisioning. |
| `docs/roadmap/L8-auth.md` | ADR-0022 pointer. |