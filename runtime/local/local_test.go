package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/runtime"
)

// waitLimit bounds how long a test waits for a goroutine to reach an observed
// state. It is a liveness guard, never the thing that makes a test pass.
const waitLimit = 10 * time.Second

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
	f.rt = New(Options{Clock: clock, IDs: &coretest.SeqIds{}})
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

// waitOn waits for ch to close, failing after the liveness limit.
func waitOn(t *testing.T, what string, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(waitLimit):
		t.Fatalf("timed out waiting for %s", what)
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

// waitState waits for a run to reach a state by watching its completion
// channel, not by polling.
func (f *fixture) waitState(t *testing.T, tenant, runID, want string) {
	t.Helper()
	target := f.rt.lookup(core.TenantID(tenant), runID)
	if target == nil {
		t.Fatalf("run %s not found", runID)
	}
	deadline := time.After(waitLimit)
	for {
		target.mu.Lock()
		state := target.state
		target.mu.Unlock()
		if state == want {
			return
		}
		select {
		case <-target.wake():
		case <-deadline:
			t.Fatalf("timed out waiting for run %s to reach %s (still %s)", runID, want, state)
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

func TestStepRunsOncePerName(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	var executions atomic.Int32
	if err := f.rt.RegisterStep("fetch", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		executions.Add(1)
		return json.RawMessage(`{"call":1}`), nil
	}, runtime.StepDefaults{}); err != nil {
		t.Fatal(err)
	}

	var first, second json.RawMessage
	f.flow("twice", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		if err := dc.Step("fetch", nil, runtime.StepOptions{}).Get(&first); err != nil {
			return nil, err
		}
		if err := dc.Step("fetch", nil, runtime.StepOptions{}).Get(&second); err != nil {
			return nil, err
		}
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "r1", "twice", `{}`)
	f.waitState(t, "acme", "r1", stateCompleted)

	if got := executions.Load(); got != 1 {
		t.Fatalf("step executed %d times, want 1", got)
	}
	if string(first) != `{"call":1}` || string(second) != `{"call":1}` {
		t.Fatalf("results %s and %s, want both {\"call\":1}", first, second)
	}
}

func TestStepJournalIsPerRun(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	var executions atomic.Int32
	if err := f.rt.RegisterStep("fetch", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		n := executions.Add(1)
		return json.RawMessage(fmt.Sprintf(`{"call":%d}`, n)), nil
	}, runtime.StepDefaults{}); err != nil {
		t.Fatal(err)
	}
	f.flow("once", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		if err := dc.Step("fetch", nil, runtime.StepOptions{}).Get(&out); err != nil {
			return nil, err
		}
		return out, nil
	})
	f.start(t, "acme", "r1", "once", `{}`)
	f.waitState(t, "acme", "r1", stateCompleted)
	f.start(t, "acme", "r2", "once", `{}`)
	f.waitState(t, "acme", "r2", stateCompleted)

	if got := executions.Load(); got != 2 {
		t.Fatalf("step executed %d times, want 2", got)
	}
}

func TestTimerOnlyCompletesWhenClockReachesIt(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	sleeping := make(chan struct{})
	f.flow("timer", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		wait := dc.Sleep(30 * time.Minute)
		close(sleeping)
		if err := wait.Get(nil); err != nil {
			return nil, err
		}
		return json.RawMessage(`{}`), nil
	})
	ref := f.start(t, "acme", "r1", "timer", `{}`)
	if ref.RunID != "r1" || ref.TenantID != "acme" {
		t.Fatalf("ref %+v, want r1 in acme", ref)
	}
	waitOn(t, "the timer to register", sleeping)

	f.clock.Advance(29 * time.Minute)
	if got := f.state(t, "acme", "r1"); got != stateRunning {
		t.Fatalf("state after 29m = %s, want running", got)
	}
	f.clock.Advance(time.Minute)
	f.waitState(t, "acme", "r1", stateCompleted)
}

