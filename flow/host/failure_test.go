package host_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/host"
	"github.com/epzero/basamento/flow/ir"
	"github.com/epzero/basamento/runtime"
)

func TestLoadedDefinitionHashMismatchFailsBeforeAnyCall(t *testing.T) {
	cat := newCatalog()
	v1 := newFlow("review")
	v1.Nodes["prep"] = node("set", `{"v":1}`, "", "")
	addLinear(v1, "prep")
	ref := cat.register(v1)
	v2 := newFlow("review")
	v2.Nodes["prep"] = node("set", `{"v":2}`, "", "")
	addLinear(v2, "prep")
	cat.byHash[ref.Hash] = v2

	dc := newFakeDC()
	h := host.New(cat)
	_, err := h.Run(dc, ref, json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("run succeeded with mismatched pinned definition, want failure before any durable call")
	}
	if core.CodeOf(err) != core.CodeInternal {
		t.Fatalf("code %v, want internal", core.CodeOf(err))
	}
	if calls := dc.callLog(); len(calls) != 0 {
		t.Fatalf("made calls before hash check: %+v", calls)
	}
}

func TestNegativeWaitDurationFailsRun(t *testing.T) {
	def := newFlow("bad-wait")
	def.Nodes["w"] = node("wait", `{"duration":"-1s"}`, "", "")
	addLinear(def, "w")
	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	_, err := h.Run(dc, ref, json.RawMessage(`{}`))
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code %v (%v), want invalid_argument", core.CodeOf(err), err)
	}
}

func TestNegativeGateTimeoutFailsRun(t *testing.T) {
	def := newFlow("bad-gate")
	def.Nodes["g"] = node("gate", `{"schema":"x.json","accept_if":"true","timeout":"-1s"}`, "", "")
	addLinear(def, "g")
	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	_, err := h.Run(dc, ref, json.RawMessage(`{}`))
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code %v (%v), want invalid_argument", core.CodeOf(err), err)
	}
}

func TestNodeFailurePayloadDoesNotLeakCauseOrDuplicateCode(t *testing.T) {
	marker := "SECRET_VENDOR_MARKER_XYZ"
	typed := &core.Error{
		Code:    core.CodeUnavailable,
		Message: "tool a failed",
		Cause:   errors.New(marker),
	}
	cases := []struct {
		name string
		run  func(cat *catalog, h *host.Host, dc *fakeDC, ref runtime.FlowRef) error
		code core.Code
		msg  string
	}{
		{
			name: "handler",
			code: core.CodeUnavailable,
			msg:  "tool a failed",
			run: func(cat *catalog, h *host.Host, dc *fakeDC, _ runtime.FlowRef) error {
				def := newFlow("handler-fail")
				def.Nodes["plan"] = node("agent", "", "", "")
				addLinear(def, "plan")
				ref := cat.register(def)
				h.RegisterHandler("agent", func(runtime.DurableContext, flow.Node, json.RawMessage) (host.NodeResult, error) {
					return host.NodeResult{}, typed
				})
				_, err := h.Run(dc, ref, json.RawMessage(`{}`))
				return err
			},
		},
		{
			name: "step",
			code: core.CodeUnavailable,
			msg:  "tool a failed",
			run: func(cat *catalog, h *host.Host, dc *fakeDC, _ runtime.FlowRef) error {
				def := newFlow("step-fail")
				def.Nodes["worker"] = node("tool", `{"tool":"w1"}`, "", "")
				addLinear(def, "worker")
				ref := cat.register(def)
				dc.steps["w1"] = scripted{err: typed}
				_, err := h.Run(dc, ref, json.RawMessage(`{}`))
				return err
			},
		},
		{
			name: "gate",
			code: core.CodeTimeout,
			msg:  "gate timed out",
			run: func(cat *catalog, h *host.Host, dc *fakeDC, _ runtime.FlowRef) error {
				def := newFlow("gate-fail")
				def.Nodes["g"] = node("gate", `{"schema":"r.json","accept_if":"true","timeout":"1s"}`, "", "")
				addLinear(def, "g")
				ref := cat.register(def)
				rh := dc.startRun(h, ref, json.RawMessage(`{}`))
				waitFor(t, dc, func(d *fakeDC) bool { return len(d.calls) == 1 })
				dc.advance(time.Second)
				dc.settle()
				<-rh.done
				return rh.err
			},
		},
		{
			name: "child",
			code: core.CodeUnavailable,
			msg:  "tool a failed",
			run: func(cat *catalog, h *host.Host, dc *fakeDC, _ runtime.FlowRef) error {
				def := newFlow("child-fail")
				def.Nodes["verify"] = node("subflow", `{"flow":"checks"}`, "", "")
				addLinear(def, "verify")
				ref := cat.register(def)
				dc.steps["child:checks"] = scripted{err: typed}
				_, err := h.Run(dc, ref, json.RawMessage(`{}`))
				return err
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dc := newFakeDC()
			cat := newCatalog()
			h := host.New(cat)
			err := tc.run(cat, h, dc, runtime.FlowRef{})
			if core.CodeOf(err) != tc.code {
				t.Fatalf("code %v (%v), want %s", core.CodeOf(err), err, tc.code)
			}
			var pe *core.Error
			if !errors.As(err, &pe) || pe.Message != tc.msg {
				t.Fatalf("message %q, want %q (%v)", pe.Message, tc.msg, err)
			}
			if strings.Contains(err.Error(), marker) {
				t.Fatalf("run error leaks marker: %s", err.Error())
			}
		})
	}
}

