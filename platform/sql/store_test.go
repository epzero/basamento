package sql

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/epzero/basamento/core"
)

func testExecCommand(t *testing.T, name string, arg ...string) *exec.Cmd {
	t.Helper()
	return exec.Command(name, arg...)
}

func TestSQLiteFileMode0600(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	path := filepath.Join(dir, "kit.db")
	url := "sqlite:///" + path
	store, err := Open(ctx, url, emptyMigrations())
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Execer().Exec(ctx, `CREATE TABLE t(x)`, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Execer().Exec(ctx, `INSERT INTO t(x) VALUES (1)`, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		t.Fatalf("db mode %o want 0600", st.Mode().Perm())
	}
	walPath := path + "-wal"
	if st, err := os.Stat(walPath); err == nil {
		if st.Mode().Perm()&0o077 != 0 {
			t.Fatalf("wal mode %o", st.Mode().Perm())
		}
	}
}

func TestInTxRollbackOnPanic(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	store, err := Open(ctx, "sqlite:///"+filepath.Join(dir, "kit.db"), emptyMigrations())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	_, err = store.Execer().Exec(ctx, `CREATE TABLE t(x INT UNIQUE)`, nil)
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() { _ = recover() }()
		_ = store.InTx(ctx, func(ctx context.Context, exec *Execer) error {
			_, _ = exec.Exec(ctx, `INSERT INTO t(x) VALUES (1)`, nil)
			panic("injected")
		})
	}()
	if _, err := store.Execer().Exec(ctx, `SELECT 1`, nil); err != nil {
		t.Fatalf("post-panic exec: %v", err)
	}
}

func TestInTxNestedReturnsInvalidArgument(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	store, err := Open(ctx, "sqlite:///"+filepath.Join(dir, "kit.db"), emptyMigrations())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	err = store.InTx(ctx, func(ctx context.Context, exec *Execer) error {
		return store.InTx(ctx, func(context.Context, *Execer) error { return nil })
	})
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("nested InTx: %v", err)
	}
}

func TestParsedURLRedactedOnStore(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	store, err := Open(ctx, "sqlite:///"+filepath.Join(dir, "kit.db"), emptyMigrations())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if store.ParsedURLRedacted() == "" {
		t.Fatal("expected redacted url")
	}
	if store.DB() == nil {
		t.Fatal("DB handle")
	}
}

func TestSQLiteMigrateLockBlocksSecondProcess(t *testing.T) {
	if os.Getenv("SQL_MIGRATE_LOCK_CHILD") == "hold" {
		lockPath := os.Getenv("SQL_MIGRATE_LOCK_PATH")
		f, err := os.OpenFile(lockPath, os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		if tryFlockNBPath(f) == nil {
			t.Fatal("acquired lock held by parent")
		}
		fmt.Println("blocked")
		return
	}
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "kit.db")
	lockPath := dbPath + ".migrate.lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	defer func() { _ = f.Close() }()

	cmd := testExecCommand(t, os.Args[0], "-test.run=^TestSQLiteMigrateLockBlocksSecondProcess$")
	cmd.Env = append(os.Environ(),
		"SQL_MIGRATE_LOCK_CHILD=hold",
		"SQL_MIGRATE_LOCK_PATH="+lockPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child: %v %s", err, out)
	}
	if !strings.Contains(string(out), "blocked") {
		t.Fatalf("child output %q", out)
	}
}

func tryFlockNBPath(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
