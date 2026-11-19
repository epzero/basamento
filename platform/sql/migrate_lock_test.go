//go:build unix

package sql

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"github.com/epzero/basamento/core"
)

type callsErrCtx struct {
	context.Context
	max   int
	calls int
}

func (c *callsErrCtx) Err() error {
	c.calls++
	if c.calls >= c.max {
		return context.Canceled
	}
	return c.Context.Err()
}

func TestSQLiteMigrateLockFileExistsAfterOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kit.db")
	ctx := context.Background()
	store, err := Open(ctx, "sqlite:///"+path, emptyMigrations())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	lockPath := path + ".migrate.lock"
	st, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		t.Fatalf("lock mode %o want 0600", st.Mode().Perm())
	}
}

func TestOpenWaitsOnMigrateLockUntilContextCancelled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kit.db")
	lockPath := path + ".migrate.lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	defer func() { _ = f.Close() }()

	ctx := &callsErrCtx{Context: context.Background(), max: 3}
	var wg sync.WaitGroup
	var openErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, openErr = Open(ctx, "sqlite:///"+path, emptyMigrations())
	}()
	wg.Wait()
	if openErr == nil {
		t.Fatal("Open succeeded while lock held")
	}
	if core.CodeOf(mapExecErr(openErr)) != core.CodeCancelled {
		t.Fatalf("got %v", openErr)
	}
	if ctx.calls < 3 {
		t.Fatalf("ctx.Err calls %d want >= 3", ctx.calls)
	}
}

func TestSQLiteMigrateLockRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.lock")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "kit.db.migrate.lock")
	dbPath := filepath.Join(dir, "kit.db")
	_ = os.WriteFile(dbPath, nil, 0o600)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	err := withSQLiteMigrateFileLock(context.Background(), dbPath, func() error { return nil })
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("got %v", err)
	}
	if stringsContains(err.Error(), dir) {
		t.Fatal("path leaked")
	}
}

func TestSQLiteMigrateLockRejectsDanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "kit.db.migrate.lock")
	dbPath := filepath.Join(dir, "kit.db")
	_ = os.WriteFile(dbPath, nil, 0o600)
	if err := os.Symlink(filepath.Join(dir, "missing"), link); err != nil {
		t.Fatal(err)
	}
	err := withSQLiteMigrateFileLock(context.Background(), dbPath, func() error { return nil })
	if err == nil {
		t.Fatal("expected error")
	}
	if stringsContains(err.Error(), dir) {
		t.Fatal("path leaked")
	}
}
