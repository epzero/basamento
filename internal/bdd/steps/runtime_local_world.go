package steps

import (
	"context"
	"encoding/json"
	"fmt"
	goruntime "runtime"
	"sync"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/runtime"
	"github.com/epzero/basamento/runtime/local"
)

const localSpinBudget = 100_000

func recvWithin[T any](ch <-chan T, budget int, what string) (T, error) {
	var zero T
	for i := 0; i < budget; i++ {
		select {
		case v := <-ch:
			return v, nil
		default:
			goruntime.Gosched()
		}
	}
	return zero, fmt.Errorf("timed out receiving %s after %d yields", what, budget)
}

// localRunKey identifies a run in the step world.
type localRunKey struct {
	tenant core.TenantID
	id     string
}

// localFlow is the scripted flow code a scenario registers under a name. The
// step library plays the composition root for the flow host; the runtime under
// test is the real runtime/local.
type localFlow func(e *localExec, dc runtime.DurableContext, in json.RawMessage) (json.RawMessage, error)

// localExec observes one invocation of one flow, so a step can wait on the
// exact event a scenario talks about (a gate awaited, a timer registered, a
// select answered) without sleeping.
type localExec struct {
	name string

	mu      sync.Mutex
	changed chan struct{}

	done   bool
	result json.RawMessage
	err    error

	waiting bool
	atGate  string
	waitErr error
	timers  int

	selectIndex   int
	selectDone    bool
	selectPayload json.RawMessage

	first, second json.RawMessage
	childOut      json.RawMessage
	stepErr       error
}

func newLocalExec(name string) *localExec {
	return &localExec{name: name, changed: make(chan struct{})}
}

func (e *localExec) update(fn func()) {
	e.mu.Lock()
	fn()
	close(e.changed)
	e.changed = make(chan struct{})
	e.mu.Unlock()
}

// wait blocks until ok holds; ok runs under the exec's lock.
func (e *localExec) wait(what string, ok func() bool) error {
	for i := 0; i < localSpinBudget; i++ {
		e.mu.Lock()
		done := ok()
		changed := e.changed
		e.mu.Unlock()
		if done {
			return nil
		}
		select {
		case <-changed:
		default:
			goruntime.Gosched()
		}
	}
	return fmt.Errorf("timed out waiting for %s after %d yields", what, localSpinBudget)
}

func (e *localExec) finish(out json.RawMessage, err error) {
	e.update(func() {
		e.done = true
		e.result = append(json.RawMessage(nil), out...)
		e.err = err
	})
}

// localStep is the behavior of one scripted step name.
type localStep struct {
	defaultResult json.RawMessage
	defaultErr    *core.Error
	results       map[int]json.RawMessage
	errors        map[int]*core.Error
	tenantResults map[string]json.RawMessage

	barrier *localBarrier
	block   bool
}

func (s *localStep) result(execution int, tenant string) (json.RawMessage, error) {
	if s == nil {
		return json.RawMessage(`{}`), nil
	}
	if s.tenantResults != nil {
		if out, ok := s.tenantResults[tenant]; ok {
			return out, nil
		}
	}
	if s.errors != nil {
		if err := s.errors[execution]; err != nil {
			return nil, err
		}
	}
	if s.results != nil {
		if out, ok := s.results[execution]; ok {
			return out, nil
		}
	}
	if s.defaultErr != nil {
		return nil, s.defaultErr
	}
	if s.defaultResult != nil {
		return s.defaultResult, nil
	}
	return json.RawMessage(`{}`), nil
}

// localBarrier releases the scripted steps of a fan-out only when all of them
// are executing at the same time (REQ-RT-019).
type localBarrier struct {
	mu      sync.Mutex
	want    int
	arrived int
	changed chan struct{}
}

func newLocalBarrier(want int) *localBarrier {
	return &localBarrier{want: want, changed: make(chan struct{})}
}

func (b *localBarrier) enter() {
	b.mu.Lock()
	b.arrived++
	close(b.changed)
	b.changed = make(chan struct{})
	b.mu.Unlock()
}

