package sql

import "testing"

func TestRewritePostgresPlaceholderProbesEP063(t *testing.T) {
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
			name: "u_and_doubled_quotes_hide_question_mark",
			in:   `SELECT U&''?'', ?`,
			want: `SELECT U&''?'', $1`,
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