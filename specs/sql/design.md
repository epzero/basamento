# SQL store: design notes for the implementer

Work package WP-L1-01, owned directory `platform/sql`. Requirements:
[`requirements.md`](requirements.md). Decisions: ADR-0001 (pure Go,
`CGO_ENABLED=0`), ADR-0004 (ports selected by URL, one conformance suite for
both implementations).

## 1. Shape

- One package `platform/sql` opens a store from a URL:
  `sqlite:///<path>` (also `sqlite://<relative path>`, RFC 3986 host+path for
  `sqlite://data/kit.db`) or `postgres://user:password@host:port/db?…`. Schemes
  other than `sqlite` or `postgres`, a URL with no database path, a SQLite URL
  with query or fragment, or the legacy `postgresql` scheme is `invalid_argument`
  (REQ-SQL-002) and nothing is created. SQLite `:memory:` is never passed through
  `filepath.Abs`.
- Opening takes the **migration set** as an `fs.FS` (in production an
  `embed.FS`), so the conformance suite can open stores with its own sample
  migrations. Migrations are goose migrations written once for both dialects.
- Errors are `*core.Error` with fixed messages. Driver and vendor text is not
  forwarded in messages or details (stable codes such as SQLSTATE or SQLite
  result codes may appear in details). Connection refused, DNS failure or
  timeout while opening → `unavailable` with `Retryable: true`. Postgres
  serialization failure and deadlock (`40001`, `40P01`), connection and resource
  classes (`08xxx`, `53xxx`, `57P01`), and SQLite `SQLITE_BUSY` / `SQLITE_LOCKED`
  map to `unavailable` with `Retryable: true`. Unique, foreign-key, not-null and
  check violations map to `conflict`. A URL is redacted (userinfo and credential
  query parameters such as `password` and `sslpassword`) before it reaches any
  message, detail or cause; driver text is not forwarded when a secret was present
  (REQ-SQL-004).
- New SQLite database files are created mode `0600`; missing parent directories
  are created mode `0700` (existing directories are not chmodded).

## 2. Migrations

- Each migration runs in its own transaction, on both dialects, so a failed
  one leaves no trace (REQ-SQL-007).
- A schema version newer than the set's newest migration (an older binary on a
  newer database) is refused with `unsupported` before anything is written
  (REQ-SQL-008).
- Concurrent opens (REQ-SQL-009): on SQLite, an advisory `flock` on
  `<dbpath>.migrate.lock` beside the database serialises migration across
  processes; schema version is checked again inside that critical section, then
  goose runs. On Postgres, a session `pg_advisory_lock` on one dedicated pool
  connection is held for the whole migrate; schema version is re-checked under
  that lock. Both concurrent opens succeed when migrations are compatible.
- Store **InTx** rejects a second InTx on the **same** store when `fn` passes the
  ctx InTx provides; another store may nest independently. Using a different ctx
  on the same store can deadlock on SQLite (one connection).

## 3. Dialect differences the store must hide

| Concern | SQLite | Postgres | Rule |
| --- | --- | --- | --- |
| Foreign keys | off by default | on | `PRAGMA foreign_keys=ON` on every connection (REQ-SQL-015) |
| Text order | `BINARY` | locale collation | `TextOrderClause` takes a simple identifier and appends `COLLATE "C"` on Postgres (REQ-SQL-017) |
| Time precision | whatever text is stored | microseconds, **rounded** | convert to UTC and truncate to microseconds in Go before writing (REQ-SQL-016) |
| NUL in text | accepted | rejected by the server | reject before writing on both (REQ-SQL-018) |
| Placeholders | `?` | `?` in repository SQL; Execer rewrites to `$n` | only `?` bind markers; authored `$n` is `invalid_argument`; `?` outside literals is a bind (no jsonb `?` operators — use `jsonb_exists*` functions) |
| Busy writers | `SQLITE_BUSY` | none | DSN: `busy_timeout=10000` before `journal_mode=wal`, `_txlock=immediate`, as for the embedded Temporal store (`docs/architecture/runtime.md`) |

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