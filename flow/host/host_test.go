package host_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/host"
	"github.com/epzero/basamento/flow/ir"
	"github.com/epzero/basamento/runtime"
)

func TestToolAndTaskRunAsOneStep(t *testing.T) {
	for _, kind := range []string{"tool", "task"} {
		t.Run(kind, func(t *testing.T) {
			def := newFlow("work-" + kind)
			def.Nodes["worker"] = node(kind, fmt.Sprintf(`{%q:"w1"}`, kind), "state.draft", "worker")
			addLinear(def, "worker")
			def.State.Schema = json.RawMessage(`{"type":"object","properties":{"draft":{"type":"object"},"worker":{"type":"object"}}}`)

			dc := newFakeDC()
			dc.steps["w1"] = scripted{result: json.RawMessage(`{"said":"done"}`)}
			cat := newCatalog()
			ref := cat.register(def)
			h := host.New(cat)

			got, err := h.Run(dc, ref, json.RawMessage(`{"draft":{"text":"hello"},"extra":1}`))
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			calls := dc.callLog()
			if len(calls) != 1 || calls[0].kind != "step" || calls[0].name != "w1" {
				t.Fatalf("calls %+v, want one step w1", calls)
			}
			assertJSON(t, calls[0].input, `{"text":"hello"}`)
			assertJSON(t, got, `{"draft":{"text":"hello"},"extra":1,"worker":{"said":"done"}}`)
		})
	}
}

func TestHandlerRunsThroughRegisteredHandler(t *testing.T) {
	def := newFlow("handled")
	def.Nodes["worker"] = node("agent", `{"agent":"w1"}`, "state.draft", "worker")
	addLinear(def, "worker")
	def.State.Schema = json.RawMessage(`{"type":"object","properties":{"draft":{"type":"object"},"worker":{"type":"object"}}}`)

	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	var gotNode flow.Node
	var gotInput json.RawMessage
	h.RegisterHandler("agent", func(_ runtime.DurableContext, n flow.Node, input json.RawMessage) (host.NodeResult, error) {
		gotNode = n
		gotInput = append(json.RawMessage(nil), input...)
		return host.NodeResult{Output: json.RawMessage(`{"said":"done"}`)}, nil
	})

	out, err := h.Run(dc, ref, json.RawMessage(`{"draft":{"text":"hello"},"extra":1}`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotNode.Name != "worker" || gotNode.Kind != "agent" || gotNode.Spec == nil {
		t.Fatalf("handler node %+v", gotNode)
	}
	assertJSON(t, gotInput, `{"text":"hello"}`)
	assertJSON(t, out, `{"draft":{"text":"hello"},"extra":1,"worker":{"said":"done"}}`)
	if calls := dc.callLog(); len(calls) != 0 {
		t.Fatalf("handler made durable calls: %+v", calls)
	}
}

func TestMissingHandlerFailsBeforeAnyNode(t *testing.T) {
	def := newFlow("triage")
	def.Nodes["prep"] = node("tool", `{"tool":"t1"}`, "", "")
	def.Nodes["worker"] = node("model", "", "", "")
	addLinear(def, "prep", "worker")

	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)

	_, err := h.Run(dc, ref, json.RawMessage(`{}`))
	if core.CodeOf(err) != core.CodeUnsupported {
		t.Fatalf("code %v (%v), want unsupported", core.CodeOf(err), err)
	}
	if !bytes.Contains([]byte(err.Error()), []byte(`"worker"`)) {
		t.Fatalf("error %q does not name worker", err)
	}
	if calls := dc.callLog(); len(calls) != 0 {
		t.Fatalf("run made calls before failing: %+v", calls)
	}
}

func TestWaitRunsAsTimer(t *testing.T) {
	def := newFlow("cool")
	def.Nodes["cool_down"] = node("wait", `{"duration":"30s"}`, "", "")
	addLinear(def, "cool_down")
	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)

	rh := dc.startRun(h, ref, json.RawMessage(`{}`))
	waitFor(t, dc, func(d *fakeDC) bool { return len(d.calls) == 1 })
	if calls := dc.callLog(); calls[0].kind != "timer" || calls[0].duration != 30*time.Second {
		t.Fatalf("calls %+v, want one 30s timer", calls)
	}
	dc.advance(29 * time.Second)
	dc.settle()
	if rh.finished() {
		t.Fatalf("run finished after 29s: %v", rh.err)
	}
	dc.advance(1 * time.Second)
	dc.settle()
	<-rh.done
	if rh.err != nil {
		t.Fatalf("run after 30s: %v", rh.err)
	}
}

