package host

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"sync/atomic"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/ir"
	"github.com/epzero/basamento/runtime"
)

type nilTestCatalog struct {
	def *flow.IR
}

func (c *nilTestCatalog) Load(_ context.Context, _ runtime.FlowRef) (*flow.IR, error) {
	return c.def, nil
}

type nilTestDC struct {
	steps int32
}

func (d *nilTestDC) Context() context.Context { return context.Background() }
func (d *nilTestDC) Now() time.Time           { return time.Unix(1, 0).UTC() }
func (d *nilTestDC) NewID(string) string      { return "id-1" }
func (d *nilTestDC) Rand(string) *rand.Rand   { return rand.New(rand.NewPCG(1, 2)) }
func (d *nilTestDC) Step(string, any, runtime.StepOptions) runtime.Future {
	atomic.AddInt32(&d.steps, 1)
	return &nilTestFuture{}
}
func (d *nilTestDC) Child(runtime.FlowRef, any, runtime.ChildOptions) runtime.Future {
	return &nilTestFuture{}
}
func (d *nilTestDC) Sleep(time.Duration) runtime.Future    { return &nilTestFuture{} }
func (d *nilTestDC) Await(runtime.GateSpec) runtime.Future { return &nilTestFuture{} }
func (d *nilTestDC) Select(...runtime.Future) (int, error) { return 0, nil }
func (d *nilTestDC) Emit(runtime.ProgressEvent) error      { return nil }
func (d *nilTestDC) Checkpoint(runtime.SnapshotRef) error  { return nil }
func (d *nilTestDC) Capabilities() runtime.Caps            { return runtime.Caps{} }

type nilTestFuture struct{}

func (f *nilTestFuture) Get(any) error { return nil }
func (f *nilTestFuture) Ready() bool   { return true }

func agentOnlyFlow() *flow.IR {
	return &flow.IR{
		Name:    "nil-handler",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes:   map[string]flow.Node{"worker": {Kind: "agent"}},
		Edges:   []flow.Edge{{From: ir.Start, To: "worker"}},
	}
}

func TestNilHandlerRegistration(t *testing.T) {
	never := func(_ runtime.DurableContext, _ flow.Node, _ json.RawMessage) (NodeResult, error) {
		return NodeResult{Output: json.RawMessage(`{}`)}, nil
	}
	dc := &nilTestDC{}
	cat := &nilTestCatalog{def: agentOnlyFlow()}
	ref := runtime.FlowRef{Name: "nil-handler"}

	t.Run("RegisterHandler_kind_nil_matches_never_registered", func(t *testing.T) {
		h := New(cat)
		h.RegisterHandler("agent", never)
		h.RegisterHandler("agent", nil)

		_, err := h.Run(dc, ref, json.RawMessage(`{}`))
		if core.CodeOf(err) != core.CodeUnsupported {
			t.Fatalf("code %v (%v), want unsupported", core.CodeOf(err), err)
		}
		wantMsg := `flow: node "worker": no node handler is registered for kind "agent"`
		var pe *core.Error
		if !errors.As(err, &pe) || pe.Message != wantMsg {
			t.Fatalf("error %v, want message %q", err, wantMsg)
		}
		if atomic.LoadInt32(&dc.steps) != 0 {
			t.Fatalf("durable steps %d, want 0 before any node", dc.steps)
		}

		h2 := New(cat)
		_, errNever := h2.Run(dc, ref, json.RawMessage(`{}`))
		if core.CodeOf(errNever) != core.CodeUnsupported {
			t.Fatalf("never-registered code %v, want unsupported", core.CodeOf(errNever))
		}
		var pe2 *core.Error
		if !errors.As(errNever, &pe2) || pe2.Message != wantMsg {
			t.Fatalf("never-registered error %v, want message %q", errNever, wantMsg)
		}
	})

	t.Run("handlers_map_holds_nil_value", func(t *testing.T) {
		h := &Host{
			catalog:  cat,
			handlers: map[string]NodeHandler{"agent": nil},
		}
		_, err := h.Run(dc, ref, json.RawMessage(`{}`))
		if core.CodeOf(err) != core.CodeUnsupported {
			t.Fatalf("code %v (%v), want unsupported", core.CodeOf(err), err)
		}
		if atomic.LoadInt32(&dc.steps) != 0 {
			t.Fatalf("durable steps %d, want 0", dc.steps)
		}
	})
}
