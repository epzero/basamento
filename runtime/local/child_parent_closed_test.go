package local

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func TestChildAfterParentCancelledDoesNotStartOrphan(t *testing.T) {
	for _, policy := range []string{"abandon", "request_cancel", "terminate"} {
		t.Run(policy, func(t *testing.T) {
			f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
			defer f.close(t)
			hold := make(chan struct{})
			unblock := make(chan struct{})
			childErr := make(chan error, 1)
			f.flow("child", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{}`), nil
			})
			f.flow("parent", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
				close(hold)
				<-unblock
				fut := dc.Child(runtime.FlowRef{Name: "child"}, nil, runtime.ChildOptions{ID: "late-child", ParentClose: policy})
				err := fut.Get(nil)
				childErr <- err
				return nil, err
			})
			f.start(t, "acme", "p1", "parent", `{}`)
			waitOn(t, "parent to block", hold)
			if err := f.rt.Cancel(tenantCtx("acme"), runtime.RunRef{RunID: "p1"}, "test"); err != nil {
				t.Fatal(err)
			}
			close(unblock)
			f.waitState(t, "acme", "p1", stateCancelled)
			err := recvWithin(t, childErr, "child future result")
			if err == nil {
				t.Fatal("Child after parent cancel: want error")
			}
			if codeOf(err) != "cancelled" {
				t.Fatalf("Child error code %v, want cancelled", err)
			}
			if _, err := f.rt.Describe(tenantCtx("acme"), runtime.RunRef{RunID: "late-child"}); codeOf(err) != "not_found" {
				t.Fatalf("late-child Describe: %v, want not_found", err)
			}
			runs, _, err := f.rt.List(tenantCtx("acme"), runtime.RunQuery{})
			if err != nil {
				t.Fatal(err)
			}
			for _, st := range runs {
				if st.Ref.RunID == "late-child" {
					t.Fatalf("List still has late-child")
				}
			}
		})
	}
}

func TestChildAfterParentCancelledMessage(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	hold := make(chan struct{})
	unblock := make(chan struct{})
	childErr := make(chan error, 1)
	f.flow("child", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	f.flow("parent", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		close(hold)
		<-unblock
		err := dc.Child(runtime.FlowRef{Name: "child"}, nil, runtime.ChildOptions{ID: "x", ParentClose: "abandon"}).Get(nil)
		childErr <- err
		return nil, err
	})
	f.start(t, "acme", "p1", "parent", `{}`)
	waitOn(t, "parent to block", hold)
	_ = f.rt.Cancel(tenantCtx("acme"), runtime.RunRef{RunID: "p1"}, "test")
	close(unblock)
	f.waitState(t, "acme", "p1", stateCancelled)
	err := recvWithin(t, childErr, "child future result")
	var plat *core.Error
	if !errors.As(err, &plat) || plat.Message != "local: the parent is closed" {
		t.Fatalf("child error %v, want cancelled parent closed", err)
	}
}
