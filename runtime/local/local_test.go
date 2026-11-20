package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
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
	result := recvWithin(t, got, "Select result")
	if result.err != nil || result.index != 1 {
		t.Fatalf("Select = (%d, %v), want (1, nil)", result.index, result.err)
	}
	f.waitState(t, "acme", "r1", stateCompleted)
}

func TestSelectIsDeterministicOverReadySubsets(t *testing.T) {
	rt, err := New(Options{Clock: coretest.NewFakeClock(time.Unix(1_700_000_000, 0)), IDs: &coretest.SeqIds{}})
	if err != nil {
		t.Fatal(err)
	}
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
	rt, err := New(Options{Clock: coretest.NewFakeClock(time.Unix(1_700_000_000, 0)), IDs: &coretest.SeqIds{}})
	if err != nil {
		t.Fatal(err)
	}
	r := newRun(rt, "acme", "r1", runtime.FlowRef{Name: "x"}, nil, context.Background())
	first, second := newFuture(r), newFuture(r)

	second.complete(nil, nil)
	first.complete(nil, nil)

	if index := r.firstCompleted([]runtime.Future{first, second}); index != 1 {
		t.Fatalf("firstCompleted = %d, want 1 (earliest seq)", index)
	}

	third, fourth := newFuture(r), newFuture(r)
	seq := r.seqs.Add(1)
	third.completeWithSeq(seq, nil, nil, true)
	fourth.completeWithSeq(seq, nil, nil, true)
	if index := r.firstCompleted([]runtime.Future{third, fourth}); index != 0 {
		t.Fatalf("equal seq firstCompleted = %d, want 0", index)
	}
}

// hookFuture runs hook on its first Ready() call and always reports not ready.
type hookFuture struct {
	once sync.Once
	hook func()
}

func (h *hookFuture) Get(any) error { return fmt.Errorf("hookFuture: unexpected Get") }
func (h *hookFuture) Ready() bool {
	h.once.Do(h.hook)
	return false
}

func TestSelectNoLostWakeCompletingAfterReadyScan(t *testing.T) {
	rt, err := New(Options{Clock: coretest.NewFakeClock(time.Unix(1_700_000_000, 0)), IDs: &coretest.SeqIds{}})
	if err != nil {
		t.Fatal(err)
	}
	r := newRun(rt, "acme", "r1", runtime.FlowRef{Name: "x"}, nil, context.Background())
	t.Cleanup(r.cancel)
	dc := &durableContext{run: r}
	f0 := newFuture(r)
	seq := r.seqs.Add(1)
	hook := &hookFuture{hook: func() {
		f0.completeWithSeq(seq, nil, nil, true)
	}}
	got := make(chan int, 1)
	go func() {
		index, selErr := dc.Select(f0, hook)
		if selErr != nil {
			t.Errorf("Select: %v", selErr)
			got <- -1
			return
		}
		got <- index
	}()
	if index := recvWithin(t, got, "Select result"); index != 0 {
		t.Fatalf("Select index %d, want 0", index)
	}
}

func TestSelectNoLostWakeFireDueBatchWindow(t *testing.T) {
	rt, err := New(Options{Clock: coretest.NewFakeClock(time.Unix(1_700_000_000, 0)), IDs: &coretest.SeqIds{}})
	if err != nil {
		t.Fatal(err)
	}
	r := newRun(rt, "acme", "r1", runtime.FlowRef{Name: "x"}, nil, context.Background())
	t.Cleanup(r.cancel)
	dc := &durableContext{run: r}
	f0 := newFuture(r)
	batchSeq := r.seqs.Add(1)
	hook := &hookFuture{hook: func() {
		f0.completeWithSeq(batchSeq, nil, nil, true)
	}}
	got := make(chan int, 1)
	go func() {
		index, selErr := dc.Select(f0, hook)
		if selErr != nil {
			t.Errorf("Select: %v", selErr)
			got <- -1
			return
		}
		got <- index
	}()
	if index := recvWithin(t, got, "Select result"); index != 0 {
		t.Fatalf("Select index %d, want 0", index)
	}
}

