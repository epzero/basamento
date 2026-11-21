package local

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/epzero/basamento/runtime"
)

func TestParentCloseTerminatesGrandchild(t *testing.T) {
	for _, tc := range []struct {
		policy string
		want   string
	}{
		{"terminate", stateTerminated},
		{"request_cancel", stateCancelled},
	} {
		t.Run(tc.policy, func(t *testing.T) {
			f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
			defer f.close(t)
			treeReady := make(chan struct{})
			f.flow("grandchild", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
				return nil, dc.Await(runtime.GateSpec{Name: "hold"}).Get(nil)
			})
			f.flow("child", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
				dc.Child(runtime.FlowRef{Name: "grandchild"}, nil, runtime.ChildOptions{ID: "gc1", ParentClose: "terminate"})
				close(treeReady)
				return nil, dc.Await(runtime.GateSpec{Name: "child-hold"}).Get(nil)
			})
			f.flow("parent", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
				dc.Child(runtime.FlowRef{Name: "child"}, nil, runtime.ChildOptions{ID: "c1", ParentClose: tc.policy})
				return nil, dc.Await(runtime.GateSpec{Name: "parent-hold"}).Get(nil)
			})
			f.start(t, "acme", "p1", "parent", `{}`)
			waitOn(t, "child tree ready", treeReady)
			f.waitState(t, "acme", "gc1", stateRunning)
			if err := f.rt.Resolve(tenantCtx("acme"), runtime.RunRef{RunID: "p1"}, "parent-hold", json.RawMessage(`{}`)); err != nil {
				t.Fatal(err)
			}
			f.waitState(t, "acme", "p1", stateCompleted)
			f.waitState(t, "acme", "c1", tc.want)
			f.waitState(t, "acme", "gc1", stateTerminated)
		})
	}
}

func TestAbandonChildLeavesGrandchildRunning(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	treeReady := make(chan struct{})
	f.flow("grandchild", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		return nil, dc.Await(runtime.GateSpec{Name: "hold"}).Get(nil)
	})
	f.flow("child", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		dc.Child(runtime.FlowRef{Name: "grandchild"}, nil, runtime.ChildOptions{ID: "gc1", ParentClose: "terminate"})
		close(treeReady)
		return nil, dc.Await(runtime.GateSpec{Name: "child-hold"}).Get(nil)
	})
	f.flow("parent", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		dc.Child(runtime.FlowRef{Name: "child"}, nil, runtime.ChildOptions{ID: "c1", ParentClose: "abandon"})
		return nil, dc.Await(runtime.GateSpec{Name: "parent-hold"}).Get(nil)
	})
	f.start(t, "acme", "p1", "parent", `{}`)
	waitOn(t, "child tree ready", treeReady)
	f.waitState(t, "acme", "gc1", stateRunning)
	if err := f.rt.Resolve(tenantCtx("acme"), runtime.RunRef{RunID: "p1"}, "parent-hold", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, "acme", "p1", stateCompleted)
	f.waitState(t, "acme", "c1", stateRunning)
	f.waitState(t, "acme", "gc1", stateRunning)
}
