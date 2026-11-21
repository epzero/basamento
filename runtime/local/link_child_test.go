package local

import (
	"encoding/json"
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
