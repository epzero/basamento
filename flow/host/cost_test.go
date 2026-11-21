package host_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/host"
	"github.com/epzero/basamento/flow/ir"
	"github.com/epzero/basamento/runtime"
)

func TestWithMaxFanoutLimitsMapAndRejectsZero(t *testing.T) {
	makeFlow := func(n int) *flow.IR {
		items := make([]int, n)
		for i := range items {
			items[i] = i
		}
		payload, _ := json.Marshal(map[string]any{"items": items})
		def := newFlow("big-map")
		def.State.Schema = json.RawMessage(`{"type":"object","properties":{"items":{"type":"array"}}}`)
		def.Nodes["fan"] = flow.Node{Kind: "set", Spec: payload}
		def.Nodes["mapn"] = flow.Node{Kind: "map", Spec: json.RawMessage(`{"over":"state.items","node":"work"}`)}
		def.Nodes["work"] = node("tool", `{"tool":"w1"}`, "", "")
		def.Edges = []flow.Edge{
			{From: ir.Start, To: "fan"},
			{From: "fan", To: "mapn"},
			{From: "mapn", To: "work"},
		}
		return def
	}
	cat := newCatalog()
	ref3 := cat.register(makeFlow(3))
	ref4 := cat.register(makeFlow(4))
	dc := newFakeDC()
	dc.steps["w1"] = scripted{result: json.RawMessage(`"x"`)}
	h := host.New(cat, host.WithMaxFanout(3))
	if _, err := h.Run(dc, ref3, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("map of 3: %v", err)
	}
	if len(dc.callLog()) != 3 {
		t.Fatalf("map of 3: %d durable calls, want 3", len(dc.callLog()))
	}
	dc2 := newFakeDC()
	_, err := h.Run(dc2, ref4, json.RawMessage(`{}`))
	if core.CodeOf(err) != core.CodeBudgetExceeded {
		t.Fatalf("map of 4: %v, want budget_exceeded", err)
	}
	if len(dc2.callLog()) != 0 {
		t.Fatalf("map of 4 made durable calls: %+v", dc2.callLog())
	}
	h0 := host.New(cat, host.WithMaxFanout(0))
	if _, err := h0.Run(newFakeDC(), ref3, json.RawMessage(`{}`)); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("max fanout 0: %v, want invalid_argument", err)
	}
}