func (b *localBarrier) wait(ctx context.Context) error {
	for {
		b.mu.Lock()
		done := b.arrived >= b.want
		changed := b.changed
		b.mu.Unlock()
		if done {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// localWorld drives runtime/local from the scenarios in local.feature.
type localWorld struct {
	mu sync.Mutex

	rt         *local.Runtime
	retired    []*local.Runtime
	registered map[string]bool

	flows map[string]localFlow
	execs map[string][]*localExec
	byRun map[localRunKey]*localExec

	steps            map[string]*localStep
	stepOptions      map[string]runtime.StepOptions
	executions       map[string]int
	stepStarted      map[string]chan struct{}
	stepWaits        map[string]string
	stepFutureDone   map[string]chan struct{}
	steppedCancelled map[string]bool
	activeSteps      int
	peakSteps        int

	current     runtime.RunRef
	currentExec *localExec
	outputs     map[localRunKey]json.RawMessage

	lastErr       error
	lastStart     runtime.RunRef
	lastStatus    runtime.RunStatus
	lastList      []runtime.RunStatus
	pendingRef    runtime.RunRef
	serveCancel   context.CancelFunc
	serveDone     chan error
	shutdownError error
	clockBefore   time.Time
}

func newLocalWorld() *localWorld {
	return &localWorld{
		registered:       map[string]bool{},
		flows:            map[string]localFlow{},
		execs:            map[string][]*localExec{},
		byRun:            map[localRunKey]*localExec{},
		steps:            map[string]*localStep{},
		stepOptions:      map[string]runtime.StepOptions{},
		executions:       map[string]int{},
		stepStarted:      map[string]chan struct{}{},
		stepWaits:        map[string]string{},
		stepFutureDone:   map[string]chan struct{}{},
		steppedCancelled: map[string]bool{},
		outputs:          map[localRunKey]json.RawMessage{},
	}
}

func localStepWorld(ctx context.Context) *localWorld { return world(ctx).Local }

func (w *localWorld) ctx(tenant string) context.Context {
	if tenant == "" {
		return context.Background()
	}
	return core.WithTenant(context.Background(), core.TenantID(tenant))
}

// runtime returns the scenario's runtime, creating it on first use with the
// scenario clock (a coretest.FakeClock, which also drives timers).
func (w *localWorld) runtime(ctx context.Context) *local.Runtime {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.runtimeLocked(ctx)
}

func (w *localWorld) runtimeLocked(ctx context.Context) *local.Runtime {
	if w.rt != nil {
		return w.rt
	}
	if w.registered == nil {
		w.registered = map[string]bool{}
	}
	rt, err := local.New(local.Options{Clock: world(ctx).Core.clock, IDs: &coretest.SeqIds{}})
	if err != nil {
		panic(err)
	}
	if err := rt.RegisterFlowHost(w.host); err != nil {
		panic(err)
	}
	for name := range w.registered {
		_ = rt.RegisterStep(name, w.stepFunc(name), runtime.StepDefaults{})
	}
	w.rt = rt
	return rt
}

// newRuntime starts a fresh runtime, retiring the previous one: its runs are
// invisible to the new instance (REQ-RT-006).
func (w *localWorld) newRuntime(ctx context.Context) *local.Runtime {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.rt != nil {
		w.retired = append(w.retired, w.rt)
		w.rt = nil
	}
	return w.runtimeLocked(ctx)
}

// host is the flow host the composition root would register.
func (w *localWorld) host(dc runtime.DurableContext, ref runtime.FlowRef, in json.RawMessage) (json.RawMessage, error) {
	exec := newLocalExec(ref.Name)
	w.mu.Lock()
	builder := w.flows[ref.Name]
	w.execs[ref.Name] = append(w.execs[ref.Name], exec)
	w.mu.Unlock()
	if builder == nil {
		err := fmt.Errorf("bdd: no flow %q is scripted", ref.Name)
		exec.finish(nil, err)
		return nil, err
	}
	out, err := builder(exec, dc, in)
	exec.finish(out, err)
	return out, err
}

func (w *localWorld) setFlow(name string, flow localFlow) {
	w.mu.Lock()
	w.flows[name] = flow
	w.mu.Unlock()
}

func (w *localWorld) execCount(name string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.execs[name])
}

func (w *localWorld) execAt(name string, index int) *localExec {
	w.mu.Lock()
	defer w.mu.Unlock()
	if index < len(w.execs[name]) {
		return w.execs[name][index]
	}
	return nil
}

func (w *localWorld) execsOf(name string) []*localExec {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]*localExec(nil), w.execs[name]...)
}