func TestSelectReturnsLowestReadyIndex(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	started := make(chan struct{})
	type outcome struct {
		index int
		err   error
	}
	got := make(chan outcome, 1)
	f.flow("select", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		first := dc.Sleep(2 * time.Hour)
		second := dc.Sleep(time.Hour)
		close(started)
		index, err := dc.Select(first, second)
		got <- outcome{index: index, err: err}
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "r1", "select", `{}`)
	waitOn(t, "the timers to register", started)
	f.clock.Advance(time.Hour)
	select {
	case result := <-got:
		if result.err != nil || result.index != 1 {
			t.Fatalf("Select = (%d, %v), want (1, nil)", result.index, result.err)
		}
	case <-time.After(waitLimit):
		t.Fatal("Select did not return after the 1h timer")
	}
	f.waitState(t, "acme", "r1", stateCompleted)
}

func TestSelectIsDeterministicOverReadySubsets(t *testing.T) {
	rt := New(Options{Clock: coretest.NewFakeClock(time.Unix(1_700_000_000, 0)), IDs: &coretest.SeqIds{}})
	rng := rand.New(rand.NewPCG(7, 11))
	for trial := 0; trial < 200; trial++ {
		r := newRun(rt, "acme", fmt.Sprintf("r%d", trial), runtime.FlowRef{Name: "x"}, nil, context.Background())
		fs := make([]runtime.Future, 8)
		ready := map[int]bool{}
		for i := range fs {
			fs[i] = newFuture(r)
			if rng.IntN(2) == 0 {
				ready[i] = true
				fs[i].(*future).complete(nil, nil)
			}
		}
		if len(ready) == 0 {
			ready[0] = true
			fs[0].(*future).complete(nil, nil)
		}
		index, err := (&durableContext{run: r}).Select(fs...)
		if err != nil {
			t.Fatalf("trial %d: Select: %v", trial, err)
		}
		if !ready[index] {
			t.Fatalf("trial %d: Select returned unready index %d (ready %v)", trial, index, ready)
		}
		for i := range fs {
			if ready[i] && i < index {
				t.Fatalf("trial %d: Select returned %d but %d is ready", trial, index, i)
			}
		}
	}
}

func TestSelectPrefersTheFutureThatCompletedFirst(t *testing.T) {
	rt := New(Options{Clock: coretest.NewFakeClock(time.Unix(1_700_000_000, 0)), IDs: &coretest.SeqIds{}})
	r := newRun(rt, "acme", "r1", runtime.FlowRef{Name: "x"}, nil, context.Background())
	first, second := newFuture(r), newFuture(r)
	marked := r.seqs.Load()

	second.complete(nil, nil)
	first.complete(nil, nil)

	// first (index 0) completed after second (index 1): the first completion
	// after the call wins, not the lowest index.
	if index := r.firstCompletedSince([]runtime.Future{first, second}, marked); index != 1 {
		t.Fatalf("firstCompletedSince = %d, want 1", index)
	}
	// Futures already complete at the mark are not candidates.
	if index := r.firstCompletedSince([]runtime.Future{first, second}, r.seqs.Load()); index != -1 {
		t.Fatalf("firstCompletedSince with no new completion = %d, want -1", index)
	}
}

func TestGateResolutionAndConflict(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	atGate := make(chan struct{})
	f.flow("waiting", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		wait := dc.Await(runtime.GateSpec{Name: "approve"})
		close(atGate)
		var payload json.RawMessage
		if err := wait.Get(&payload); err != nil {
			return nil, err
		}
		return payload, nil
	})
	f.start(t, "acme", "r1", "waiting", `{}`)
	waitOn(t, "the run to await the gate", atGate)

	if err := f.rt.Resolve(tenantCtx("globex"), runtime.RunRef{RunID: "r1"}, "approve", json.RawMessage(`{"approved":true}`)); codeOf(err) != "not_found" {
		t.Fatalf("cross-tenant Resolve = %v, want not_found", err)
	}
	if err := f.rt.Resolve(tenantCtx("acme"), runtime.RunRef{RunID: "r1"}, "approve", json.RawMessage(`{"approved":true}`)); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := f.rt.Resolve(tenantCtx("acme"), runtime.RunRef{RunID: "r1"}, "approve", json.RawMessage(`{"approved":false}`)); codeOf(err) != "conflict" {
		t.Fatalf("second Resolve = %v, want conflict", err)
	}
	f.waitState(t, "acme", "r1", stateCompleted)
	if out := f.output(t, "acme", "r1"); string(out) != `{"approved":true}` {
		t.Fatalf("output %s, want {\"approved\":true}", out)
	}
}

