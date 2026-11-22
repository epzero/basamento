package authz

import (
	"strings"
	"testing"
)

func TestSanitizeAuditPartTable(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{in: "ada", want: "ada"},
		{in: "a\x00b", want: "[invalid]"},
		{in: "line\nbreak", want: "[invalid]"},
		{in: string([]byte{0xff, 0xfe}), want: "[invalid]"},
		{in: "\x85", want: "[invalid]"},
		{in: "\u0085", want: "[invalid]"},
		{in: "\u009f", want: "[invalid]"},
		{in: "\u202e", want: "[invalid]"},
		{in: "\u200b", want: "[invalid]"},
		{in: "\u2028", want: "[invalid]"},
		{in: "\u2029", want: "[invalid]"},
		{in: strings.Repeat("b", 256), want: strings.Repeat("b", 256)},
		{in: strings.Repeat("b", 257), want: "[invalid]"},
		{in: strings.Repeat("x", 1<<20), want: "[invalid]"},
	}
	for _, tc := range cases {
		if got := sanitizeAuditPart(tc.in); got != tc.want {
			t.Fatalf("got %q want %q", got, tc.want)
		}
	}
}

func TestSanitizeAuditResourceClean(t *testing.T) {
	if got := sanitizeAuditResource("run", "r1"); got != "run:r1" {
		t.Fatalf("got %q", got)
	}
}
