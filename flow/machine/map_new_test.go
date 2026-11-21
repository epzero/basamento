package machine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/ir"
)

func mapWithChild(child string, kind string) *flow.IR {
	nodes := map[string]flow.Node{
		"prep": {Kind: "set", Spec: json.RawMessage(`{"items":[1,2]}`)},
		"mapn": {Kind: "map", Spec: mustJSON(map[string]string{"over": "state.items", "node": child})},
	}
	if kind != "ghost" {
		nodes[child] = flow.Node{Kind: kind, Spec: machineChildSpec(kind)}
		if kind == "map" {
			nodes["x"] = flow.Node{Kind: "task"}
		}
	}
	edges := []flow.Edge{{From: ir.Start, To: "prep"}, {From: "prep", To: "mapn"}}
	if kind != "ghost" {
		edges = append(edges, flow.Edge{From: "mapn", To: child})
		if kind == "map" {
			edges = append(edges, flow.Edge{From: child, To: "x"})
		}
	}
	return &flow.IR{
		Name:    "map-child",
		Version: 1,
		State: flow.StateSpec{
			Schema: json.RawMessage(`{"type":"object","properties":{"items":{"type":"array"}}}`),
		},
		Nodes: nodes,
		Edges: edges,
	}
}

func mapTwoOwners() *flow.IR {
	return &flow.IR{
		Name:    "two-maps",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes: map[string]flow.Node{
			"prep": {Kind: "set", Spec: json.RawMessage(`{"items":[1]}`)},
			"mapA": {Kind: "map", Spec: mustJSON(map[string]string{"over": "state.items", "node": "work"})},
			"mapB": {Kind: "map", Spec: mustJSON(map[string]string{"over": "state.items", "node": "work"})},
			"work": {Kind: "task"},
		},
		Edges: []flow.Edge{
			{From: ir.Start, To: "prep"},
			{From: "prep", To: "mapA"},
			{From: "prep", To: "mapB"},
			{From: "mapA", To: "work"},
			{From: "mapB", To: "work"},
		},
	}
}

func mapDistinctChildren() *flow.IR {
	return &flow.IR{
		Name:    "distinct-map-children",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes: map[string]flow.Node{
			"prep":  {Kind: "set", Spec: json.RawMessage(`{"a":[1],"b":[2]}`), Out: "out"},
			"mapA":  {Kind: "map", Spec: mustJSON(map[string]string{"over": "state.a", "node": "workA"})},
			"mapB":  {Kind: "map", Spec: mustJSON(map[string]string{"over": "state.b", "node": "workB"})},
			"workA": {Kind: "task"},
			"workB": {Kind: "task"},
		},
		Edges: []flow.Edge{
			{From: ir.Start, To: "prep"},
			{From: "prep", To: "mapA"},
			{From: "prep", To: "mapB"},
			{From: "mapA", To: "workA"},
			{From: "mapB", To: "workB"},
		},
	}
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

func TestMapChildGhostRejectedAtNew(t *testing.T) {
	def := mapWithChild("ghost", "ghost")
	_, err := New(def)
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("error %v, want invalid_argument", err)
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("error %q, want child named", err)
	}
}

func TestMapChildMachineKindRejectedAtNew(t *testing.T) {
	for _, kind := range []string{"set", "map", "parallel"} {
		t.Run(kind, func(t *testing.T) {
			_, err := New(mapWithChild("child", kind))
			if core.CodeOf(err) != core.CodeInvalidArgument {
				t.Fatalf("error %v, want invalid_argument", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "mapn") || !strings.Contains(msg, "child") || !strings.Contains(msg, kind) {
				t.Fatalf("error %q, want map, child and kind", msg)
			}
		})
	}
}

func TestMapChildHostKindAcceptedAtNew(t *testing.T) {
	for _, kind := range []string{"tool", "task", "model", "agent", "subflow", "gate", "wait"} {
		t.Run(kind, func(t *testing.T) {
			def := mapWithChild("child", kind)
			def.Nodes["child"] = flow.Node{Kind: kind, Spec: hostKindSpec(kind)}
			if _, err := New(def); err != nil {
				t.Fatalf("New: %v", err)
			}
		})
	}
}

func machineChildSpec(kind string) json.RawMessage {
	switch kind {
	case "set":
		return json.RawMessage(`{}`)
	case "map":
		return mustJSON(map[string]string{"over": "state.items", "node": "x"})
	case "parallel":
		return json.RawMessage(`{}`)
	default:
		return hostKindSpec(kind)
	}
}

func hostKindSpec(kind string) json.RawMessage {
	switch kind {
	case "gate":
		return json.RawMessage(`{"accept_if":"true","timeout":"1h"}`)
	case "wait":
		return json.RawMessage(`{"duration":"1s"}`)
	default:
		return json.RawMessage(`{}`)
	}
}

// A whitespace-only "over" is an expression, not a blank spec: ir.Validate
// and parseMapSpec both accept its shape, and New rejects it at the expression
// stage, the same verdict flow/compile reaches (EP0-166, F2).
func TestWhitespaceMapOverRejectedByExpressionNotShape(t *testing.T) {
	def := mapWithChild("child", "task")
	node := def.Nodes["mapn"]
	node.Spec = json.RawMessage(`{"over":"   ","node":"child"}`)
	def.Nodes["mapn"] = node
	if err := ir.Validate(def); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	over, child, err := parseMapSpec(node)
	if err != nil || over != "   " || child != "child" {
		t.Fatalf("parseMapSpec = %q, %q, %v; want the whitespace expression accepted", over, child, err)
	}
	_, err = New(def)
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("error %v, want invalid_argument", err)
	}
	if !strings.Contains(err.Error(), "map over") || strings.Contains(err.Error(), "needs over and node") {
		t.Fatalf("error %q, want the expression stage", err)
	}
}

func TestMapChildSharedByTwoMapsRejectedAtNew(t *testing.T) {
	_, err := New(mapTwoOwners())
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("error %v, want invalid_argument", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "mapA") || !strings.Contains(msg, "mapB") || !strings.Contains(msg, "work") {
		t.Fatalf("error %q, want both maps and child", msg)
	}
}

func TestTwoMapsDistinctChildrenAcceptedAtNew(t *testing.T) {
	if _, err := New(mapDistinctChildren()); err != nil {
		t.Fatalf("New: %v", err)
	}
}
