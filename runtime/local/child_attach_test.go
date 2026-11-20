package local

import (
	"encoding/json"
	goruntime "runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/epzero/basamento/runtime"
)

func TestChildRejectsDifferentParentRun(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	var childRuns atomic.Int32
	f.flow("child", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		childRuns.Add(1)
		return nil, dc.Await(runtime.GateSpec{Name: "approve"}).Get(nil)
	})
	f.flow("parent", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		dc.Child(runtime.FlowRef{Name: "child"}, nil, runtime.ChildOptions{ID: "c1", ParentClose: "abandon"})
		return json.RawMessage(`{}`), nil
	})
	f.flow("parent2", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		return nil, dc.Child(runtime.FlowRef{Name: "child"}, nil, runtime.ChildOptions{ID: "c1"}).Get(nil)
	})
	f.start(t, "acme", "parent-1", "parent", `{}`)
	f.waitState(t, "acme", "parent-1", stateCompleted)
	f.waitState(t, "acme", "c1", stateRunning)
	before := childRuns.Load()
	f.start(t, "acme", "parent-2", "parent2", `{}`)
	f.waitState(t, "acme", "parent-2", stateFailed)
	if childRuns.Load() != before {
		t.Fatalf("child flow ran %d times total, want %d (no second start)", childRuns.Load(), before)
	}
}

func TestChildRejectsTopLevelRunID(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	var childRuns atomic.Int32
	f.flow("review", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		return nil, dc.Await(runtime.GateSpec{Name: "approve"}).Get(nil)
	})
	f.flow("child", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		childRuns.Add(1)
		return json.RawMessage(`{}`), nil
	})
	f.flow("parent", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		return nil, dc.Child(runtime.FlowRef{Name: "child"}, nil, runtime.ChildOptions{ID: "existing-1"}).Get(nil)
	})
	f.start(t, "acme", "existing-1", "review", `{}`)
	f.waitState(t, "acme", "existing-1", stateRunning)
	f.start(t, "acme", "parent-1", "parent", `{}`)
	f.waitState(t, "acme", "parent-1", stateFailed)
	if childRuns.Load() != 0 {
		t.Fatalf("child flow ran %d times, want 0", childRuns.Load())
	}
	f.waitState(t, "acme", "existing-1", stateRunning)
}

func TestChildReattachRequiresMatchingFlowHash(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	var runs atomic.Int32
	f.flow("child", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		runs.Add(1)
		return nil, dc.Await(runtime.GateSpec{Name: "approve"}).Get(nil)
	})
	f.flow("parent", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		dc.Child(runtime.FlowRef{Name: "child"}, nil, runtime.ChildOptions{ID: "c1", ParentClose: "abandon"})
		return nil, dc.Child(runtime.FlowRef{Name: "other"}, nil, runtime.ChildOptions{ID: "c1"}).Get(nil)
	})
	f.flow("other", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "p1", "parent", `{}`)
	f.waitState(t, "acme", "p1", stateFailed)
	for i := 0; i < spinBudget && runs.Load() == 0; i++ {
		goruntime.Gosched()
	}
	if runs.Load() != 1 {
		t.Fatalf("child flow ran %d times, want 1", runs.Load())
	}
	f.waitState(t, "acme", "c1", stateRunning)
}
