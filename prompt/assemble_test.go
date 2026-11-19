package prompt

import (
	"testing"

	"github.com/epzero/basamento/core"
)

func TestAssembleDeterministicInputs(t *testing.T) {
	t.Helper()
	reg := NewRegistry()
	tpl := &Template{
		ID: "pair", Version: "1",
		Inputs:   []Input{{Name: "first", Trust: TrustTrusted}, {Name: "second", Trust: TrustTrusted}},
		Segments: []Segment{{Name: "context", Kind: KindDynamic, Budget: 100, Text: `{{first}} / {{second}}`}},
	}
	if _, err := reg.Register(tpl); err != nil {
		t.Fatal(err)
	}
	a1, err := reg.Assemble("pair@1", map[string]string{"first": "a", "second": "b"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := reg.Assemble("pair@1", map[string]string{"second": "b", "first": "a"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a1.Hash != a2.Hash {
		t.Fatalf("hash mismatch %s vs %s", a1.Hash, a2.Hash)
	}
}

func TestAssembleNoReexpand(t *testing.T) {
	t.Helper()
	reg := NewRegistry()
	tpl := &Template{
		ID: "pair", Version: "1",
		Inputs:   []Input{{Name: "first", Trust: TrustTrusted}, {Name: "second", Trust: TrustTrusted}},
		Segments: []Segment{{Name: "context", Kind: KindDynamic, Budget: 100, Text: `{{first}} / {{second}}`}},
	}
	if _, err := reg.Register(tpl); err != nil {
		t.Fatal(err)
	}
	a, err := reg.Assemble("pair@1", map[string]string{"first": "{{second}}", "second": "hunter2"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Parts[0].Text != `{{second}} / hunter2` {
		t.Fatalf("got %q", a.Parts[0].Text)
	}
}

func TestSegmentBudgetBoundaries(t *testing.T) {
	t.Helper()
	reg := NewRegistry()
	tpl := &Template{
		ID: "docs", Version: "1",
		Inputs:   []Input{{Name: "doc"}},
		Segments: []Segment{{Name: "context", Kind: KindDynamic, Budget: 10, Text: `{{doc}}`}},
	}
	if _, err := reg.Register(tpl); err != nil {
		t.Fatal(err)
	}
	_, err := reg.Assemble("docs@1", map[string]string{"doc": "hello world"}, nil)
	if err == nil || string(core.CodeOf(err)) != "invalid_argument" {
		t.Fatalf("want invalid_argument, got %v", err)
	}
	a, err := reg.Assemble("docs@1", map[string]string{"doc": "hello worl"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Parts[0].ContentSize != 10 {
		t.Fatalf("size %d", a.Parts[0].ContentSize)
	}
}

func TestRegisterIdempotent(t *testing.T) {
	t.Helper()
	reg := NewRegistry()
	tpl := &Template{
		ID: "greeting", Version: "1",
		Segments: []Segment{{Name: "main", Kind: KindStatic, Text: "Say hello."}},
	}
	if _, err := reg.Register(tpl); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Register(tpl); err != nil {
		t.Fatal(err)
	}
	if reg.VersionCount("greeting") != 1 {
		t.Fatal("expected one version")
	}
}
