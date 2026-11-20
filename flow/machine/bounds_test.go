package machine

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/ir"
)

func TestPureSuperstepLimitEndsCostOnlySetCycle(t *testing.T) {
	def := pureSetCostCycle()
	m, err := New(def, WithMaxPureSupersteps(50))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var commands []flow.Command
	var startErr error
	go func() {
		defer close(done)
		_, commands, startErr = m.Start(json.RawMessage(`{}`))
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Start hung: want budget_exceeded from pure superstep limit")
	}
	if startErr != nil {
		t.Fatal(startErr)
	}
	if len(commands) != 1 || commands[0].Type != "fail" {
		t.Fatalf("commands %+v, want fail", commands)
	}
	if code := commandCode(t, commands[0]); code != string(core.CodeBudgetExceeded) {
		t.Fatalf("code %q, want budget_exceeded", code)
	}
}

func TestMaxVisitsPureLoopRespectsDeclaredBudget(t *testing.T) {
	def := pureMaxVisitsLoop(200)
	m, err := New(def, WithMaxPureSupersteps(50))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, commands, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	for step := 0; len(commands) != 1 || commands[0].Type != "fail"; step++ {
		if step > 250 {
			t.Fatalf("loop did not finish within 250 steps; commands %+v", commands)
		}
		if len(commands) != 1 || commands[0].Node != "loop" {
			t.Fatalf("step %d: commands %+v, want run_node loop", step, commands)
		}
		snapshot, commands, err = m.Apply(snapshot, flow.Event{Type: "node_completed", Node: "loop"})
		if err != nil {
			t.Fatal(err)
		}
	}
	if code := commandCode(t, commands[0]); code != string(core.CodeBudgetExceeded) {
		t.Fatalf("code %q, want budget_exceeded", code)
	}
	if snapshot.Visit["loop"] != 200 {
		t.Fatalf("visits %d, want 200", snapshot.Visit["loop"])
	}
}

func TestWhileTerminatedPureLoopHonorsFloor(t *testing.T) {
	def := pureWhileLoop(40)
	t.Run("floor 50 passes", func(t *testing.T) {
		m, err := New(def, WithMaxPureSupersteps(50))
		if err != nil {
			t.Fatal(err)
		}
		snapshot, commands, err := m.Start(json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if len(commands) != 1 || commands[0].Type != "complete" {
			t.Fatalf("commands %+v, want complete", commands)
		}
		var state map[string]any
		if err := json.Unmarshal(snapshot.State, &state); err != nil {
			t.Fatal(err)
		}
		if n, _ := state["n"].(float64); int(n) != 40 {
			t.Fatalf("n = %v, want 40", state["n"])
		}
	})
	t.Run("floor 20 fails", func(t *testing.T) {
		m, err := New(def, WithMaxPureSupersteps(20))
		if err != nil {
			t.Fatal(err)
		}
		_, commands, err := m.Start(json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if len(commands) != 1 || commands[0].Type != "fail" {
			t.Fatalf("commands %+v, want fail", commands)
		}
		if code := commandCode(t, commands[0]); code != string(core.CodeBudgetExceeded) {
			t.Fatalf("code %q, want budget_exceeded", code)
		}
	})
}

func TestWithMaxPureSuperstepsRejectsZero(t *testing.T) {
	_, err := New(triageFlow(), WithMaxPureSupersteps(0))
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("error %v, want invalid_argument", err)
	}
}

func TestMapChildWithGuardRejectedAtNew(t *testing.T) {
	def := &flow.IR{
		Name:    "guarded-child",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes: map[string]flow.Node{
			"fan":  {Kind: "set", Spec: json.RawMessage(`{"items":[1]}`)},
			"mapn": {Kind: "map", Spec: json.RawMessage(`{"over":"state.items","node":"work"}`)},
			"work": {Kind: "task", Guard: &flow.Guard{MaxVisits: 3}},
		},
		Edges: []flow.Edge{{From: ir.Start, To: "fan"}, {From: "fan", To: "mapn"}, {From: "mapn", To: "work"}},
	}
	_, err := New(def)
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("error %v, want invalid_argument", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "mapn") || !strings.Contains(msg, "work") {
		t.Fatalf("error %q, want map and child named", msg)
	}
	if !strings.Contains(msg, "guard") {
		t.Fatalf("error %q, want guard explanation", msg)
	}
}

func pureSetCostCycle() *flow.IR {
	return &flow.IR{
		Name:    "pure-cost-cycle",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes: map[string]flow.Node{
			"a": {Kind: "set", Spec: json.RawMessage(`{}`), Guard: &flow.Guard{CostMpts: 10}},
			"b": {Kind: "set", Spec: json.RawMessage(`{}`)},
		},
		Edges: []flow.Edge{
			{From: ir.Start, To: "a"},
			{From: "a", To: "b"},
			{From: "b", To: "a"},
		},
	}
}

func pureMaxVisitsLoop(max int) *flow.IR {
	return &flow.IR{
		Name:    "visit-loop",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes: map[string]flow.Node{
			"loop": {Kind: "task", Guard: &flow.Guard{MaxVisits: max}},
		},
		Edges: []flow.Edge{{From: ir.Start, To: "loop"}, {From: "loop", To: "loop"}},
	}
}

func pureWhileLoop(target int) *flow.IR {
	return &flow.IR{
		Name:     "while-loop",
		Version:  1,
		Defaults: map[string]json.RawMessage{"n": json.RawMessage(`0`)},
		State: flow.StateSpec{
			Schema:   json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}}}`),
			Reducers: map[string]string{"n": "replace"},
		},
		Nodes: map[string]flow.Node{
			"loop": {
				Kind:  "set",
				Out:   "n",
				Spec:  json.RawMessage(`"state.n + 1"`),
				Guard: &flow.Guard{While: "state.n < " + strconv.Itoa(target), CostMpts: 1},
			},
		},
		Edges: []flow.Edge{{From: ir.Start, To: "loop"}, {From: "loop", To: "loop"}},
	}
}
