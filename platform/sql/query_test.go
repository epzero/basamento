package sql

import (
	"context"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
)

func TestTruncateMicrosecond(t *testing.T) {
	in, _ := time.Parse(time.RFC3339Nano, "2026-10-01T09:00:00.9999999Z")
	want, _ := time.Parse(time.RFC3339, "2026-10-01T09:00:00.999999Z")
	got := TruncateMicrosecond(in)
	if !got.Equal(want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestValidateTextRejectsNUL(t *testing.T) {
	if err := ValidateText("a\x00"); err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("ValidateText: %v", err)
	}
}

func TestExecRejectsNULInArgument(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	store, err := Open(ctx, "sqlite:///"+dir+"/kit.db", emptyMigrations())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	_, err = store.Execer().Exec(ctx, `SELECT 1`, "\x00")
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("Exec NUL: %v", err)
	}
	if stringsContains(err.Error(), "\x00") {
		t.Fatalf("error echoed argument")
	}
}

func TestQueryRowRejectsNULInArgument(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	store, err := Open(ctx, "sqlite:///"+dir+"/kit.db", emptyMigrations())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	var n int
	err = store.Execer().QueryRow(ctx, `SELECT 1`, "\x00").Scan(&n)
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("QueryRow NUL: %v", err)
	}
}

func TestRewritePlaceholdersPostgres(t *testing.T) {
	e := &Execer{dialect: dialectPostgres}
	got := e.rewrite("SELECT * FROM t WHERE id = ? AND x = ?")
	want := "SELECT * FROM t WHERE id = $1 AND x = $2"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
