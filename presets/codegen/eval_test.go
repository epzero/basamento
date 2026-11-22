package codegen

import (
	"testing"

	"github.com/epzero/basamento/core"
)

// S10: the suite runs a recorded case and exposes the values its thresholds
// score, rather than a hardcoded mean.
func TestEvalSuiteScoresTheRecordedRuns(t *testing.T) {
	t.Parallel()
	s := NewEvalSuite("codegen")
	if err := s.Run("plan-modules", DefaultSources()); err != nil {
		t.Fatal(err)
	}
	if s.Ran != 1 {
		t.Fatalf("suite ran %d cases, want 1", s.Ran)
	}
	if got := s.Runs("plan-modules"); got != 5 {
		t.Fatalf("case ran %d times, want 5", got)
	}
	runs, err := DefaultSources().LoadEval("plan-modules")
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, run := range runs {
		total += run["module_coverage"]
	}
	want := float64(total) / float64(len(runs))
	got, err := s.Mean("plan-modules", "module_coverage")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("mean module coverage %.2f, want %.2f", got, want)
	}
}

func TestEvalSuiteRejectsAnUnknownCase(t *testing.T) {
	t.Parallel()
	s := NewEvalSuite("codegen")
	if err := s.Run("no-such-case", DefaultSources()); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("err = %v, want not_found", err)
	}
	if s.Ran != 0 {
		t.Fatalf("suite ran %d cases, want 0", s.Ran)
	}
}

func TestEvalSuiteRejectsAMissingMetric(t *testing.T) {
	t.Parallel()
	s := NewEvalSuite("codegen")
	if err := s.Run("plan-modules", DefaultSources()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Mean("plan-modules", "no_such_metric"); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("err = %v, want not_found", err)
	}
}
