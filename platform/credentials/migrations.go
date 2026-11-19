package credentials

import (
	"embed"
	"io/fs"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations returns this package's goose migration set (credentials and audit
// events). The composition root includes it in the SQL store's migrations; the
// package does not open or migrate a database itself.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic("credentials: embedded migrations: " + err.Error())
	}
	return sub
}
