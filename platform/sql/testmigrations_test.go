package sql

import (
	"embed"
	"io/fs"
)

//go:embed testdata/migrations/*.sql
var testMigrationFiles embed.FS

func emptyMigrations() fs.FS {
	sub, _ := fs.Sub(testMigrationFiles, "testdata/migrations")
	return sub
}
