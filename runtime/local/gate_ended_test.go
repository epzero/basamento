package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func errMessage(err error) string {
	var e *core.Error
	if errors.As(err, &e) {
		return e.Message
	}
	return ""
}

func TestResolveAfterCancelIsConflict(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	atGate := make(chan struct{})
	f.flow("waiting", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		wait := dc.Await(runtime.GateSpec{Name: "approve"})
		close(atGate)
		return nil, wait.Get(nil)
	})
	ref := f.start(t, "acme", "r1", "waiting", `{}`)
	waitOn(t, "the run to await the gate", atGate)

	if err := f.rt.Cancel(tenantCtx("acme"), ref, "user"); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, "acme", "r1", stateCancelled)

	err := f.rt.Resolve(tenantCtx("acme"), ref, "approve", json.RawMessage(`{"approved":true}`))
	if codeOf(err) != "conflict" {
		t.Fatalf("Resolve after cancel = %v, want conflict", err)
	}
	if errMessage(err) != "local: the gate wait already ended" {
		t.Fatalf("Resolve message %q, want gate wait already ended", errMessage(err))
	}
	if got := f.state(t, "acme", "r1"); got != stateCancelled {
		t.Fatalf("Describe state %s, want cancelled", got)
	}

	replayed := f.rt.lookup(core.TenantID("acme"), "r1")
	if replayed == nil {
		t.Fatal("run not found")
	}
	future := (&durableContext{run: replayed}).Await(runtime.GateSpec{Name: "approve"})
	if err := future.Get(nil); codeOf(err) != "cancelled" {
		t.Fatalf("replayed Await = %v, want cancelled", err)
	}
}

func TestResolveAfterGateTimeoutIsConflict(t *testing.T) {
	const gateWait = time.Minute
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	atApprove := make(chan struct{})
	atPublish := make(chan struct{})
	f.flow("two-gates", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		approve := dc.Await(runtime.GateSpec{Name: "approve", Timeout: gateWait})
		close(atApprove)
		approveErr := approve.Get(nil)
		approveCode := codeOf(approveErr)
		if approveErr != nil && approveCode != "timeout" {
			return nil, approveErr
		}
		publish := dc.Await(runtime.GateSpec{Name: "publish"})
		close(atPublish)
		var payload json.RawMessage
		if err := publish.Get(&payload); err != nil {
			return nil, err
		}
		return json.RawMessage(fmt.Sprintf(
			`{"approve_wait":%q,"publish":%s}`,
			approveCode, payload,
		)), nil
	})
	ref := f.start(t, "acme", "r1", "two-gates", `{}`)
	waitOn(t, "the run to await approve", atApprove)
	f.clock.Advance(gateWait)
	waitOn(t, "the run to await publish", atPublish)

	err := f.rt.Resolve(tenantCtx("acme"), ref, "approve", json.RawMessage(`{"late":true}`))
	if codeOf(err) != "conflict" {
		t.Fatalf("late Resolve(approve) = %v, want conflict", err)
	}
	if err := f.rt.Resolve(tenantCtx("acme"), ref, "publish", json.RawMessage(`{"published":true}`)); err != nil {
		t.Fatalf("Resolve(publish): %v", err)
	}
	f.waitState(t, "acme", "r1", stateCompleted)
	want := `{"approve_wait":"timeout","publish":{"published":true}}`
	if out := f.output(t, "acme", "r1"); string(out) != want {
		t.Fatalf("output %s, want %s", out, want)
	}
	if got := f.state(t, "acme", "r1"); got != stateCompleted {
		t.Fatalf("state %s, want completed", got)
	}
}

func TestResolveAfterShutdownIsConflict(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	atGate := make(chan struct{})
	waitDone := make(chan struct{})
	f.flow("waiting", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		wait := dc.Await(runtime.GateSpec{Name: "approve"})
		close(atGate)
		err := wait.Get(nil)
		close(waitDone)
		return nil, err
	})
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan struct{})
	go func() {
		_ = f.rt.Serve(ctx)
		close(serveDone)
	}()
	ref := f.start(t, "acme", "r1", "waiting", `{}`)
	waitOn(t, "the run to await the gate", atGate)
	cancel()
	waitOn(t, "runtime shutdown", serveDone)
	waitOn(t, "gate wait to end", waitDone)

	if got := f.state(t, "acme", "r1"); got != stateRunning {
		t.Fatalf("run after shutdown = %s, want running", got)
	}
	err := f.rt.Resolve(tenantCtx("acme"), ref, "approve", json.RawMessage(`{"approved":true}`))
	if codeOf(err) != "conflict" {
		t.Fatalf("Resolve after shutdown = %v, want conflict", err)
	}
	if errMessage(err) != "local: the gate wait already ended" {
		t.Fatalf("Resolve message %q, want gate wait already ended", errMessage(err))
	}
}