// start starts a run and waits until its flow invocation exists, so the caller
// can wait on the events of that exact invocation.
func (w *localWorld) start(ctx context.Context, tenant, runID, flow string) (runtime.RunRef, *localExec, error) {
	rt := w.runtime(ctx)
	before := w.execCount(flow)
	ref, err := rt.Start(w.ctx(tenant), runtime.StartRequest{
		Flow:  runtime.FlowRef{Name: flow},
		RunID: runID,
	})
	if err != nil {
		return runtime.RunRef{}, nil, err
	}
	if err := w.spin("the run of "+flow+" to start", func() bool { return w.execCount(flow) > before }); err != nil {
		return ref, nil, err
	}
	exec := w.execAt(flow, before)
	w.mu.Lock()
	w.current = ref
	w.currentExec = exec
	w.byRun[localRunKey{tenant: core.TenantID(tenant), id: ref.RunID}] = exec
	w.mu.Unlock()
	return ref, exec, nil
}

// waitRunState waits for a run's observable state. It yields instead of
// sleeping; the deadline only turns a hang into a failing step.
func (w *localWorld) waitRunState(ctx context.Context, ref runtime.RunRef, want string) error {
	var last string
	err := w.spin("run "+ref.RunID+" to reach "+want, func() bool {
		status, err := w.runtime(ctx).Describe(w.ctx(string(ref.TenantID)), ref)
		if err != nil {
			return false
		}
		last = status.State
		return status.State == want
	})
	if err != nil {
		return fmt.Errorf("%w (still %q)", err, last)
	}
	return nil
}

// waitRunClosed waits until a run leaves "running", whatever its outcome.
func (w *localWorld) waitRunClosed(ctx context.Context, ref runtime.RunRef) error {
	var last string
	err := w.spin("run "+ref.RunID+" to close", func() bool {
		status, err := w.runtime(ctx).Describe(w.ctx(string(ref.TenantID)), ref)
		if err != nil {
			return false
		}
		last = status.State
		return status.State != "running"
	})
	if err != nil {
		return fmt.Errorf("%w (still %q)", err, last)
	}
	return nil
}

// spin polls a scenario fact, yielding the processor between checks.
func (w *localWorld) spin(what string, ok func() bool) error {
	for i := 0; i < localSpinBudget; i++ {
		if ok() {
			return nil
		}
		goruntime.Gosched()
	}
	return fmt.Errorf("timed out waiting for %s after %d yields", what, localSpinBudget)
}

func (w *localWorld) stateOf(ctx context.Context, tenant, id string) (string, error) {
	status, err := w.runtime(ctx).Describe(w.ctx(tenant), runtime.RunRef{RunID: id})
	if err != nil {
		return "", err
	}
	return status.State, nil
}

func (w *localWorld) recordOutput(ref runtime.RunRef, exec *localExec) {
	exec.mu.Lock()
	out := append(json.RawMessage(nil), exec.result...)
	exec.mu.Unlock()
	w.mu.Lock()
	w.outputs[localRunKey{tenant: ref.TenantID, id: ref.RunID}] = out
	w.mu.Unlock()
}

func (w *localWorld) output(ref runtime.RunRef) json.RawMessage {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.outputs[localRunKey{tenant: ref.TenantID, id: ref.RunID}]
}

func (w *localWorld) noteError(err error) { w.lastErr = err }

func localCode(err error) string {
	if err == nil {
		return ""
	}
	return string(core.CodeOf(err))
}

func localDuration(text string) (time.Duration, error) {
	switch text {
	case "30m", "30 minutes":
		return 30 * time.Minute, nil
	case "1h", "1 hour":
		return time.Hour, nil
	case "2h", "2 hours":
		return 2 * time.Hour, nil
	}
	return time.ParseDuration(text)
}

// ensureStep registers a scripted step with the runtime under test once.
func (w *localWorld) ensureStep(ctx context.Context, name string) error {
	rt := w.runtime(ctx)
	w.mu.Lock()
	if w.registered[name] {
		w.mu.Unlock()
		return nil
	}
	w.registered[name] = true
	w.stepStarted[name] = make(chan struct{})
	w.mu.Unlock()
	return rt.RegisterStep(name, w.stepFunc(name), runtime.StepDefaults{})
}

