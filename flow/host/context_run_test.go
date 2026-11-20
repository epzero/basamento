package host_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/host"
	"github.com/epzero/basamento/flow/ir"
	"github.com/epzero/basamento/runtime"
)

type deadlineErrContext struct {
	context.Context
}

func (deadlineErrContext) Err() error { return context.DeadlineExceeded }

func (deadlineErrContext) Done() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func assertRunCancelled(t *testing.T, err error) {
	t.Helper()
	if core.CodeOf(err) != core.CodeCancelled {
		t.Fatalf("code %v (%v), want cancelled", core.CodeOf(err), err)
	}
	var pe *core.Error
	if !errors.As(err, &pe) || pe.Message != "flow: cancelled" {
		t.Fatalf("error %v, want cancelled message %q", err, "flow: cancelled")
	}
}

func assertRunTimeout(t *testing.T, err error) {
	t.Helper()
	if core.CodeOf(err) != core.CodeTimeout {
		t.Fatalf("code %v (%v), want timeout", core.CodeOf(err), err)
	}
	var pe *core.Error
	if !errors.As(err, &pe) || pe.Message != "flow: timeout" {
		t.Fatalf("error %v, want timeout message %q", err, "flow: timeout")
	}
}

func TestRunStopsWhenContextIsDone(t *testing.T) {
	t.Run("cancelled_before_first_superstep", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		dc := newFakeDC()
		dc.bindContext(ctx)
		def := newFlow("early-cancel")
		def.Nodes["worker"] = node("agent", "", "", "")
		addLinear(def, "worker")
		cat := newCatalog()
		ref := cat.register(def)
		h := host.New(cat)
		var handlerCalls int
		h.RegisterHandler("agent", func(runtime.DurableContext, flow.Node, json.RawMessage) (host.NodeResult, error) {
			handlerCalls++
			return host.NodeResult{Output: json.RawMessage(`{}`)}, nil
		})
		_, err := h.Run(dc, ref, json.RawMessage(`{}`))
		assertRunCancelled(t, err)
		if handlerCalls != 0 {
			t.Fatalf("handler calls %d, want 0", handlerCalls)
		}
		if calls := dc.callLog(); len(calls) != 0 {
			t.Fatalf("durable calls %d, want 0", len(calls))
		}
	})

	t.Run("cancel_in_first_handler_of_parallel_pair", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		dc := newFakeDC()
		dc.bindContext(ctx)
		def := newFlow("handler-cancel")
		def.Nodes["fan"] = flow.Node{Kind: "parallel"}
		def.Nodes["a"] = node("model", "", "", "")
		def.Nodes["b"] = node("agent", "", "", "")
		def.Edges = []flow.Edge{
			{From: ir.Start, To: "fan"},
			{From: "fan", To: "a"},
			{From: "fan", To: "b"},
		}
		cat := newCatalog()
		ref := cat.register(def)
		h := host.New(cat)
		var bCalls int
		h.RegisterHandler("model", func(runtime.DurableContext, flow.Node, json.RawMessage) (host.NodeResult, error) {
			cancel()
			return host.NodeResult{Output: json.RawMessage(`{}`)}, nil
		})
		h.RegisterHandler("agent", func(_ runtime.DurableContext, n flow.Node, _ json.RawMessage) (host.NodeResult, error) {
			if n.Name == "b" {
				bCalls++
			}
			return host.NodeResult{Output: json.RawMessage(`{}`)}, nil
		})
		_, err := h.Run(dc, ref, json.RawMessage(`{}`))
		assertRunCancelled(t, err)
		if bCalls != 0 {
			t.Fatalf("second handler calls %d, want 0", bCalls)
		}
	})

	t.Run("cancel_between_supersteps", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		dc := newFakeDC()
		dc.bindContext(ctx)
		def := newFlow("two-step")
		def.Nodes["first"] = node("agent", "", "", "")
		def.Nodes["second"] = node("tool", `{"tool":"t1"}`, "", "")
		addLinear(def, "first", "second")
		dc.steps["t1"] = scripted{result: json.RawMessage(`{}`)}
		cat := newCatalog()
		ref := cat.register(def)
		h := host.New(cat)
		h.RegisterHandler("agent", func(runtime.DurableContext, flow.Node, json.RawMessage) (host.NodeResult, error) {
			cancel()
			return host.NodeResult{Output: json.RawMessage(`{}`)}, nil
		})
		_, err := h.Run(dc, ref, json.RawMessage(`{}`))
		assertRunCancelled(t, err)
		if calls := dc.callLog(); len(calls) != 0 {
			t.Fatalf("second superstep durable calls %+v, want none", calls)
		}
	})

	t.Run("cancel_during_await_before_second_select", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		dc := newFakeDC()
		dc.bindContext(ctx)
		def := newFlow("await-cancel")
		def.Nodes["fan"] = flow.Node{Kind: "parallel"}
		def.Nodes["a"] = node("tool", `{"tool":"a"}`, "", "")
		def.Nodes["b"] = node("tool", `{"tool":"b"}`, "", "")
		def.Edges = []flow.Edge{
			{From: ir.Start, To: "fan"},
			{From: "fan", To: "a"},
			{From: "fan", To: "b"},
		}
		dc.steps["a"] = scripted{manual: true}
		dc.steps["b"] = scripted{manual: true}
		dc.onStepGet = map[string]func(){
			"a": cancel,
		}
		cat := newCatalog()
		ref := cat.register(def)
		h := host.New(cat)
		rh := dc.startRun(h, ref, json.RawMessage(`{}`))
		waitFor(t, dc, func(d *fakeDC) bool { return len(d.manual) == 2 })
		dc.resolveStep("a", json.RawMessage(`"a"`), nil)
		dc.resolveStep("b", json.RawMessage(`"b"`), nil)
		<-rh.done
		assertRunCancelled(t, rh.err)
		dc.mu.Lock()
		selects := dc.selectCalls
		dc.mu.Unlock()
		if selects > 1 {
			t.Fatalf("Select calls %d, want at most 1 after cancel in Get", selects)
		}
	})

	t.Run("deadline_exceeded_maps_to_timeout", func(t *testing.T) {
		dc := newFakeDC()
		dc.bindContext(deadlineErrContext{Context: context.Background()})
		def := newFlow("timeout")
		def.Nodes["worker"] = node("agent", "", "", "")
		addLinear(def, "worker")
		cat := newCatalog()
		ref := cat.register(def)
		h := host.New(cat)
		h.RegisterHandler("agent", func(runtime.DurableContext, flow.Node, json.RawMessage) (host.NodeResult, error) {
			return host.NodeResult{Output: json.RawMessage(`{}`)}, nil
		})
		_, err := h.Run(dc, ref, json.RawMessage(`{}`))
		assertRunTimeout(t, err)
	})
}

func TestRunContextErrorMapping(t *testing.T) {
	// Guards the cancelled vs timeout mapping used by the host (mutant anchor).
	t.Run("canceled_not_timeout", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		dc := newFakeDC()
		dc.bindContext(ctx)
		def := newFlow("map-cancel")
		def.Nodes["w"] = node("agent", "", "", "")
		addLinear(def, "w")
		cat := newCatalog()
		ref := cat.register(def)
		h := host.New(cat)
		h.RegisterHandler("agent", func(runtime.DurableContext, flow.Node, json.RawMessage) (host.NodeResult, error) {
			return host.NodeResult{}, fmt.Errorf("should not run")
		})
		_, err := h.Run(dc, ref, json.RawMessage(`{}`))
		assertRunCancelled(t, err)
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("cancelled run mapped to deadline: %v", err)
		}
	})
}