// Failed-handler settled cost is not observable through Host.Run; see TestRunHandlersFailurePassesSettledCostToEvent (REQ-FLOW-049).
func TestNodeCostAccruesOnCompletedHandlers(t *testing.T) {
	t.Run("success path reaches guard after accrual", func(t *testing.T) {
		def := newFlow("cost-sum")
		def.Nodes["a"] = node("agent", "", "", "")
		def.Nodes["b"] = node("agent", "", "", "")
		probe := node("tool", `{"tool":"w1"}`, "", "")
		probe.Guard = &flow.Guard{CostMpts: 65}
		def.Nodes["probe"] = probe
		addLinear(def, "a", "b", "probe")
		dc := newFakeDC()
		cat := newCatalog()
		ref := cat.register(def)
		h := host.New(cat)
		runs := map[string]int{}
		h.RegisterHandler("agent", func(_ runtime.DurableContext, n flow.Node, _ json.RawMessage) (host.NodeResult, error) {
			if err := checkHandlerRunLimit(n.Name, runs); err != nil {
				return host.NodeResult{}, err
			}
			switch n.Name {
			case "a":
				return host.NodeResult{Output: json.RawMessage(`{}`), CostMpts: 40}, nil
			case "b":
				return host.NodeResult{Output: json.RawMessage(`{}`), CostMpts: 25}, nil
			}
			return host.NodeResult{}, nil
		})
		_, err := h.Run(dc, ref, json.RawMessage(`{}`))
		if core.CodeOf(err) != core.CodeBudgetExceeded {
			t.Fatalf("want budget_exceeded at guard 65 after 65 mpts, got %v", err)
		}
		if len(dc.callLog()) != 0 {
			t.Fatalf("guard refusal made durable calls: %+v", dc.callLog())
		}
	})
	t.Run("failing handler ends the run with its error", func(t *testing.T) {
		def := newFlow("cost-fail-repair")
		def.State.Schema = json.RawMessage(`{"type":"object","properties":{"failed":{"type":"integer"}}}`)
		def.Nodes["verify"] = node("set", `{"verified":true}`, "", "")
		def.Nodes["repair"] = flow.Node{
			Kind:  "agent",
			Spec:  json.RawMessage(`{"agent":"repair"}`),
			Out:   "failed",
			Guard: &flow.Guard{CostMpts: 5000},
		}
		def.Edges = []flow.Edge{
			{From: ir.Start, To: "verify"},
			{From: "verify", To: "repair"},
			{From: "repair", To: "verify"},
		}
		dc := newFakeDC()
		cat := newCatalog()
		ref := cat.register(def)
		h := host.New(cat)
		runs := map[string]int{}
		var repairCalls int
		h.RegisterHandler("agent", func(_ runtime.DurableContext, n flow.Node, _ json.RawMessage) (host.NodeResult, error) {
			if n.Name != "repair" {
				return host.NodeResult{}, nil
			}
			if err := checkHandlerRunLimit(n.Name, runs); err != nil {
				return host.NodeResult{}, err
			}
			repairCalls++
			if repairCalls == 1 {
				return host.NodeResult{Output: json.RawMessage(`{"fixed":true}`), CostMpts: 2500}, nil
			}
			return host.NodeResult{
				Output:   json.RawMessage(`{"fixed":false}`),
				CostMpts: 2500,
			}, core.NewError(core.CodeVendor, "model failed", nil)
		})
		_, err := h.Run(dc, ref, json.RawMessage(`{"failed":1}`))
		if core.CodeOf(err) != core.CodeVendor {
			t.Fatalf("want vendor error from repair, got %v", err)
		}
		if repairCalls != 2 {
			t.Fatalf("repair ran %d times, want 2", repairCalls)
		}
	})
	t.Run("negative settled cost", func(t *testing.T) {
		def := newFlow("neg-cost")
		def.Nodes["a"] = node("agent", "", "", "")
		addLinear(def, "a")
		dc := newFakeDC()
		cat := newCatalog()
		ref := cat.register(def)
		h := host.New(cat)
		h.RegisterHandler("agent", func(runtime.DurableContext, flow.Node, json.RawMessage) (host.NodeResult, error) {
			return host.NodeResult{Output: json.RawMessage(`{}`), CostMpts: -1}, nil
		})
		_, err := h.Run(dc, ref, json.RawMessage(`{}`))
		if core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("want invalid_argument, got %v", err)
		}
	})
}

