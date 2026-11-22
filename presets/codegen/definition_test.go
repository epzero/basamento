package codegen

import (
	"encoding/json"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	codegenir "github.com/epzero/basamento/presets/codegen/ir"
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

// S8: Compile cross-checks the IR's order, edges, guards and sequential
// max_parallel against the runner, so the declarative flow cannot drift.
func TestCompileCrossChecksTheIRAgainstTheRunner(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*flow.IR)
	}{
		{
			name: "repair guard",
			mutate: func(def *flow.IR) {
				node := def.Nodes[codegenir.RepairStage]
				node.Guard = &flow.Guard{MaxVisits: 9}
				def.Nodes[codegenir.RepairStage] = node
			},
		},
		{
			name: "parallel verify",
			mutate: func(def *flow.IR) {
				node := def.Nodes[codegenir.VerifyStage]
				node.Spec = json.RawMessage(`{"over":"state.plan.tests","max_parallel":2,"node":"verify_test"}`)
				def.Nodes[codegenir.VerifyStage] = node
			},
		},
		{
			name: "empty run-tests args",
			mutate: func(def *flow.IR) {
				node := def.Nodes[codegenir.VerifyChild]
				node.Spec = json.RawMessage(`{"tool":"run-tests","args":[]}`)
				def.Nodes[codegenir.VerifyChild] = node
			},
		},
		{
			name: "run-tests outside the plan map",
			mutate: func(def *flow.IR) {
				node := def.Nodes[codegenir.CommitStage]
				node.Spec = json.RawMessage(`{"tool":"run-tests"}`)
				def.Nodes[codegenir.CommitStage] = node
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := DefaultSources()
			tc.mutate(src.Flows[FlowBootstrap])
			if err := src.Compile(); core.CodeOf(err) != core.CodeInvalidArgument {
				t.Fatalf("err = %v, want invalid_argument", err)
			}
		})
	}
}

func TestCheckLoopMatchesRunnerRejectsADroppedEdge(t *testing.T) {
	t.Parallel()
	def := codegenir.Bootstrap()
	if err := checkLoopMatchesRunner(def); err != nil {
		t.Fatalf("the default flow does not match the runner: %v", err)
	}
	def.Edges = def.Edges[1:]
	if err := checkLoopMatchesRunner(def); err == nil {
		t.Fatal("a flow with a dropped edge matched the runner")
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
