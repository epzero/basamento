//go:build unix

package sql

import (
	"errors"
	"os"
	"syscall"

	"github.com/epzero/basamento/core"
)

func chmodSQLiteDBFile(dbPath string) error {
	fd, err := syscall.Open(dbPath, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return openSQLiteFileErr(err)
	}
	f := os.NewFile(uintptr(fd), dbPath)
	defer func() { _ = f.Close() }()
	return os.Chmod(dbPath, 0o600)
}

func createSQLiteDBFile(dbPath string) error {
	fd, err := syscall.Open(dbPath, syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return openSQLiteFileErr(err)
	}
	f := os.NewFile(uintptr(fd), dbPath)
	return f.Close()
}

func openSQLiteFileErr(err error) error {
	if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTSUP) {
		return core.NewError(core.CodeInvalidArgument, "sql: sqlite database path invalid", nil)
	}
	return err
}
