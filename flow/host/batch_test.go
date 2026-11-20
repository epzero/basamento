package host

import (
	"encoding/json"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/ir"
	"github.com/epzero/basamento/runtime"
)

func TestStartBatchRejectsUnknownNode(t *testing.T) {
	def := &flow.IR{
		Name:    "batch",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes: map[string]flow.Node{
			"w": {Kind: "wait", Spec: json.RawMessage(`{"duration":"1s"}`)},
		},
		Edges: []flow.Edge{{From: ir.Start, To: "w"}},
	}
	h := &Host{}
	dc := ctxOnlyDC{}
	snapshot := &flow.Snapshot{Seq: 1}
	ref := runtime.FlowRef{Name: def.Name}

	cases := []struct {
		name string
		cmd  flow.Command
	}{
		{name: "start_timer", cmd: flow.Command{Type: "start_timer", Node: "ghost", Attempt: 1}},
		{name: "await_gate", cmd: flow.Command{Type: "await_gate", Node: "ghost", Attempt: 1}},
		{name: "run_node", cmd: flow.Command{Type: "run_node", Node: "ghost", Attempt: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := h.startBatch(dc, def, ref, snapshot, []flow.Command{tc.cmd})
			if core.CodeOf(err) != core.CodeInvalidArgument {
				t.Fatalf("code %v (%v), want invalid_argument", core.CodeOf(err), err)
			}
		})
	}
}
