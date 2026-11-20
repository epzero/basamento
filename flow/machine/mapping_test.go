package machine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/ir"
)

func TestJQMapOverAcceptedAndRuns(t *testing.T) {
	def := &flow.IR{
		Name:    "jq-map",
		Version: 1,
		State: flow.StateSpec{
			Schema: json.RawMessage(`{"type":"object","properties":{"items":{"type":"array"},"out":{"type":"array"}}}`),
			Reducers: map[string]string{
				"items": "replace",
				"out":   "append",
			},
		},
		Nodes: map[string]flow.Node{
			"prep": {Kind: "set", Spec: json.RawMessage(`{"items":["a","b"]}`), Out: "items"},
			"mapn": {Kind: "map", Spec: json.RawMessage(`{"over":".items","node":"work"}`)},
			"work": {Kind: "task", Out: "out"},
		},
		Edges: []flow.Edge{{From: ir.Start, To: "prep"}, {From: "prep", To: "mapn"}, {From: "mapn", To: "work"}},
	}
	m, err := New(def)
	if err != nil {
		t.Fatal(err)
	}
	_, commands, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 2 {
		t.Fatalf("commands %+v, want two run_node", commands)
	}
}

func TestJQNodeInputAcceptedAtNew(t *testing.T) {
	def := &flow.IR{
		Name:    "jq-in",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes: map[string]flow.Node{
			"prep": {Kind: "set", Spec: json.RawMessage(`{"x":1}`), Out: "x"},
			"work": {Kind: "task", In: ".x"},
		},
		Edges: []flow.Edge{{From: ir.Start, To: "prep"}, {From: "prep", To: "work"}},
	}
	if _, err := New(def); err != nil {
		t.Fatalf("New: %v", err)
	}
}

func TestMappingNeitherCELNorJQMessageMatchesCompiler(t *testing.T) {
	def := &flow.IR{
		Name:    "bad-in",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object","properties":{"failed":{"type":"integer"}}}`)},
		Nodes: map[string]flow.Node{
			"work": {Kind: "task", In: "state.failed =="},
		},
		Edges: []flow.Edge{{From: ir.Start, To: "work"}},
	}
	_, err := New(def)
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("error %v, want invalid_argument", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "not CEL") || !strings.Contains(msg, "not jq") {
		t.Fatalf("message %q does not keep both parsers' errors", msg)
	}
}
