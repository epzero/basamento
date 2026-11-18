package sql

import (
	"context"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"testing"

	"github.com/epzero/basamento/core"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// TestSQLiteLockedMapping uses a real driver error, not a hand-built one. A
// shared-cache database with a competing reader cannot surface
// SQLITE_LOCKED_SHAREDCACHE to this package: the driver's step and prepare
// paths catch that code and call sqlite3_unlock_notify, which blocks until
// the reader releases instead of returning (modernc.org/sqlite step/retry and
// prepareV2). The reachable real lock is SQLITE_LOCKED proper, which SQLite
// raises when a statement touches a table that this same connection still has
// an open cursor on; the driver returns it unchanged.
func TestSQLiteLockedMapping(t *testing.T) {
	conn, err := (&sqlite.Driver{}).Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	exec := func(query string) {
		t.Helper()
		stmt, err := conn.Prepare(query)
		if err != nil {
			t.Fatalf("prepare %q: %v", query, err)
		}
		defer func() { _ = stmt.Close() }()
		if _, err := stmt.(driver.StmtExecContext).ExecContext(context.Background(), nil); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}
	exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, v TEXT)`)
	exec(`INSERT INTO items (v) VALUES ('x'), ('y')`)

	// Leave the SELECT's cursor open on items, then drop the table.
	sel, err := conn.Prepare(`SELECT id, v FROM items`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sel.Close() })
	rows, err := sel.(driver.StmtQueryContext).QueryContext(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rows.Close() })
	if err := rows.Next(make([]driver.Value, 2)); err != nil {
		t.Fatal(err)
	}

	drop, err := conn.Prepare(`DROP TABLE items`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = drop.Close() })
	_, err = drop.(driver.StmtExecContext).ExecContext(context.Background(), nil)
	if err == nil {
		t.Fatal("DROP TABLE succeeded while a cursor held the table")
	}
	var se *sqlite.Error
	if !errors.As(err, &se) {
		t.Fatalf("got %T, want *sqlite.Error: %v", err, err)
	}
	if got := se.Code(); got != sqlite3.SQLITE_LOCKED {
		t.Fatalf("driver code %d, want SQLITE_LOCKED (%d): %v", got, sqlite3.SQLITE_LOCKED, err)
	}

	mapped := mapExecErr(err)
	if got := core.CodeOf(mapped); got != core.CodeUnavailable {
		t.Fatalf("mapped code %s, want unavailable: %v", got, mapped)
	}
	if !core.IsRetryable(mapped) {
		t.Fatalf("locked error is not retryable: %v", mapped)
	}
	var ce *core.Error
	if !errors.As(mapped, &ce) {
		t.Fatalf("got %T, want *core.Error", mapped)
	}
	if got := ce.Details["sqlite_code"]; got != sqlite3.SQLITE_LOCKED {
		t.Fatalf("details sqlite_code %v, want %d", got, sqlite3.SQLITE_LOCKED)
	}
}

func TestMapExecErrSQLiteConstraintKinds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want core.Code
	}{
		{"not null", errors.New("constraint failed: NOT NULL constraint failed: items.key (1299)"), core.CodeInvalidArgument},
		{"check", errors.New("constraint failed: CHECK constraint failed: tag = 'ok' (275)"), core.CodeInvalidArgument},
		{"unique", errors.New("constraint failed: UNIQUE constraint failed: items.tenant_id, items.key (2067)"), core.CodeConflict},
		{"foreign key", errors.New("constraint failed: FOREIGN KEY constraint failed (787)"), core.CodeConflict},
		{"locked", errors.New("database table is locked (6) (SQLITE_LOCKED)"), core.CodeUnavailable},
		{"busy", errors.New("database is locked (5) (SQLITE_BUSY)"), core.CodeUnavailable},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mapped := mapExecErr(tc.err)
			if got := core.CodeOf(mapped); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
			if tc.want == core.CodeUnavailable && !core.IsRetryable(mapped) {
				t.Fatalf("unavailable error not retryable: %v", mapped)
			}
		})
	}
}

func TestSQLiteConstraintViolationsThroughStore(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(context.Background(), "sqlite:///"+filepath.Join(dir, "kit.db"), emptyMigrations())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	e := s.Execer()
	if _, err := e.Exec(ctx, `CREATE TABLE items (
		id TEXT PRIMARY KEY,
		tenant_id TEXT NOT NULL,
		key TEXT NOT NULL,
		required_note TEXT NOT NULL,
		tag TEXT NOT NULL CHECK (tag = 'ok')
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Exec(ctx, `INSERT INTO items (id, tenant_id, key) VALUES ('a','t','k')`); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("not-null: got %v", err)
	}
	if _, err := e.Exec(ctx, `INSERT INTO items (id, tenant_id, key, required_note, tag) VALUES ('b','t','k2','ok','bad')`); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("check: got %v", err)
	}
	if _, err := e.Exec(ctx, `INSERT INTO items (id, tenant_id, key, required_note, tag) VALUES ('c','t','k3','ok','ok')`); err != nil {
		t.Fatalf("valid insert: %v", err)
	}
	if _, err := e.Exec(ctx, `INSERT INTO items (id, tenant_id, key, required_note, tag) VALUES ('c','t','k4','ok','ok')`); core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("unique: got %v", err)
	}
}
