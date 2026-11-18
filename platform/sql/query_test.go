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
	t.Parallel()
	e := &Execer{dialect: dialectPostgres}
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{
			name: "bind_only",
			in:   "SELECT * FROM t WHERE id = ? AND x = ?",
			want: "SELECT * FROM t WHERE id = $1 AND x = $2",
		},
		{
			name: "single_quoted",
			in:   "SELECT '?' AS q, ? FROM t",
			want: "SELECT '?' AS q, $1 FROM t",
		},
		{
			name: "double_quoted",
			in:   `SELECT "?" FROM t WHERE id = ?`,
			want: `SELECT "?" FROM t WHERE id = $1`,
		},
		{
			name: "line_comment",
			in:   "SELECT ? -- bind ?\nFROM t WHERE x = ?",
			want: "SELECT $1 -- bind ?\nFROM t WHERE x = $2",
		},
		{
			name: "block_comment",
			in:   "SELECT /* ? */ ? FROM t",
			want: "SELECT /* ? */ $1 FROM t",
		},
		{
			name: "dollar_quoted",
			in:   "SELECT $tag$? literal$tag$, ? FROM t",
			want: "SELECT $tag$? literal$tag$, $1 FROM t",
		},
		{
			name: "dollar_empty_tag",
			in:   "SELECT $$?$$, ? FROM t",
			want: "SELECT $$?$$, $1 FROM t",
		},
		{
			name: "double_question",
			in:   "SELECT ?? FROM t",
			want: "SELECT $1$2 FROM t",
		},
		{
			name:    "reject_dollar_number",
			in:      "SELECT $1 FROM t",
			wantErr: true,
		},
		{
			name:    "invalid_dollar_tag_still_rewrites_bind",
			in:      "SELECT $1notvalid$, ?",
			wantErr: true,
		},
		{
			name: "postgres_escape_string",
			in:   `SELECT E'\\?' AS q, ? FROM t`,
			want: `SELECT E'\\?' AS q, $1 FROM t`,
		},
		{
			name: "escape_string_lowercase",
			in:   `SELECT e'\'?', ?`,
			want: `SELECT e'\'?', $1`,
		},
		{
			name: "like_plain_string_not_escape",
			in:   `WHERE a LIKE'\' AND b = ?`,
			want: `WHERE a LIKE'\' AND b = $1`,
		},
		{
			name: "escape_string_doubled_quote",
			in:   `SELECT E'a''?', ?`,
			want: `SELECT E'a''?', $1`,
		},
		{
			name: "escape_after_ident_letter",
			in:   `SELECT size'?', ?`,
			want: `SELECT size'?', $1`,
		},
		{
			name: "escape_after_ident_digit",
			in:   `SELECT x1'?', ?`,
			want: `SELECT x1'?', $1`,
		},
		{
			name: "escape_after_ident_underscore",
			in:   `SELECT _e'?', ?`,
			want: `SELECT _e'?', $1`,
		},
		{
			name: "escape_after_ident_dollar",
			in:   `SELECT $e'?', ?`,
			want: `SELECT $e'?', $1`,
		},
		{
			name: "escape_after_high_byte",
			in:   "SELECT \xe2e'?', ?",
			want: "SELECT \xe2e'?', $1",
		},
		{
			name: "escape_at_start",
			in:   `E'\?', ?`,
			want: `E'\?', $1`,
		},
		{
			name: "escape_after_paren",
			in:   `(E'\?', ?)`,
			want: `(E'\?', $1)`,
		},
		{
			name: "escape_after_comma",
			in:   `x, E'\?', ?`,
			want: `x, E'\?', $1`,
		},
		{
			name: "escape_after_space",
			in:   ` x E'\?', ?`,
			want: ` x E'\?', $1`,
		},
		{
			name: "dollar_after_ident",
			in:   `SELECT foo$tag$?, ?`,
			want: `SELECT foo$tag$$1, $2`,
		},
		{
			name: "dollar_quote_after_space",
			in:   `SELECT $tag$? in$tag$, ?`,
			want: `SELECT $tag$? in$tag$, $1`,
		},
		{
			name: "byte_string",
			in:   `SELECT B'??', ?`,
			want: `SELECT B'??', $1`,
		},
		{
			name: "byte_string_after_ident",
			in:   `SELECT fooB'?', ?`,
			want: `SELECT fooB'?', $1`,
		},
		{
			name: "hex_string",
			in:   `SELECT X'AB?', ?`,
			want: `SELECT X'AB?', $1`,
		},
		{
			name: "unicode_string",
			in:   `SELECT U&'a\0441?', ?`,
			want: `SELECT U&'a\0441?', $1`,
		},
		{
			name: "unicode_string_after_ident",
			in:   `SELECT fooU&'?', ?`,
			want: `SELECT fooU&'?', $1`,
		},
		{
			name: "nested_block_comment",
			in:   "SELECT /* /* ? */ ? */ 1 WHERE x = ?",
			want: "SELECT /* /* ? */ ? */ 1 WHERE x = $1",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := e.rewrite(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestTextOrderClauseRejectsInvalidIdent(t *testing.T) {
	_, err := TextOrderClause("postgres", `key"; DROP TABLE items;--`)
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("got %v", err)
	}
}

func TestTextOrderClausePostgres(t *testing.T) {
	got, err := TextOrderClause("postgres", "key")
	if err != nil {
		t.Fatal(err)
	}
	if got != `key COLLATE "C"` {
		t.Fatalf("got %q", got)
	}
}
