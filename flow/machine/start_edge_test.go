package machine

import (
	"encoding/json"
	"errors"
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

// New runs ir.Validate first, so its own START-edge check is unreachable
// through New; the helper is tested directly to keep its wording, which must
// match the validator's, covered (design.md §2.6).
func TestCheckStartEdgeMatchesValidatorMessage(t *testing.T) {
	err := checkStartEdge(flow.Edge{From: ir.Start, To: "plan", When: "true"})
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("error %v, want invalid_argument", err)
	}
	var platform *core.Error
	if !errors.As(err, &platform) {
		t.Fatalf("error %v, want a core.Error", err)
	}
	if got, want := platform.Message, `flow: edge "START" -> "plan" takes no condition`; got != want {
		t.Fatalf("message %q, want %q", got, want)
	}
	if err := checkStartEdge(flow.Edge{From: ir.Start, To: "plan"}); err != nil {
		t.Fatalf("unconditional START edge: %v", err)
	}
	if err := checkStartEdge(flow.Edge{From: "plan", To: "work", When: "true"}); err != nil {
		t.Fatalf("conditioned later edge: %v", err)
	}
}
