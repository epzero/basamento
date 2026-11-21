package local

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func TestChildReattachKeepsFirstAbandonPolicy(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	f.flow("child", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		if err := dc.Await(runtime.GateSpec{Name: "approve"}).Get(&out); err != nil {
			return nil, err
		}
		return out, nil
	})
	f.flow("parent", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		ref := runtime.FlowRef{Name: "child"}
		dc.Child(ref, nil, runtime.ChildOptions{ID: "c1", ParentClose: "abandon"})
		dc.Child(ref, nil, runtime.ChildOptions{ID: "c1"})
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "p1", "parent", `{}`)
	f.waitState(t, "acme", "p1", stateCompleted)
	f.waitState(t, "acme", "c1", stateRunning)
}

func TestChildReattachReturnsRecordedFutureAndOneLink(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	ref := runtime.FlowRef{Name: "child"}
	f.flow("child", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"n":1}`), nil
	})
	f.flow("parent", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		first := dc.Child(ref, nil, runtime.ChildOptions{ID: "c1"})
		second := dc.Child(ref, nil, runtime.ChildOptions{ID: "c1"})
		var a, b json.RawMessage
		if err := first.Get(&a); err != nil {
			return nil, err
		}
		if err := second.Get(&b); err != nil {
			return nil, err
		}
		if string(a) != string(b) {
			t.Errorf("outputs differ: %s vs %s", a, b)
		}
		return a, nil
	})
	f.start(t, "acme", "p1", "parent", `{}`)
	f.waitState(t, "acme", "p1", stateCompleted)
	parent := f.rt.lookup(core.TenantID("acme"), "p1")
	if parent == nil {
		t.Fatal("parent not found")
	}
	parent.mu.Lock()
	n := len(parent.children)
	parent.mu.Unlock()
	if n != 1 {
		t.Fatalf("len(children) = %d, want 1", n)
	}
}

func TestChildConcurrentReattachOneLink(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	ref := runtime.FlowRef{Name: "child"}
	barrier := make(chan struct{})
	var started atomic.Int32
	f.flow("child", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		started.Add(1)
		<-barrier
		return json.RawMessage(`{}`), nil
	})
	f.flow("parent", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var wg sync.WaitGroup
		var fut1, fut2 runtime.Future
		wg.Add(2)
		go func() {
			defer wg.Done()
			fut1 = dc.Child(ref, nil, runtime.ChildOptions{ID: "c1"})
		}()
		go func() {
			defer wg.Done()
			fut2 = dc.Child(ref, nil, runtime.ChildOptions{ID: "c1"})
		}()
		wg.Wait()
		if fut1 == nil || fut2 == nil {
			return nil, fmt.Errorf("concurrent Child returned nil future")
		}
		close(barrier)
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "p1", "parent", `{}`)
	f.waitState(t, "acme", "p1", stateCompleted)
	if started.Load() != 1 {
		t.Fatalf("child started %d times, want 1", started.Load())
	}
	parent := f.rt.lookup(core.TenantID("acme"), "p1")
	if parent == nil {
		t.Fatal("parent not found")
	}
	parent.mu.Lock()
	n := len(parent.children)
	parent.mu.Unlock()
	if n != 1 {
		t.Fatalf("len(children) = %d, want 1", n)
	}
}