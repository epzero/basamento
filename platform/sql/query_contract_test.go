package sql

import "testing"

type postgresScannerContractRow struct {
	construct     string
	in            string
	want          string
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
			construct: "unicode_string_u_amp",
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
			construct:     "unicode_escaped_identifier_u_amp",
			in:            `SELECT U&"foo", ?`,
			want:          `SELECT U&"foo", $1`,
			supported:     false,
			outOfContract: false,
		},
		{
			construct:     "standard_conforming_strings_off",
			in:            `SELECT '\?', ?`,
			want:          `SELECT '\?', $1`,
			supported:     false,
			outOfContract: false,
		},
		{
			construct:     "jsonb_exists_operator",
			in:            `SELECT col ? 'key'`,
			want:          `SELECT col $1 'key'`,
			supported:     false,
			outOfContract: true,
		},
	}
	for _, row := range rows {
		t.Run(row.construct, func(t *testing.T) {
			t.Parallel()
			if !row.supported {
				return
			}
			got, err := e.rewrite(row.in)
			if err != nil {
				t.Fatal(err)
			}
			if got != row.want {
				t.Fatalf("got %q want %q", got, row.want)
			}
		})
	}
}
