package prompt

import "testing"

func TestTemplateContentHashInputOrder(t *testing.T) {
	t.Helper()
	base := []Input{{Name: "b", Trust: TrustUntrusted}, {Name: "a", Trust: TrustTrusted}}
	t1 := &Template{ID: "t", Version: "1", Inputs: base, Segments: []Segment{{Name: "s", Kind: KindStatic, Text: "x"}}}
	t2 := &Template{ID: "t", Version: "1", Inputs: []Input{{Name: "a", Trust: TrustTrusted}, {Name: "b", Trust: TrustUntrusted}}, Segments: t1.Segments}
	if TemplateContentHash(t1) != TemplateContentHash(t2) {
		t.Fatal("hash must not depend on input slice order")
	}
	dupA := &Template{
		ID: "d", Version: "1",
		Inputs: []Input{
			{Name: "doc", Trust: TrustUntrusted},
			{Name: "doc", Trust: TrustTrusted},
			{Name: "doc", Trust: TrustTrusted},
		},
		Segments: []Segment{{Name: "s", Kind: KindStatic, Text: "x"}},
	}
	dupB := &Template{
		ID: "d", Version: "1",
		Inputs: []Input{
			{Name: "doc", Trust: TrustTrusted},
			{Name: "doc", Trust: TrustUntrusted},
			{Name: "doc", Trust: TrustTrusted},
		},
		Segments: dupA.Segments,
	}
	if TemplateContentHash(dupA) != TemplateContentHash(dupB) {
		t.Fatal("duplicate input names must hash last-wins deterministically")
	}
	reg := NewRegistry()
	if _, err := reg.Register(dupA); err == nil {
		t.Fatal("duplicate input names must be refused at register")
	}
}