func TestGateWaitsAndCompletesWithPayload(t *testing.T) {
	def := newFlow("approve")
	def.Nodes["approve"] = node("gate", `{"schema":"review.json","accept_if":"input.approved","timeout":"72h"}`, "", "out")
	addLinear(def, "approve")
	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)

	rh := dc.startRun(h, ref, json.RawMessage(`{}`))
	waitFor(t, dc, func(d *fakeDC) bool { return len(d.calls) == 1 })
	spec := dc.callLog()[0].gate
	if spec.Name != "approve" || spec.AcceptIf != "input.approved" || spec.Timeout != 72*time.Hour {
		t.Fatalf("gate spec %+v", spec)
	}
	assertJSON(t, spec.Schema, `"review.json"`)
	dc.resolveGate("approve", json.RawMessage(`{"approved":true}`))
	dc.settle()
	<-rh.done
	if rh.err != nil {
		t.Fatalf("run: %v", rh.err)
	}
	assertJSON(t, rh.result, `{"out":{"approved":true}}`)
}

func TestGateTimeoutFailsRun(t *testing.T) {
	def := newFlow("approve")
	def.Nodes["approve"] = node("gate", `{"schema":"review.json","accept_if":"input.approved","timeout":"72h"}`, "", "")
	addLinear(def, "approve")
	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)

	rh := dc.startRun(h, ref, json.RawMessage(`{}`))
	waitFor(t, dc, func(d *fakeDC) bool { return len(d.calls) == 1 })
	dc.advance(72 * time.Hour)
	dc.settle()
	<-rh.done
	if core.CodeOf(rh.err) != core.CodeTimeout {
		t.Fatalf("code %v (%v), want timeout", core.CodeOf(rh.err), rh.err)
	}
}

