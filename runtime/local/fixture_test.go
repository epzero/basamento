package local

import (
	"context"
	"encoding/json"
	"fmt"
	goruntime "runtime"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/runtime"
)

type flowFunc func(dc runtime.DurableContext, in json.RawMessage) (json.RawMessage, error)

type testHost map[string]flowFunc

func (h testHost) Run(dc runtime.DurableContext, ref runtime.FlowRef, in json.RawMessage) (json.RawMessage, error) {
	fn := h[ref.Name]
	if fn == nil {
		return nil, fmt.Errorf("no flow %q", ref.Name)
	}
	return fn(dc, in)
}

type fixture struct {
	rt    *Runtime
	clock *coretest.FakeClock
	host  testHost
}

func newFixture(t *testing.T, at time.Time) *fixture {
	t.Helper()
	clock := coretest.NewFakeClock(at)
	f := &fixture{clock: clock, host: testHost{}}
	var err error
	f.rt, err = New(Options{Clock: clock, IDs: &coretest.SeqIds{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := f.rt.RegisterFlowHost(f.host.Run); err != nil {
		t.Fatalf("RegisterFlowHost: %v", err)
	}
	return f
}

func (f *fixture) flow(name string, fn flowFunc) { f.host[name] = fn }

func tenantCtx(tenant string) context.Context {
	if tenant == "" {
		return context.Background()
	}
	return core.WithTenant(context.Background(), core.TenantID(tenant))
}

func (f *fixture) start(t *testing.T, tenant, runID, flow string, in string) runtime.RunRef {
	t.Helper()
	ref, err := f.rt.Start(tenantCtx(tenant), runtime.StartRequest{
		Flow:  runtime.FlowRef{Name: flow},
		Input: json.RawMessage(in),
		RunID: runID,
	})
	if err != nil {
		t.Fatalf("Start(%s): %v", flow, err)
	}
	return ref
}

const spinBudget = 100_000

func recvWithin[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	for i := 0; i < spinBudget; i++ {
		select {
		case v := <-ch:
			return v
		default:
			goruntime.Gosched()
		}
	}
	t.Fatalf("timed out receiving %s after %d yields", what, spinBudget)
	var zero T
	return zero
}

func waitGateTimeouts(t *testing.T, r *run, want int32) {
	t.Helper()
	for i := 0; i < spinBudget; i++ {
		if got := r.gateTimeouts.Load(); got == want {
			return
		}
		goruntime.Gosched()
	}
	t.Fatalf("gate timeout helpers = %d, want %d after %d yields", r.gateTimeouts.Load(), want, spinBudget)
}

// waitOn waits for ch to close, yielding until the liveness budget is exhausted.
func waitOn(t *testing.T, what string, ch <-chan struct{}) {
	t.Helper()
	for i := 0; i < spinBudget; i++ {
		select {
		case <-ch:
			return
		default:
			goruntime.Gosched()
		}
	}
	t.Fatalf("timed out waiting for %s after %d yields", what, spinBudget)
}

func (f *fixture) state(t *testing.T, tenant, runID string) string {
	t.Helper()
	status, err := f.rt.Describe(tenantCtx(tenant), runtime.RunRef{RunID: runID})
	if err != nil {
		t.Fatalf("Describe(%s): %v", runID, err)
	}
	return status.State
}

// waitState waits for a run to reach a state by watching its completion
// channel, not by polling.
func (f *fixture) waitState(t *testing.T, tenant, runID, want string) {
	t.Helper()
	target := f.rt.lookup(core.TenantID(tenant), runID)
	if target == nil {
		t.Fatalf("run %s not found", runID)
	}
	for i := 0; i < spinBudget; i++ {
		target.mu.Lock()
		state := target.state
		target.mu.Unlock()
		if state == want {
			return
		}
		select {
		case <-target.wake():
		default:
			goruntime.Gosched()
		}
	}
	target.mu.Lock()
	still := target.state
	target.mu.Unlock()
	t.Fatalf("timed out waiting for run %s to reach %s (still %s)", runID, want, still)
}

func (f *fixture) close(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = f.rt.Serve(ctx)
		close(done)
	}()
	cancel()
	waitOn(t, "the runtime to shut down", done)
}

func codeOf(err error) string {
	if err == nil {
		return ""
	}
	return string(core.CodeOf(err))
}
func (f *fixture) output(t *testing.T, tenant, runID string) json.RawMessage {
	t.Helper()
	r := f.rt.lookup(core.TenantID(tenant), runID)
	if r == nil {
		t.Fatalf("run %s not found", runID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append(json.RawMessage(nil), r.output...)
}
