package local

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func TestChildRejectsConflictingRunID(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	var childRuns atomic.Int32
	f.flow("child", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		childRuns.Add(1)
		return json.RawMessage(`{"from":"child"}`), nil
	})
	f.flow("parent", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		first := dc.Child(runtime.FlowRef{Name: "child"}, nil, runtime.ChildOptions{ID: "c1"})
		if err := first.Get(nil); err != nil {
			return nil, err
		}
		second := dc.Child(runtime.FlowRef{Name: "other-child"}, nil, runtime.ChildOptions{ID: "c1"})
		return nil, second.Get(nil)
	})
	f.start(t, "acme", "parent-1", "parent", `{}`)
	f.waitState(t, "acme", "parent-1", stateFailed)
	if got := childRuns.Load(); got != 1 {
		t.Fatalf("child ran %d times, want 1", got)
	}
	target := f.rt.lookup(core.TenantID("acme"), "parent-1")
	target.mu.Lock()
	err := target.err
	target.mu.Unlock()
	if codeOf(err) != "conflict" {
		t.Fatalf("parent error %v, want conflict", err)
	}
}

func TestChildIdempotentSameParentAndFlow(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	var childRuns atomic.Int32
	f.flow("child", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		childRuns.Add(1)
		return json.RawMessage(`{"n":1}`), nil
	})
	f.flow("parent", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		opts := runtime.ChildOptions{ID: "c1"}
		first, second := dc.Child(runtime.FlowRef{Name: "child"}, nil, opts), dc.Child(runtime.FlowRef{Name: "child"}, nil, opts)
		var a, b json.RawMessage
		if err := first.Get(&a); err != nil {
			return nil, err
		}
		if err := second.Get(&b); err != nil {
			return nil, err
		}
		if string(a) != string(b) {
			return nil, fmt.Errorf("outputs differ: %s vs %s", a, b)
		}
		return a, nil
	})
	f.start(t, "acme", "parent-1", "parent", `{}`)
	f.waitState(t, "acme", "parent-1", stateCompleted)
	if got := childRuns.Load(); got != 1 {
		t.Fatalf("child ran %d times, want 1", got)
	}
}

func TestChildDoesNotAttachAcrossTenants(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	f.flow("marker", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"tenant":"globex"}`), nil
	})
	f.start(t, "globex", "shared-id", "marker", `{}`)
	f.waitState(t, "globex", "shared-id", stateCompleted)

	var childOut json.RawMessage
	f.flow("parent", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		return nil, dc.Child(runtime.FlowRef{Name: "child"}, nil, runtime.ChildOptions{ID: "shared-id"}).Get(&childOut)
	})
	f.flow("child", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"tenant":"acme"}`), nil
	})
	f.start(t, "acme", "parent-1", "parent", `{}`)
	f.waitState(t, "acme", "parent-1", stateCompleted)
	if string(childOut) != `{"tenant":"acme"}` {
		t.Fatalf("child output %s, want acme run not globex reuse", childOut)
	}
}

func TestChildRunReturnsOutputInParentTenant(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	f.flow("child", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"status":"ok"}`), nil
	})
	parentGot := make(chan json.RawMessage, 1)
	f.flow("parent", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var childOut json.RawMessage
		if err := dc.Child(runtime.FlowRef{Name: "child"}, nil, runtime.ChildOptions{ID: "child-1"}).Get(&childOut); err != nil {
			return nil, err
		}
		parentGot <- childOut
		return childOut, nil
	})
	f.start(t, "acme", "parent-1", "parent", `{}`)
	f.waitState(t, "acme", "parent-1", stateCompleted)
	f.waitState(t, "acme", "child-1", stateCompleted)
	if _, err := f.rt.Describe(tenantCtx("globex"), runtime.RunRef{RunID: "child-1"}); codeOf(err) != "not_found" {
		t.Fatalf("child described from globex: %v, want not_found", err)
	}
	out := <-parentGot
	if string(out) != `{"status":"ok"}` {
		t.Fatalf("child output %s, want {\"status\":\"ok\"}", out)
	}
}

func TestParentClosePolicies(t *testing.T) {
	for _, tc := range []struct {
		policy string
		want   string
	}{
		{"terminate", stateTerminated},
		{"request_cancel", stateCancelled},
		{"abandon", stateRunning},
	} {
		t.Run(tc.policy, func(t *testing.T) {
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
				dc.Child(runtime.FlowRef{Name: "child"}, nil, runtime.ChildOptions{ID: "child-1", ParentClose: tc.policy})
				return json.RawMessage(`{}`), nil
			})
			f.start(t, "acme", "parent-1", "parent", `{}`)
			f.waitState(t, "acme", "parent-1", stateCompleted)
			f.waitState(t, "acme", "child-1", tc.want)
		})
	}
}
