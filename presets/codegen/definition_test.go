package codegen

import (
	"testing"

	"github.com/epzero/basamento/core"
)

func TestDefaultSourcesCompileAndHashStably(t *testing.T) {
	t.Parallel()
	src := DefaultSources()
	if err := src.Compile(); err != nil {
		t.Fatal(err)
	}
	first, err := src.Version()
	if err != nil {
		t.Fatal(err)
	}
	second, err := DefaultSources().Version()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("version %q is not stable, got %q", first, second)
	}
	if len(first) != 64 {
		t.Fatalf("version %q is not a sha256 hex digest", first)
	}
}

func TestCompileRejectsAMissingPrompt(t *testing.T) {
	t.Parallel()
	src := DefaultSources()
	delete(src.Prompts, "prompts/planner.md")
	err := src.Compile()
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
}

func TestRecordedEvalMeans(t *testing.T) {
	t.Parallel()
	src := DefaultSources()
	mean, err := src.Mean("plan-modules", "module_coverage")
	if err != nil {
		t.Fatal(err)
	}
	if mean < 80 {
		t.Fatalf("mean module coverage %.2f, want at least 80", mean)
	}
}
