package sql

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestMapMigrationErrPostgresFailureIsInternal(t *testing.T) {
	t.Parallel()
	for _, code := range []string{"42P01", "42601"} {
		code := code
		t.Run(code, func(t *testing.T) {
			t.Parallel()
			err := &pgconn.PgError{Code: code, Message: "migration detail"}
			mapped := mapMigrationErr(err)
			if core.CodeOf(mapped) != core.CodeInternal {
				t.Fatalf("got %s want internal", core.CodeOf(mapped))
			}
		})
	}
}

func TestMapMigrationErrPreservesContextAndUnavailable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   error
		want core.Code
	}{
		{"cancelled", fmt.Errorf("w: %w", context.Canceled), core.CodeCancelled},
		{"timeout", fmt.Errorf("w: %w", context.DeadlineExceeded), core.CodeTimeout},
		{"unavailable_pg", &pgconn.PgError{Code: "40001", Message: "serialization"}, core.CodeUnavailable},
		{"unavailable_connect", &pgconn.ConnectError{}, core.CodeUnavailable},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mapped := mapMigrationErr(tc.in)
			if core.CodeOf(mapped) != tc.want {
				t.Fatalf("got %s want %s", core.CodeOf(mapped), tc.want)
			}
		})
	}
}

func TestMapMigrationErrAlreadyCoreError(t *testing.T) {
	in := core.NewError(core.CodeUnsupported, "sql: database schema newer than binary", nil)
	mapped := mapMigrationErr(in)
	if core.CodeOf(mapped) != core.CodeInternal {
		t.Fatalf("got %s", core.CodeOf(mapped))
	}
	avail := core.NewError(core.CodeUnavailable, "sql: database busy", nil)
	if core.CodeOf(mapMigrationErr(avail)) != core.CodeUnavailable {
		t.Fatal("unavailable")
	}
	if !errors.Is(mapMigrationErr(context.Canceled), context.Canceled) {
		t.Fatal("cancelled unwrap")
	}
}
