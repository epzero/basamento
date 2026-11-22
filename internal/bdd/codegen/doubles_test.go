package codegen

import (
	"context"
	"testing"

	preset "github.com/epzero/basamento/presets/codegen"
)

// S7: the scripted model records what it returned per stage, so the scenario
// can compare the sandbox's input with the model's output, not the runner's
// copy of it.
func TestScriptedModelRecordsTheLastWorkspacePerStage(t *testing.T) {
	t.Parallel()
	m := NewScriptedModel()
	m.SetPlan("store")
	if _, err := m.Complete(context.Background(), preset.Request{
		Agent: "implement_module", Stage: preset.StageImplement, Module: "store",
	}); err != nil {
		t.Fatal(err)
	}
	ws, ok := m.LastWorkspaceFor(preset.StageImplement)
	if !ok || ws.ID != "ws-store" {
		t.Fatalf("last implement workspace = %+v, %v; want ws-store", ws, ok)
	}
	if _, ok := m.LastWorkspaceFor(preset.StageRepair); ok {
		t.Fatal("the model reported a repair workspace it never returned")
	}
	if got := m.LastWorkspace(); got.ID != "ws-store" {
		t.Fatalf("LastWorkspace = %q, want ws-store", got.ID)
	}
}

// S7: the sandbox records the result it returned with the call, so a repair
// request can be compared with the verification that produced it.
func TestSimulatedSandboxRecordsTheResultItReturned(t *testing.T) {
	t.Parallel()
	s := NewSimulatedSandbox()
	s.SetResults(preset.TestResult{Failures: []string{"failure 1"}}, preset.TestResult{})
	first, err := s.RunTests(context.Background(), preset.Workspace{ID: "ws-store"}, "go test ./...")
	if err != nil {
		t.Fatal(err)
	}
	calls := s.Calls()
	if len(calls) != 1 {
		t.Fatalf("sandbox recorded %d calls, want 1", len(calls))
	}
	if len(first.Failures) != 1 || calls[0].Result.Failures[0] != "failure 1" {
		t.Fatalf("first result %+v, recorded %+v; want failure 1", first, calls[0].Result)
	}
}
