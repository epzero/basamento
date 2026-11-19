package sql

import (
	"embed"
	"io/fs"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations returns the schema migrations that ship in the binary. Pass it to
// [Open] to create the platform tables, including the embedded telemetry span
// table. The same set applies to SQLite and Postgres.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic("sql: embedded migrations are missing: " + err.Error())
	}
	return sub
}
