package prompt

import (
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestLintTemplateStructure(t *testing.T) {
	t.Helper()
	cases := []struct {
		name string
		tpl  *Template
		rule string
	}{
		{"empty_id", &Template{Version: "1"}, "empty_template_id"},
		{"empty_version", &Template{ID: "x"}, "empty_template_version"},
		{"at_id", &Template{ID: "a@b", Version: "1"}, "at_in_template_id"},
		{"dup_input", &Template{ID: "x", Version: "1", Inputs: []Input{{Name: "a"}, {Name: "a"}}}, "duplicate_input"},
		{"dup_segment", &Template{ID: "x", Version: "1", Segments: []Segment{{Name: "s", Kind: KindStatic}, {Name: "s", Kind: KindUser}}}, "duplicate_segment"},
		{"two_history", &Template{ID: "x", Version: "1", Segments: []Segment{{Name: "h1", Kind: KindHistory}, {Name: "h2", Kind: KindHistory}}}, "multiple_history"},
		{"unknown_kind", &Template{ID: "x", Version: "1", Segments: []Segment{{Name: "x", Kind: Kind("bogus")}}}, "unknown_segment_kind"},
	}
	for _, tc := range cases {
		findings := LintTemplate(tc.tpl)
		var found bool
		for _, f := range findings {
			if f.Rule == tc.rule && f.Severity == "error" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s: want rule %q in %+v", tc.name, tc.rule, findings)
		}
	}
	dupSeg := &Template{
		ID: "dup", Version: "1",
		Inputs: []Input{{Name: "doc"}},
		Segments: []Segment{
			{Name: "a", Kind: KindDynamic, Budget: 5, Text: `{{doc}}`},
			{Name: "b", Kind: KindUser, Budget: 5, Text: `{{doc}}`},
		},
	}
	long := strings.Repeat("x", 6)
	_, err := Assemble(dupSeg, "dup@1", map[string]string{"doc": long}, nil)
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("duplicate segment names: want budget error, got %v", err)
	}
}

func TestHistoryOverBudgetNonPositive(t *testing.T) {
	t.Helper()
	reg := NewRegistry()
	tpl := &Template{
		ID: "hist", Version: "1",
		Segments: []Segment{{Name: "past", Kind: KindHistory, Budget: -1}},
	}
	if _, err := reg.Register(tpl); err != nil {
		t.Fatal(err)
	}
	hist := []HistoryEntry{{Role: "user", Text: strings.Repeat("y", 100)}}
	a, err := reg.Assemble("hist@1", map[string]string{}, hist)
	if err != nil {
		t.Fatalf("negative history budget is unbounded: %v", err)
	}
	if a.HistoryDropped != 0 {
		t.Fatalf("dropped %d", a.HistoryDropped)
	}
}