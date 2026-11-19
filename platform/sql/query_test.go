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

// Regression probes for U&'…' using the standard single-quote rule (not escape rules).
// Only a backslash inside U&' distinguishes the two; doubled-quote continuations match
// both rules and belong in the scanner contract table, not here.
func TestRewritePostgresPlaceholderUescapeProbes(t *testing.T) {
	t.Parallel()
	e := &Execer{dialect: dialectPostgres}
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "uescape_backslash_not_escape_string",
			in:   `SELECT U&'\' UESCAPE '!' AS s, ?`,
			want: `SELECT U&'\' UESCAPE '!' AS s, $1`,
		},
		{
			name: "uescape_backslash_after_content_hides_trailing_placeholders",
			in:   `SELECT U&'\' AS s, ?, ?`,
			want: `SELECT U&'\' AS s, $1, $2`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := e.rewrite(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

type postgresScannerContractRow struct {
	construct     string
	in            string
	want          string
	wantErr       bool
	knownGap      string
	gapReason     string
	supported     bool
	outOfContract bool
}

func TestRewritePostgresPlaceholderScannerContract(t *testing.T) {
	t.Parallel()
	e := &Execer{dialect: dialectPostgres}
	rows := []postgresScannerContractRow{
		{
			construct: "standard_single_quoted",
			in:        `SELECT '?' AS q, ?`,
			want:      `SELECT '?' AS q, $1`,
			supported: true,
		},
		{
			construct: "escape_string_e",
			in:        `SELECT E'\\?' AS q, ?`,
			want:      `SELECT E'\\?' AS q, $1`,
			supported: true,
		},
		{
			construct: "byte_string_b",
			in:        `SELECT B'??', ?`,
			want:      `SELECT B'??', $1`,
			supported: true,
		},
		{
			construct: "hex_string_x",
			in:        `SELECT X'AB?', ?`,
			want:      `SELECT X'AB?', $1`,
			supported: true,
		},
		{
			construct: "unicode_string_u_amp_doubled_quote",
			in:        `SELECT U&'''?''', ?`,
			want:      `SELECT U&'''?''', $1`,
			supported: true,
		},
		{
			construct: "unicode_string_u_amp_lower",
			in:        `SELECT u&'a', ?`,
			want:      `SELECT u&'a', $1`,
			supported: true,
		},
		{
			construct: "dollar_quoted_tag",
			in:        `SELECT $tag$? literal$tag$, ?`,
			want:      `SELECT $tag$? literal$tag$, $1`,
			supported: true,
		},
		{
			construct: "dollar_quoted_tag_non_ascii_latin",
			in:        `SELECT $é$?$é$ WHERE id = ?`,
			want:      `SELECT $é$?$é$ WHERE id = $1`,
			supported: true,
		},
		{
			construct: "dollar_quoted_tag_greek",
			in:        `SELECT $Ω$?$Ω$, ?`,
			want:      `SELECT $Ω$?$Ω$, $1`,
			supported: true,
		},
		{
			construct: "dollar_quoted_tag_cjk",
			in:        `SELECT $中$?$中$, ?`,
			want:      `SELECT $中$?$中$, $1`,
			supported: true,
		},
		{
			construct: "dollar_quoted_tag_mixed_ascii_nonascii_digit",
			in:        `SELECT $a€1$?$a€1$, ?`,
			want:      `SELECT $a€1$?$a€1$, $1`,
			supported: true,
		},
		{
			construct: "dollar_quoted_non_ascii_body_nested_different_tag",
			in:        `SELECT $é$?$ê$?$é$, ?`,
			want:      `SELECT $é$?$ê$?$é$, $1`,
			supported: true,
		},
		{
			construct: "dollar_quoted_unclosed_non_ascii_at_eof",
			in:        `SELECT $é$?`,
			want:      `SELECT $é$?`,
			supported: true,
		},
		{
			construct: "dollar_quoted_unclosed_tag_after_bind",
			in:        `SELECT ?, $a$ ?`,
			want:      `SELECT $1, $a$ ?`,
			supported: true,
		},
		{
			construct: "dollar_quoted_unclosed_empty_tag_after_bind",
			in:        `SELECT ?, $$ ?`,
			want:      `SELECT $1, $$ ?`,
			supported: true,
		},
		{
			construct: "dollar_quoted_closed_then_unclosed_non_ascii",
			in:        `SELECT $t$?$t$, ?, $é$?`,
			want:      `SELECT $t$?$t$, $1, $é$?`,
			supported: true,
		},
		{
			construct: "unicode_string_u_amp_trailing_at_eof",
			in:        `SELECT U&'a'`,
			want:      `SELECT U&'a'`,
			supported: true,
		},
		{
			construct: "unicode_string_u_amp_unterminated_quote_at_eof",
			in:        `SELECT ?, U&'`,
			want:      `SELECT $1, U&'`,
			supported: true,
		},
		{
			construct: "unicode_string_u_amp_unterminated_with_content_at_eof",
			in:        `SELECT ?, U&'a?`,
			want:      `SELECT $1, U&'a?`,
			supported: true,
		},
		{
			construct: "unicode_string_u_amp_prefix_without_quote_at_eof",
			in:        `SELECT ?, U&`,
			want:      `SELECT $1, U&`,
			supported: true,
		},
		{
			construct: "unicode_string_u_amp_adjacent_literals",
			in:        `SELECT U&'a'U&'b', ?`,
			want:      `SELECT U&'a'U&'b', $1`,
			supported: true,
		},
		{
			construct: "reject_dollar_number_after_single_quoted",
			in:        `SELECT 'x'$1, ?`,
			wantErr:   true,
			supported: true,
		},
		{
			construct: "unicode_string_uescape_operand_doubled_single_quote_literal",
			in:        `SELECT U&'in\0003side?' UESCAPE '''', ?`,
			want:      `SELECT U&'in\0003side?' UESCAPE '''', $1`,
			supported: true,
		},
		{
			construct: "unicode_string_uescape_double_quote_escape_char",
			in:        `SELECT U&'in\0003side?' UESCAPE '"', ?`,
			want:      `SELECT U&'in\0003side?' UESCAPE '"', $1`,
			supported: true,
		},
		{
			construct: "crlf_line_endings_and_comment",
			in:        "SELECT ?\r\nWHERE x = ? -- bind ?\r\nAND y = ?",
			want:      "SELECT $1\r\nWHERE x = $2 -- bind ?\r\nAND y = $3",
			supported: true,
		},
		{
			construct: "dollar_quote_identifier_boundary",
			in:        `SELECT foo$tag$?, ?`,
			want:      `SELECT foo$tag$$1, $2`,
			supported: true,
		},
		{
			construct: "double_quoted_identifier",
			in:        `SELECT "?" FROM t WHERE id = ?`,
			want:      `SELECT "?" FROM t WHERE id = $1`,
			supported: true,
		},
		{
			construct: "unicode_identifier_u_amp_double_quoted",
			in:        `SELECT U&"foo", ?`,
			want:      `SELECT U&"foo", $1`,
			supported: true,
		},
		{
			construct: "line_comment",
			in:        "SELECT ? -- bind ?\nFROM t WHERE x = ?",
			want:      "SELECT $1 -- bind ?\nFROM t WHERE x = $2",
			supported: true,
		},
		{
			construct: "nested_block_comment",
			in:        "SELECT /* /* ? */ ? */ 1 WHERE x = ?",
			want:      "SELECT /* /* ? */ ? */ 1 WHERE x = $1",
			supported: true,
		},
		{
			construct: "standard_conforming_strings_off",
			in:        `SELECT 'x\' AS s, ?`,
			knownGap:  `SELECT 'x\' AS s, $1`,
			gapReason: "plain '…' strings always use standard_conforming_strings=on rules; backslash does not escape the closing quote",
			supported: false,
		},
		{
			construct:     "jsonb_exists_operator",
			in:            `SELECT col ? 'key'`,
			knownGap:      `SELECT col $1 'key'`,
			gapReason:     "jsonb ? operator is out of contract; use jsonb_exists instead",
			supported:     false,
			outOfContract: true,
		},
	}
	for _, row := range rows {
		t.Run(row.construct, func(t *testing.T) {
			t.Parallel()
			got, err := e.rewrite(row.in)
			if row.wantErr {
				if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
					t.Fatalf("got err %v want invalid_argument", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := row.want
			if !row.supported {
				want = row.knownGap
			}
			if got != want {
				t.Fatalf("got %q want %q", got, want)
			}
			if row.gapReason != "" && row.supported {
				t.Fatal("gapReason set on supported row")
			}
			if !row.supported && row.knownGap == "" {
				t.Fatal("unsupported row missing knownGap")
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
