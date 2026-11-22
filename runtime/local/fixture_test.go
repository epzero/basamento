package local

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
	f.rt, err = New(Options{Clock: clock, IDs: &coretest.SeqIds{}, Logger: slog.New(slog.DiscardHandler)})
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

// waitBound is the maximum wall-clock time a test wait may spend before the
// hang guard fails it; waits end as soon as their condition holds.
const waitBound = 10 * time.Second

// pollUntil spins on cond with Gosched between attempts until it holds or the
// hang guard expires; use when the condition has no notification channel.
func pollUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitBound)
	defer cancel()
	for {
		if cond() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s after %v", what, waitBound)
		default:
			goruntime.Gosched()
		}
	}
}

func recvWithin[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitBound)
	defer cancel()
	select {
	case v := <-ch:
		return v
	case <-ctx.Done():
		t.Fatalf("timed out receiving %s after %v", what, waitBound)
		var zero T
		return zero
	}
}

// waitGate waits until a run has registered the named gate. A flow may start
// a child before it awaits its own gate, so the child being ready does not
// mean the parent's gate exists yet.
func (f *fixture) waitGate(t *testing.T, tenant, runID, name string) {
	t.Helper()
	target := f.rt.lookup(core.TenantID(tenant), runID)
	if target == nil {
		t.Fatalf("run %s not found", runID)
	}
	pollUntil(t, fmt.Sprintf("run %s to register gate %s", runID, name), func() bool {
		target.mu.Lock()
		defer target.mu.Unlock()
		return target.gates[name] != nil
	})
}

func waitGateTimeouts(t *testing.T, r *run, want int32) {
	t.Helper()
	pollUntil(t, fmt.Sprintf("gate timeout helpers = %d", want), func() bool {
		return r.gateTimeouts.Load() == want
	})
}

// waitOn waits for ch to close or send.
func waitOn(t *testing.T, what string, ch <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitBound)
	defer cancel()
	select {
	case <-ch:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for %s after %v", what, waitBound)
	}
}

func (f *fixture) state(t *testing.T, tenant, runID string) string {
	t.Helper()
	status, err := f.rt.Describe(tenantCtx(tenant), runtime.RunRef{RunID: runID})
	if err != nil {
		t.Fatalf("Describe(%s): %v", runID, err)
	}
	return status.State
}

// waitState waits for a run to reach a state by blocking on its wake channel.
func (f *fixture) waitState(t *testing.T, tenant, runID, want string) {
	t.Helper()
	target := f.rt.lookup(core.TenantID(tenant), runID)
	if target == nil {
		t.Fatalf("run %s not found", runID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), waitBound)
	defer cancel()
	for {
		wake := target.wake()
		target.mu.Lock()
		state := target.state
		target.mu.Unlock()
		if state == want {
			return
		}
		select {
		case <-wake:
		case <-ctx.Done():
			target.mu.Lock()
			still := target.state
			target.mu.Unlock()
			t.Fatalf("timed out waiting for run %s to reach %s (still %s) after %v", runID, want, still, waitBound)
		}
	}
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
