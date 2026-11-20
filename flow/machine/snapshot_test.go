package machine

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/ir"
)

func TestStartReturnsZeroSnapshotWhenAdvanceFails(t *testing.T) {
	def := &flow.IR{
		Name:    "bad-map",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object","properties":{"items":{}}}`)},
		Nodes: map[string]flow.Node{
			"prep": {Kind: "set", Spec: json.RawMessage(`{"items":"not-an-array"}`)},
			"mapn": {Kind: "map", Spec: json.RawMessage(`{"over":"state.items","node":"work"}`)},
			"work": {Kind: "task"},
		},
		Edges: []flow.Edge{
			{From: ir.Start, To: "prep"},
			{From: "prep", To: "mapn"},
			{From: "mapn", To: "work"},
		},
	}
	m := requireMachine(t, def)
	snapshot, commands, err := m.Start(json.RawMessage(`{}`))
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("error %v, want invalid_argument", err)
	}
	if !reflect.DeepEqual(snapshot, flow.Snapshot{}) {
		t.Fatalf("snapshot %+v, want zero", snapshot)
	}
	if commands != nil {
		t.Fatalf("commands %+v, want nil", commands)
	}
}

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
