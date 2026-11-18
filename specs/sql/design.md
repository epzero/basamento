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
- Opening takes the **migration set** as an `fs.FS` (in production an
  `embed.FS`), so the conformance suite can open stores with its own sample
  migrations. Migrations are goose migrations written once for both dialects.
- Errors are `*core.Error`. Driver errors are mapped once, per dialect, to
  platform codes: unique violation → `conflict`; foreign-key violation →
  `conflict`; no row → `not_found`; connection refused, DNS failure or timeout
  while opening → `unavailable` with `Retryable: true`. A URL is redacted
  (password removed) before it reaches any message, detail or cause
  (REQ-SQL-004); wrap driver errors so their text cannot carry the URL.

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
| Text order | `BINARY` | locale collation | order by `COLLATE "C"` on Postgres (REQ-SQL-017) |
| Time precision | whatever text is stored | microseconds, **rounded** | convert to UTC and truncate to microseconds in Go before writing (REQ-SQL-016) |
| NUL in text | accepted | rejected by the server | reject before writing on both (REQ-SQL-018) |
| Placeholders | `?` | `$n` | one query layer rewrites them |
| Busy writers | `SQLITE_BUSY` | none | `journal_mode=wal`, `busy_timeout=10000`, as for the embedded Temporal store (`docs/architecture/runtime.md`) |

## 4. Tenancy

Every repository method reads the tenant from its `context.Context`. With no
tenant it fails with `invalid_argument` before touching the database
(REQ-SQL-010). Every statement filters by the tenant column, and every insert
sets it from the context, never from the caller's arguments. Row-level
security in the cloud profile is WP-L8-02's second line of defense, not this
work package's.

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
and then fails.

Scenarios tagged with both `@profile:embedded` and `@profile:cloud` run once
per dialect. The component driver always runs the SQLite pass, on a fresh file
in a temporary directory. It runs the Postgres pass when the test environment
names a Postgres server (the CI matrix of gate G3, `docs/engineering/qa-gate.md`),
each scenario in a fresh database, and otherwise reports the pass as skipped,
like any tool missing locally. "A database server address where nothing
listens" is a loopback port the step opened and closed.
