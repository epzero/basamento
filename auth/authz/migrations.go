package authz

import (
	"embed"
	"io/fs"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations returns this package's goose migration set for authz_bindings.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic("authz: embedded migrations: " + err.Error())
	}
	return sub
}
