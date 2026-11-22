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
			Over string `json:"over"`
			Node string `json:"node"`
		}
		if err := json.Unmarshal(verify.Spec, &spec); err != nil {
			t.Fatal(err)
		}
		if spec.Over != "state.plan.tests" || spec.Node != VerifyChild {
			t.Fatalf("verify map %+v, want one run-tests child per plan test", spec)
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
	}

	nodes := VerifyNodes([]string{"go test ./store", "go test ./api"})
	if len(nodes) != 2 {
		t.Fatalf("VerifyNodes returned %d nodes, want 2", len(nodes))
	}
	want := []string{"go test ./store", "go test ./api"}
	for i, id := range []string{"verify_test_1", "verify_test_2"} {
		node, ok := nodes[id]
		if !ok {
			t.Fatalf("no node %q", id)
		}
		var spec struct {
			Tool string   `json:"tool"`
			Args []string `json:"args"`
		}
		if err := json.Unmarshal(node.Spec, &spec); err != nil {
			t.Fatal(err)
		}
		if spec.Tool != "run-tests" || len(spec.Args) != 1 || spec.Args[0] != want[i] {
			t.Fatalf("node %q spec %+v, want run-tests with %q", id, spec, want[i])
		}
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
