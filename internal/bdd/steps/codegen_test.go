package steps

import (
	"testing"

	"github.com/epzero/basamento/internal/bdd/codegen"
	preset "github.com/epzero/basamento/presets/codegen"
)

// S7: the sandbox assertion fails when the runner passes a workspace other
// than the one the model returned.
func TestCheckSandboxWorkspaceRejectsAnotherWorkspace(t *testing.T) {
	t.Parallel()
	calls := []codegen.SandboxCall{{Workspace: preset.Workspace{ID: "ws-stale"}}}
	if err := checkSandboxWorkspace(calls, preset.Workspace{ID: "ws-store"}); err == nil {
		t.Fatal("a call on a different workspace passed")
	}
	if err := checkSandboxWorkspace(calls, preset.Workspace{ID: "ws-stale"}); err != nil {
		t.Fatalf("a call on the model's workspace failed: %v", err)
	}
}

// S7: the repair assertion fails when the request carries failures other than
// the verification result that produced it.
func TestCheckRepairFailuresRejectsAnotherVerification(t *testing.T) {
	t.Parallel()
	calls := []codegen.SandboxCall{{Result: preset.TestResult{Failures: []string{"failure 1"}}}}
	if err := checkRepairFailures(calls, []string{"unrelated failure"}); err == nil {
		t.Fatal("a repair with unrelated failures passed")
	}
	if err := checkRepairFailures(calls, []string{"failure 1"}); err != nil {
		t.Fatalf("a repair with the verification's failures failed: %v", err)
	}
}
