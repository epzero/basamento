package temporal

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// TestKillAndResumeKeepCompletedSteps kills a run while its second step is in
// flight, resumes it, and checks that the completed step ran once and the
// durable event was not written twice (REQ-RT-002).
func TestKillAndResumeKeepCompletedSteps(t *testing.T) {
	rt, _ := newTestRuntime(t, 0)
	var calls atomic.Int32
	started := make(chan struct{})
	registerHost(t, rt, func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		if err := dc.Emit(runtime.ProgressEvent{Type: "started", Data: json.RawMessage(`{}`)}); err != nil {
			return nil, err
		}
		var one string
		if err := dc.Step("s1", nil, runtime.StepOptions{}).Get(&one); err != nil {
			return nil, err
		}
		var two string
		if err := dc.Step("s2", nil, runtime.StepOptions{}).Get(&two); err != nil {
			return nil, err
		}
		return json.RawMessage(`{"one":"` + one + `","two":"` + two + `"}`), nil
	})
	registerStep(t, rt, "s1", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`"one"`), nil
	})
	registerStep(t, rt, "s2", func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			return nil, core.NewError(core.CodeCancelled, "killed", nil)
		}
		return json.RawMessage(`"two"`), nil
	})

	ref := startRun(t, rt, "acme", "r1", nil)
	<-started
	if err := rt.Kill(testTenantCtx("acme"), ref); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if err := rt.Resume(testTenantCtx("acme"), ref); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if err := rt.Wait(testTenantCtx("acme"), ref); err != nil {
		t.Fatalf("wait: %v", err)
	}
	status, err := rt.Describe(testTenantCtx("acme"), ref)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if status.State != stateCompleted {
		t.Fatalf("state %q, want completed", status.State)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("the in-flight step ran %d times, want 2 (killed and resumed)", got)
	}
	target := rt.lookup("acme", "r1")
	target.mu.Lock()
	outbox := append([]runtime.ProgressEvent(nil), target.outbox...)
	steps := len(target.steps)
	target.mu.Unlock()
	if len(outbox) != 1 {
		t.Fatalf("outbox holds %d events, want the one written before the crash", len(outbox))
	}
	if steps != 2 {
		t.Fatalf("journal holds %d steps, want 2", steps)
	}
}