func TestRepairCostGuardFailsAfterTwoHandlerVisits(t *testing.T) {
	def := newFlow("repair-loop")
	def.State.Schema = json.RawMessage(`{"type":"object","properties":{"failed":{"type":"integer"}}}`)
	def.Nodes["verify"] = node("set", `{"verified":true}`, "", "")
	def.Nodes["repair"] = flow.Node{
		Kind:  "agent",
		Spec:  json.RawMessage(`{"agent":"repair"}`),
		Out:   "failed",
		Guard: &flow.Guard{CostMpts: 5000},
	}
	def.Edges = []flow.Edge{
		{From: ir.Start, To: "verify"},
		{From: "verify", To: "repair"},
		{From: "repair", To: "verify"},
	}
	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	var repairRuns int
	runs := map[string]int{}
	h.RegisterHandler("agent", func(_ runtime.DurableContext, n flow.Node, _ json.RawMessage) (host.NodeResult, error) {
		if n.Name != "repair" {
			return host.NodeResult{}, nil
		}
		if err := checkHandlerRunLimit(n.Name, runs); err != nil {
			return host.NodeResult{}, err
		}
		repairRuns++
		return host.NodeResult{Output: json.RawMessage(`{"fixed":true}`), CostMpts: 2500}, nil
	})
	_, err := h.Run(dc, ref, json.RawMessage(`{"failed":1}`))
	if repairRuns != 2 {
		t.Fatalf("repair ran %d times, want 2: %v", repairRuns, err)
	}
	if core.CodeOf(err) != core.CodeBudgetExceeded {
		t.Fatalf("code %v (%v), want budget_exceeded", core.CodeOf(err), err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "repair") || !strings.Contains(msg, "5000") || !strings.Contains(msg, "milli-points") {
		t.Fatalf("error %q, want repair, 5000 and milli-points", msg)
	}
	if len(dc.callLog()) != 0 {
		t.Fatalf("refused visit made durable calls: %+v", dc.callLog())
	}
}

func TestWithMaxFanoutClampsAboveDefault(t *testing.T) {
	items := make([]int, 10001)
	for i := range items {
		items[i] = i
	}
	payload, _ := json.Marshal(map[string]any{"items": items})
	def := newFlow("clamp-map")
	def.State.Schema = json.RawMessage(`{"type":"object","properties":{"items":{"type":"array"}}}`)
	def.Nodes["fill"] = node("set", string(payload), "", "")
	def.Nodes["impl"] = flow.Node{Kind: "map", Spec: json.RawMessage(`{"over":"state.items","node":"work"}`)}
	def.Nodes["work"] = node("tool", `{"tool":"w1"}`, "", "")
	def.Edges = []flow.Edge{
		{From: ir.Start, To: "fill"},
		{From: "fill", To: "impl"},
		{From: "impl", To: "work"},
	}
	cat := newCatalog()
	ref := cat.register(def)
	dc := newFakeDC()
	h := host.New(cat, host.WithMaxFanout(20000))
	_, err := h.Run(dc, ref, json.RawMessage(`{}`))
	if core.CodeOf(err) != core.CodeBudgetExceeded {
		t.Fatalf("map of 10001: %v, want budget_exceeded", err)
	}
	if !strings.Contains(err.Error(), "impl") || !strings.Contains(err.Error(), "10001") || !strings.Contains(err.Error(), "10000") {
		t.Fatalf("error %q, want node impl, count 10001 and limit 10000", err.Error())
	}
	if len(dc.callLog()) != 0 {
		t.Fatalf("budget failure made durable calls: %+v", dc.callLog())
	}
}

func TestSubflowStepCostIsZeroInRunCost(t *testing.T) {
	child := newFlow("child")
	child.Nodes["done"] = node("set", `{"v":1}`, "", "")
	addLinear(child, "done")
	cat := newCatalog()
	cat.register(child)
	def := newFlow("parent")
	def.State.Schema = json.RawMessage(`{"type":"object","properties":{"childOut":{"type":"object"}}}`)
	def.Nodes["sub"] = node("subflow", `{"flow":"child"}`, "", "childOut")
	def.Nodes["probe"] = node("agent", "", "run.cost", "")
	def.Edges = []flow.Edge{{From: ir.Start, To: "sub"}, {From: "sub", To: "probe"}}
	ref := cat.register(def)
	dc := newFakeDC()
	dc.steps["child:child"] = scripted{result: json.RawMessage(`{"v":1}`)}
	h := host.New(cat)
	var gotCost int64
	h.RegisterHandler("agent", func(_ runtime.DurableContext, node flow.Node, input json.RawMessage) (host.NodeResult, error) {
		if node.Name != "probe" {
			return host.NodeResult{}, fmt.Errorf("unexpected %q", node.Name)
		}
		_ = json.Unmarshal(input, &gotCost)
		return host.NodeResult{}, nil
	})
	if _, err := h.Run(dc, ref, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotCost != 0 {
		t.Fatalf("run.cost at probe %d, want 0 after subflow", gotCost)
	}
}