func TestResolveUnknownGateIsNotFound(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	atGate := make(chan struct{})
	f.flow("waiting", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		wait := dc.Await(runtime.GateSpec{Name: "approve"})
		close(atGate)
		return nil, wait.Get(nil)
	})
	f.start(t, "acme", "r1", "waiting", `{}`)
	waitOn(t, "the run to await the gate", atGate)
	err := f.rt.Resolve(tenantCtx("acme"), runtime.RunRef{RunID: "r1"}, "other", json.RawMessage(`{}`))
	if codeOf(err) != "not_found" {
		t.Fatalf("Resolve unknown gate = %v, want not_found", err)
	}
	if err := f.rt.Cancel(tenantCtx("acme"), runtime.RunRef{RunID: "r1"}, "test"); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, "acme", "r1", stateCancelled)
}

func TestCancelStopsInFlightStepAndWaits(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	entered := make(chan struct{})
	var delivered atomic.Bool
	if err := f.rt.RegisterStep("render", func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		close(entered)
		<-ctx.Done()
		delivered.Store(true)
		return nil, ctx.Err()
	}, runtime.StepDefaults{}); err != nil {
		t.Fatal(err)
	}
	f.flow("render", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		return nil, dc.Step("render", nil, runtime.StepOptions{}).Get(&out)
	})
	f.start(t, "acme", "c1", "render", `{}`)
	waitOn(t, "the step to start", entered)

	if err := f.rt.Cancel(tenantCtx("acme"), runtime.RunRef{RunID: "c1"}, "user"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if !delivered.Load() {
		t.Fatal("Cancel returned before the step observed cancellation")
	}
	if got := f.state(t, "acme", "c1"); got != stateCancelled {
		t.Fatalf("state = %s, want cancelled", got)
	}
}

func TestCancelEndsPendingWaitsWithCancelled(t *testing.T) {
	for _, kind := range []string{"gate", "timer"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
			defer f.close(t)
			waiting := make(chan struct{})
			failed := make(chan error, 1)
			f.flow("wait", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
				var wait runtime.Future
				if kind == "gate" {
					wait = dc.Await(runtime.GateSpec{Name: "approve"})
				} else {
					wait = dc.Sleep(time.Hour)
				}
				close(waiting)
				err := wait.Get(nil)
				failed <- err
				return nil, err
			})
			f.start(t, "acme", "r1", "wait", `{}`)
			waitOn(t, "the wait to register", waiting)
			if err := f.rt.Cancel(tenantCtx("acme"), runtime.RunRef{RunID: "r1"}, "user"); err != nil {
				t.Fatal(err)
			}
			f.waitState(t, "acme", "r1", stateCancelled)
			select {
			case err := <-failed:
				if codeOf(err) != "cancelled" {
					t.Fatalf("wait error = %v, want cancelled", err)
				}
			case <-time.After(waitLimit):
				t.Fatal("the wait did not end with cancellation")
			}
		})
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

