package sqltest

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestAllocateBDDDatabaseRetriesDuplicate(t *testing.T) {
	var names []string
	first := true
	name, err := allocateBDDDatabase(context.Background(), func(_ context.Context, n string) error {
		names = append(names, n)
		if first {
			first = false
			return &pgconn.PgError{Code: "42P04"}
		}
		return nil
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 {
		t.Fatalf("calls %d", len(names))
	}
	if name != names[1] {
		t.Fatalf("name %q", name)
	}
	if name == names[0] {
		t.Fatal("expected different name after collision")
	}
}

func TestAllocateBDDDatabasePropagatesOtherError(t *testing.T) {
	want := errors.New("boom")
	_, err := allocateBDDDatabase(context.Background(), func(context.Context, string) error {
		return want
	}, 7)
	if !errors.Is(err, want) {
		t.Fatal(err)
	}
}
