package prompt

import "testing"

func TestTemplateContentHashTrustSpelling(t *testing.T) {
	t.Helper()
	base := &Template{
		ID: "docs", Version: "1",
		Inputs:   []Input{{Name: "doc"}},
		Segments: []Segment{{Name: "context", Kind: KindDynamic, Budget: 100, Text: `{{doc}}`}},
	}
	explicit := &Template{
		ID: "docs", Version: "1",
		Inputs:   []Input{{Name: "doc", Trust: TrustUntrusted}},
		Segments: []Segment{{Name: "context", Kind: KindDynamic, Budget: 100, Text: `{{doc}}`}},
	}
	if TemplateContentHash(base) != TemplateContentHash(explicit) {
		t.Fatal("empty and explicit untrusted trust differ in hash")
	}
	trusted := &Template{
		ID: "docs", Version: "1",
		Inputs:   []Input{{Name: "doc", Trust: TrustTrusted}},
		Segments: []Segment{{Name: "context", Kind: KindDynamic, Budget: 100, Text: `{{doc}}`}},
	}
	if TemplateContentHash(base) == TemplateContentHash(trusted) {
		t.Fatal("trusted change must change hash")
	}
}

func TestRegisterIdempotentTrustSpelling(t *testing.T) {
	t.Helper()
	reg := NewRegistry()
	first := &Template{
		ID: "docs", Version: "1",
		Inputs:   []Input{{Name: "doc"}},
		Segments: []Segment{{Name: "context", Kind: KindDynamic, Budget: 100, Text: `{{doc}}`}},
	}
	second := &Template{
		ID: "docs", Version: "1",
		Inputs:   []Input{{Name: "doc", Trust: TrustUntrusted}},
		Segments: []Segment{{Name: "context", Kind: KindDynamic, Budget: 100, Text: `{{doc}}`}},
	}
	if _, err := reg.Register(first); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Register(second); err != nil {
		t.Fatalf("re-register explicit untrusted: %v", err)
	}
}