func TestSubflowRunsAsChild(t *testing.T) {
	def := newFlow("verify")
	def.Nodes["verify"] = node("subflow", `{"flow":"checks"}`, "", "out")
	addLinear(def, "verify")
	dc := newFakeDC()
	dc.steps["child:checks"] = scripted{result: json.RawMessage(`{"passed":4}`)}
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)

	out, err := h.Run(dc, ref, json.RawMessage(`{"tests":4}`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	calls := dc.callLog()
	if len(calls) != 1 || calls[0].kind != "child" || calls[0].child.Name != "checks" {
		t.Fatalf("calls %+v, want one child run checks", calls)
	}
	assertJSON(t, calls[0].input, `{"tests":4}`)
	assertJSON(t, out, `{"tests":4,"out":{"passed":4}}`)
}

func TestParallelHandlerFailureStopsLaterHandlers(t *testing.T) {
	def := newFlow("parallel-handlers")
	def.Nodes["fan"] = flow.Node{Kind: "parallel"}
	def.Nodes["a"] = node("agent", `{"agent":"a"}`, "", "")
	def.Nodes["b"] = node("agent", `{"agent":"b"}`, "", "")
	def.Nodes["c"] = node("agent", `{"agent":"c"}`, "", "")
	def.Edges = []flow.Edge{
		{From: ir.Start, To: "fan"},
		{From: "fan", To: "a"},
		{From: "fan", To: "b"},
		{From: "fan", To: "c"},
	}
	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	var bCalls, cCalls int
	h.RegisterHandler("agent", func(_ runtime.DurableContext, n flow.Node, _ json.RawMessage) (host.NodeResult, error) {
		switch n.Name {
		case "a":
			return host.NodeResult{}, core.NewError(core.CodeVendor, "model failed", nil)
		case "b":
			bCalls++
			return host.NodeResult{Output: json.RawMessage(`{}`)}, nil
		case "c":
			cCalls++
			return host.NodeResult{Output: json.RawMessage(`{}`)}, nil
		}
		return host.NodeResult{}, nil
	})
	_, err := h.Run(dc, ref, json.RawMessage(`{}`))
	if core.CodeOf(err) != core.CodeVendor {
		t.Fatalf("code %v (%v), want vendor_error", core.CodeOf(err), err)
	}
	if bCalls != 0 || cCalls != 0 {
		t.Fatalf("after a failed: b=%d c=%d, want 0", bCalls, cCalls)
	}
}

func TestPureSetAndRoutingMakeNoDurableCall(t *testing.T) {
	def := newFlow("pure")
	def.State.Schema = json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"},"route":{"type":"string"}}}`)
	def.Nodes["prep"] = node("set", `{}`, "", "")
	def.Nodes["fast"] = node("set", `{"route":"fast"}`, "", "")
	def.Nodes["slow"] = node("set", `{"route":"slow"}`, "", "")
	def.Edges = []flow.Edge{
		{From: ir.Start, To: "prep"},
		{From: "prep", To: "fast", When: "state.n < 3"},
		{From: "prep", To: "slow"},
	}
	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)

	out, err := h.Run(dc, ref, json.RawMessage(`{"n":5}`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	assertJSON(t, out, `{"n":5,"route":"slow"}`)
	if calls := dc.callLog(); len(calls) != 0 {
		t.Fatalf("pure flow made durable calls: %+v", calls)
	}
}

func TestCompletedRunMakesNoFurtherCall(t *testing.T) {
	def := newFlow("greet")
	def.State.Schema = json.RawMessage(`{"type":"object","properties":{"greeting":{"type":"string"}}}`)
	def.Nodes["prep"] = node("set", `{"greeting":"hi"}`, "", "")
	addLinear(def, "prep")
	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)

	out, err := h.Run(dc, ref, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	assertJSON(t, out, `{"greeting":"hi"}`)
	if calls := dc.callLog(); len(calls) != 0 {
		t.Fatalf("completed run made durable calls: %+v", calls)
	}
}

func TestSuperstepStartsEveryCallBeforeWaiting(t *testing.T) {
	def := newFlow("fanout")
	def.Nodes["fan"] = flow.Node{Kind: "parallel"}
	def.Nodes["a"] = node("wait", `{"duration":"10s"}`, "", "")
	def.Nodes["b"] = node("wait", `{"duration":"30s"}`, "", "")
	def.Edges = []flow.Edge{
		{From: ir.Start, To: "fan"},
		{From: "fan", To: "b"},
		{From: "fan", To: "a"},
	}
	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)

	rh := dc.startRun(h, ref, json.RawMessage(`{}`))
	waitFor(t, dc, func(d *fakeDC) bool { return len(d.calls) == 2 })
	calls := dc.callLog()
	if calls[0].duration != 10*time.Second || calls[1].duration != 30*time.Second {
		t.Fatalf("timers %+v, want 10s then 30s in node-id order", calls)
	}
	dc.advance(10 * time.Second)
	dc.settle()
	if rh.finished() {
		t.Fatalf("run finished with a timer pending: %v", rh.err)
	}
	dc.advance(20 * time.Second)
	dc.settle()
	<-rh.done
	if rh.err != nil {
		t.Fatalf("run: %v", rh.err)
	}
}

func TestCompletionOrderDoesNotChangeTheResult(t *testing.T) {
	def := newFlow("order")
	def.State.Schema = json.RawMessage(`{"type":"object","properties":{"log":{"type":"array"}}}`)
	def.State.Reducers = map[string]string{"log": "append"}
	def.Nodes["fan"] = flow.Node{Kind: "parallel"}
	for _, id := range []string{"a", "b", "c"} {
		def.Nodes[id] = node("tool", fmt.Sprintf(`{"tool":%q}`, id), "", "log")
		def.Edges = append(def.Edges, flow.Edge{From: "fan", To: id})
	}
	def.Edges = append([]flow.Edge{{From: ir.Start, To: "fan"}}, def.Edges...)
	cat := newCatalog()
	ref := cat.register(def)

	var want json.RawMessage
	rng := rand.New(rand.NewPCG(7, 11))
	for round := 0; round < 20; round++ {
		dc := newFakeDC()
		for _, id := range []string{"a", "b", "c"} {
			dc.steps[id] = scripted{manual: true}
		}
		h := host.New(cat)
		rh := dc.startRun(h, ref, json.RawMessage(`{}`))
		waitFor(t, dc, func(d *fakeDC) bool { return len(d.manual) == 3 })
		order := []string{"a", "b", "c"}
		rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		for _, id := range order {
			payload, _ := json.Marshal(id)
			dc.resolveStep(id, payload, nil)
			dc.settle()
		}
		<-rh.done
		if rh.err != nil {
			t.Fatalf("round %d: %v", round, rh.err)
		}
		if want == nil {
			want = rh.result
			continue
		}
		if !jsonEqual(want, rh.result) {
			t.Fatalf("round %d: state %s, want %s", round, rh.result, want)
		}
	}
}