func TestUnclassifiedErrorsUseInternalMessageWithoutRawText(t *testing.T) {
	secret := "secret vendor text"
	def := newFlow("unclassified")
	def.Nodes["plan"] = node("agent", "", "", "")
	addLinear(def, "plan")
	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	h.RegisterHandler("agent", func(runtime.DurableContext, flow.Node, json.RawMessage) (host.NodeResult, error) {
		return host.NodeResult{}, errors.New(secret)
	})
	_, err := h.Run(dc, ref, json.RawMessage(`{}`))
	if core.CodeOf(err) != core.CodeInternal {
		t.Fatalf("code %v, want internal", core.CodeOf(err))
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("leaked secret in %q", err.Error())
	}
}

func TestSuperstepFailureUsesFirstSelectResult(t *testing.T) {
	build := func() (*fakeDC, *host.Host, runtime.FlowRef) {
		def := newFlow("pair")
		def.Nodes["fan"] = flow.Node{Kind: "parallel"}
		def.Nodes["left"] = node("tool", `{"tool":"left"}`, "", "")
		def.Nodes["right"] = node("tool", `{"tool":"right"}`, "", "")
		def.Edges = []flow.Edge{
			{From: ir.Start, To: "fan"},
			{From: "fan", To: "left"},
			{From: "fan", To: "right"},
		}
		dc := newFakeDC()
		dc.steps["left"] = scripted{manual: true}
		dc.steps["right"] = scripted{manual: true}
		cat := newCatalog()
		ref := cat.register(def)
		h := host.New(cat)
		return dc, h, ref
	}
	runWithOrder := func(t *testing.T, prefer int, wantCode core.Code) {
		t.Helper()
		dc, h, ref := build()
		dc.selectPrefer = prefer
		rh := dc.startRun(h, ref, json.RawMessage(`{}`))
		waitFor(t, dc, func(d *fakeDC) bool { return len(d.manual) == 2 })
		dc.failManualSteps(map[string]error{
			"left":  core.NewError(core.CodeUnavailable, "left failed", nil),
			"right": core.NewError(core.CodeTimeout, "right failed", nil),
		})
		dc.settle()
		<-rh.done
		if core.CodeOf(rh.err) != wantCode {
			t.Fatalf("select prefer %d: code %v (%v), want %s", prefer, core.CodeOf(rh.err), rh.err, wantCode)
		}
	}
	t.Run("prefers_index_zero", func(t *testing.T) {
		runWithOrder(t, 0, core.CodeUnavailable)
	})
	t.Run("prefers_index_one", func(t *testing.T) {
		runWithOrder(t, 1, core.CodeTimeout)
	})
}

