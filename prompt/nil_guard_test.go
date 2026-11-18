package prompt

import (
	"testing"

	"github.com/epzero/basamento/core"
)

func TestNilTemplateGuards(t *testing.T) {
	t.Helper()
	reg := NewRegistry()
	if _, err := reg.Register(nil); err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("Register(nil): %v", err)
	}
	if f := LintTemplate(nil); len(f) == 0 || f[0].Rule != "template_required" {
		t.Fatalf("LintTemplate(nil): %+v", f)
	}
	if TemplateContentHash(nil) != "" {
		t.Fatal("TemplateContentHash(nil)")
	}
}

func TestNilAssembledGuards(t *testing.T) {
	t.Helper()
	if f := LintAssembled(nil); len(f) == 0 || f[0].Rule != "assembled_required" {
		t.Fatalf("LintAssembled(nil): %+v", f)
	}
	if ContentHash(nil) != "" {
		t.Fatal("ContentHash(nil)")
	}
	if PrefixHash(nil) != "" {
		t.Fatal("PrefixHash(nil)")
	}
}
