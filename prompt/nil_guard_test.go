package prompt

import (
	"testing"

	"github.com/epzero/basamento/core"
)

// Add a row when a new exported API takes a pointer, map, slice, or interface.
func TestExportedNilArguments(t *testing.T) {
	t.Helper()
	reg := NewRegistry()
	if _, err := reg.Register(nil); err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("Register(nil): %v", err)
	}
	if _, err := Assemble(nil, "x@1", map[string]string{}, nil); err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("Assemble(nil template): %v", err)
	}
	if _, err := reg.Assemble("missing@1", nil, nil); err == nil {
		t.Fatal("Assemble nil inputs on missing template")
	}
	if f := LintTemplate(nil); len(f) == 0 || f[0].Rule != "template_required" {
		t.Fatalf("LintTemplate(nil): %+v", f)
	}
	if f := LintAssembled(nil); len(f) == 0 || f[0].Rule != "assembled_required" {
		t.Fatalf("LintAssembled(nil): %+v", f)
	}
	if TemplateContentHash(nil) != "" {
		t.Fatal("TemplateContentHash(nil)")
	}
	if ContentHash(nil) != "" {
		t.Fatal("ContentHash(nil)")
	}
	if PrefixHash(nil) != "" {
		t.Fatal("PrefixHash(nil)")
	}
	if TotalContentSize(nil) != 0 {
		t.Fatal("TotalContentSize(nil)")
	}
	if AssembledText(nil) != "" {
		t.Fatal("AssembledText(nil)")
	}
	if n, ok := SegmentContentSize(nil, "x"); n != 0 || ok {
		t.Fatalf("SegmentContentSize(nil): %d, %v", n, ok)
	}
	if _, ok := CheckDeclaredVars(nil); ok {
		t.Fatal("CheckDeclaredVars(nil)")
	}
	if Substitute("x", nil) != "x" {
		t.Fatal("Substitute(nil values)")
	}
}