func TestFireDueBatchSameSeq(t *testing.T) {
	rt, err := New(Options{Clock: coretest.NewFakeClock(time.Unix(1_700_000_000, 0)), IDs: &coretest.SeqIds{}})
	if err != nil {
		t.Fatal(err)
	}
	r := newRun(rt, "acme", "r1", runtime.FlowRef{Name: "x"}, nil, context.Background())
	first, second := newFuture(r), newFuture(r)
	deadline := rt.clock.Now()
	timers := []*runTimer{
		{deadline: deadline, future: first},
		{deadline: deadline, future: second},
	}
	r.mu.Lock()
	r.timers = timers
	r.mu.Unlock()
	r.fireDue(timers[1])

	s0, ok0 := first.completedSeq()
	s1, ok1 := second.completedSeq()
	if !ok0 || !ok1 {
		t.Fatal("futures not complete")
	}
	if s0 != s1 {
		t.Fatalf("seq %d and %d, want the same batch seq", s0, s1)
	}
	index, err := (&durableContext{run: r}).Select(first, second)
	if err != nil {
		t.Fatal(err)
	}
	if index != 0 {
		t.Fatalf("Select index %d, want 0", index)
	}
}

func testSelectSameAdvanceOrder(t *testing.T, longFirst bool) {
	t.Helper()
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	got := make(chan int, 1)
	started := make(chan struct{})
	f.flow("select", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var first, second runtime.Future
		if longFirst {
			first = dc.Sleep(2 * time.Hour)
			second = dc.Sleep(time.Hour)
		} else {
			second = dc.Sleep(2 * time.Hour)
			first = dc.Sleep(time.Hour)
		}
		close(started)
		index, err := dc.Select(first, second)
		if err != nil {
			return nil, err
		}
		got <- index
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "r1", "select", `{}`)
	waitOn(t, "timers", started)
	f.clock.Advance(2 * time.Hour)
	index := recvWithin(t, got, "Select result")
	if index != 0 {
		t.Fatalf("Select index %d, want 0 (lowest when both timers fire together)", index)
	}
}

func TestSelectSameAdvanceReturnsLowestIndex(t *testing.T) {
	t.Run("long_first", func(t *testing.T) { testSelectSameAdvanceOrder(t, true) })
	t.Run("long_second", func(t *testing.T) { testSelectSameAdvanceOrder(t, false) })
}

func TestSelectSameAdvanceLowestIndexUnderSchedulerNoise(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for trial := 0; trial < 200; trial++ {
		f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
		got := make(chan int, 1)
		started := make(chan struct{})
		f.flow("select", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
			first := dc.Sleep(2 * time.Hour)
			second := dc.Sleep(time.Hour)
			close(started)
			index, err := dc.Select(first, second)
			if err != nil {
				return nil, err
			}
			got <- index
			return json.RawMessage(`{}`), nil
		})
		f.start(t, "acme", "r1", "select", `{}`)
		waitOn(t, "timers", started)
		f.clock.Advance(2 * time.Hour)
		for i := 0; i < rng.IntN(20)+1; i++ {
			goruntime.Gosched()
		}
		index := recvWithin(t, got, "Select result")
		f.close(t)
		if index != 0 {
			t.Fatalf("trial %d: Select index %d, want 0", trial, index)
		}
	}
}

func TestSelectReadyAtCallBeatsLaterReady(t *testing.T) {
	rt, err := New(Options{Clock: coretest.NewFakeClock(time.Unix(1_700_000_000, 0)), IDs: &coretest.SeqIds{}})
	if err != nil {
		t.Fatal(err)
	}
	r := newRun(rt, "acme", "r1", runtime.FlowRef{Name: "x"}, nil, context.Background())
	late, early := newFuture(r), newFuture(r)
	early.complete(nil, nil)
	index, err := (&durableContext{run: r}).Select(late, early)
	if err != nil {
		t.Fatal(err)
	}
	if index != 1 {
		t.Fatalf("Select index %d, want 1 (higher index ready at call)", index)
	}
}