func TestHandlerFailureFailsRunAndStopsLaterNodes(t *testing.T) {
	def := newFlow("handled")
	def.Nodes["plan"] = node("agent", `{"agent":"p"}`, "", "")
	def.Nodes["review"] = node("tool", `{"tool":"t1"}`, "", "")
	addLinear(def, "plan", "review")
	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	h.RegisterHandler("agent", func(runtime.DurableContext, flow.Node, json.RawMessage) (host.NodeResult, error) {
		return host.NodeResult{}, core.NewError(core.CodeVendor, "model vendor failed", nil)
	})

	_, err := h.Run(dc, ref, json.RawMessage(`{}`))
	if core.CodeOf(err) != core.CodeVendor {
		t.Fatalf("code %v (%v), want vendor_error", core.CodeOf(err), err)
	}
	if calls := dc.callLog(); len(calls) != 0 {
		t.Fatalf("run made calls after failing handler: %+v", calls)
	}
}

func TestUnclassifiedHandlerFailureIsInternal(t *testing.T) {
	def := newFlow("handled")
	def.Nodes["plan"] = node("agent", "", "", "")
	addLinear(def, "plan")
	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	h.RegisterHandler("agent", func(runtime.DurableContext, flow.Node, json.RawMessage) (host.NodeResult, error) {
		return host.NodeResult{}, errors.New("unexpected panic")
	})
	_, err := h.Run(dc, ref, json.RawMessage(`{}`))
	if core.CodeOf(err) != core.CodeInternal {
		t.Fatalf("code %v (%v), want internal", core.CodeOf(err), err)
	}
}

func TestStepFailureFailsRunWithItsCode(t *testing.T) {
	for _, kind := range []string{"tool", "task"} {
		t.Run(kind, func(t *testing.T) {
			def := newFlow("work-" + kind)
			def.Nodes["worker"] = node(kind, fmt.Sprintf(`{%q:"w1"}`, kind), "", "")
			def.Nodes["after"] = node("tool", `{"tool":"t2"}`, "", "")
			addLinear(def, "worker", "after")
			dc := newFakeDC()
			dc.steps["w1"] = scripted{err: core.NewError(core.CodeUnavailable, "step down", nil)}
			cat := newCatalog()
			ref := cat.register(def)
			h := host.New(cat)

			_, err := h.Run(dc, ref, json.RawMessage(`{}`))
			if core.CodeOf(err) != core.CodeUnavailable {
				t.Fatalf("code %v (%v), want unavailable", core.CodeOf(err), err)
			}
			calls := dc.callLog()
			if len(calls) != 1 || calls[0].name != "w1" {
				t.Fatalf("calls %+v, want exactly one step w1", calls)
			}
			assertJSON(t, calls[0].input, `{}`)
		})
	}
}

func TestChildFailureFailsRunWithItsCode(t *testing.T) {
	def := newFlow("verify")
	def.Nodes["verify"] = node("subflow", `{"flow":"checks"}`, "", "")
	def.Nodes["after"] = node("tool", `{"tool":"t2"}`, "", "")
	addLinear(def, "verify", "after")
	dc := newFakeDC()
	dc.steps["child:checks"] = scripted{err: core.NewError(core.CodeUnavailable, "child down", nil)}
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)

	_, err := h.Run(dc, ref, json.RawMessage(`{}`))
	if core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("code %v (%v), want unavailable", core.CodeOf(err), err)
	}
	calls := dc.callLog()
	if len(calls) != 1 || calls[0].kind != "child" {
		t.Fatalf("calls %+v, want only the child run", calls)
	}
}
