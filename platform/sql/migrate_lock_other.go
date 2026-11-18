//go:build !unix

package sql

import "context"

func withSQLiteMigrateFileLock(ctx context.Context, dbPath string, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}
