package local

import (
	"encoding/json"
	"fmt"
	"sync"
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
	childStarted := make(chan struct{})
	barrier := make(chan struct{})
	pairOut := make(chan [2]runtime.Future, 1)
	f.flow("child", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		close(childStarted)
		<-barrier
		return json.RawMessage(`{}`), nil
	})
	f.flow("parent", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		opts := runtime.ChildOptions{ID: "c1", ParentClose: "abandon"}
		var wg sync.WaitGroup
		var fut1, fut2 runtime.Future
		wg.Add(2)
		go func() {
			defer wg.Done()
			fut1 = dc.Child(ref, nil, opts)
		}()
		go func() {
			defer wg.Done()
			fut2 = dc.Child(ref, nil, opts)
		}()
		wg.Wait()
		if fut1 == nil || fut2 == nil {
			return nil, fmt.Errorf("concurrent Child returned nil future")
		}
		pairOut <- [2]runtime.Future{fut1, fut2}
		close(barrier)
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "p1", "parent", `{}`)
	waitOn(t, "child flow to start", childStarted)
	pair := recvWithin(t, pairOut, "futures from concurrent Child")
	if pair[0] != pair[1] {
		t.Fatalf("concurrent Child returned different futures")
	}
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

func TestChildReattachAfterParentClosedReturnsRecordedFuture(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	hold := make(chan struct{})
	unblock := make(chan struct{})
	ref := runtime.FlowRef{Name: "child"}
	out := make(chan struct {
		first, second runtime.Future
		err           error
	}, 1)
	f.flow("child", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		return nil, dc.Await(runtime.GateSpec{Name: "child-hold"}).Get(nil)
	})
	f.flow("parent", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		first := dc.Child(ref, nil, runtime.ChildOptions{ID: "c1", ParentClose: "abandon"})
		close(hold)
		<-unblock
		second := dc.Child(ref, nil, runtime.ChildOptions{ID: "c1"})
		var err error
		if e := second.Get(nil); e != nil {
			err = e
		}
		out <- struct {
			first, second runtime.Future
			err           error
		}{first, second, err}
		return nil, err
	})
	f.start(t, "acme", "p1", "parent", `{}`)
	waitOn(t, "parent blocked after first Child", hold)
	if err := f.rt.Cancel(tenantCtx("acme"), runtime.RunRef{RunID: "p1"}, "test"); err != nil {
		t.Fatal(err)
	}
	close(unblock)
	f.waitState(t, "acme", "p1", stateCancelled)
	got := recvWithin(t, out, "reattach result")
	if got.first != got.second {
		t.Fatalf("reattach after parent closed: different futures")
	}
	if got.err == nil || codeOf(got.err) != "cancelled" {
		t.Fatalf("second Get %v, want cancelled", got.err)
	}
	runs, _, err := f.rt.List(tenantCtx("acme"), runtime.RunQuery{})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, st := range runs {
		ids = append(ids, st.Ref.RunID)
	}
	if len(ids) != 2 {
		t.Fatalf("List runs %v, want exactly p1 and c1", ids)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	if !seen["p1"] || !seen["c1"] {
		t.Fatalf("List runs %v, want p1 and c1", ids)
	}
}
