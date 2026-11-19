package machine

import (
	"encoding/json"
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
