package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/epzero/basamento/core"
)

type valuerString string

func (v valuerString) Value() (any, error) { return string(v), nil }

type valueRecvValuer string

func (v valueRecvValuer) Value() (any, error) { return string(v), nil }

type ptrRecvValuer struct {
	s string
}

func (p *ptrRecvValuer) Value() (any, error) {
	if p == nil {
		return nil, nil
	}
	return p.s, nil
}

type errValuer string

func (v errValuer) Value() (any, error) { return nil, errors.New("valuer err") }

type namedBindString string

func TestValidateArgsTextKinds(t *testing.T) {
	t.Parallel()
	nul := "a\x00b"
	var nilValueValuer *valueRecvValuer
	cases := []struct {
		name string
		arg  any
		want core.Code
	}{
		{"string", nul, core.CodeInvalidArgument},
		{"string_ok", "ok", ""},
		{"string_ptr", strPtr(nul), core.CodeInvalidArgument},
		{"string_ptr_nil", (*string)(nil), ""},
		{"named_string_nul", namedBindString(nul), core.CodeInvalidArgument},
		{"null_string_invalid", sql.NullString{}, ""},
		{"null_string_nul", sql.NullString{String: nul, Valid: true}, core.CodeInvalidArgument},
		{"valuer", valuerString(nul), core.CodeInvalidArgument},
		{"valuer_err", errValuer("x"), ""},
		{"typed_nil_value_valuer", nilValueValuer, ""},
		{"nil_ptr_recv_valuer", (*ptrRecvValuer)(nil), ""},
		{"bytes_nul", []byte{0, 1}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateOneArg(tc.arg)
			got := core.CodeOf(err)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected %v", err)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestExecTypedNilValueValuerDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	store, err := Open(ctx, "sqlite:///"+dir+"/kit.db", emptyMigrations())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	var nilV *valueRecvValuer
	_, err = store.Execer().Exec(ctx, `SELECT 1`, nilV)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
}

func strPtr(s string) *string { return &s }

func TestExecRejectsNULViaNullString(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	store, err := Open(ctx, "sqlite:///"+dir+"/kit.db", emptyMigrations())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	_, err = store.Execer().Exec(ctx, `SELECT ?`, sql.NullString{String: "x\x00", Valid: true})
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("got %v", err)
	}
	if stringsContains(fmt.Sprint(err), "\x00") {
		t.Fatal("echoed nul")
	}
}
