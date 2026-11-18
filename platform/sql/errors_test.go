package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestMapExecErrNoDriverTextPostgres(t *testing.T) {
	pg := &pgconn.PgError{Code: "22P02", Message: `invalid input syntax for type integer: "secret-val"`}
	mapped := mapExecErr(pg)
	if mapped == nil {
		t.Fatal("expected error")
	}
	var pe *core.Error
	if !errors.As(mapped, &pe) {
		t.Fatalf("got %T", mapped)
	}
	if strings.Contains(pe.Message, "secret") {
		t.Fatalf("message echoes driver text: %s", pe.Message)
	}
	if pe.Details["sqlstate"] != "22P02" {
		t.Fatalf("details %+v", pe.Details)
	}
	if pe.Code != core.CodeInvalidArgument {
		t.Fatalf("code %s", pe.Code)
	}
}

func TestMapExecErrPostgresSQLStates(t *testing.T) {
	t.Parallel()
	cases := []struct {
		code      string
		want      core.Code
		retryable bool
	}{
		{"23505", core.CodeConflict, false},
		{"23503", core.CodeConflict, false},
		{"23502", core.CodeConflict, false},
		{"23514", core.CodeConflict, false},
		{"40001", core.CodeUnavailable, true},
		{"40P01", core.CodeUnavailable, true},
		{"57P01", core.CodeUnavailable, true},
		{"53000", core.CodeUnavailable, true},
		{"08006", core.CodeUnavailable, true},
		{"42601", core.CodeInvalidArgument, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.code, func(t *testing.T) {
			t.Parallel()
			mapped := mapExecErr(&pgconn.PgError{Code: tc.code, Message: "driver detail"})
			if core.CodeOf(mapped) != tc.want {
				t.Fatalf("got %s want %s", core.CodeOf(mapped), tc.want)
			}
			if core.IsRetryable(mapped) != tc.retryable {
				t.Fatalf("retryable=%v want %v", core.IsRetryable(mapped), tc.retryable)
			}
		})
	}
}

func TestMapExecErrSQLiteBusyRetryable(t *testing.T) {
	mapped := mapExecErr(errors.New("database is locked (SQLITE_BUSY)"))
	if core.CodeOf(mapped) != core.CodeUnavailable || !core.IsRetryable(mapped) {
		t.Fatalf("got %v", mapped)
	}
}

func TestMapExecErrSQLiteHidesValue(t *testing.T) {
	driver := errors.New(`Key (col)=(secret-val) already exists`)
	mapped := mapExecErr(driver)
	var pe *core.Error
	_ = errors.As(mapped, &pe)
	if strings.Contains(mapped.Error(), "secret") {
		t.Fatalf("leaked value: %v", mapped)
	}
	if pe.Code != core.CodeInternal && pe.Code != core.CodeConflict {
		t.Fatalf("code %s", pe.Code)
	}
}

func TestMapExecErrNoRows(t *testing.T) {
	mapped := mapExecErr(sql.ErrNoRows)
	if core.CodeOf(mapped) != core.CodeNotFound {
		t.Fatalf("got %v", mapped)
	}
}

func TestMapExecErrContextMappedScrubbed(t *testing.T) {
	wrapped := fmt.Errorf("secret-token: %w", context.Canceled)
	mapped := mapExecErr(wrapped)
	if strings.Contains(mapped.Error(), "secret-token") {
		t.Fatalf("leaked: %v", mapped)
	}
	if !errors.Is(mapped, context.Canceled) {
		t.Fatal("unwrap")
	}
	if core.CodeOf(mapped) != core.CodeCancelled {
		t.Fatalf("code %s", core.CodeOf(mapped))
	}
	deadline := fmt.Errorf("pwd=%w", context.DeadlineExceeded)
	mapped = mapExecErr(deadline)
	if strings.Contains(mapped.Error(), "pwd") {
		t.Fatal("leaked deadline wrap")
	}
	if core.CodeOf(mapped) != core.CodeTimeout {
		t.Fatal(mapped)
	}
}

func TestScrubCauseHidesDriverTextWhenURLHasSecret(t *testing.T) {
	raw := "postgres://u:complex@p@h/db"
	err := scrubCause(errors.New("auth failed for user u at complex@p"), raw)
	if strings.Contains(err.Error(), "complex") {
		t.Fatalf("leaked password in cause: %v", err)
	}
}

func TestWrapOpenErrUnreachableServerUnavailable(t *testing.T) {
	p := parsedURL{dialect: dialectPostgres, raw: "postgres://u@127.0.0.1:9/db"}
	wrapped := wrapOpenErr(p, errors.New("dial tcp 127.0.0.1:9: connect: connection refused"))
	if core.CodeOf(wrapped) != core.CodeUnavailable || !core.IsRetryable(wrapped) {
		t.Fatalf("got %v", wrapped)
	}
}
