package ir

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
)

func requireInvalid(t *testing.T, err error, fragments ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("want error")
	}
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code %s; message %q", core.CodeOf(err), err)
	}
	for _, want := range fragments {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("message %q does not contain %q", err.Error(), want)
		}
	}
}

func TestValidateAcceptsStartAndFailWithoutNodes(t *testing.T) {
	if err := Validate(simpleFlow()); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsReservedNodeNames(t *testing.T) {
	for _, name := range []string{Start, Fail} {
		f := simpleFlow()
		f.Nodes[name] = flow.Node{Kind: "task"}
		requireInvalid(t, Validate(f), `node "`+name+`"`, "reserved")
	}
}

func TestValidateRejectsEdgeIntoStart(t *testing.T) {
	f := simpleFlow()
	f.Edges = append(f.Edges, flow.Edge{From: "verify", To: Start})
	requireInvalid(t, Validate(f), `edge "verify" -> "START"`, "only an edge source")
}

func TestValidateRejectsEdgeOutOfFail(t *testing.T) {
	f := simpleFlow()
	f.Edges = append(f.Edges, flow.Edge{From: Fail, To: "verify"})
	requireInvalid(t, Validate(f), `edge "FAIL" -> "verify"`, "only an edge target")
}

func TestValidateRequiresExactlyOneEdgeFromStart(t *testing.T) {
	for _, count := range []int{0, 2} {
		f := simpleFlow()
		f.Edges = f.Edges[1:] // drop START -> plan
		for i := 0; i < count; i++ {
			f.Edges = append([]flow.Edge{{From: Start, To: "plan"}}, f.Edges...)
		}
		requireInvalid(t, Validate(f), "exactly one edge from", `"START"`)
	}
}

func TestValidateRejectsUnknownEdgeEnds(t *testing.T) {
	f := simpleFlow()
	f.Edges[1].To = "missing"
	requireInvalid(t, Validate(f), `edge "plan" -> "missing"`, `"missing"`)

	f = simpleFlow()
	f.Edges[1].From = "missing"
	requireInvalid(t, Validate(f), `edge "missing" -> "verify"`, `"missing"`)
}

func TestValidateRejectsUnknownKind(t *testing.T) {
	f := simpleFlow()
	node := f.Nodes["plan"]
	node.Kind = "teleport"
	f.Nodes["plan"] = node
	requireInvalid(t, Validate(f), `node "plan"`, `unknown kind "teleport"`)
}

func TestValidateRejectsUnreachableNode(t *testing.T) {
	f := simpleFlow()
	f.Nodes["orphan"] = flow.Node{Kind: "task"}
	requireInvalid(t, Validate(f), `node "orphan"`, "unreachable")
}

func TestValidateRejectsUnguardedCycle(t *testing.T) {
	f := &flow.IR{
		Name:    "cycle",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes:   taskNodes("verify", "repair"),
		Edges: []flow.Edge{
			{From: Start, To: "verify"},
			{From: "verify", To: "repair"},
			{From: "repair", To: "verify"},
		},
	}
	requireInvalid(t, Validate(f), `node "repair"`, "guard")
}

func TestValidateAcceptsGuardedCycle(t *testing.T) {
	f := &flow.IR{
		Name:    "cycle",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes: map[string]flow.Node{
			"verify": {Kind: "task"},
			"repair": {Kind: "task", Guard: &flow.Guard{MaxVisits: 3}},
		},
		Edges: []flow.Edge{
			{From: Start, To: "verify"},
			{From: "verify", To: "repair"},
			{From: "repair", To: "verify"},
		},
	}
	if err := Validate(f); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsSelfLoopWithoutGuard(t *testing.T) {
	f := &flow.IR{
		Name:    "loop",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes:   taskNodes("retry"),
		Edges: []flow.Edge{
			{From: Start, To: "retry"},
			{From: "retry", To: "retry"},
		},
	}
	requireInvalid(t, Validate(f), `node "retry"`, "guard")
}

func TestValidateTreatsEmptyGuardAsNoGuard(t *testing.T) {
	f := &flow.IR{
		Name:    "cycle",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes: map[string]flow.Node{
			"verify": {Kind: "task"},
			"repair": {Kind: "task", Guard: &flow.Guard{}},
		},
		Edges: []flow.Edge{
			{From: Start, To: "verify"},
			{From: "verify", To: "repair"},
			{From: "repair", To: "verify"},
		},
	}
	requireInvalid(t, Validate(f), `node "repair"`, "guard")
}

func TestValidateReportsTheSameCycleEveryTime(t *testing.T) {
	names := []string{"repair", "verify", "plan", "check", "review"}
	var first string
	rnd := rand.New(rand.NewSource(1)) // seeded: the test reproduces the same orders
	for i := 0; i < 50; i++ {
		nodes := map[string]flow.Node{}
		for _, j := range rnd.Perm(len(names)) {
			nodes[names[j]] = flow.Node{Kind: "task"}
		}
		f := &flow.IR{
			Name:    "cycle",
			Version: 1,
			State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
			Nodes:   nodes,
			Edges: []flow.Edge{
				{From: Start, To: "plan"},
				{From: "plan", To: "verify"},
				{From: "verify", To: "repair"},
				{From: "repair", To: "verify"},
				{From: "plan", To: "check"},
				{From: "check", To: "review"},
			},
		}
		err := Validate(f)
		if err == nil {
			t.Fatal("want error")
		}
		if first == "" {
			first = err.Error()
		} else if err.Error() != first {
			t.Fatalf("iteration %d: %q != %q", i, err, first)
		}
	}
	if !strings.Contains(first, `node "repair"`) {
		t.Fatalf("names %q, want the lowest node of the cycle", first)
	}
}

func TestValidateRejectsInvalidSchema(t *testing.T) {
	f := simpleFlow()
	f.State.Schema = json.RawMessage(`{"type":"count"}`)
	requireInvalid(t, Validate(f), "state schema")
}

func TestValidateRejectsNilSchema(t *testing.T) {
	f := simpleFlow()
	f.State.Schema = nil
	requireInvalid(t, Validate(f), "state schema")
}

func TestValidateRejectsReducerOnUnknownKey(t *testing.T) {
	f := simpleFlow()
	f.State.Reducers = map[string]string{"notes": "append"}
	requireInvalid(t, Validate(f), `"notes"`, "reducer")
}

func TestValidateRejectsUnknownReducer(t *testing.T) {
	f := simpleFlow()
	f.State.Reducers = map[string]string{"tests": "shuffle"}
	requireInvalid(t, Validate(f), `"tests"`, "reducer")
}

func TestValidateAcceptsKnownReducers(t *testing.T) {
	f := simpleFlow()
	f.State.Reducers = map[string]string{"tests": "merge_by(path)"}
	if err := Validate(f); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRequiresName(t *testing.T) {
	f := simpleFlow()
	f.Name = ""
	requireInvalid(t, Validate(f), "name")
}
