package prompt

import (
	"strings"
	"testing"
)

func TestWrapUntrustedSanitizesSource(t *testing.T) {
	t.Helper()
	cases := []struct {
		source string
		want   string
	}{
		{`quote"hack`, `quote_hack`},
		{`gt>lt<`, `gt_lt_`},
		{`amp&`, `amp_`},
		{"new\nline", "new_line"},
		{"", ""},
		{strings.Repeat("a", 200), strings.Repeat("a", 200)},
	}
	for _, tc := range cases {
		w := WrapUntrusted(tc.source, "body")
		if !strings.HasPrefix(w, openDelim) || !strings.HasSuffix(w, `">body</untrusted>`) {
			t.Fatalf("source %q: bad wrap %q", tc.source, w)
		}
		rest := w[len(openDelim):]
		quote := strings.IndexByte(rest, '"')
		if quote < 0 {
			t.Fatal("no closing quote on source attribute")
		}
		inner := rest[:quote]
		if inner != tc.want {
			t.Fatalf("source %q: sanitized %q, want %q", tc.source, inner, tc.want)
		}
		rec, ok := delimiterRecordInWrapped(w, 0)
		if !ok {
			t.Fatal("expected record in wrapped output")
		}
		if f := LintAssembled(&Assembled{Parts: []AssembledPart{{Name: "x", Kind: KindDynamic, Text: w, Wrapped: []DelimiterRecord{rec}}}}); len(f) != 0 {
			t.Fatalf("lint rejected wrapped output: %+v", f)
		}
	}
}
