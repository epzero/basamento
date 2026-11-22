# SQL store: design notes for the implementer

Work package WP-L1-01, owned directory `platform/sql`. Requirements:
[`requirements.md`](requirements.md). Decisions: ADR-0001 (pure Go,
`CGO_ENABLED=0`), ADR-0004 (ports selected by URL, one conformance suite for
both implementations).

## 1. Shape

- One package `platform/sql` opens a store from a URL:
  `sqlite:///<path>` (also `sqlite://<relative path>`) or
  `postgres://user:password@host:port/db?…`. Any other scheme, or a URL with no
  database path, is `invalid_argument` (REQ-SQL-002) and nothing is created.
- SQLite URLs are read as RFC 3986: in `sqlite://data/kit.db` the first path
  segment is the authority and what follows is the path, so the database is
  `data/kit.db` relative to the working directory, made absolute before the
  driver sees it (REQ-SQL-019). `sqlite:///:memory:` and `sqlite://:memory:`
  keep the literal `:memory:` and are never passed through `filepath.Abs`
  (REQ-SQL-021). A SQLite URL with a query or a fragment is `invalid_argument` (REQ-SQL-020),
  and the legacy `postgresql` scheme is `invalid_argument` (REQ-SQL-002): the
  store's own parameters are set through the DSN, not through a user-supplied
  query.
- Opening takes the **migration set** as an `fs.FS` (in production an
  `embed.FS`), so the conformance suite can open stores with its own sample
  migrations. Migrations are goose migrations written once for both dialects.
- Errors are `*core.Error`. Driver errors are mapped once, per dialect, to
  platform codes, and unmapped errors are `internal`:
  - `conflict`: unique and foreign-key violations (`23505`, `23503`;
    REQ-SQL-014, REQ-SQL-015).
  - `invalid_argument`: not-null and check violations (`23502`, `23514`; the
    SQLite `NOT NULL` and constraint codes) (REQ-SQL-022). The value the caller
    passed breaks the schema, so retrying it unchanged never succeeds.
  - `unavailable` with `Retryable: true` (REQ-SQL-023): serialization failure
    and deadlock (`40001`, `40P01`), the connection and resource classes
    (`08xxx`, `53xxx`, `57P01`–`57P03`), connection refused, DNS failure or
    timeout, and SQLite `SQLITE_BUSY` and `SQLITE_LOCKED`.
  - `not_found`: no row.
- Open errors carry the redacted URL, and a URL is redacted before it reaches
  any message, detail or cause (REQ-SQL-004): the userinfo password and the
  values of the credential query parameters (`password`, `sslpassword`,
  `passfile`, `sslcert`, `sslkey`, `sslrootcert`) become literal `***`
  (REQ-SQL-024). When the URL carried any of them, driver text is replaced by
  the fixed cause `"driver error withheld"` (REQ-SQL-025): a driver may quote
  its connection parameters.

## 2. Migrations

- Each migration runs in its own transaction, on both dialects, so a failed
  one leaves no trace (REQ-SQL-007).
- A schema version newer than the set's newest migration (an older binary on a
  newer database) is refused with `unsupported` before anything is written
  (REQ-SQL-008).
- Concurrent opens (REQ-SQL-009): on Postgres take a session advisory lock
  around migrating; on SQLite take the write lock (`BEGIN IMMEDIATE`) and read
  the version again under it. Both opens succeed.

## 3. Dialect differences the store must hide

| Concern | SQLite | Postgres | Rule |
| --- | --- | --- | --- |
| Foreign keys | off by default | on | `PRAGMA foreign_keys=ON` on every connection (REQ-SQL-015) |
| Text order | `BINARY` | locale collation | `TextOrderClause` takes a simple identifier (letters, digits, `_`) and returns it with `COLLATE "C"` on Postgres; any other argument is `invalid_argument` (REQ-SQL-017) |
| Time precision | whatever text is stored | microseconds, **rounded** | convert to UTC and truncate to microseconds in Go before writing (REQ-SQL-016) |
| NUL in text | accepted | rejected by the server | reject before writing on both (REQ-SQL-018) |
| Placeholders | `?` | `$n` | one query layer rewrites them |
| Busy writers | `SQLITE_BUSY` | none | DSN `_pragma=busy_timeout(10000)` set before `_pragma=journal_mode(WAL)`, and `_txlock=immediate`, as for the embedded Temporal store (`docs/architecture/runtime.md`) |

## 4. Tenancy

Every repository method reads the tenant from its `context.Context`. With no
tenant it fails with `invalid_argument` before touching the database
(REQ-SQL-010). Every statement filters by the tenant column, and every insert
sets it from the context, never from the caller's arguments. Row-level
security in the cloud profile is WP-L8-02's second line of defense; normative
decisions are [ADR-0022](../../docs/adr/0022-postgres-row-level-security.md)
**D1**–**D8**. This section records only `platform/sql` mechanics by ADR
number.

**Open (ADR D2, D3).** `Open` gains an explicit **RLS mode** option the cloud
profile sets; without it Postgres behaviour stays as today. RLS mode refuses
`invalid_argument` when: URL is not Postgres; `MigrateURL` is missing; migrate
and application URLs resolve to the same `current_user`; or the application
role is superuser or has `BYPASSRLS`. In RLS mode `OpenOptions` carries
`MigrateURL` (`sql.migrate_url`, **EP0-217**) and uses `rawURL` (`sql.url`,
`bas_app`) for the application pool. Migrate runs on `MigrateURL`, then the
pool opens on `sql.url`. **Role self-check** runs on a short-lived plain
connection to `sql.url` before the tenant wrapper is registered (D2); then that
handle is closed.

