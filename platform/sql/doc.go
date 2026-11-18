// Package sql is the SQL port of platform: SQLite in the embedded profile and
// Postgres in the cloud profile, selected by URL (ADR-0004). Migrations come from
// an fs.FS (typically embed.FS). Repository code built on this store is expected
// to read the tenant from context (REQ-SQL-010 through REQ-SQL-012); this package
// does not enforce tenancy by itself.
package sql
