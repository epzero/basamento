package prompt

import (
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestRepeatedVariableCountsEveryOccurrence(t *testing.T) {
	t.Helper()
	reg := NewRegistry()
	doc6 := "123456"
	tpl := &Template{
		ID: "docs", Version: "1",
		Inputs:   []Input{{Name: "doc"}},
		Segments: []Segment{{Name: "context", Kind: KindDynamic, Budget: 10, Text: `{{doc}} / {{doc}}`}},
	}
	if _, err := reg.Register(tpl); err != nil {
		t.Fatal(err)
	}
	_, err := reg.Assemble("docs@1", map[string]string{"doc": doc6}, nil)
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("two occurrences of six code points: want segment over budget, got %v", err)
	}
	tpl2 := &Template{
		ID: "docs2", Version: "1",
		Inputs:      []Input{{Name: "doc"}},
		TotalBudget: 14,
		Segments:    []Segment{{Name: "context", Kind: KindDynamic, Budget: 20, Text: `{{doc}} / {{doc}}`}},
	}
	if _, err := reg.Register(tpl2); err != nil {
		t.Fatal(err)
	}
	_, err = reg.Assemble("docs2@1", map[string]string{"doc": doc6}, nil)
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("two occurrences exceed total budget 14: got %v", err)
	}
	one := strings.Repeat("x", 10)
	tpl3 := &Template{
		ID: "once", Version: "1",
		Inputs:   []Input{{Name: "doc"}},
		Segments: []Segment{{Name: "context", Kind: KindDynamic, Budget: 10, Text: `{{doc}}`}},
	}
	if _, err := reg.Register(tpl3); err != nil {
		t.Fatal(err)
	}
	a, err := reg.Assemble("once@1", map[string]string{"doc": one}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Parts[0].ContentSize != 10 {
		t.Fatalf("once: size %d", a.Parts[0].ContentSize)
	}
	tpl4 := &Template{
		ID: "thrice", Version: "1",
		Inputs:   []Input{{Name: "doc"}},
		Segments: []Segment{{Name: "context", Kind: KindDynamic, Budget: 15, Text: `{{doc}}{{doc}}{{doc}}`}},
	}
	if _, err := reg.Register(tpl4); err != nil {
		t.Fatal(err)
	}
	six := "123456"
	_, err = reg.Assemble("thrice@1", map[string]string{"doc": six}, nil)
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("three x six code points over budget 15: got %v", err)
	}
}