package machine

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/ir"
)

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