func (w *localWorld) behavior(name string) *localStep {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.steps[name] == nil {
		w.steps[name] = &localStep{}
	}
	return w.steps[name]
}

func (w *localWorld) options(name string) runtime.StepOptions {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stepOptions[name]
}

func (w *localWorld) stepFunc(name string) runtime.StepFunc {
	return func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		w.mu.Lock()
		execution := w.executions[name] + 1
		w.executions[name] = execution
		behavior := w.steps[name]
		w.activeSteps++
		if w.activeSteps > w.peakSteps {
			w.peakSteps = w.activeSteps
		}
		started := w.stepStarted[name]
		if execution == 1 && started != nil {
			close(started)
		}
		var after <-chan struct{}
		if other := w.stepWaits[name]; other != "" {
			after = w.stepFutureDone[other]
		}
		w.mu.Unlock()
		defer func() {
			w.mu.Lock()
			w.activeSteps--
			w.mu.Unlock()
		}()
		if after != nil {
			select {
			case <-after:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if behavior == nil {
			return json.RawMessage(`{}`), nil
		}
		if behavior.barrier != nil {
			behavior.barrier.enter()
			if err := behavior.barrier.wait(ctx); err != nil {
				return nil, err
			}
		}
		if behavior.block {
			<-ctx.Done()
			w.mu.Lock()
			w.steppedCancelled[name] = true
			w.mu.Unlock()
			return nil, ctx.Err()
		}
		tenant, _ := core.TenantFrom(ctx)
		return behavior.result(execution, string(tenant))
	}
}

func (w *localWorld) executionsOf(name string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.executions[name]
}

func (w *localWorld) started(name string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	ch := w.stepStarted[name]
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func (w *localWorld) cancelled(name string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.steppedCancelled[name]
}

// noteClock freezes the reference time of a run for the "no retry waited" check.
func (w *localWorld) noteClock(ctx context.Context) {
	w.clockBefore = world(ctx).Core.clock.Now()
}

// --- flow scripts ----------------------------------------------------------

// stepFlow calls one step and returns its result; an unhandled error fails the
// run, as the contract says.
func (w *localWorld) stepFlow(name string) localFlow {
	return func(e *localExec, dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		if err := w.ensureStep(dc.Context(), name); err != nil {
			return nil, err
		}
		var out json.RawMessage
		err := dc.Step(name, nil, w.options(name)).Get(&out)
		e.update(func() { e.stepErr = err })
		if err != nil {
			return nil, err
		}
		return out, nil
	}
}

func (w *localWorld) twiceFlow(name string) localFlow {
	return func(e *localExec, dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		if err := w.ensureStep(dc.Context(), name); err != nil {
			return nil, err
		}
		var first, second json.RawMessage
		if err := dc.Step(name, nil, w.options(name)).Get(&first); err != nil {
			return nil, err
		}
		if err := dc.Step(name, nil, w.options(name)).Get(&second); err != nil {
			return nil, err
		}
		e.update(func() {
			e.first = first
			e.second = second
		})
		return json.RawMessage(`{}`), nil
	}
}

func (w *localWorld) blockingStepFlow(name string) localFlow {
	return func(e *localExec, dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		if err := w.ensureStep(dc.Context(), name); err != nil {
			return nil, err
		}
		var out json.RawMessage
		err := dc.Step(name, nil, w.options(name)).Get(&out)
		e.update(func() { e.stepErr = err })
		return nil, err
	}
}

func (w *localWorld) gateFlow(gate string) localFlow {
	return func(e *localExec, dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		wait := dc.Await(runtime.GateSpec{Name: gate})
		e.update(func() {
			e.waiting = true
			e.atGate = gate
		})
		var payload json.RawMessage
		if err := wait.Get(&payload); err != nil {
			e.update(func() { e.waitErr = err })
			return nil, err
		}
		return payload, nil
	}
}

func (w *localWorld) timerFlow(d time.Duration) localFlow {
	return func(e *localExec, dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		wait := dc.Sleep(d)
		e.update(func() { e.waiting = true })
		if err := wait.Get(nil); err != nil {
			e.update(func() { e.waitErr = err })
			return nil, err
		}
		e.update(func() { e.timers++ })
		return json.RawMessage(`{}`), nil
	}
}