func TestTenantIsolationAndListing(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	atGate := make(chan struct{})
	if err := f.rt.RegisterStep("fetch", func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		tenant, _ := core.TenantFrom(ctx)
		return json.RawMessage(fmt.Sprintf(`{"tenant":%q}`, tenant)), nil
	}, runtime.StepDefaults{}); err != nil {
		t.Fatal(err)
	}
	f.flow("work", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		if err := dc.Step("fetch", nil, runtime.StepOptions{}).Get(&out); err != nil {
			return nil, err
		}
		return out, nil
	})
	f.flow("review", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		wait := dc.Await(runtime.GateSpec{Name: "approve"})
		close(atGate)
		if err := wait.Get(nil); err != nil {
			return nil, err
		}
		return json.RawMessage(`{}`), nil
	})

	f.start(t, "acme", "shared-1", "work", `{}`)
	f.start(t, "globex", "shared-1", "work", `{}`)
	f.waitState(t, "acme", "shared-1", stateCompleted)
	f.waitState(t, "globex", "shared-1", stateCompleted)

	if acmeOut, globexOut := f.output(t, "acme", "shared-1"), f.output(t, "globex", "shared-1"); string(acmeOut) != `{"tenant":"acme"}` || string(globexOut) != `{"tenant":"globex"}` {
		t.Fatalf("outputs %s and %s, want each run's own tenant", acmeOut, globexOut)
	}

	f.start(t, "acme", "review-1", "review", `{}`)
	waitOn(t, "the review run to await its gate", atGate)
	if status, err := f.rt.Describe(tenantCtx("globex"), runtime.RunRef{RunID: "review-1"}); codeOf(err) != "not_found" {
		t.Fatalf("Describe cross-tenant = (%+v, %v), want not_found", status, err)
	}
	if err := f.rt.Cancel(tenantCtx("globex"), runtime.RunRef{RunID: "review-1"}, "x"); codeOf(err) != "not_found" {
		t.Fatalf("Cancel cross-tenant = %v, want not_found", err)
	}
	runs, _, err := f.rt.List(tenantCtx("globex"), runtime.RunQuery{Flow: "review"})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("globex lists %d review runs, want 0", len(runs))
	}
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

func TestStartAfterShutdownIsUnavailable(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	sleeping := make(chan struct{})
	f.flow("timer", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		wait := dc.Sleep(time.Hour)
		close(sleeping)
		if err := wait.Get(nil); err != nil {
			return nil, err
		}
		return json.RawMessage(`{}`), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.rt.Serve(ctx) }()
	f.start(t, "acme", "r1", "timer", `{}`)
	waitOn(t, "the timer to register", sleeping)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(waitLimit):
		t.Fatal("Serve did not return")
	}
	if got := f.state(t, "acme", "r1"); got != stateRunning {
		t.Fatalf("in-flight run after shutdown = %s, want running", got)
	}
	_, err := f.rt.Start(tenantCtx("acme"), runtime.StartRequest{Flow: runtime.FlowRef{Name: "timer"}, RunID: "r2"})
	if codeOf(err) != "unavailable" {
		t.Fatalf("Start after shutdown = %v, want unavailable", err)
	}
}

func TestShutdownCancelsStepsBeforeReturning(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	entered := make(chan struct{})
	var delivered atomic.Bool
	var active atomic.Int32
	if err := f.rt.RegisterStep("render", func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		active.Add(1)
		defer active.Add(-1)
		close(entered)
		<-ctx.Done()
		delivered.Store(true)
		return nil, ctx.Err()
	}, runtime.StepDefaults{}); err != nil {
		t.Fatal(err)
	}
	f.flow("render", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		return nil, dc.Step("render", nil, runtime.StepOptions{}).Get(&out)
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = f.rt.Serve(ctx)
		close(done)
	}()
	f.start(t, "acme", "r1", "render", `{}`)
	waitOn(t, "the step to start", entered)
	cancel()
	waitOn(t, "the runtime to shut down", done)
	if !delivered.Load() {
		t.Fatal("Serve returned before the step observed cancellation")
	}
	if got := active.Load(); got != 0 {
		t.Fatalf("%d steps still executing, want 0", got)
	}
	if got := f.state(t, "acme", "r1"); got != stateRunning {
		t.Fatalf("run after shutdown = %s, want running", got)
	}
}

