package local

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func TestLinkChildIsIdempotent(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	childFlow := runtime.FlowRef{Name: "child"}
	parentAtGate := make(chan struct{})
	f.flow("child", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		return nil, dc.Await(runtime.GateSpec{Name: "child-hold"}).Get(nil)
	})
	f.flow("parent", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		wait := dc.Await(runtime.GateSpec{Name: "parent-hold"})
		close(parentAtGate)
		return nil, wait.Get(nil)
	})
	f.start(t, "acme", "p1", "parent", `{}`)
	waitOn(t, "parent at gate", parentAtGate)
	parent := f.rt.lookup(core.TenantID("acme"), "p1")
	if parent == nil {
		t.Fatal("parent run not found")
	}
	child, err := f.rt.startChild(parent.tenant, "c1", childFlow, json.RawMessage(`{}`), parent)
	if err != nil {
		t.Fatalf("startChild: %v", err)
	}
	f1 := parent.linkChild(child, "abandon")
	f2 := parent.linkChild(child, "terminate")
	if f1 != f2 {
		t.Fatalf("linkChild returned different futures")
	}
	parent.mu.Lock()
	n := len(parent.children)
	policy := ""
	if n > 0 {
		policy = parent.children[0].policy
	}
	parent.mu.Unlock()
	if n != 1 {
		t.Fatalf("len(children) = %d, want 1", n)
	}
	if policy != "abandon" {
		t.Fatalf("first policy %q, want abandon", policy)
	}
}

func assertLinkChildClosedParent(t *testing.T, f *fixture, parent, child *run, policy string, waitersBefore int) {
	t.Helper()
	parent.mu.Lock()
	childrenBefore := len(parent.children)
	parent.mu.Unlock()
	fut := parent.linkChild(child, policy)
	if !fut.Ready() {
		t.Fatal("future not ready after linkChild on closed parent")
	}
	futErr := fut.Get(nil)
	if futErr == nil || codeOf(futErr) != "cancelled" {
		t.Fatalf("future error %v, want cancelled", futErr)
	}
	var plat *core.Error
	if !errors.As(futErr, &plat) || plat.Message != "local: the parent closed before the child was linked" {
		t.Fatalf("future error %v, want parent closed before link message", futErr)
	}
	child.mu.Lock()
	waitersAfter := len(child.waiters)
	child.mu.Unlock()
	if waitersAfter != waitersBefore {
		t.Fatalf("len(child.waiters) = %d, want unchanged %d", waitersAfter, waitersBefore)
	}
	parent.mu.Lock()
	childrenAfter := len(parent.children)
	parent.mu.Unlock()
	if childrenAfter != childrenBefore {
		t.Fatalf("len(parent.children) = %d, want unchanged %d", childrenAfter, childrenBefore)
	}
}

func TestLinkChildAfterParentClosed(t *testing.T) {
	policies := []string{"terminate", "request_cancel", "abandon"}
	for _, policy := range policies {
		for _, tc := range []struct {
			name       string
			childState string
		}{
			{"running", stateRunning},
			{"completed", stateCompleted},
			{"failed", stateFailed},
		} {
			t.Run(policy+"/"+tc.name, func(t *testing.T) {
				f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
				defer f.close(t)
				childFlow := runtime.FlowRef{Name: "child"}
				parentAtGate := make(chan struct{})
				var childAtGate chan struct{}
				childWaitErr := make(chan error, 1)
				switch tc.childState {
				case stateRunning:
					childAtGate = make(chan struct{})
					f.flow("child", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
						close(childAtGate)
						err := dc.Await(runtime.GateSpec{Name: "child-hold"}).Get(nil)
						childWaitErr <- err
						return nil, err
					})
				case stateCompleted:
					f.flow("child", func(_ runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
						return json.RawMessage(`{"done":true}`), nil
					})
				case stateFailed:
					f.flow("child", func(_ runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
						return nil, invalidErr("child failed for test", nil)
					})
				}
				f.flow("parent", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
					wait := dc.Await(runtime.GateSpec{Name: "parent-hold"})
					close(parentAtGate)
					return nil, wait.Get(nil)
				})
				f.start(t, "acme", "p1", "parent", `{}`)
				waitOn(t, "parent at gate", parentAtGate)
				parent := f.rt.lookup(core.TenantID("acme"), "p1")
				if parent == nil {
					t.Fatal("parent run not found")
				}
				child, err := f.rt.startChild(parent.tenant, "c1", childFlow, json.RawMessage(`{}`), parent)
				if err != nil {
					t.Fatalf("startChild: %v", err)
				}
				if childAtGate != nil {
					waitOn(t, "child at gate", childAtGate)
				} else {
					f.waitState(t, "acme", "c1", tc.childState)
				}
				child.mu.Lock()
				waitersBefore := len(child.waiters)
				child.mu.Unlock()
				if err := f.rt.Cancel(tenantCtx("acme"), runtime.RunRef{RunID: "p1"}, "test"); err != nil {
					t.Fatal(err)
				}
				f.waitState(t, "acme", "p1", stateCancelled)
				assertLinkChildClosedParent(t, f, parent, child, policy, waitersBefore)
				if tc.childState == stateRunning {
					f.waitState(t, "acme", "c1", stateTerminated)
					waitErr := recvWithin(t, childWaitErr, "child gate wait")
					if codeOf(waitErr) != "cancelled" {
						t.Fatalf("child wait error %v, want cancelled", waitErr)
					}
				}
			})
		}
	}
	t.Run("grandchild", func(t *testing.T) {
		f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
		defer f.close(t)
		childFlow := runtime.FlowRef{Name: "child"}
		parentAtGate := make(chan struct{})
		treeReady := make(chan struct{})
		f.flow("grandchild", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
			return nil, dc.Await(runtime.GateSpec{Name: "gc-hold"}).Get(nil)
		})
		f.flow("child", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
			dc.Child(runtime.FlowRef{Name: "grandchild"}, nil, runtime.ChildOptions{ID: "gc1"})
			close(treeReady)
			return nil, dc.Await(runtime.GateSpec{Name: "child-hold"}).Get(nil)
		})
		f.flow("parent", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
			wait := dc.Await(runtime.GateSpec{Name: "parent-hold"})
			close(parentAtGate)
			return nil, wait.Get(nil)
		})
		f.start(t, "acme", "p1", "parent", `{}`)
		waitOn(t, "parent at gate", parentAtGate)
		parent := f.rt.lookup(core.TenantID("acme"), "p1")
		if parent == nil {
			t.Fatal("parent run not found")
		}
		child, err := f.rt.startChild(parent.tenant, "c1", childFlow, json.RawMessage(`{}`), parent)
		if err != nil {
			t.Fatalf("startChild: %v", err)
		}
		waitOn(t, "child tree ready", treeReady)
		f.waitState(t, "acme", "gc1", stateRunning)
		if err := f.rt.Cancel(tenantCtx("acme"), runtime.RunRef{RunID: "p1"}, "test"); err != nil {
			t.Fatal(err)
		}
		f.waitState(t, "acme", "p1", stateCancelled)
		if err := parent.linkChild(child, "abandon").Get(nil); codeOf(err) != "cancelled" {
			t.Fatalf("link future error %v, want cancelled", err)
		}
		f.waitState(t, "acme", "c1", stateTerminated)
		f.waitState(t, "acme", "gc1", stateTerminated)
	})
}
