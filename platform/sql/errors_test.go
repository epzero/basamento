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

func TestErrorContainsSecret(t *testing.T) {
	err := core.NewError(core.CodeInternal, "failed", map[string]any{"url": "postgres://u:leak@h/db"})
	if !ErrorContainsSecret(err, "leak") {
		t.Fatal("expected secret in details")
	}
	if ErrorContainsSecret(err, "other") {
		t.Fatal("false positive")
	}
}
