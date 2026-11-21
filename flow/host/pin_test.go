package host_test

import (
	"encoding/json"
	"testing"

	"github.com/epzero/basamento/flow/host"
	"github.com/epzero/basamento/runtime"
)

func TestSubflowChildRunIsPinnedOnlyWhenTheSpecCarriesAHash(t *testing.T) {
	child := newFlow("checks")
	child.Nodes["done"] = node("set", `{"passed":4}`, "", "")
	addLinear(child, "done")
	cat := newCatalog()
	cat.register(child)

	tests := []struct {
		spec string
		want runtime.FlowRef
	}{
		{spec: `{"flow":"checks","hash":"pin-v1"}`, want: runtime.FlowRef{Name: "checks", Hash: "pin-v1"}},
		{spec: `{"flow":"checks"}`, want: runtime.FlowRef{Name: "checks", Hash: ""}},
	}
	for _, tc := range tests {
		t.Run(tc.spec, func(t *testing.T) {
			cat.byHash["pin-v1"] = child
			def := newFlow("verify")
			def.Nodes["verify"] = node("subflow", tc.spec, "", "out")
			addLinear(def, "verify")
			ref := cat.register(def)
			if ref.Hash == "" {
				t.Fatal("parent run must be pinned to a non-empty hash")
			}
			dc := newFakeDC()
			dc.steps["child:checks"] = scripted{result: json.RawMessage(`{"passed":4}`)}
			h := host.New(cat)
			if _, err := h.Run(dc, ref, json.RawMessage(`{}`)); err != nil {
				t.Fatalf("run: %v", err)
			}
			calls := dc.callLog()
			if len(calls) != 1 || calls[0].kind != "child" {
				t.Fatalf("calls %+v, want one child run", calls)
			}
			if calls[0].child != tc.want {
				t.Fatalf("child FlowRef %+v, want %+v", calls[0].child, tc.want)
			}
		})
	}
}

func TestPinnedDefinitionSurvivesANewVersion(t *testing.T) {
	cat := newCatalog()
	v1 := newFlow("review")
	v1.Version = 1
	v1.Nodes["prep"] = node("set", `{"v":1}`, "", "")
	addLinear(v1, "prep")
	ref := cat.register(v1)

	v2 := newFlow("review")
	v2.Version = 2
	v2.Nodes["prep"] = node("set", `{"v":2}`, "", "")
	addLinear(v2, "prep")
	cat.register(v2)

	dc := newFakeDC()
	h := host.New(cat)
	out, err := h.Run(dc, ref, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	assertJSON(t, out, `{"v":1}`)
}

// TestCompletionOrderDoesNotChangeTheResult drives a parallel fan-out with
// random resolution orders. The host starts the calls in command order and the
// machine merges their updates canonically, so the final state is the same.
