//go:build unix

package sql

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"

	"github.com/epzero/basamento/core"
)

const sqliteMigrateLockPoll = 25 * time.Millisecond

// withSQLiteMigrateFileLock serialises migration for one SQLite database file
// across every process on this machine. It takes an advisory flock on
// <dbPath>.migrate.lock beside the database, waits until the lock is free or
// ctx is cancelled (polls with a short timer between non-blocking attempts),
// and releases the lock when fn returns. No package-level mutable state is used.
func withSQLiteMigrateFileLock(ctx context.Context, dbPath string, fn func() error) error {
	if dbPath == "" {
		return fn()
	}
	lockPath := dbPath + ".migrate.lock"
	fd, err := syscall.Open(lockPath, syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return mapExecErr(openLockErr(err))
	}
	f := os.NewFile(uintptr(fd), lockPath)
	defer func() { _ = f.Close() }()
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return mapExecErr(err)
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return core.NewError(core.CodeInvalidArgument, "sql: migrate lock file invalid", nil)
	}
	timer := time.NewTimer(sqliteMigrateLockPoll)
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return mapExecErr(err)
		}
		err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return core.NewError(core.CodeInternal, "sql: migrate lock failed", nil)
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(sqliteMigrateLockPoll)
		select {
		case <-ctx.Done():
			return mapExecErr(ctx.Err())
		case <-timer.C:
		}
	}
	defer func() { _ = syscall.Flock(fd, syscall.LOCK_UN) }()
	return fn()
}

func openLockErr(err error) error {
	if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTSUP) {
		return core.NewError(core.CodeInvalidArgument, "sql: migrate lock file invalid", nil)
	}
	return err
}
