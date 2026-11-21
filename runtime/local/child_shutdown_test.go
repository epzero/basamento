package local

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func TestChildAfterShutdownStartsNothing(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	atGate := make(chan struct{})
	waitDone := make(chan struct{})
	childErr := make(chan error, 1)
	f.flow("child", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	f.flow("parent", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		wait := dc.Await(runtime.GateSpec{Name: "hold"})
		close(atGate)
		_ = wait.Get(nil)
		close(waitDone)
		err := dc.Child(runtime.FlowRef{Name: "child"}, nil, runtime.ChildOptions{ID: "late-child", ParentClose: "abandon"}).Get(nil)
		childErr <- err
		return nil, err
	})
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan struct{})
	go func() {
		_ = f.rt.Serve(ctx)
		close(serveDone)
	}()
	f.start(t, "acme", "p1", "parent", `{}`)
	waitOn(t, "parent at gate", atGate)
	cancel()
	waitOn(t, "runtime shutdown", serveDone)
	waitOn(t, "gate wait ended", waitDone)
	err := recvWithin(t, childErr, "Child after shutdown")
	if err == nil {
		t.Fatal("want Child error after shutdown")
	}
	var plat *core.Error
	if !errors.As(err, &plat) || plat.Message != "local: the parent is closed" {
		t.Fatalf("Child error %v, want parent closed", err)
	}
	if codeOf(err) != "cancelled" {
		t.Fatalf("code %v, want cancelled", err)
	}
	if _, descErr := f.rt.Describe(tenantCtx("acme"), runtime.RunRef{RunID: "late-child"}); codeOf(descErr) != "not_found" {
		t.Fatalf("late-child Describe: %v, want not_found", descErr)
	}
}
