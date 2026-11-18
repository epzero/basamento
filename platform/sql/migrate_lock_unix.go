//go:build unix

package sql

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
)

// withSQLiteMigrateFileLock serialises migration for one SQLite database file
// across every process on this machine. It takes an advisory flock on
// <dbPath>.migrate.lock beside the database, waits until the lock is free or
// ctx is cancelled (non-blocking flock retries with Gosched, no wall clock),
// and releases the lock when fn returns. No package-level mutable state is used.
func withSQLiteMigrateFileLock(ctx context.Context, dbPath string, fn func() error) error {
	if dbPath == "" {
		return fn()
	}
	lockPath := dbPath + ".migrate.lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	fd := int(f.Fd())
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return fmt.Errorf("sql: sqlite migrate lock: %w", err)
		}
		runtime.Gosched()
	}
	defer func() { _ = syscall.Flock(fd, syscall.LOCK_UN) }()
	return fn()
}
