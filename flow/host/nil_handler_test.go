package host_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/host"
	"github.com/epzero/basamento/runtime"
)

func TestNilHandlerRegistration(t *testing.T) {
	def := newFlow("nil-handler")
	def.Nodes["worker"] = node("agent", "", "", "")
	addLinear(def, "worker")

	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	wantMsg := `flow: node "worker": no node handler is registered for kind "agent"`

	assertUnsupported := func(t *testing.T, err error) {
		t.Helper()
		if core.CodeOf(err) != core.CodeUnsupported {
			t.Fatalf("code %v (%v), want unsupported", core.CodeOf(err), err)
		}
		var pe *core.Error
		if !errors.As(err, &pe) || pe.Message != wantMsg {
			t.Fatalf("error %v, want message %q", err, wantMsg)
		}
	}

	t.Run("RegisterHandler_kind_nil_matches_never_registered", func(t *testing.T) {
		var handlerCalls int
		h := host.New(cat)
		h.RegisterHandler("agent", func(runtime.DurableContext, flow.Node, json.RawMessage) (host.NodeResult, error) {
			handlerCalls++
			return host.NodeResult{Output: json.RawMessage(`{}`)}, nil
		})
		h.RegisterHandler("agent", nil)

		_, err := h.Run(dc, ref, json.RawMessage(`{}`))
		assertUnsupported(t, err)
		if handlerCalls != 0 {
			t.Fatalf("handler calls %d, want 0", handlerCalls)
		}
		if calls := dc.callLog(); len(calls) != 0 {
			t.Fatalf("durable calls %+v, want none", calls)
		}

		h2 := host.New(cat)
		_, errNever := h2.Run(dc, ref, json.RawMessage(`{}`))
		assertUnsupported(t, errNever)
		if !bytes.Equal([]byte(errNever.Error()), []byte(err.Error())) {
			t.Fatalf("never-registered error %q differs from deregistered %q", errNever, err)
		}
	})

	t.Run("RegisterHandler_nil_on_never_registered_kind_is_noop", func(t *testing.T) {
		toolDef := newFlow("tool-only")
		toolDef.Nodes["worker"] = node("tool", `{"tool":"w1"}`, "", "")
		addLinear(toolDef, "worker")
		toolRef := cat.register(toolDef)
		dc2 := newFakeDC()
		dc2.steps["w1"] = scripted{result: json.RawMessage(`{"done":true}`)}

		h := host.New(cat)
		h.RegisterHandler("agent", nil)

		_, err := h.Run(dc2, toolRef, json.RawMessage(`{"a":1}`))
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if calls := dc2.callLog(); len(calls) != 1 || calls[0].kind != "step" {
			t.Fatalf("calls %+v, want one step", calls)
		}
	})
}
