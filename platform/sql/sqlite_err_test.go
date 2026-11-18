package sql

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/epzero/basamento/core"
)

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