func TestSelectReadyAtCallUsesLowestIndex(t *testing.T) {
	rt, err := New(Options{Clock: coretest.NewFakeClock(time.Unix(1_700_000_000, 0)), IDs: &coretest.SeqIds{}})
	if err != nil {
		t.Fatal(err)
	}
	r := newRun(rt, "acme", "r1", runtime.FlowRef{Name: "x"}, nil, context.Background())
	first, second := newFuture(r), newFuture(r)
	first.complete(nil, nil)
	second.complete(nil, nil)
	index, err := (&durableContext{run: r}).Select(first, second)
	if err != nil {
		t.Fatal(err)
	}
	if index != 0 {
		t.Fatalf("Select index %d, want 0", index)
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

func TestGateTimeoutHelperStopsAfterEarlyResolve(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	atGate := make(chan struct{})
	release := make(chan struct{})
	f.flow("waiting", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		wait := dc.Await(runtime.GateSpec{Name: "approve", Timeout: time.Hour})
		close(atGate)
		var payload json.RawMessage
		if err := wait.Get(&payload); err != nil {
			return nil, err
		}
		<-release
		return payload, nil
	})
	f.start(t, "acme", "r1", "waiting", `{}`)
	waitOn(t, "the run to await the gate", atGate)
	target := f.rt.lookup(core.TenantID("acme"), "r1")
	if target == nil {
		t.Fatal("run not found")
	}
	if got := target.gateTimeouts.Load(); got != 1 {
		t.Fatalf("gate timeout helpers = %d, want 1", got)
	}
	if err := f.rt.Resolve(tenantCtx("acme"), runtime.RunRef{RunID: "r1"}, "approve", json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	waitGateTimeouts(t, target, 0)
	close(release)
	f.waitState(t, "acme", "r1", stateCompleted)
	f.clock.Advance(time.Hour)
	if got := f.state(t, "acme", "r1"); got != stateCompleted {
		t.Fatalf("state after 1h advance = %s, want completed", got)
	}
	if out := f.output(t, "acme", "r1"); string(out) != `{"ok":true}` {
		t.Fatalf("output %s, want resolve payload after clock advance", out)
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
			err := recvWithin(t, failed, "cancelled wait error")
			if codeOf(err) != "cancelled" {
				t.Fatalf("wait error = %v, want cancelled", err)
			}
		})
	}
}

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
	err := recvWithin(t, done, "Serve return")
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if got := f.state(t, "acme", "r1"); got != stateRunning {
		t.Fatalf("in-flight run after shutdown = %s, want running", got)
	}
	_, startErr := f.rt.Start(tenantCtx("acme"), runtime.StartRequest{Flow: runtime.FlowRef{Name: "timer"}, RunID: "r2"})
	if codeOf(startErr) != "unavailable" {
		t.Fatalf("Start after shutdown = %v, want unavailable", startErr)
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

type clockNowOnly struct{}

func (clockNowOnly) Now() time.Time { return time.Unix(1_700_000_000, 0).UTC() }

func TestNewRejectsClockWithoutAfter(t *testing.T) {
	_, err := New(Options{Clock: clockNowOnly{}})
	if codeOf(err) != "invalid_argument" {
		t.Fatalf("New = %v, want invalid_argument", err)
	}
	var platform *core.Error
	if !errors.As(err, &platform) {
		t.Fatalf("New = %v, want *core.Error", err)
	}
	if !strings.Contains(platform.Message, "cannot schedule timers") {
		t.Fatalf("message %q, want substring %q", platform.Message, "cannot schedule timers")
	}
}

func TestCapabilities(t *testing.T) {
	rt, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	caps := rt.Capabilities()
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

func TestListPaging(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	f.flow("noop", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	for i := 1; i <= 5; i++ {
		f.start(t, "acme", fmt.Sprintf("r%d", i), "noop", `{}`)
		f.waitState(t, "acme", fmt.Sprintf("r%d", i), stateCompleted)
	}
	ctx := tenantCtx("acme")
	var seen []string
	cursor := ""
	for page := 0; page < 3; page++ {
		pageRuns, next, err := f.rt.List(ctx, runtime.RunQuery{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatalf("page %d: List: %v", page, err)
		}
		for _, st := range pageRuns {
			seen = append(seen, st.Ref.RunID)
		}
		cursor = next
		if page < 2 && cursor == "" {
			t.Fatalf("page %d: expected a next cursor", page)
		}
		if page == 2 && cursor != "" {
			t.Fatalf("final page cursor %q, want empty", cursor)
		}
	}
	if len(seen) != 5 {
		t.Fatalf("listed %d runs, want 5", len(seen))
	}
	if len(dedup(seen)) != 5 {
		t.Fatalf("duplicate ids in %v", seen)
	}
}

func dedup(ids []string) []string {
	set := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := set[id]; ok {
			continue
		}
		set[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func TestListPagingRespectsFilters(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	f.flow("a", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	f.flow("b", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "r1", "a", `{}`)
	f.waitState(t, "acme", "r1", stateCompleted)
	f.start(t, "acme", "r2", "b", `{}`)
	f.waitState(t, "acme", "r2", stateCompleted)
	f.start(t, "acme", "r3", "a", `{}`)
	f.waitState(t, "acme", "r3", stateCompleted)

	ctx := tenantCtx("acme")
	page, next, err := f.rt.List(ctx, runtime.RunQuery{Flow: "a", Limit: 1})
	if err != nil || len(page) != 1 || page[0].Ref.RunID != "r1" || next == "" {
		t.Fatalf("first page = %#v, %q, %v", page, next, err)
	}
	page, next, err = f.rt.List(ctx, runtime.RunQuery{Flow: "a", Limit: 1, Cursor: next})
	if err != nil || len(page) != 1 || page[0].Ref.RunID != "r3" || next != "" {
		t.Fatalf("second page = %#v, %q, %v", page, next, err)
	}
}

func TestListInvalidCursor(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	f.flow("noop", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "r1", "noop", `{}`)
	f.waitState(t, "acme", "r1", stateCompleted)
	ctx := tenantCtx("acme")
	if _, _, err := f.rt.List(ctx, runtime.RunQuery{Cursor: "not-a-token"}); codeOf(err) != "invalid_argument" {
		t.Fatalf("bad cursor = %v, want invalid_argument", err)
	}
	pastEnd := listCursorPositionForTests("acme", 1)
	if _, _, err := f.rt.List(ctx, runtime.RunQuery{Cursor: pastEnd}); codeOf(err) != "invalid_argument" {
		t.Fatalf("out of range cursor = %v, want invalid_argument", err)
	}
}

func TestListLimitLargerThanList(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	f.flow("noop", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "r1", "noop", `{}`)
	f.waitState(t, "acme", "r1", stateCompleted)
	page, next, err := f.rt.List(tenantCtx("acme"), runtime.RunQuery{Limit: 100})
	if err != nil || len(page) != 1 || next != "" {
		t.Fatalf("List = %#v, %q, %v", page, next, err)
	}
}

func TestListCursorDoesNotWidenTenantVisibility(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	f.flow("noop", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "a1", "noop", `{}`)
	f.waitState(t, "acme", "a1", stateCompleted)
	f.start(t, "globex", "g1", "noop", `{}`)
	f.waitState(t, "globex", "g1", stateCompleted)
	f.start(t, "acme", "a2", "noop", `{}`)
	f.waitState(t, "acme", "a2", stateCompleted)

	page, next, err := f.rt.List(tenantCtx("acme"), runtime.RunQuery{Limit: 1})
	if err != nil || len(page) != 1 || page[0].Ref.RunID != "a1" {
		t.Fatalf("first acme page = %#v, %v", page, err)
	}
	page, next, err = f.rt.List(tenantCtx("acme"), runtime.RunQuery{Limit: 1, Cursor: next})
	if err != nil || len(page) != 1 || page[0].Ref.RunID != "a2" || next != "" {
		t.Fatalf("second acme page = %#v, %q, %v", page, next, err)
	}
}

func TestStartDuplicateRunIDSameTenant(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	f.flow("noop", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "r1", "noop", `{}`)
	_, err := f.rt.Start(tenantCtx("acme"), runtime.StartRequest{
		Flow:  runtime.FlowRef{Name: "noop"},
		RunID: "r1",
		Input: json.RawMessage(`{}`),
	})
	if codeOf(err) != "conflict" {
		t.Fatalf("duplicate Start = %v, want conflict", err)
	}
}

func TestStartDuplicateRunIDDifferentTenantsAllowed(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	f.flow("noop", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "shared", "noop", `{}`)
	f.start(t, "globex", "shared", "noop", `{}`)
	f.waitState(t, "acme", "shared", stateCompleted)
	f.waitState(t, "globex", "shared", stateCompleted)
}

func TestStartIdempotencyKeySameTenant(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	var starts atomic.Int32
	f.flow("noop", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		starts.Add(1)
		return json.RawMessage(`{}`), nil
	})
	req := runtime.StartRequest{
		Flow:           runtime.FlowRef{Name: "noop"},
		RunID:          "r1",
		Input:          json.RawMessage(`{}`),
		IdempotencyKey: "idem-1",
	}
	ref1, err := f.rt.Start(tenantCtx("acme"), req)
	if err != nil {
		t.Fatal(err)
	}
	ref2, err := f.rt.Start(tenantCtx("acme"), req)
	if err != nil {
		t.Fatal(err)
	}
	f.waitState(t, "acme", "r1", stateCompleted)
	if ref1 != ref2 || starts.Load() != 1 {
		t.Fatalf("refs %#v %#v starts %d, want one run", ref1, ref2, starts.Load())
	}
}

func TestStartIdempotencyKeyDifferentTenants(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	var starts atomic.Int32
	f.flow("noop", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		starts.Add(1)
		return json.RawMessage(`{}`), nil
	})
	key := "shared-idem"
	for _, tenant := range []string{"acme", "globex"} {
		if _, err := f.rt.Start(tenantCtx(tenant), runtime.StartRequest{
			Flow:           runtime.FlowRef{Name: "noop"},
			RunID:          tenant + "-r",
			Input:          json.RawMessage(`{}`),
			IdempotencyKey: key,
		}); err != nil {
			t.Fatalf("Start(%s): %v", tenant, err)
		}
	}
	f.waitState(t, "acme", "acme-r", stateCompleted)
	f.waitState(t, "globex", "globex-r", stateCompleted)
	if starts.Load() != 2 {
		t.Fatalf("starts %d, want 2", starts.Load())
	}
}

func TestStepPanicFailsWithoutKillingProcess(t *testing.T) {
	const leak = "step-panic-must-not-leak"
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	var executions atomic.Int32
	if err := f.rt.RegisterStep("boom", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		executions.Add(1)
		panic(leak)
	}, runtime.StepDefaults{}); err != nil {
		t.Fatal(err)
	}
	f.flow("step-panic", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		if err := dc.Step("boom", nil, runtime.StepOptions{}).Get(nil); err == nil {
			return nil, fmt.Errorf("expected step error")
		}
		return json.RawMessage(`{"ok":true}`), nil
	})
	f.start(t, "acme", "r1", "step-panic", `{}`)
	f.waitState(t, "acme", "r1", stateCompleted)
	if executions.Load() != 1 {
		t.Fatalf("step ran %d times, want 1", executions.Load())
	}
	target := f.rt.lookup(core.TenantID("acme"), "r1")
	target.mu.Lock()
	runErr := target.err
	target.mu.Unlock()
	if runErr != nil {
		t.Fatalf("run err %v, want nil (flow handled step)", runErr)
	}
	if out := f.output(t, "acme", "r1"); string(out) != `{"ok":true}` {
		t.Fatalf("output %s", out)
	}
	// step error is on the future, not exported via Describe — verify via a second flow call is overkill;
	// re-read the step future is gone; check panic message not in Describe.
	status, err := f.rt.Describe(tenantCtx("acme"), runtime.RunRef{RunID: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(status.State+status.Ref.RunID, leak) {
		t.Fatal("panic value leaked into Describe")
	}
	f.close(t)
}

func TestStepPanicWithErrorValue(t *testing.T) {
	sentinel := core.NewError(core.CodeUnavailable, "boom-step", nil)
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	var got atomic.Value
	if err := f.rt.RegisterStep("panic-err", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		panic(sentinel)
	}, runtime.StepDefaults{}); err != nil {
		t.Fatal(err)
	}
	f.flow("catch", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		err := dc.Step("panic-err", nil, runtime.StepOptions{}).Get(nil)
		got.Store(err)
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "r1", "catch", `{}`)
	f.waitState(t, "acme", "r1", stateCompleted)
	err, _ := got.Load().(error)
	var platform *core.Error
	if !errors.As(err, &platform) || platform.Message != "local: the step panicked" {
		t.Fatalf("step error %v, want internal step panic wrapper", err)
	}
	if !errors.Is(platform.Cause, sentinel) && platform.Cause.Error() != sentinel.Error() {
		t.Fatalf("cause %v, want %v", platform.Cause, sentinel)
	}
	if strings.Contains(platform.Message, "boom-step") {
		t.Fatal("cause leaked into the platform message")
	}
}

func TestStepPanicThenShutdown(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	if err := f.rt.RegisterStep("panic-step", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		panic("x")
	}, runtime.StepDefaults{}); err != nil {
		t.Fatal(err)
	}
	f.flow("go", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		_ = dc.Step("panic-step", nil, runtime.StepOptions{}).Get(nil)
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "r1", "go", `{}`)
	f.waitState(t, "acme", "r1", stateCompleted)
	f.close(t)
}
