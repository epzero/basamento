package sql

import (
	"testing"
	"time"

	"github.com/epzero/basamento/core"
)

func TestTruncateMicrosecond(t *testing.T) {
	in, _ := time.Parse(time.RFC3339Nano, "2026-10-01T09:00:00.9999999Z")
	want, _ := time.Parse(time.RFC3339, "2026-10-01T09:00:00.999999Z")
	got := truncateMicrosecond(in)
	if !got.Equal(want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestValidateTextRejectsNUL(t *testing.T) {
	if err := validateText("a\x00"); err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("validateText: %v", err)
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