func TestRetryPolicies(t *testing.T) {
	t.Run("non-retryable error stops after one attempt", func(t *testing.T) {
		f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
		defer f.close(t)
		var attempts atomic.Int32
		if err := f.rt.RegisterStep("charge", func(context.Context, json.RawMessage) (json.RawMessage, error) {
			attempts.Add(1)
			return nil, core.NewError(core.CodeInvalidArgument, "bad", nil)
		}, runtime.StepDefaults{}); err != nil {
			t.Fatal(err)
		}
		f.flow("pay", payFlow)
		f.start(t, "acme", "r1", "pay", `{}`)
		f.waitState(t, "acme", "r1", stateFailed)
		if got := attempts.Load(); got != 1 {
			t.Fatalf("attempts = %d, want 1", got)
		}
	})

	t.Run("listed code stops retries even when marked retryable", func(t *testing.T) {
		f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
		defer f.close(t)
		var attempts atomic.Int32
		if err := f.rt.RegisterStep("charge", func(context.Context, json.RawMessage) (json.RawMessage, error) {
			attempts.Add(1)
			return nil, &core.Error{Code: core.CodeRateLimited, Message: "slow", Retryable: true}
		}, runtime.StepDefaults{}); err != nil {
			t.Fatal(err)
		}
		f.flow("pay", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
			var out json.RawMessage
			return nil, dc.Step("charge", nil, runtime.StepOptions{NonRetryable: []string{"rate_limited"}}).Get(&out)
		})
		f.start(t, "acme", "r1", "pay", `{}`)
		f.waitState(t, "acme", "r1", stateFailed)
		if got := attempts.Load(); got != 1 {
			t.Fatalf("attempts = %d, want 1", got)
		}
	})

	t.Run("attempt limit keeps the last error", func(t *testing.T) {
		f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
		defer f.close(t)
		var attempts atomic.Int32
		if err := f.rt.RegisterStep("charge", func(context.Context, json.RawMessage) (json.RawMessage, error) {
			n := attempts.Add(1)
			if n == 1 {
				return nil, &core.Error{Code: core.CodeRateLimited, Message: "slow", Retryable: true}
			}
			return nil, &core.Error{Code: core.CodeVendor, Message: "vendor", Retryable: true}
		}, runtime.StepDefaults{}); err != nil {
			t.Fatal(err)
		}
		stepErrs := make(chan error, 1)
		f.flow("pay", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
			var out json.RawMessage
			err := dc.Step("charge", nil, runtime.StepOptions{MaxAttempts: 2}).Get(&out)
			stepErrs <- err
			return nil, err
		})
		f.start(t, "acme", "r1", "pay", `{}`)
		f.waitState(t, "acme", "r1", stateFailed)
		if got := attempts.Load(); got != 2 {
			t.Fatalf("attempts = %d, want 2", got)
		}
		if err := <-stepErrs; codeOf(err) != "vendor_error" {
			t.Fatalf("step error = %v, want vendor_error", err)
		}
		if f.clock.Now() != time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC) {
			t.Fatal("a retry advanced the clock")
		}
	})

	t.Run("default limit is three", func(t *testing.T) {
		f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
		defer f.close(t)
		var attempts atomic.Int32
		if err := f.rt.RegisterStep("charge", func(context.Context, json.RawMessage) (json.RawMessage, error) {
			attempts.Add(1)
			return nil, &core.Error{Code: core.CodeVendor, Message: "vendor", Retryable: true}
		}, runtime.StepDefaults{}); err != nil {
			t.Fatal(err)
		}
		f.flow("pay", payFlow)
		f.start(t, "acme", "r1", "pay", `{}`)
		f.waitState(t, "acme", "r1", stateFailed)
		if got := attempts.Load(); got != 3 {
			t.Fatalf("attempts = %d, want 3", got)
		}
	})

	t.Run("a successful attempt ends the step", func(t *testing.T) {
		f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
		defer f.close(t)
		var attempts atomic.Int32
		if err := f.rt.RegisterStep("charge", func(context.Context, json.RawMessage) (json.RawMessage, error) {
			if attempts.Add(1) == 1 {
				return nil, &core.Error{Code: core.CodeVendor, Message: "vendor", Retryable: true}
			}
			return json.RawMessage(`{"charged":true}`), nil
		}, runtime.StepDefaults{}); err != nil {
			t.Fatal(err)
		}
		f.flow("pay", payFlow)
		f.start(t, "acme", "r1", "pay", `{}`)
		f.waitState(t, "acme", "r1", stateCompleted)
		if got := attempts.Load(); got != 2 {
			t.Fatalf("attempts = %d, want 2", got)
		}
		if out := f.output(t, "acme", "r1"); string(out) != `{"charged":true}` {
			t.Fatalf("output %s, want {\"charged\":true}", out)
		}
	})
}

