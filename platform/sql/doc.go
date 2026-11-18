// Package sql is the SQL port of platform: SQLite in the embedded profile and
// Postgres in the cloud profile, selected by URL (ADR-0004). Migrations come from
// an fs.FS (typically embed.FS). Repository calls read the tenant from context.
package sql
