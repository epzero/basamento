//go:build !unix

package sql

import "context"

// withSQLiteMigrateFileLock runs fn without a cross-process lock; non-Unix
// builds do not serialise SQLite migration across processes.
func withSQLiteMigrateFileLock(ctx context.Context, dbPath string, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}