func payFlow(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
	var out json.RawMessage
	if err := dc.Step("charge", nil, runtime.StepOptions{}).Get(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func TestConcurrentStepsOverlap(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	var running, peak atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	for _, name := range []string{"a", "b", "c"} {
		if err := f.rt.RegisterStep(name, func(context.Context, json.RawMessage) (json.RawMessage, error) {
			now := running.Add(1)
			for {
				old := peak.Load()
				if now <= old || peak.CompareAndSwap(old, now) {
					break
				}
			}
			if now == 3 {
				once.Do(func() { close(release) })
			}
			<-release
			running.Add(-1)
			return json.RawMessage(`{}`), nil
		}, runtime.StepDefaults{}); err != nil {
			t.Fatal(err)
		}
	}
	f.flow("fanout", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		first := dc.Step("a", nil, runtime.StepOptions{})
		second := dc.Step("b", nil, runtime.StepOptions{})
		third := dc.Step("c", nil, runtime.StepOptions{})
		if _, err := dc.Select(first, second, third); err != nil {
			return nil, err
		}
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "r1", "fanout", `{}`)
	f.waitState(t, "acme", "r1", stateCompleted)
	if got := peak.Load(); got != 3 {
		t.Fatalf("peak concurrent steps = %d, want 3", got)
	}
}

func TestTenantFieldMustAgreeWithContext(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	var executions atomic.Int32
	if err := f.rt.RegisterStep("render", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		executions.Add(1)
		return json.RawMessage(`{"ok":true}`), nil
	}, runtime.StepDefaults{}); err != nil {
		t.Fatal(err)
	}
	f.flow("render", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		if err := dc.Step("render", nil, runtime.StepOptions{}).Get(&out); err != nil {
			return nil, err
		}
		return out, nil
	})

	_, err := f.rt.Start(tenantCtx("acme"), runtime.StartRequest{
		Flow:     runtime.FlowRef{Name: "render"},
		RunID:    "r1",
		TenantID: "globex",
	})
	if codeOf(err) != "invalid_argument" {
		t.Fatalf("mismatched Start = %v, want invalid_argument", err)
	}
	if got := executions.Load(); got != 0 {
		t.Fatalf("step executed %d times, want 0", got)
	}
	if _, err := f.rt.Describe(tenantCtx("acme"), runtime.RunRef{RunID: "r1"}); codeOf(err) != "not_found" {
		t.Fatalf("acme describes r1: %v, want not_found", err)
	}
	if _, err := f.rt.Describe(tenantCtx("globex"), runtime.RunRef{RunID: "r1"}); codeOf(err) != "not_found" {
		t.Fatalf("globex describes r1: %v, want not_found", err)
	}

	ref, err := f.rt.Start(tenantCtx("acme"), runtime.StartRequest{Flow: runtime.FlowRef{Name: "render"}, RunID: "r2"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if ref.TenantID != "acme" {
		t.Fatalf("ref tenant %q, want acme", ref.TenantID)
	}
	f.waitState(t, "acme", "r2", stateCompleted)
	if _, err := f.rt.Describe(context.Background(), runtime.RunRef{RunID: "r2"}); codeOf(err) != "invalid_argument" {
		t.Fatalf("Describe without tenant = %v, want invalid_argument", err)
	}
	if _, err := f.rt.Describe(tenantCtx("acme"), runtime.RunRef{RunID: "r2", TenantID: "globex"}); codeOf(err) != "invalid_argument" {
		t.Fatalf("Describe with a foreign ref tenant = %v, want invalid_argument", err)
	}
	if _, _, err := f.rt.List(context.Background(), runtime.RunQuery{}); codeOf(err) != "invalid_argument" {
		t.Fatalf("List without tenant = %v, want invalid_argument", err)
	}
}

func TestAwaitReturnsTheGateTheJournalAlreadyHolds(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	atGate := make(chan struct{})
	f.flow("waiting", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		close(atGate)
		return nil, dc.Await(runtime.GateSpec{Name: "approve"}).Get(nil)
	})
	ref := f.start(t, "acme", "r1", "waiting", `{}`)
	waitOn(t, "the run to await the gate", atGate)
	if err := f.rt.Resolve(tenantCtx("acme"), ref, "approve", json.RawMessage(`{"approved":true}`)); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, "acme", "r1", stateCompleted)

	// A replayed wait reads the recorded payload instead of waiting again.
	replayed := f.rt.lookup("acme", "r1")
	if replayed == nil {
		t.Fatal("run not found")
	}
	future := (&durableContext{run: replayed}).Await(runtime.GateSpec{Name: "approve"})
	var payload json.RawMessage
	if err := future.Get(&payload); err != nil {
		t.Fatalf("replayed Await: %v", err)
	}
	if string(payload) != `{"approved":true}` {
		t.Fatalf("replayed payload %s, want {\"approved\":true}", payload)
	}
}

