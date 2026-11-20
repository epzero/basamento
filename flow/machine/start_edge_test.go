package machine

import (
	"encoding/json"
	"errors"
	"reflect"
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
// through New; the helper is tested directly to keep its message tied to the
// validator's (design.md §2.6). The expected message comes from ir.Validate
// itself, so the two cannot diverge silently.
func TestCheckStartEdgeMatchesValidatorMessage(t *testing.T) {
	edge := flow.Edge{From: ir.Start, To: "plan", When: "true"}
	def := &flow.IR{
		Name:    "start-when",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes:   map[string]flow.Node{"plan": {Kind: "task"}},
		Edges:   []flow.Edge{edge},
	}
	verr := ir.Validate(def)
	if core.CodeOf(verr) != core.CodeInvalidArgument {
		t.Fatalf("validator error %v, want invalid_argument", verr)
	}
	var want *core.Error
	if !errors.As(verr, &want) {
		t.Fatalf("validator error %v, want a core.Error", verr)
	}
	err := checkStartEdge(edge)
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("error %v, want invalid_argument", err)
	}
	var platform *core.Error
	if !errors.As(err, &platform) {
		t.Fatalf("error %v, want a core.Error", err)
	}
	if platform.Message != want.Message {
		t.Fatalf("message %q, validator says %q", platform.Message, want.Message)
	}
	if !reflect.DeepEqual(platform.Details, want.Details) {
		t.Fatalf("details %v, validator says %v", platform.Details, want.Details)
	}
	if err := checkStartEdge(flow.Edge{From: ir.Start, To: "plan"}); err != nil {
		t.Fatalf("unconditional START edge: %v", err)
	}
	if err := checkStartEdge(flow.Edge{From: "plan", To: "work", When: "true"}); err != nil {
		t.Fatalf("conditioned later edge: %v", err)
	}
}
