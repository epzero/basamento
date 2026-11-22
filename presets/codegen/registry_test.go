package codegen

import (
	"testing"

	"github.com/epzero/basamento/core"
)

func newLifecycle() (*Registry, Sources) {
	src := DefaultSources()
	reg := NewRegistry([]string{"plan-modules", "iterate-minimal"})
	reg.Add("v1", src, map[string]int{"plan-modules": 82, "iterate-minimal": 75})
	reg.AcceptInitial("v1")
	return reg, src
}

func TestCompileFailureRejectsBeforeEval(t *testing.T) {
	t.Parallel()
	reg, src := newLifecycle()
	broken := Sources{Flows: src.Flows, Prompts: map[string][]byte{}, Evals: src.Evals}
	reg.Add("v2", broken, map[string]int{"plan-modules": 90, "iterate-minimal": 90})
	v := reg.Propose("v2")
	if v.Status != StatusRejected || v.Code != core.CodeInvalidArgument {
		t.Fatalf("v2 = %s/%s, want rejected invalid_argument", v.Status, v.Code)
	}
	if v.GateOpen {
		t.Fatal("the review gate opened for a version that does not compile")
	}
	if reg.EvalRuns != 0 {
		t.Fatalf("eval ran %d cases, want 0", reg.EvalRuns)
	}
}

func TestLowerScoreRejectsWithConflict(t *testing.T) {
	t.Parallel()
	reg, src := newLifecycle()
	reg.Add("v2", src, map[string]int{"plan-modules": 85, "iterate-minimal": 74})
	v := reg.Propose("v2")
	if v.Status != StatusRejected || v.Code != core.CodeConflict || v.Case != "iterate-minimal" {
		t.Fatalf("v2 = %s/%s/%s, want rejected conflict iterate-minimal", v.Status, v.Code, v.Case)
	}
	if reg.Current != "v1" {
		t.Fatalf("current = %q, want v1", reg.Current)
	}
}

func TestMissingCaseIsARegression(t *testing.T) {
	t.Parallel()
	reg, src := newLifecycle()
	reg.Add("v2", src, map[string]int{"plan-modules": 82})
	v := reg.Propose("v2")
	if v.Code != core.CodeConflict || v.Case != "iterate-minimal" {
		t.Fatalf("v2 = %s/%s, want conflict iterate-minimal", v.Code, v.Case)
	}
}

func TestEqualScoresOpenTheGate(t *testing.T) {
	t.Parallel()
	reg, src := newLifecycle()
	reg.Add("v2", src, map[string]int{"plan-modules": 82, "iterate-minimal": 75})
	v := reg.Propose("v2")
	if !v.GateOpen || v.Status != StatusWaiting {
		t.Fatalf("v2 = %s gate=%v, want waiting with the gate open", v.Status, v.GateOpen)
	}
}

func TestApprovalMakesTheVersionCurrent(t *testing.T) {
	t.Parallel()
	reg, src := newLifecycle()
	reg.Add("v2", src, map[string]int{"plan-modules": 85, "iterate-minimal": 80})
	reg.Propose("v2")
	if _, err := reg.Resolve("v2", "approved"); err != nil {
		t.Fatal(err)
	}
	if reg.Current != "v2" || reg.Get("v2").Status != StatusAccepted {
		t.Fatalf("current = %q, v2 = %s", reg.Current, reg.Get("v2").Status)
	}
	if reg.Get("v1").Status != StatusSuperseded {
		t.Fatalf("v1 = %s, want superseded", reg.Get("v1").Status)
	}
}

func TestRejectionLeavesThePredecessorCurrent(t *testing.T) {
	t.Parallel()
	reg, src := newLifecycle()
	reg.Add("v2", src, map[string]int{"plan-modules": 85, "iterate-minimal": 80})
	reg.Propose("v2")
	if _, err := reg.Resolve("v2", "rejected"); err != nil {
		t.Fatal(err)
	}
	if reg.Current != "v1" {
		t.Fatalf("current = %q, want v1", reg.Current)
	}
	if v := reg.Get("v2"); v.Status != StatusRejected || v.Code != core.CodeCancelled {
		t.Fatalf("v2 = %s/%s, want rejected cancelled", v.Status, v.Code)
	}
}

// S11: an unknown decision is an error and leaves the review gate open.
func TestResolveRejectsAnUnknownDecision(t *testing.T) {
	t.Parallel()
	reg, src := newLifecycle()
	reg.Add("v2", src, map[string]int{"plan-modules": 82, "iterate-minimal": 75})
	reg.Propose("v2")
	v, err := reg.Resolve("v2", "maybe")
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
	if v == nil || !v.GateOpen || v.Status != StatusWaiting {
		t.Fatalf("v2 = %+v, want waiting with the gate open", v)
	}
}

func TestResolveRejectsAVersionNotWaiting(t *testing.T) {
	t.Parallel()
	reg, src := newLifecycle()
	reg.Add("v2", src, map[string]int{"plan-modules": 85, "iterate-minimal": 80})
	reg.Propose("v2")
	if _, err := reg.Resolve("v2", "approved"); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Resolve("v2", "rejected"); core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("err = %v, want conflict for a version that is no longer waiting", err)
	}
	if reg.Current != "v2" {
		t.Fatalf("current = %q, want v2", reg.Current)
	}
}

// S11: a predecessor with no score for a case blocks acceptance; the
// regression check is not skipped.
func TestProposeFailsClosedWhenThePredecessorHasNoScore(t *testing.T) {
	t.Parallel()
	src := DefaultSources()
	reg := NewRegistry([]string{"plan-modules", "iterate-minimal"})
	reg.Add("v1", src, map[string]int{"plan-modules": 82})
	reg.AcceptInitial("v1")
	reg.Add("v2", src, map[string]int{"plan-modules": 85, "iterate-minimal": 80})
	v := reg.Propose("v2")
	if v.Status != StatusRejected || v.Code != core.CodeConflict || v.Case != "iterate-minimal" {
		t.Fatalf("v2 = %s/%s/%s, want rejected conflict iterate-minimal", v.Status, v.Code, v.Case)
	}
}

// S10: EvalRuns counts the cases that actually produced a score, not the suite
// length.
func TestEvalRunsCountsOnlyScoredCases(t *testing.T) {
	t.Parallel()
	reg, src := newLifecycle()
	reg.Add("v2", src, map[string]int{"plan-modules": 82})
	reg.Propose("v2")
	if reg.EvalRuns != 1 {
		t.Fatalf("eval ran %d cases, want 1", reg.EvalRuns)
	}
}

// S11: Add keeps a prior rejection and Propose does not silently re-propose it.
func TestAddKeepsAPriorRejection(t *testing.T) {
	t.Parallel()
	reg, src := newLifecycle()
	reg.Add("v2", src, map[string]int{"plan-modules": 85, "iterate-minimal": 74})
	if v := reg.Propose("v2"); v.Code != core.CodeConflict {
		t.Fatalf("v2 = %s/%s, want conflict", v.Status, v.Code)
	}
	reg.Add("v2", src, map[string]int{"plan-modules": 95, "iterate-minimal": 95})
	if v := reg.Propose("v2"); v.Status != StatusRejected || v.Code != core.CodeConflict {
		t.Fatalf("v2 = %s/%s, want the recorded rejection", v.Status, v.Code)
	}
	if reg.Current != "v1" {
		t.Fatalf("current = %q, want v1", reg.Current)
	}
}