func TestConcurrentGateResolutionHasOneWinner(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	atGate := make(chan struct{})
	f.flow("waiting", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		wait := dc.Await(runtime.GateSpec{Name: "approve"})
		close(atGate)
		var payload json.RawMessage
		if err := wait.Get(&payload); err != nil {
			return nil, err
		}
		return payload, nil
	})
	ref := f.start(t, "acme", "r1", "waiting", `{}`)
	waitOn(t, "the run to await the gate", atGate)

	const resolvers = 8
	errs := make(chan error, resolvers)
	var wg sync.WaitGroup
	for i := 0; i < resolvers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- f.rt.Resolve(tenantCtx("acme"), ref, "approve", json.RawMessage(fmt.Sprintf(`{"n":%d}`, i)))
		}(i)
	}
	wg.Wait()
	close(errs)
	winners, conflicts := 0, 0
	for err := range errs {
		switch codeOf(err) {
		case "":
			winners++
		case "conflict":
			conflicts++
		default:
			t.Fatalf("Resolve returned %v, want nil or conflict", err)
		}
	}
	if winners != 1 || conflicts != resolvers-1 {
		t.Fatalf("winners %d, conflicts %d; want 1 and %d", winners, conflicts, resolvers-1)
	}
	f.waitState(t, "acme", "r1", stateCompleted)
}

func TestStepJournalRecordsFailuresForReplay(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	var executions atomic.Int32
	if err := f.rt.RegisterStep("charge", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		executions.Add(1)
		return nil, core.NewError(core.CodeInvalidArgument, "bad", nil)
	}, runtime.StepDefaults{}); err != nil {
		t.Fatal(err)
	}
	stepErrs := make(chan error, 2)
	f.flow("pay", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		first := dc.Step("charge", nil, runtime.StepOptions{}).Get(&out)
		second := dc.Step("charge", nil, runtime.StepOptions{}).Get(&out)
		stepErrs <- first
		stepErrs <- second
		return nil, first
	})
	f.start(t, "acme", "r1", "pay", `{}`)
	f.waitState(t, "acme", "r1", stateFailed)
	if got := executions.Load(); got != 1 {
		t.Fatalf("step executed %d times, want 1", got)
	}
	first, second := <-stepErrs, <-stepErrs
	if codeOf(first) != "invalid_argument" || codeOf(second) != "invalid_argument" {
		t.Fatalf("recorded errors %v and %v, want both invalid_argument", first, second)
	}
}

