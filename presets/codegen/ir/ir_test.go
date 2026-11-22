package ir

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/epzero/basamento/flow"
	flowir "github.com/epzero/basamento/flow/ir"
)

func TestVerifyRunsOneToolNodePerPlanTest(t *testing.T) {
	t.Parallel()
	for _, def := range []*flow.IR{Bootstrap(), Iterate()} {
		verify, ok := def.Nodes[VerifyStage]
		if !ok {
			t.Fatalf("flow %q has no verify node", def.Name)
		}
		if verify.Kind != "map" {
			t.Fatalf("verify kind %q, want map", verify.Kind)
		}
		var spec struct {
			Over        string `json:"over"`
			Node        string `json:"node"`
			MaxParallel *int   `json:"max_parallel"`
		}
		if err := json.Unmarshal(verify.Spec, &spec); err != nil {
			t.Fatal(err)
		}
		if spec.Over != "state.plan.tests" || spec.Node != VerifyChild || spec.MaxParallel == nil || *spec.MaxParallel != 0 {
			t.Fatalf("verify map %+v, want one run-tests child per plan test, sequentially", spec)
		}
		child, ok := def.Nodes[VerifyChild]
		if !ok {
			t.Fatalf("flow %q has no verify child", def.Name)
		}
		if child.Kind != "tool" {
			t.Fatalf("verify child kind %q, want tool", child.Kind)
		}
		var childSpec map[string]json.RawMessage
		if err := json.Unmarshal(child.Spec, &childSpec); err != nil {
			t.Fatal(err)
		}
		if _, ok := childSpec["command"]; ok {
			t.Fatal("verify child carries command, which the host never delivers")
		}
		if string(childSpec["tool"]) != `"run-tests"` {
			t.Fatalf("verify child tool %s, want run-tests", childSpec["tool"])
		}
		// EP36-F1: the map item is the test and the host passes a tool
		// node's input as its call args, so a static args list is wrong.
		if _, ok := childSpec["args"]; ok {
			t.Fatal("verify child declares spec.args; the map supplies the test as its input")
		}
		if err := CheckVerify(def); err != nil {
			t.Fatalf("CheckVerify: %v", err)
		}
	}
}

// EP36-F1: a run-tests node outside the plan map, or a map child that claims
// static args, is rejected instead of running the tool with no command.
func TestCheckVerifyRejectsARunTestsNodeWithoutAPlanCommand(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*flow.IR)
	}{
		{
			name: "map child with empty args",
			mutate: func(def *flow.IR) {
				node := def.Nodes[VerifyChild]
				node.Spec = json.RawMessage(`{"tool":"run-tests","args":[]}`)
				def.Nodes[VerifyChild] = node
			},
		},
		{
			name: "tool outside the map with missing args",
			mutate: func(def *flow.IR) {
				node := def.Nodes[CommitStage]
				node.Spec = json.RawMessage(`{"tool":"run-tests"}`)
				def.Nodes[CommitStage] = node
			},
		},
		{
			name: "tool outside the map with empty args",
			mutate: func(def *flow.IR) {
				node := def.Nodes[CommitStage]
				node.Spec = json.RawMessage(`{"tool":"run-tests","args":[]}`)
				def.Nodes[CommitStage] = node
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := Bootstrap()
			tc.mutate(def)
			if err := CheckVerify(def); err == nil {
				t.Fatal("CheckVerify accepted a run-tests node with no command")
			}
		})
	}
}

// S8: the version flow states both lifecycle outcomes, not only FAIL.
func TestVersionFlowStatesTheApprovedAndRejectedEdges(t *testing.T) {
	t.Parallel()
	def := Version()
	var approved, rejected bool
	for _, edge := range def.Edges {
		if edge.From != VersionReview {
			continue
		}
		if edge.To == VersionAccepted && strings.Contains(edge.When, "approved") {
			approved = true
		}
		if edge.To == flowir.Fail && strings.Contains(edge.When, "rejected") {
			rejected = true
		}
	}
	if !approved || !rejected {
		t.Fatalf("version flow edges approved=%v rejected=%v, want both", approved, rejected)
	}
	if _, ok := def.Nodes[VersionAccepted]; !ok {
		t.Fatal("version flow has no accepted node")
	}
}

// S8: the IR's loop matches the edges and guards the runner implements.
func TestCheckLoopAcceptsBothRunFlows(t *testing.T) {
	t.Parallel()
	for _, def := range []*flow.IR{Bootstrap(), Iterate()} {
		if err := CheckLoop(def); err != nil {
			t.Fatalf("flow %q: %v", def.Name, err)
		}
	}
}
