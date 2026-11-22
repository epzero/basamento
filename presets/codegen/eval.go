package codegen

import (
	"fmt"

	"github.com/epzero/basamento/core"
)

// EvalCase is one recorded eval case: the judge's metric scores for each run.
type EvalCase struct {
	ID   string
	Runs []map[string]int
}

// EvalSuite is the @eval suite a scenario scores. Run loads a case's recorded
// runs and collects their metric values; the scenario's thresholds decide
// pass/fail (REQ-PRESET-019). The suite never hardcodes a threshold.
type EvalSuite struct {
	Name  string
	Cases map[string]*EvalCase
	Ran   int // cases that produced recorded runs
}

// NewEvalSuite returns an empty suite.
func NewEvalSuite(name string) *EvalSuite {
	return &EvalSuite{Name: name, Cases: map[string]*EvalCase{}}
}

// Suite is the preset's @eval case ids, in fixture order.
func Suite() []string { return []string{"plan-modules", "iterate-minimal"} }

// EvalFixtures maps each case to the fixture specs/presets/design.md §4 names.
func EvalFixtures() map[string]string {
	return map[string]string{
		"plan-modules":    "expenses-app",
		"iterate-minimal": "cli-tool",
	}
}

// Run loads one case's recorded runs from src. A case with no recorded run, or
// with no fixture, fails closed.
func (s *EvalSuite) Run(caseID string, src Sources) error {
	runs, err := src.LoadEval(caseID)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		return core.NewError(core.CodeNotFound, fmt.Sprintf("codegen: eval case %q has no runs", caseID), nil)
	}
	if _, ok := EvalFixtures()[caseID]; !ok {
		return invalid("codegen: eval case %q has no fixture", caseID)
	}
	if _, ok := s.Cases[caseID]; !ok {
		s.Ran++
	}
	s.Cases[caseID] = &EvalCase{ID: caseID, Runs: runs}
	return nil
}

// Runs returns the number of recorded runs of a case.
func (s *EvalSuite) Runs(caseID string) int {
	if c, ok := s.Cases[caseID]; ok {
		return len(c.Runs)
	}
	return 0
}

// Mean returns the arithmetic mean of metric over the case's recorded runs.
func (s *EvalSuite) Mean(caseID, metric string) (float64, error) {
	c, ok := s.Cases[caseID]
	if !ok {
		return 0, core.NewError(core.CodeNotFound, fmt.Sprintf("codegen: eval case %q has not run", caseID), nil)
	}
	total := 0
	for i, run := range c.Runs {
		score, ok := run[metric]
		if !ok {
			return 0, core.NewError(core.CodeNotFound, fmt.Sprintf("codegen: eval case %q run %d has no %q score", caseID, i+1, metric), nil)
		}
		total += score
	}
	return float64(total) / float64(len(c.Runs)), nil
}
