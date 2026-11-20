package host

import (
	"encoding/json"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/runtime"
)

// REQ-FLOW-049: failed-node settled cost is wired in runHandlers and consumed by the machine;
// it is not observable through Host.Run, so this test pins the call site.
func TestRunHandlersFailurePassesSettledCostToEvent(t *testing.T) {
	def := &flow.IR{
		Name:    "repair",
		Version: 1,
		Nodes: map[string]flow.Node{
			"repair": {Kind: "agent"},
		},
	}
	h := &Host{
		handlers: map[string]NodeHandler{
			"agent": func(_ runtime.DurableContext, _ flow.Node, _ json.RawMessage) (NodeResult, error) {
				return NodeResult{
					Output:   json.RawMessage(`{"fixed":false}`),
					CostMpts: 2500,
				}, core.NewError(core.CodeVendor, "model failed", nil)
			},
		},
	}
	var dc runtime.DurableContext
	events, err := h.runHandlers(dc, def, []flow.Command{{Type: "run_node", Node: "repair", Attempt: 2}})
	if err != nil {
		t.Fatalf("runHandlers: %v", err)
	}
	if len(events) != 1 || events[0].Type != "node_failed" {
		t.Fatalf("events %+v, want one node_failed", events)
	}
	if events[0].CostMpts != 2500 {
		t.Fatalf("node_failed CostMpts %d, want 2500", events[0].CostMpts)
	}
	if events[0].Attempt != 2 {
		t.Fatalf("node_failed Attempt %d, want 2", events[0].Attempt)
	}
}
