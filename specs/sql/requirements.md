# SQL: store, migrations and tenant-scoped repositories

Status: **Planned** (WP-L1-01). Design: ADR-0001, ADR-0004,
[`design.md`](design.md).

The SQL store is the SQL port of `platform`: SQLite (`modernc.org/sqlite`) in
the embedded profile, Postgres (`pgx`) in the cloud profile, selected by URL.
One query layer serves both dialects, migrations are embedded in the binary,
and every repository call is scoped to the tenant of its context
(constitution article 8). The same scenarios run against both dialects.
REQ-AUTH-002 builds on the tenant scoping specified here, at the API level.

## Opening a store

### REQ-SQL-001 — Store selected by URL
When a SQL store is opened with a URL whose scheme is `sqlite` or `postgres`, the SQL store shall open the SQLite database file or the Postgres database the URL names, creating a missing SQLite file.

### REQ-SQL-002 — Unsupported URLs
If the URL of a SQL store has a scheme other than `sqlite` or `postgres`, or names no database, then the SQL store shall fail to open with code `invalid_argument` without creating any file.

### REQ-SQL-003 — Unreachable database
If the database a SQL store URL names cannot be reached, then the SQL store shall fail to open with code `unavailable`, marked retryable.

### REQ-SQL-004 — No passwords in errors
If opening a SQL store fails, then the SQL store shall return an error whose message and details do not contain the password of the URL.

## Migrations

### REQ-SQL-005 — Migrate on open
When a SQL store is opened, the SQL store shall apply every migration of its migration set that is newer than the schema version, in version order, before it accepts any repository call.

### REQ-SQL-006 — Up-to-date schema left alone
When a SQL store is opened on a database whose schema version is the newest migration of its migration set, the SQL store shall apply no migration.

### REQ-SQL-007 — Failed migrations roll back
If a migration fails, then the SQL store shall undo every change of that migration, keep the schema version of the last migration that succeeded, and fail to open with code `internal`.

### REQ-SQL-008 — Newer schema refused
If the schema version of a database is newer than the newest migration of the migration set, then the SQL store shall fail to open with code `unsupported` and leave the database unchanged.

### REQ-SQL-009 — Concurrent opens migrate once
When several SQL stores open the same database at the same time, the SQL store shall apply each pending migration exactly once and open every one of them.

## Tenancy

### REQ-SQL-010 — Tenant required
If a repository call carries no tenant in its context, then the SQL store shall reject the call with code `invalid_argument` without reading or writing any record.

### REQ-SQL-011 — Reads scoped to the tenant
The SQL store shall return to a repository read only records of the tenant of the call, and shall fail a read by id of another tenant's record with code `not_found`.

### REQ-SQL-012 — Writes scoped to the tenant
If a repository call updates or deletes a record of another tenant, then the SQL store shall fail the call with code `not_found` and leave the record unchanged.

## Transactions and constraints

### REQ-SQL-013 — All-or-nothing transactions
If the work of a transaction fails, then the SQL store shall undo every write the transaction made.

### REQ-SQL-014 — Uniqueness conflicts
If a write would break a uniqueness rule of the schema, then the SQL store shall reject the write with code `conflict` and leave the existing record unchanged.

### REQ-SQL-015 — References enforced
If a write would leave a record referring to a record that does not exist, then the SQL store shall reject the write with code `conflict` on both dialects.

## Values that behave the same on both dialects

### REQ-SQL-016 — Timestamps in UTC to the microsecond
When a timestamp is stored, the SQL store shall return it as the same instant in UTC, truncated to whole microseconds.

### REQ-SQL-017 — Text ordered by bytes
When a repository lists records ordered by a text value, the SQL store shall order them by the bytes of the value's UTF-8 encoding, on both dialects.

### REQ-SQL-018 — NUL characters rejected
If a text value to be stored contains a NUL character, then the SQL store shall reject the write with code `invalid_argument` on both dialects.
