package prompt

import "testing"

func TestLintUntrustedInStatic(t *testing.T) {
	t.Helper()
	tpl := &Template{
		ID: "x", Version: "1",
		Inputs:   []Input{{Name: "doc"}},
		Segments: []Segment{{Name: "rules", Kind: KindStatic, Text: `{{doc}}`}},
	}
	findings := LintTemplate(tpl)
	var found bool
	for _, f := range findings {
		if f.Rule == "untrusted_in_cacheable" && f.Segment == "rules" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("static with untrusted input: %+v", findings)
	}
}
