package eventlog

import (
	"embed"
	"io/fs"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations returns the goose migration set of the durable event log. The
// composition root merges it with the other capabilities' migrations when it
// opens the SQL store.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic("eventlog: embedded migrations: " + err.Error())
	}
	return sub
}
