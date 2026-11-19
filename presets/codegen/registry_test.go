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
	reg.Resolve("v2", "approved")
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
	reg.Resolve("v2", "rejected")
	if reg.Current != "v1" {
		t.Fatalf("current = %q, want v1", reg.Current)
	}
	if v := reg.Get("v2"); v.Status != StatusRejected || v.Code != core.CodeCancelled {
		t.Fatalf("v2 = %s/%s, want rejected cancelled", v.Status, v.Code)
	}
}