One shared migration `fs.FS` per open; `platform/sql` walks the FS and passes
**exact** basenames ending in `.postgres.sql` to `goose.WithExcludeNames` on
SQLite (not a glob pattern). `maxMigrationVersion` and `checkSchemaVersion`
skip the same names (D3). Multi-package merge: **EP0-216**.

**Wrapper (ADR D1, D5, D6).** Postgres application pools in RLS mode register
the tenant connector wrapper (pgx stdlib). A `sql.Tx` does not pin the tenant;
each statement uses its own context's tenant. Invariants: per-statement
`set_config` when cached tenant or system scope differs; cache unknown after
errors, `Rollback`, failed `Commit`; no unconditional `RESET` on reuse;
`ResetSession` forwards to pgx; every driver statement path intercepted;
embed-forward pgx optional interfaces; no tenant work at `Connect`. System scope
API names (e.g. `WithSystemScope`, `ScopeRelayDiscovery`) and allowed call-site
test: ADR D6. Statements without tenant and without allowed system scope:
`invalid_argument` before SQL.

**Errors (ADR D7).** SQLSTATE `42501` maps to `core.CodeInternal` with
`Details["sqlstate"]` and no table, policy, or key in the message; implementation
pins mapping with a test.

**Topology (ADR D1).** Session-level poolers only; transaction-mode PgBouncer
unsupported.

Repository tenant filters remain the first line of defence (REQ-SQL-010);
Postgres RLS does not relax them.

## 5. The conformance suite

The scenarios in this directory are the conformance suite. They use a
test-only **sample repository** over **sample migrations**, kept under
`internal/bdd` or a `_test` package, never in production code:

| Version | Sample migration |
| --- | --- |
| 1 | creates lists (id, tenant, name) and items (id, tenant, key unique per tenant, optional list referring to a list, timestamp `at`) |
| 2 | stores the item with key `seed` for tenant `acme` |
| 3 | adds an optional note to items |

A **broken migration 4** stores the item `seeded-by-4` for tenant `acme`
and then fails. The schema also carries a not-null column and a check column
that the sample repository sets to violate the named constraint (`not-null` or
`check`) when requested by a scenario.

Steps about a database failure trigger real dialect failures from `internal/bdd`
with no test hooks in production code.

A `busy` step opens a second connection to the same SQLite file and begins an
immediate transaction on it to hold the write lock. It then runs
`PRAGMA busy_timeout=0` through the store's own `Execer`: the SQLite store
keeps exactly one connection (`SetMaxOpenConns(1)`), and the pragma applies to
that connection, so the store's next write fails at once with `SQLITE_BUSY`
instead of waiting out the DSN's `busy_timeout`. The step uses only the store's
public port, and nothing waits on a timeout, so the scenario is deterministic
and fast.

`SQLITE_LOCKED` is not reachable through the store's connections. Its
shared-cache form needs a shared-cache connection the store never opens, and
the driver waits on `sqlite3_unlock_notify` instead of returning it. The plain
`SQLITE_LOCKED` code needs a statement on a connection that still has a cursor
open on the table it touches, which the store's queries never do, and in WAL
mode an exclusive lock behaves like an immediate one and still yields
`SQLITE_BUSY`. The mapper's handling of `SQLITE_LOCKED` is therefore covered by
a unit test that drives it with a real driver error
(`TestSQLiteLockedMapping`), not by a scenario.

Postgres refusals come from conflicting transactions, test-schema SQLSTATE
triggers, or closing a local proxy to the database, for `serialization failure`,
`deadlock`, `out of resources`, and `unreachable`.

Scenarios tagged with both `@profile:embedded` and `@profile:cloud` run once
per dialect. The component driver always runs the SQLite pass, on a fresh file
in a temporary directory. "A database server address where nothing listens" is
a loopback port the step opened and closed.

The Postgres pass of the driver (the G3 matrix line in
`docs/engineering/qa-gate.md`) takes its server, and reports when it cannot,
as [ADR-0015](../../docs/adr/0015-how-the-pr-gate-runs-postgres.md) decides:

- **Server.** A non-empty `BDD_POSTGRES_URL` names the server, and only that
  server: there is never a fallback to an embedded one. An empty or unset
  `BDD_POSTGRES_URL` means an embedded Postgres on major version 16. The
  driver depends on nothing but a server URL.
- **No server.** When the named server is unreachable or unusable, or the
  embedded one does not start, a run under `GATE_STRICT=1` fails. Otherwise
  the Postgres pass is reported skipped with the one closed reason
  `postgres-unavailable`. A server that is up but fails to create or use a
  scenario's own database is a failed run, never a skip.
- **Fresh database.** Each scenario of the Postgres pass opens its own fresh
  database, and the driver creates and drops only the databases it created.
- **Collation.** The server's default collation is not part of the contract:
  REQ-SQL-017 orders text with `COLLATE "C"` wherever text is ordered, so
  nothing depends on how the server was initialized.
