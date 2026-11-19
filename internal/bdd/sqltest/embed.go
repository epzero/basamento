package sqltest

import (
	"embed"
	"io/fs"

	"github.com/epzero/basamento/platform/sql"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations returns the conformance migration set root.
func Migrations() fs.FS { return migrationFiles }

// MigrationsThrough returns migrations up to maxVersion, optionally including the broken v4.
func MigrationsThrough(maxVersion int, includeBroken bool) (fs.FS, error) {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	mig, err := sql.FilterMigrations(sub, maxVersion)
	if err != nil {
		return nil, err
	}
	if includeBroken {
		return mig, nil
	}
	return excludeMigration(mig, 4)
}

func excludeMigration(all fs.FS, ver int) (fs.FS, error) {
	return sql.ExcludeMigrationVersion(all, ver)
}
