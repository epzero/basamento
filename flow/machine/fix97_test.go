package machine

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/ir"
)

func TestApplyLeavesInputSnapshotUnchangedOnSuccess(t *testing.T) {
	m := requireMachine(t, triageFlow())
	held, _, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(held)
	if err != nil {
		t.Fatal(err)
	}
	next, _, err := m.Apply(held, flow.Event{
		Type:    "node_completed",
		Node:    "verify",
		Payload: json.RawMessage(`0`),
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(held)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("input snapshot changed after success")
	}
	if next.Seq == held.Seq {
		t.Fatal("want advanced snapshot returned")
	}
}

func TestApplyLeavesInputSnapshotUnchangedOnError(t *testing.T) {
	m := requireMachine(t, triageFlow())
	held, _, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(held)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = m.Apply(held, flow.Event{Type: "node_completed", Node: "missing"})
	if err == nil {
		t.Fatal("want error")
	}
	after, err := json.Marshal(held)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("input snapshot changed after error")
	}
}

func TestBarrierErrorLeavesInputSnapshotUnchanged(t *testing.T) {
	def := &flow.IR{
		Name:    "parallel-merge",
		Version: 1,
		State: flow.StateSpec{
			Schema:   json.RawMessage(`{"type":"object","properties":{"doc":{"type":"object"}}}`),
			Reducers: map[string]string{"doc": "merge"},
		},
		Nodes: map[string]flow.Node{
			"fan": {Kind: "parallel"},
			"a":   {Kind: "task", Out: "doc"},
			"b":   {Kind: "task", Out: "doc"},
		},
		Edges: []flow.Edge{
			{From: ir.Start, To: "fan"},
			{From: "fan", To: "a"},
			{From: "fan", To: "b"},
		},
	}
	m := requireMachine(t, def)
	held, _, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	held, _, err = m.Apply(held, flow.Event{Type: "node_completed", Node: "a", Payload: json.RawMessage(`{"x":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(held)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = m.Apply(held, flow.Event{Type: "node_completed", Node: "b", Payload: json.RawMessage(`"bad"`)})
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("error %v, want invalid_argument", err)
	}
	after, err := json.Marshal(held)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("input snapshot changed after barrier error")
	}
	_, _, err = m.Apply(held, flow.Event{Type: "node_completed", Node: "b", Payload: json.RawMessage(`{"y":2}`)})
	if err != nil {
		t.Fatalf("retry after error: %v", err)
	}
}

func TestSumOverflowRejectsPositiveAndNegative(t *testing.T) {
	m := reducerMachine(t, map[string]string{"n": "sum"})
	for _, tc := range []struct {
		name  string
		start string
		add   string
	}{
		{"max plus one", "9223372036854775807", "1"},
		{"min minus one", "-9223372036854775808", "-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := map[string]any{"n": json.Number(tc.start)}
			err := m.reduceKey(state, "n", json.Number(tc.add))
			if core.CodeOf(err) != core.CodeInvalidArgument {
				t.Fatalf("error %v, want invalid_argument", err)
			}
			if !strings.Contains(err.Error(), "overflow") {
				t.Fatalf("error %q, want overflow in message", err)
			}
			if !strings.Contains(err.Error(), `"n"`) {
				t.Fatalf("error %q, want key name", err)
			}
		})
	}
}

func TestSumAtMaxInt64StillWorks(t *testing.T) {
	m := reducerMachine(t, map[string]string{"n": "sum"})
	state := map[string]any{"n": json.Number("9223372036854775806")}
	if err := m.reduceKey(state, "n", json.Number("1")); err != nil {
		t.Fatal(err)
	}
	if got := state["n"].(json.Number).String(); got != "9223372036854775807" {
		t.Fatalf("n = %s, want MaxInt64", got)
	}
}

func TestMapFanoutAtLimitWorks(t *testing.T) {
	def := mapFanoutFlow(DefaultMaxFanout)
	m := requireMachine(t, def)
	_, commands, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != DefaultMaxFanout {
		t.Fatalf("commands %d, want %d", len(commands), DefaultMaxFanout)
	}
}

func TestMapFanoutOverLimitFailsRun(t *testing.T) {
	def := mapFanoutFlow(DefaultMaxFanout + 1)
	m := requireMachine(t, def)
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
	if !strings.Contains(string(commands[0].Input), strconv.Itoa(DefaultMaxFanout+1)) {
		t.Fatalf("message %s, want item count", commands[0].Input)
	}
}

func TestWithMaxFanoutLowersLimit(t *testing.T) {
	def := mapFanoutFlow(5)
	m, err := New(def, WithMaxFanout(3))
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
}

func TestWithMaxFanoutRejectsZero(t *testing.T) {
	_, err := New(triageFlow(), WithMaxFanout(0))
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("error %v, want invalid_argument", err)
	}
}

func TestCostGuardFailsOnThirdVisit(t *testing.T) {
	def := &flow.IR{
		Name:    "cost-loop",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes: map[string]flow.Node{
			"work":  {Kind: "task", Guard: &flow.Guard{CostMpts: 100}},
			"check": {Kind: "task"},
		},
		Edges: []flow.Edge{
			{From: ir.Start, To: "work"},
			{From: "work", To: "check"},
			{From: "check", To: "work"},
		},
	}
	m := requireMachine(t, def)
	snapshot, commands, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	workRuns := 0
	for len(commands) != 1 || commands[0].Type != "fail" {
		switch commands[0].Node {
		case "work":
			workRuns++
			snapshot, commands, err = m.Apply(snapshot, flow.Event{
				Type:     "node_completed",
				Node:     "work",
				CostMpts: 40,
			})
		case "check":
			snapshot, commands, err = m.Apply(snapshot, flow.Event{Type: "node_completed", Node: "check"})
		default:
			t.Fatalf("unexpected commands %+v", commands)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if code := commandCode(t, commands[0]); code != string(core.CodeBudgetExceeded) {
		t.Fatalf("code %q, want budget_exceeded", code)
	}
	if workRuns != 3 {
		t.Fatalf("completed work %d times, want 3 before budget_exceeded", workRuns)
	}
}

func TestCostMptsValidationLeavesSnapshotUnchanged(t *testing.T) {
	m := requireMachine(t, triageFlow())
	held, _, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(held)
	for _, ev := range []flow.Event{
		{Type: "node_completed", Node: "verify", CostMpts: -1},
		{Type: "gate_resolved", Node: "verify", CostMpts: 1},
	} {
		_, _, err := m.Apply(held, ev)
		if core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("event %+v: error %v, want invalid_argument", ev, err)
		}
		after, _ := json.Marshal(held)
		if string(before) != string(after) {
			t.Fatalf("snapshot changed after %+v", ev)
		}
	}
	held.Cost = math.MaxInt64
	beforeOverflow, _ := json.Marshal(held)
	_, _, err = m.Apply(held, flow.Event{Type: "node_completed", Node: "verify", CostMpts: 1})
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("overflow error %v, want invalid_argument", err)
	}
	afterOverflow, _ := json.Marshal(held)
	if string(beforeOverflow) != string(afterOverflow) {
		t.Fatal("snapshot changed after overflow")
	}
}

func TestRunCostVisibleInEdgeCondition(t *testing.T) {
	def := &flow.IR{
		Name:    "cost-route",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes: map[string]flow.Node{
			"work": {Kind: "task"},
			"done": {Kind: "task"},
		},
		Edges: []flow.Edge{
			{From: ir.Start, To: "work"},
			{From: "work", To: "done", When: "run.cost >= 50"},
		},
	}
	m := requireMachine(t, def)
	snapshot, commands, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if commands[0].Node != "work" {
		t.Fatal(commands)
	}
	snapshot, commands, err = m.Apply(snapshot, flow.Event{
		Type:     "node_completed",
		Node:     "work",
		CostMpts: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 || commands[0].Node != "done" {
		t.Fatalf("commands %+v, want done when run.cost >= 50", commands)
	}
	if snapshot.Cost != 50 {
		t.Fatalf("cost %d, want 50", snapshot.Cost)
	}
}

func TestCostOrderIndependentInParallel(t *testing.T) {
	def := &flow.IR{
		Name:    "cost-parallel",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes: map[string]flow.Node{
			"fan": {Kind: "parallel"},
			"a":   {Kind: "task"},
			"b":   {Kind: "task"},
		},
		Edges: []flow.Edge{
			{From: ir.Start, To: "fan"},
			{From: "fan", To: "a"},
			{From: "fan", To: "b"},
		},
	}
	m := requireMachine(t, def)
	run := func(order []string, costs []int64) int64 {
		snapshot, commands, err := m.Start(json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		for i, node := range order {
			snapshot, commands, err = m.Apply(snapshot, flow.Event{
				Type:     "node_completed",
				Node:     node,
				CostMpts: costs[i],
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		if len(commands) != 1 || commands[0].Type != "complete" {
			t.Fatalf("commands %+v, want complete", commands)
		}
		return snapshot.Cost
	}
	c1 := run([]string{"a", "b"}, []int64{30, 70})
	c2 := run([]string{"b", "a"}, []int64{70, 30})
	if c1 != 100 || c2 != 100 {
		t.Fatalf("costs %d and %d, want 100", c1, c2)
	}
}

func TestPropertyCostIndependentOfCompletionOrder(t *testing.T) {
	def := fanoutFlow(2)
	def.Nodes["a"] = flow.Node{Kind: "task", Out: "log"}
	def.Nodes["b"] = flow.Node{Kind: "task", Out: "log"}
	m := requireMachine(t, def)
	var first int64
	orders := [][]string{{"a", "b"}, {"b", "a"}}
	costs := map[string]int64{"a": 11, "b": 22}
	for i, order := range orders {
		snapshot, commands, err := m.Start(json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range order {
			payload, _ := json.Marshal(name)
			snapshot, commands, err = m.Apply(snapshot, flow.Event{
				Type:     "node_completed",
				Node:     name,
				Payload:  payload,
				CostMpts: costs[name],
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		if i == 0 {
			first = snapshot.Cost
		} else if snapshot.Cost != first {
			t.Fatalf("order %v cost %d, want %d", order, snapshot.Cost, first)
		}
		_ = commands
	}
}

func mapFanoutFlow(n int) *flow.IR {
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	payload, _ := json.Marshal(map[string]any{"items": items})
	return &flow.IR{
		Name:    "big-map",
		Version: 1,
		State: flow.StateSpec{
			Schema: json.RawMessage(`{"type":"object","properties":{"items":{"type":"array"}}}`),
		},
		Nodes: map[string]flow.Node{
			"fan":  {Kind: "set", Spec: payload},
			"mapn": {Kind: "map", Spec: json.RawMessage(`{"over":"state.items","node":"work"}`)},
			"work": {Kind: "task"},
		},
		Edges: []flow.Edge{
			{From: ir.Start, To: "fan"},
			{From: "fan", To: "mapn"},
			{From: "mapn", To: "work"},
		},
	}
}