func TestDurableContextHelpers(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	f.flow("helpers", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		first := dc.NewID("span")
		second := dc.NewID("span")
		sourceOne := dc.Rand("shuffle")
		sourceTwo := dc.Rand("shuffle")
		if sourceOne != sourceTwo {
			return nil, fmt.Errorf("Rand(key) did not return the same source")
		}
		if err := dc.Emit(runtime.ProgressEvent{Type: "progress", Data: json.RawMessage(`{"n":1}`)}); err != nil {
			return nil, err
		}
		if err := dc.Checkpoint(runtime.SnapshotRef{BlobKey: "b1", Seq: 1}); err != nil {
			return nil, err
		}
		if caps := dc.Capabilities(); caps != f.rt.Capabilities() {
			return nil, fmt.Errorf("context capabilities %+v differ from the runtime's", caps)
		}
		return json.RawMessage(fmt.Sprintf(`{"first":%q,"second":%q}`, first, second)), nil
	})
	ref := f.start(t, "acme", "r1", "helpers", `{}`)
	f.waitState(t, "acme", "r1", stateCompleted)
	if out := f.output(t, "acme", "r1"); string(out) != `{"first":"span_1","second":"span_2"}` {
		t.Fatalf("ids %s, want span_1 then span_2", out)
	}
	target := f.rt.lookup(core.TenantID("acme"), "r1")
	if target == nil {
		t.Fatal("run not found")
	}
	target.mu.Lock()
	outbox := append([]runtime.ProgressEvent(nil), target.outbox...)
	target.mu.Unlock()
	if len(outbox) != 1 || outbox[0].Type != "progress" {
		t.Fatalf("outbox %+v, want one progress event", outbox)
	}
	if _, err := f.rt.Describe(tenantCtx("acme"), ref); err != nil {
		t.Fatal(err)
	}
}

func TestFlowHostPanicFailsRunWithoutKillingProcess(t *testing.T) {
	const leak = "panic-value-must-not-leak"
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	f.flow("panic", func(_ runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		panic(leak)
	})
	f.start(t, "acme", "r1", "panic", `{}`)
	f.waitState(t, "acme", "r1", stateFailed)
	target := f.rt.lookup(core.TenantID("acme"), "r1")
	if target == nil {
		t.Fatal("run not found")
	}
	target.mu.Lock()
	runErr := target.err
	target.mu.Unlock()
	if codeOf(runErr) != "internal" {
		t.Fatalf("run error code %q, want internal", codeOf(runErr))
	}
	var platform *core.Error
	if !errors.As(runErr, &platform) {
		t.Fatalf("run error %v is not a *core.Error", runErr)
	}
	if platform.Message != "local: the flow host panicked" {
		t.Fatalf("message %q, want fixed host panic message", platform.Message)
	}
	if strings.Contains(platform.Message, leak) {
		t.Fatal("panic value leaked into the platform message")
	}
	status, err := f.rt.Describe(tenantCtx("acme"), runtime.RunRef{RunID: "r1"})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if status.State != stateFailed {
		t.Fatalf("Describe state %q, want failed", status.State)
	}
	f.close(t)
}

func TestCapabilities(t *testing.T) {
	caps := New(Options{}).Capabilities()
	want := runtime.Caps{SyncGateUpdates: true}
	if caps != want {
		t.Fatalf("caps %+v, want %+v", caps, want)
	}
}

func TestErrorsArePlatformErrors(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	_, err := f.rt.Describe(tenantCtx("acme"), runtime.RunRef{RunID: "missing"})
	var platform *core.Error
	if !errors.As(err, &platform) || platform.Code != core.CodeNotFound {
		t.Fatalf("Describe missing = %v, want a not_found *core.Error", err)
	}
	if msg := platform.Error(); !strings.Contains(msg, "missing") {
		t.Fatalf("error %q does not name the run", msg)
	}
}
