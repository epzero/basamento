package machine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/ir"
)

func TestStartEdgeWithConditionRejectedAtNew(t *testing.T) {
	def := &flow.IR{
		Name:    "start-when",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes:   map[string]flow.Node{"work": {Kind: "task"}},
		Edges:   []flow.Edge{{From: ir.Start, To: "work", When: "true"}},
	}
	_, err := New(def)
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("error %v, want invalid_argument", err)
	}
	if !strings.Contains(err.Error(), `edge "START" -> "work" takes no condition`) {
		t.Fatalf("error %q, want START condition message naming the edge", err)
	}
}
