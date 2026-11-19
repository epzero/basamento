// Package sql is the SQL port of platform: SQLite in the embedded profile and
// Postgres in the cloud profile, selected by URL (ADR-0004). Migrations come from
// an fs.FS (typically embed.FS). Repository code built on this store is expected
// to read the tenant from context (REQ-SQL-010 through REQ-SQL-012); this package
// does not enforce tenancy by itself.
//
// # URLs
//
// [Open] accepts sqlite://… and postgres://… URLs. The legacy postgresql scheme and
// any other scheme are invalid_argument. SQLite forms include sqlite:///absolute/path,
// sqlite://relative/path, sqlite://host/path (RFC 3986 host plus path, for example
// sqlite://data/kit.db), and sqlite://:memory: or sqlite:///:memory:. A relative path
// is resolved against the working directory. Query and fragment on SQLite URLs are
// rejected. The path :memory: is never passed through filepath.Abs. Postgres URLs must
// include a database path; otherwise nothing is created.
//
// # Migrations and locking
//
// Concurrent opens run migrations under an advisory flock on <dbpath>.migrate.lock
// beside the database for SQLite; schema version is checked again inside that critical
// section, then goose runs. On Postgres, a session pg_advisory_lock on one dedicated
// pool connection is held for the whole migrate and schema version is re-checked under
// that lock. A schema version newer than the migration set's newest file is refused
// with unsupported before anything is written.
//
// # SQLite files and DSN
//
// New SQLite database files are created mode 0600; missing parent directories are
// created mode 0700 (existing directories are not chmodded). The SQLite driver DSN sets
// busy_timeout before journal_mode=WAL, foreign_keys, and _txlock=immediate, matching
// the embedded Temporal store.
//
// # Errors
//
// Operations return *core.Error with fixed messages. Driver and vendor text is not
// forwarded in messages or details; stable codes such as SQLSTATE or SQLite result
// codes may appear in details. Connection refused, DNS failure, or timeout while
// opening map to unavailable with Retryable true. Postgres serialization failure and
// deadlock (40001, 40P01), connection and resource classes (08xxx, 53xxx, 57P01), and
// SQLite SQLITE_BUSY / SQLITE_LOCKED map to unavailable with Retryable true. Unique
// and foreign-key violations map to conflict; not-null and check violations map to
// invalid_argument. [Store.ParsedURLRedacted] and error details replace the userinfo
// password and the values of the credential query parameters (password, sslpassword,
// passfile, sslcert, sslkey, sslrootcert) with the literal ***. When the URL carried
// any of them, driver text is replaced by the fixed cause "driver error withheld".
//
// # Transactions and SQL
//
// [Store.InTx] rejects a nested InTx on the same store when fn uses the ctx InTx
// provides. [Execer] documents the ? placeholder contract on both dialects.
// [TextOrderClause] takes a simple SQL identifier for the column name.
//
// Postgres ?-to-$n scanning follows the dialect lexer for strings, comments, and
// dollar quotes, including standard single-quote rules for U&'…' strings; plain
// single-quoted strings always assume standard_conforming_strings = on.
// Jsonb operators ?, ?|, and ?& are out of contract for SQL passed through the store.
package sql
