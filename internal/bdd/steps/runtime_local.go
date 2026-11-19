package steps

import (
	"context"
	"encoding/json"
	"fmt"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/runtime"
	"github.com/epzero/basamento/runtime/local"
)

// localWaitLimit bounds how long a step waits for a goroutine to reach an
// observed state. It is a liveness guard, never the clock the scenarios move.
const localWaitLimit = 10 * time.Second

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
	deadline := time.After(localWaitLimit)
	for {
		e.mu.Lock()
		done := ok()
		changed := e.changed
		e.mu.Unlock()
		if done {
			return nil
		}
		select {
		case <-changed:
		case <-deadline:
			return fmt.Errorf("timed out waiting for %s", what)
		}
	}
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
	rt := local.New(local.Options{Clock: world(ctx).Core.clock, IDs: &coretest.SeqIds{}})
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
	deadline := time.Now().Add(localWaitLimit)
	for !ok() {
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s", what)
		}
		goruntime.Gosched()
	}
	return nil
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

// --- registration ----------------------------------------------------------

func registerLocal(sc *godog.ScenarioContext) {
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		if value, ok := ctx.Value(worldKey{}).(*World); ok && value.Local != nil {
			value.Local.cleanup()
		}
		return ctx, nil
	})

	// Capabilities.
	sc.Step(`^the runtime is "local"$`, func(ctx context.Context) error {
		localStepWorld(ctx).runtime(ctx)
		return nil
	})

	// REQ-RT-006: nothing survives a new runtime.
	sc.Step(`^tenant "([^"]*)" has run "([^"]*)" of "([^"]*)" on an earlier local runtime$`, func(ctx context.Context, tenant, runID, flow string) error {
		w := localStepWorld(ctx)
		w.setFlow(flow, w.gateFlow("approve"))
		_, exec, err := w.start(ctx, tenant, runID, flow)
		if err != nil {
			return err
		}
		return exec.wait("the run to await its gate", func() bool { return exec.atGate != "" })
	})
	sc.Step(`^a new local runtime starts and tenant "([^"]*)" describes run "([^"]*)"$`, func(ctx context.Context, tenant, runID string) error {
		w := localStepWorld(ctx)
		w.newRuntime(ctx)
		status, err := w.runtime(ctx).Describe(w.ctx(tenant), runtime.RunRef{RunID: runID})
		w.lastStatus, w.lastErr = status, err
		return nil
	})
	sc.Step(`^tenant "([^"]*)" ran flow "([^"]*)" on an earlier local runtime$`, func(ctx context.Context, tenant, flow string) error {
		w := localStepWorld(ctx)
		w.setFlow(flow, w.gateFlow("approve"))
		_, exec, err := w.start(ctx, tenant, "", flow)
		if err != nil {
			return err
		}
		return exec.wait("the run to await its gate", func() bool { return exec.atGate != "" })
	})
	sc.Step(`^a new local runtime starts and tenant "([^"]*)" lists runs of flow "([^"]*)"$`, func(ctx context.Context, tenant, flow string) error {
		w := localStepWorld(ctx)
		w.newRuntime(ctx)
		runs, _, err := w.runtime(ctx).List(w.ctx(tenant), runtime.RunQuery{Flow: flow})
		w.lastList, w.lastErr = runs, err
		return nil
	})
	sc.Step(`^tenant "([^"]*)" lists (\d+) runs?$`, func(ctx context.Context, tenant string, want int) error {
		w := localStepWorld(ctx)
		if w.lastErr != nil {
			return w.lastErr
		}
		if len(w.lastList) != want {
			return fmt.Errorf("tenant %q lists %d runs, want %d", tenant, len(w.lastList), want)
		}
		return nil
	})

	// Scripted step behavior.
	sc.Step(`^step "([^"]*)" returns (\{.*\})$`, func(ctx context.Context, name, result string) error {
		w := localStepWorld(ctx)
		if err := w.ensureStep(ctx, name); err != nil {
			return err
		}
		w.behavior(name).defaultResult = json.RawMessage(result)
		return nil
	})
	sc.Step(`^step "([^"]*)" returns (\{.*\}) for tenant "([^"]*)" and (\{.*\}) for tenant "([^"]*)"$`,
		func(ctx context.Context, name, first, firstTenant, second, secondTenant string) error {
			w := localStepWorld(ctx)
			if err := w.ensureStep(ctx, name); err != nil {
				return err
			}
			w.behavior(name).tenantResults = map[string]json.RawMessage{
				firstTenant:  json.RawMessage(first),
				secondTenant: json.RawMessage(second),
			}
			return nil
		})
	sc.Step(`^step "([^"]*)" returns (\{.*\}) on execution (\d+) and (\{.*\}) on execution (\d+)$`,
		func(ctx context.Context, name, first string, firstN int, second string, secondN int) error {
			w := localStepWorld(ctx)
			if err := w.ensureStep(ctx, name); err != nil {
				return err
			}
			w.behavior(name).results = map[int]json.RawMessage{
				firstN:  json.RawMessage(first),
				secondN: json.RawMessage(second),
			}
			return nil
		})
	sc.Step(`^step "([^"]*)" fails with code "([^"]*)" on execution (\d+) and with code "([^"]*)" on execution (\d+), each marked retryable (true|false)$`,
		func(ctx context.Context, name, first string, firstN int, second string, secondN int, retryable string) error {
			w := localStepWorld(ctx)
			if err := w.ensureStep(ctx, name); err != nil {
				return err
			}
			behavior := w.behavior(name)
			behavior.errors = map[int]*core.Error{
				firstN:  localPlatformError(first, retryable),
				secondN: localPlatformError(second, retryable),
			}
			return nil
		})
	sc.Step(`^step "([^"]*)" fails with code "([^"]*)" marked retryable (true|false) on execution (\d+) and returns (\{.*\}) on execution (\d+)$`,
		func(ctx context.Context, name, code, retryable string, at int, result string, atResult int) error {
			w := localStepWorld(ctx)
			if err := w.ensureStep(ctx, name); err != nil {
				return err
			}
			behavior := w.behavior(name)
			behavior.errors = map[int]*core.Error{at: localPlatformError(code, retryable)}
			behavior.results = map[int]json.RawMessage{atResult: json.RawMessage(result)}
			return nil
		})
	sc.Step(`^the step lists "([^"]*)" as non-retryable$`, func(ctx context.Context, code string) error {
		w := localStepWorld(ctx)
		w.mu.Lock()
		w.stepOptions["charge"] = runtime.StepOptions{NonRetryable: []string{code}}
		w.mu.Unlock()
		return nil
	})
	sc.Step(`^the step leaves its attempt limit at 0$`, func(ctx context.Context) error {
		w := localStepWorld(ctx)
		w.mu.Lock()
		options := w.stepOptions["charge"]
		options.MaxAttempts = 0
		w.stepOptions["charge"] = options
		w.mu.Unlock()
		return nil
	})
	sc.Step(`^step "([^"]*)" was executed (\d+) times?$`, func(ctx context.Context, name string, want int) error {
		if temporalScenario(ctx) {
			if got := temporalW(ctx).stepCalls(name); got != want {
				return fmt.Errorf("step %q ran %d times, want %d", name, got, want)
			}
			return nil
		}
		w := localStepWorld(ctx)
		if got := w.executionsOf(name); got != want {
			return fmt.Errorf("step %q was executed %d times, want %d", name, got, want)
		}
		return nil
	})
	sc.Step(`^the step fails with the error of its last attempt with code "([^"]*)"$`, localStepFailed)
	sc.Step(`^no retry waited for the clock to advance$`, func(ctx context.Context) error {
		w := localStepWorld(ctx)
		if now := world(ctx).Core.clock.Now(); !now.Equal(w.clockBefore) {
			return fmt.Errorf("the clock advanced from %s to %s during retries", w.clockBefore, now)
		}
		return nil
	})

	// Flows over steps.
	sc.Step(`^a flow calls step "([^"]*)" once and returns its result$`, func(ctx context.Context, name string) error {
		localStepWorld(ctx).setFlow("once", localStepWorld(ctx).stepFlow(name))
		return nil
	})
	sc.Step(`^a flow calls step "([^"]*)" twice with the same name and returns both results$`, func(ctx context.Context, name string) error {
		localStepWorld(ctx).setFlow("twice", localStepWorld(ctx).twiceFlow(name))
		return nil
	})
	sc.Step(`^a flow runs step "([^"]*)" that blocks until cancelled$`, func(ctx context.Context, name string) error {
		w := localStepWorld(ctx)
		if err := w.ensureStep(ctx, name); err != nil {
			return err
		}
		w.behavior(name).block = true
		w.setFlow(name, w.blockingStepFlow(name))
		return nil
	})
	sc.Step(`^a flow starts steps "([^"]*)" and "([^"]*)" and waits on the first to finish, in that order$`,
		func(ctx context.Context, first, second string) error {
			w := localStepWorld(ctx)
			w.setFlow("select-steps", func(e *localExec, dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
				for _, name := range []string{first, second} {
					if err := w.ensureStep(dc.Context(), name); err != nil {
						return nil, err
					}
				}
				var released chan struct{}
				w.mu.Lock()
				ordered := w.stepWaits[first] == second
				if ordered {
					released = make(chan struct{})
					w.stepFutureDone[second] = released
				}
				w.mu.Unlock()
				one := dc.Step(first, nil, w.options(first))
				two := dc.Step(second, nil, w.options(second))
				if released != nil {
					go func() {
						var out json.RawMessage
						_ = two.Get(&out)
						close(released)
					}()
				}
				index, err := dc.Select(one, two)
				if err != nil {
					return nil, err
				}
				e.update(func() {
					e.selectDone = true
					e.selectIndex = index
				})
				return json.RawMessage(`{}`), nil
			})
			return nil
		})
	sc.Step(`^step "([^"]*)" finishes only after step "([^"]*)"$`, func(ctx context.Context, first, second string) error {
		w := localStepWorld(ctx)
		w.mu.Lock()
		w.stepWaits[first] = second
		w.mu.Unlock()
		return nil
	})
	sc.Step(`^a flow starts steps "([^"]*)", "([^"]*)" and "([^"]*)" and waits on all three$`,
		func(ctx context.Context, first, second, third string) error {
			w := localStepWorld(ctx)
			w.setFlow("fanout", func(e *localExec, dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
				var futures []runtime.Future
				for _, name := range []string{first, second, third} {
					if err := w.ensureStep(dc.Context(), name); err != nil {
						return nil, err
					}
					futures = append(futures, dc.Step(name, nil, w.options(name)))
				}
				for range futures {
					if _, err := dc.Select(futures...); err != nil {
						return nil, err
					}
				}
				return json.RawMessage(`{}`), nil
			})
			return nil
		})
	sc.Step(`^each of steps "([^"]*)", "([^"]*)" and "([^"]*)" waits until all (\d+) steps are executing$`,
		func(ctx context.Context, first, second, third string, want int) error {
			w := localStepWorld(ctx)
			barrier := newLocalBarrier(want)
			for _, name := range []string{first, second, third} {
				if err := w.ensureStep(ctx, name); err != nil {
					return err
				}
				w.behavior(name).barrier = barrier
			}
			return nil
		})
	sc.Step(`^(\d+) steps executed concurrently$`, func(ctx context.Context, want int) error {
		w := localStepWorld(ctx)
		w.mu.Lock()
		peak := w.peakSteps
		w.mu.Unlock()
		if peak != want {
			return fmt.Errorf("peak concurrent steps = %d, want %d", peak, want)
		}
		return nil
	})

	// Timers.
	sc.Step(`^a flow waits on a "([^"]*)" timer and then completes$`, func(ctx context.Context, text string) error {
		d, err := localDuration(text)
		if err != nil {
			return err
		}
		localStepWorld(ctx).setFlow("timer", localStepWorld(ctx).timerFlow(d))
		return nil
	})
	sc.Step(`^a run of "([^"]*)" starts and (\d+) (minute|minutes|hour|hours) pass$`,
		func(ctx context.Context, flow string, count int, unit string) error {
			w := localStepWorld(ctx)
			_, exec, err := w.start(ctx, "acme", "", flow)
			if err != nil {
				return err
			}
			if err := exec.wait("the run to wait on its timer", func() bool { return exec.waiting }); err != nil {
				return err
			}
			world(ctx).Core.clock.Advance(localUnit(count, unit))
			return nil
		})
	sc.Step(`^the run has completed (\d+) timers?$`, func(ctx context.Context, want int) error {
		w := localStepWorld(ctx)
		exec := w.currentExec
		if exec == nil {
			return fmt.Errorf("no run has started")
		}
		exec.mu.Lock()
		got := exec.timers
		exec.mu.Unlock()
		if got != want {
			return fmt.Errorf("the run completed %d timers, want %d", got, want)
		}
		return nil
	})

	// Runs and their state.
	sc.Step(`^a run of "([^"]*)" runs$`, func(ctx context.Context, flow string) error {
		w := localStepWorld(ctx)
		w.noteClock(ctx)
		w.setFlow(flow, w.stepFlow("charge"))
		ref, _, err := w.start(ctx, "acme", "", flow)
		if err != nil {
			return err
		}
		return w.waitRunClosed(ctx, ref)
	})
	sc.Step(`^a run of "([^"]*)" completes$`, func(ctx context.Context, flow string) error {
		w := localStepWorld(ctx)
		w.noteClock(ctx)
		ref, exec, err := w.start(ctx, "acme", "", flow)
		if err != nil {
			return err
		}
		if err := w.waitRunState(ctx, ref, "completed"); err != nil {
			return err
		}
		w.recordOutput(ref, exec)
		return nil
	})
	sc.Step(`^a run of "([^"]*)" for tenant "([^"]*)" completes$`, func(ctx context.Context, flow, tenant string) error {
		w := localStepWorld(ctx)
		ref, exec, err := w.start(ctx, tenant, "", flow)
		if err != nil {
			return err
		}
		if err := w.waitRunState(ctx, ref, "completed"); err != nil {
			return err
		}
		w.recordOutput(ref, exec)
		return nil
	})
	sc.Step(`^runs "([^"]*)" and "([^"]*)" of "([^"]*)" complete in order on one local runtime$`,
		func(ctx context.Context, first, second, flow string) error {
			w := localStepWorld(ctx)
			for _, id := range []string{first, second} {
				ref, exec, err := w.start(ctx, "acme", id, flow)
				if err != nil {
					return err
				}
				if err := w.waitRunState(ctx, ref, "completed"); err != nil {
					return err
				}
				w.recordOutput(ref, exec)
			}
			return nil
		})
	sc.Step(`^run "([^"]*)" returns (\{.*\}) and run "([^"]*)" returns (\{.*\})$`, func(ctx context.Context, first, firstOut, second, secondOut string) error {
		w := localStepWorld(ctx)
		gotFirst := w.output(runtime.RunRef{RunID: first, TenantID: "acme"})
		gotSecond := w.output(runtime.RunRef{RunID: second, TenantID: "acme"})
		if !jsonEquivalent(gotFirst, json.RawMessage(firstOut)) {
			return fmt.Errorf("run %q returns %s, want %s", first, gotFirst, firstOut)
		}
		if !jsonEquivalent(gotSecond, json.RawMessage(secondOut)) {
			return fmt.Errorf("run %q returns %s, want %s", second, gotSecond, secondOut)
		}
		return nil
	})
	sc.Step(`^both results are (\{.*\})$`, func(ctx context.Context, want string) error {
		w := localStepWorld(ctx)
		exec := w.currentExec
		if exec == nil {
			return fmt.Errorf("no run has started")
		}
		exec.mu.Lock()
		first, second := exec.first, exec.second
		exec.mu.Unlock()
		if !jsonEquivalent(first, json.RawMessage(want)) || !jsonEquivalent(second, json.RawMessage(want)) {
			return fmt.Errorf("results are %s and %s, want both %s", first, second, want)
		}
		return nil
	})
	sc.Step(`^the run returns (.+)$`, func(ctx context.Context, want string) error {
		if temporalScenario(ctx) {
			w := temporalW(ctx)
			got, err := w.result(string(w.lastRef.TenantID), w.lastRef)
			if err != nil {
				return err
			}
			expected := temporalValue(want)
			if !jsonEquivalent(got, expected) {
				return fmt.Errorf("run returns %s, want %s", got, expected)
			}
			return nil
		}
		w := localStepWorld(ctx)
		exec := w.currentExec
		if exec == nil {
			return fmt.Errorf("no run has started")
		}
		exec.mu.Lock()
		result := append(json.RawMessage(nil), exec.result...)
		exec.mu.Unlock()
		if !jsonEquivalent(result, json.RawMessage(want)) {
			return fmt.Errorf("the run returns %s, want %s", result, want)
		}
		return nil
	})
	sc.Step(`^the run is still "([^"]*)"$`, localRunState)
	sc.Step(`^the run is described as "([^"]*)"$`, localRunState)

	// Gates.
	sc.Step(`^a run waiting at gate "([^"]*)" that accepts payloads where "([^"]*)"$`, func(ctx context.Context, gate, accept string) error {
		w := localStepWorld(ctx)
		w.setFlow("waiting", func(e *localExec, dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
			wait := dc.Await(runtime.GateSpec{Name: gate, AcceptIf: accept})
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
		})
		ref, exec, err := w.start(ctx, "acme", "", "waiting")
		if err != nil {
			return err
		}
		w.current = ref
		w.currentExec = exec
		return exec.wait("the run to await the gate", func() bool { return exec.atGate != "" })
	})
	sc.Step(`^a flow waits at gate "([^"]*)" and then waits at gate "([^"]*)"$`, func(ctx context.Context, first, second string) error {
		w := localStepWorld(ctx)
		w.setFlow("publish", func(e *localExec, dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
			approve := dc.Await(runtime.GateSpec{Name: first})
			e.update(func() {
				e.waiting = true
				e.atGate = first
			})
			if err := approve.Get(nil); err != nil {
				e.update(func() { e.waitErr = err })
				return nil, err
			}
			publish := dc.Await(runtime.GateSpec{Name: second})
			e.update(func() { e.atGate = second })
			var payload json.RawMessage
			if err := publish.Get(&payload); err != nil {
				e.update(func() { e.waitErr = err })
				return nil, err
			}
			return payload, nil
		})
		return nil
	})
	sc.Step(`^a run of "publish" resolved gate "([^"]*)" and received (\{.*\})$`, func(ctx context.Context, gate, payload string) error {
		w := localStepWorld(ctx)
		ref, exec, err := w.start(ctx, "acme", "", "publish")
		if err != nil {
			return err
		}
		if err := exec.wait("the run to await "+gate, func() bool { return exec.atGate == gate }); err != nil {
			return err
		}
		if err := w.runtime(ctx).Resolve(w.ctx("acme"), ref, gate, json.RawMessage(payload)); err != nil {
			return err
		}
		if err := exec.wait("the run to await the next gate", func() bool { return exec.atGate != gate }); err != nil {
			return err
		}
		return nil
	})
	sc.Step(`^the gate is resolved with payload (\{.*\})$`, func(ctx context.Context, payload string) error {
		w := localStepWorld(ctx)
		exec := w.currentExec
		if exec == nil {
			return fmt.Errorf("no run has started")
		}
		exec.mu.Lock()
		gate := exec.atGate
		exec.mu.Unlock()
		return w.runtime(ctx).Resolve(w.ctx(string(w.current.TenantID)), w.current, gate, json.RawMessage(payload))
	})
	sc.Step(`^the gate "([^"]*)" is resolved again with payload (\{.*\})$`, func(ctx context.Context, gate, payload string) error {
		w := localStepWorld(ctx)
		err := w.runtime(ctx).Resolve(w.ctx(string(w.current.TenantID)), w.current, gate, json.RawMessage(payload))
		w.noteError(err)
		return nil
	})
	sc.Step(`^the run is "running" at gate "([^"]*)"$`, func(ctx context.Context, gate string) error {
		w := localStepWorld(ctx)
		state, err := w.stateOf(ctx, "acme", w.current.RunID)
		if err != nil {
			return err
		}
		if state != "running" {
			return fmt.Errorf("the run is %q, want running", state)
		}
		exec := w.currentExec
		exec.mu.Lock()
		at := exec.atGate
		exec.mu.Unlock()
		if at != gate {
			return fmt.Errorf("the run waits at gate %q, want %q", at, gate)
		}
		return nil
	})

	// Select.
	sc.Step(`^a flow is waiting on the first of a "([^"]*)" timer and gate "([^"]*)", in that order$`,
		func(ctx context.Context, text, gate string) error {
			w := localStepWorld(ctx)
			d, err := localDuration(text)
			if err != nil {
				return err
			}
			w.setFlow("select", func(e *localExec, dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
				timer := dc.Sleep(d)
				wait := dc.Await(runtime.GateSpec{Name: gate})
				e.update(func() {
					e.waiting = true
					e.atGate = gate
				})
				index, err := dc.Select(timer, wait)
				if err != nil {
					return nil, err
				}
				var payload json.RawMessage
				chosen := []runtime.Future{timer, wait}[index]
				if err := chosen.Get(&payload); err != nil {
					return nil, err
				}
				e.update(func() {
					e.selectDone = true
					e.selectIndex = index
					e.selectPayload = payload
				})
				return payload, nil
			})
			_, exec, err := w.start(ctx, "acme", "", "select")
			if err != nil {
				return err
			}
			return exec.wait("the run to await its gate", func() bool { return exec.atGate != "" })
		})
	sc.Step(`^a flow is waiting on the first of a "([^"]*)" timer and a "([^"]*)" timer, in that order$`,
		func(ctx context.Context, first, second string) error {
			w := localStepWorld(ctx)
			one, err := localDuration(first)
			if err != nil {
				return err
			}
			two, err := localDuration(second)
			if err != nil {
				return err
			}
			w.setFlow("select", func(e *localExec, dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
				firstTimer := dc.Sleep(one)
				secondTimer := dc.Sleep(two)
				e.update(func() { e.waiting = true })
				index, err := dc.Select(firstTimer, secondTimer)
				if err != nil {
					return nil, err
				}
				e.update(func() {
					e.selectDone = true
					e.selectIndex = index
				})
				return json.RawMessage(`{}`), nil
			})
			_, exec, err := w.start(ctx, "acme", "", "select")
			if err != nil {
				return err
			}
			return exec.wait("the run to wait on its timers", func() bool { return exec.waiting })
		})
	sc.Step(`^the run answers its pending wait on the first ready operation$`, func(ctx context.Context) error {
		exec := localStepWorld(ctx).currentExec
		if exec == nil {
			return fmt.Errorf("no run has started")
		}
		return exec.wait("the run to answer its wait", func() bool { return exec.selectDone })
	})
	sc.Step(`^(\d+) hours? pass and the run answers its pending wait on the first ready operation$`, func(ctx context.Context, count int) error {
		w := localStepWorld(ctx)
		world(ctx).Core.clock.Advance(time.Duration(count) * time.Hour)
		exec := w.currentExec
		if exec == nil {
			return fmt.Errorf("no run has started")
		}
		return exec.wait("the run to answer its wait", func() bool { return exec.selectDone })
	})
	sc.Step(`^the wait returns index (\d+)$`, func(ctx context.Context, want int) error {
		exec := localStepWorld(ctx).currentExec
		if exec == nil {
			return fmt.Errorf("no run has started")
		}
		exec.mu.Lock()
		got := exec.selectIndex
		exec.mu.Unlock()
		if got != want {
			return fmt.Errorf("the wait returned index %d, want %d", got, want)
		}
		return nil
	})
	sc.Step(`^the result at index (\d+) is (\{.*\})$`, func(ctx context.Context, index int, want string) error {
		exec := localStepWorld(ctx).currentExec
		if exec == nil {
			return fmt.Errorf("no run has started")
		}
		exec.mu.Lock()
		payload := exec.selectPayload
		got := exec.selectIndex
		exec.mu.Unlock()
		if got != index {
			return fmt.Errorf("the wait returned index %d, want %d", got, index)
		}
		if !jsonEquivalent(payload, json.RawMessage(want)) {
			return fmt.Errorf("the result is %s, want %s", payload, want)
		}
		return nil
	})
	sc.Step(`^the wait answers$`, func(ctx context.Context) error {
		w := localStepWorld(ctx)
		if len(w.execsOf("select-steps")) == 0 {
			if _, _, err := w.start(ctx, "acme", "", "select-steps"); err != nil {
				return err
			}
		}
		exec := w.currentExec
		if exec == nil {
			return fmt.Errorf("no run has started")
		}
		return exec.wait("the run to answer its wait", func() bool { return exec.selectDone })
	})

	// Cancellation.
	sc.Step(`^a run of "([^"]*)" is waiting on gate "([^"]*)"$`, func(ctx context.Context, flow, gate string) error {
		w := localStepWorld(ctx)
		w.setFlow(flow, w.gateFlow(gate))
		_, exec, err := w.start(ctx, "acme", "", flow)
		if err != nil {
			return err
		}
		w.currentExec = exec
		return exec.wait("the run to await its gate", func() bool { return exec.atGate != "" })
	})
	sc.Step(`^a run of "([^"]*)" is waiting on a "([^"]*)" timer$`, func(ctx context.Context, flow, text string) error {
		w := localStepWorld(ctx)
		d, err := localDuration(text)
		if err != nil {
			return err
		}
		w.setFlow(flow, w.timerFlow(d))
		_, exec, err := w.start(ctx, "acme", "", flow)
		if err != nil {
			return err
		}
		w.currentExec = exec
		return exec.wait("the run to wait on its timer", func() bool { return exec.waiting })
	})
	sc.Step(`^the run is cancelled$`, func(ctx context.Context) error {
		// The model node scenarios cancel their own one-node run; the runtime
		// scenarios cancel the local world's current run.
		if n := world(ctx).Node; n.active() {
			return n.cancelRun()
		}
		w := localStepWorld(ctx)
		return w.runtime(ctx).Cancel(w.ctx(string(w.current.TenantID)), w.current, "")
	})
	sc.Step(`^run "([^"]*)" is cancelled with reason "([^"]*)"$`, func(ctx context.Context, runID, reason string) error {
		w := localStepWorld(ctx)
		return w.runtime(ctx).Cancel(w.ctx("acme"), runtime.RunRef{RunID: runID}, reason)
	})
	sc.Step(`^run "([^"]*)" of "([^"]*)" has step "([^"]*)" in flight$`, func(ctx context.Context, runID, flow, step string) error {
		w := localStepWorld(ctx)
		ref, exec, err := w.start(ctx, "acme", runID, flow)
		if err != nil {
			return err
		}
		w.current, w.currentExec = ref, exec
		return w.spin("step "+step+" to start", func() bool { return w.started(step) })
	})
	sc.Step(`^step "([^"]*)" received cancellation before the run closed$`, func(ctx context.Context, name string) error {
		return localStepCancelled(localStepWorld(ctx), name)
	})
	sc.Step(`^step "([^"]*)" received cancellation$`, func(ctx context.Context, name string) error {
		return localStepCancelled(localStepWorld(ctx), name)
	})
	sc.Step(`^the pending wait fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		w := localStepWorld(ctx)
		exec := w.currentExec
		if exec == nil {
			return fmt.Errorf("no run has started")
		}
		if err := exec.wait("the pending wait to end", func() bool { return exec.waitErr != nil }); err != nil {
			return err
		}
		exec.mu.Lock()
		err := exec.waitErr
		exec.mu.Unlock()
		if localCode(err) != code {
			return fmt.Errorf("the wait failed, want code %q: %w", code, err)
		}
		return nil
	})

	// Children.
	sc.Step(`^tenant "([^"]*)" has parent flow "([^"]*)" that starts child flow "([^"]*)" with child run id "([^"]*)"$`,
		func(ctx context.Context, tenant, parent, child, childID string) error {
			w := localStepWorld(ctx)
			w.setChildFlow(child, `{}`)
			w.setParentFlow(parent, child, childID, "", true)
			return nil
		})
	sc.Step(`^parent flow "([^"]*)" starts child flow "([^"]*)" with child run id "([^"]*)"$`,
		func(ctx context.Context, parent, child, childID string) error {
			w := localStepWorld(ctx)
			w.setChildFlow(child, `{}`)
			w.setParentFlow(parent, child, childID, "", true)
			return nil
		})
	sc.Step(`^parent flow "([^"]*)" starts child flow "([^"]*)" with child run id "([^"]*)" and close policy "([^"]*)"$`,
		func(ctx context.Context, parent, child, childID, policy string) error {
			w := localStepWorld(ctx)
			w.setParentFlow(parent, child, childID, policy, false)
			return nil
		})
	sc.Step(`^flow "([^"]*)" returns (\{.*\})$`, func(ctx context.Context, name, out string) error {
		localStepWorld(ctx).setChildFlow(name, out)
		return nil
	})
	sc.Step(`^a run of "([^"]*)" completes without waiting for "([^"]*)"$`, func(ctx context.Context, flow, childID string) error {
		w := localStepWorld(ctx)
		ref, _, err := w.start(ctx, "acme", "", flow)
		if err != nil {
			return err
		}
		return w.waitRunState(ctx, ref, "completed")
	})
	sc.Step(`^the parent receives child output (\{.*\})$`, func(ctx context.Context, want string) error {
		w := localStepWorld(ctx)
		exec := w.currentExec
		if exec == nil {
			return fmt.Errorf("no run has started")
		}
		exec.mu.Lock()
		out := exec.childOut
		exec.mu.Unlock()
		if !jsonEquivalent(out, json.RawMessage(want)) {
			return fmt.Errorf("the parent received %s, want %s", out, want)
		}
		return nil
	})

	// Tenancy.
	sc.Step(`^tenant "([^"]*)" has run "([^"]*)" waiting at gate "([^"]*)"$`, func(ctx context.Context, tenant, runID, gate string) error {
		w := localStepWorld(ctx)
		w.setFlow("waiting", w.gateFlow(gate))
		_, exec, err := w.start(ctx, tenant, runID, "waiting")
		if err != nil {
			return err
		}
		return exec.wait("the run to await its gate", func() bool { return exec.atGate != "" })
	})
	sc.Step(`^tenant "([^"]*)" has run "([^"]*)" of flow "([^"]*)" waiting at gate "([^"]*)"$`,
		func(ctx context.Context, tenant, runID, flow, gate string) error {
			w := localStepWorld(ctx)
			w.setFlow(flow, w.gateFlow(gate))
			_, exec, err := w.start(ctx, tenant, runID, flow)
			if err != nil {
				return err
			}
			return exec.wait("the run to await its gate", func() bool { return exec.atGate != "" })
		})
	sc.Step(`^tenant "([^"]*)" runs flow "([^"]*)" as run "([^"]*)" that calls step "([^"]*)"$`,
		func(ctx context.Context, tenant, flow, runID, step string) error {
			w := localStepWorld(ctx)
			w.setFlow(flow, w.stepFlow(step))
			_, _, err := w.start(ctx, tenant, runID, flow)
			return err
		})
	sc.Step(`^both runs complete on one local runtime$`, func(ctx context.Context) error {
		w := localStepWorld(ctx)
		for _, exec := range w.execsOf("work") {
			if err := exec.wait("both runs to complete", func() bool { return exec.done }); err != nil {
				return err
			}
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		for key, exec := range w.byRun {
			if key.id != "shared-1" {
				continue
			}
			exec.mu.Lock()
			if exec.done && len(w.outputs[key]) == 0 {
				w.outputs[key] = append(json.RawMessage(nil), exec.result...)
			}
			exec.mu.Unlock()
		}
		return nil
	})
	sc.Step(`^run "([^"]*)" of tenant "([^"]*)" returns (\{.*\})$`, func(ctx context.Context, runID, tenant, want string) error {
		got := localStepWorld(ctx).output(runtime.RunRef{RunID: runID, TenantID: core.TenantID(tenant)})
		if !jsonEquivalent(got, json.RawMessage(want)) {
			return fmt.Errorf("run %q of tenant %q returns %s, want %s", runID, tenant, got, want)
		}
		return nil
	})
	sc.Step(`^tenant "([^"]*)" describes run "([^"]*)"$`, func(ctx context.Context, tenant, runID string) error {
		w := localStepWorld(ctx)
		status, err := w.runtime(ctx).Describe(w.ctx(tenant), runtime.RunRef{RunID: runID})
		w.lastStatus, w.lastErr = status, err
		return nil
	})
	sc.Step(`^tenant "([^"]*)" describing run "([^"]*)" fails with code "([^"]*)"$`, func(ctx context.Context, tenant, runID, code string) error {
		w := localStepWorld(ctx)
		_, err := w.runtime(ctx).Describe(w.ctx(tenant), runtime.RunRef{RunID: runID})
		if localCode(err) != code {
			return fmt.Errorf("describing run %q from tenant %q failed with %q, want %q: %w", runID, tenant, localCode(err), code, err)
		}
		return nil
	})
	sc.Step(`^tenant "([^"]*)" lists runs of flow "([^"]*)"$`, func(ctx context.Context, tenant, flow string) error {
		w := localStepWorld(ctx)
		runs, _, err := w.runtime(ctx).List(w.ctx(tenant), runtime.RunQuery{Flow: flow})
		w.lastList, w.lastErr = runs, err
		return nil
	})
	sc.Step(`^tenant "([^"]*)" lists (\d+) runs? for tenant "([^"]*)" with run id "([^"]*)"$`,
		func(ctx context.Context, caller string, want int, tenant, runID string) error {
			if temporalScenario(ctx) {
				w := temporalW(ctx)
				statuses, _, err := w.rt.List(core.WithTenant(context.Background(), core.TenantID(caller)), runtime.RunQuery{Flow: "review"})
				if err != nil {
					return err
				}
				if len(statuses) != want {
					return fmt.Errorf("listed %d runs, want %d", len(statuses), want)
				}
				if len(statuses) > 0 && (statuses[0].Ref.RunID != runID || string(statuses[0].Ref.TenantID) != tenant) {
					return fmt.Errorf("listed ref %+v, want run %q of tenant %q", statuses[0].Ref, runID, tenant)
				}
				return nil
			}
			w := localStepWorld(ctx)
			if w.lastErr != nil {
				return w.lastErr
			}
			if len(w.lastList) != want {
				return fmt.Errorf("tenant %q lists %d runs, want %d", caller, len(w.lastList), want)
			}
			for _, run := range w.lastList {
				if run.Ref.RunID != runID || string(run.Ref.TenantID) != tenant {
					return fmt.Errorf("listed run is %q of tenant %q, want %q of tenant %q", run.Ref.RunID, run.Ref.TenantID, runID, tenant)
				}
			}
			return nil
		})
	sc.Step(`^tenant "([^"]*)" cancels run "([^"]*)"$`, func(ctx context.Context, tenant, runID string) error {
		w := localStepWorld(ctx)
		err := w.runtime(ctx).Cancel(w.ctx(tenant), runtime.RunRef{RunID: runID}, "")
		w.lastErr = err
		return nil
	})
	sc.Step(`^tenant "([^"]*)" resolves gate "([^"]*)" of run "([^"]*)" with payload (\{.*\})$`,
		func(ctx context.Context, tenant, gate, runID, payload string) error {
			w := localStepWorld(ctx)
			err := w.runtime(ctx).Resolve(w.ctx(tenant), runtime.RunRef{RunID: runID}, gate, json.RawMessage(payload))
			w.lastErr = err
			return nil
		})
	sc.Step(`^tenant "([^"]*)" has no run with id "([^"]*)"$`, func(ctx context.Context, tenant, runID string) error {
		w := localStepWorld(ctx)
		_, err := w.runtime(ctx).Describe(w.ctx(tenant), runtime.RunRef{RunID: runID})
		if localCode(err) != "not_found" {
			return fmt.Errorf("tenant %q has a run %q (Describe error %w), want not_found", tenant, runID, err)
		}
		return nil
	})

	// The tenant of the call is the context's (REQ-RT-042, REQ-RT-043,
	// REQ-RT-044).
	sc.Step(`^run "([^"]*)" of "([^"]*)" is started with context tenant "([^"]*)" and request tenant "([^"]*)"$`,
		localStartWithTenants)
	sc.Step(`^run "([^"]*)" of "([^"]*)" is started with context tenant "([^"]*)" and an empty request tenant$`,
		func(ctx context.Context, runID, flow, contextTenant string) error {
			return localStartWithTenants(ctx, runID, flow, contextTenant, "")
		})
	sc.Step(`^a run of "([^"]*)" is started without a context tenant and with an empty request tenant$`,
		func(ctx context.Context, flow string) error {
			return localStartWithTenants(ctx, "", flow, "", "")
		})
	sc.Step(`^run "([^"]*)" of "([^"]*)" is started without a context tenant and with request tenant "([^"]*)"$`,
		func(ctx context.Context, runID, flow, requestTenant string) error {
			return localStartWithTenants(ctx, runID, flow, "", requestTenant)
		})
	sc.Step(`^run "([^"]*)" is described with context tenant "([^"]*)"$`, func(ctx context.Context, runID, tenant string) error {
		w := localStepWorld(ctx)
		status, err := w.runtime(ctx).Describe(w.ctx(tenant), w.pendingRef)
		w.lastStatus, w.lastErr = status, err
		return nil
	})
	sc.Step(`^run "([^"]*)" is described with context tenant "([^"]*)" and an empty reference tenant$`, func(ctx context.Context, runID, tenant string) error {
		w := localStepWorld(ctx)
		status, err := w.runtime(ctx).Describe(w.ctx(tenant), runtime.RunRef{RunID: runID})
		w.lastStatus, w.lastErr = status, err
		return nil
	})
	sc.Step(`^run "([^"]*)" is described without a context tenant and with reference tenant "([^"]*)"$`,
		func(ctx context.Context, runID, tenant string) error {
			w := localStepWorld(ctx)
			status, err := w.runtime(ctx).Describe(w.ctx(""), runtime.RunRef{RunID: runID, TenantID: core.TenantID(tenant)})
			w.lastStatus, w.lastErr = status, err
			return nil
		})
	sc.Step(`^run "([^"]*)" is cancelled with context tenant "([^"]*)"$`, func(ctx context.Context, runID, tenant string) error {
		w := localStepWorld(ctx)
		w.lastErr = w.runtime(ctx).Cancel(w.ctx(tenant), w.pendingRef, "")
		return nil
	})
	sc.Step(`^run "([^"]*)" is cancelled without a context tenant and with reference tenant "([^"]*)"$`,
		func(ctx context.Context, runID, tenant string) error {
			w := localStepWorld(ctx)
			w.lastErr = w.runtime(ctx).Cancel(w.ctx(""), runtime.RunRef{RunID: runID, TenantID: core.TenantID(tenant)}, "")
			return nil
		})
	sc.Step(`^gate "([^"]*)" of run "([^"]*)" is resolved with context tenant "([^"]*)" and payload (\{.*\})$`,
		func(ctx context.Context, gate, runID, tenant, payload string) error {
			w := localStepWorld(ctx)
			w.lastErr = w.runtime(ctx).Resolve(w.ctx(tenant), w.pendingRef, gate, json.RawMessage(payload))
			return nil
		})
	sc.Step(`^gate "([^"]*)" of run "([^"]*)" is resolved without a context tenant, reference tenant "([^"]*)" and payload (\{.*\})$`,
		func(ctx context.Context, gate, runID, tenant, payload string) error {
			w := localStepWorld(ctx)
			w.lastErr = w.runtime(ctx).Resolve(w.ctx(""), runtime.RunRef{RunID: runID, TenantID: core.TenantID(tenant)}, gate, json.RawMessage(payload))
			return nil
		})
	sc.Step(`^runs of flow "([^"]*)" are listed for request tenant "([^"]*)" with context tenant "([^"]*)"$`,
		func(ctx context.Context, flow, requestTenant, contextTenant string) error {
			w := localStepWorld(ctx)
			runs, _, err := w.runtime(ctx).List(w.ctx(contextTenant), runtime.RunQuery{Flow: flow, TenantID: core.TenantID(requestTenant)})
			w.lastList, w.lastErr = runs, err
			return nil
		})
	sc.Step(`^runs of flow "([^"]*)" are listed with context tenant "([^"]*)" and an empty query tenant$`,
		func(ctx context.Context, flow, contextTenant string) error {
			w := localStepWorld(ctx)
			runs, _, err := w.runtime(ctx).List(w.ctx(contextTenant), runtime.RunQuery{Flow: flow})
			w.lastList, w.lastErr = runs, err
			return nil
		})
	sc.Step(`^runs of flow "([^"]*)" are listed without a context tenant and with query tenant "([^"]*)"$`,
		func(ctx context.Context, flow, queryTenant string) error {
			w := localStepWorld(ctx)
			runs, _, err := w.runtime(ctx).List(w.ctx(""), runtime.RunQuery{Flow: flow, TenantID: core.TenantID(queryTenant)})
			w.lastList, w.lastErr = runs, err
			return nil
		})

	// Shutdown.
	sc.Step(`^a run of "([^"]*)" is in flight on a running local runtime$`, func(ctx context.Context, flow string) error {
		w := localStepWorld(ctx)
		w.setFlow(flow, w.timerFlow(time.Hour))
		if err := w.serve(ctx); err != nil {
			return err
		}
		_, exec, err := w.start(ctx, "acme", "", flow)
		if err != nil {
			return err
		}
		w.currentExec = exec
		return exec.wait("the run to wait on its timer", func() bool { return exec.waiting })
	})
	sc.Step(`^a run of "([^"]*)" has step "([^"]*)" in flight on a running local runtime$`, func(ctx context.Context, flow, step string) error {
		w := localStepWorld(ctx)
		w.behavior(step).block = true
		w.setFlow(flow, w.blockingStepFlow(step))
		if err := w.serve(ctx); err != nil {
			return err
		}
		_, exec, err := w.start(ctx, "acme", "", flow)
		if err != nil {
			return err
		}
		w.currentExec = exec
		return w.spin("step "+step+" to start", func() bool { return w.started(step) })
	})
	sc.Step(`^a running local runtime has shut down$`, func(ctx context.Context) error {
		w := localStepWorld(ctx)
		if err := w.serve(ctx); err != nil {
			return err
		}
		return w.shutdown(ctx)
	})
	sc.Step(`^the local runtime shuts down$`, func(ctx context.Context) error {
		w := localStepWorld(ctx)
		return w.shutdown(ctx)
	})
	sc.Step(`^shutdown completes with no error$`, func(ctx context.Context) error {
		if err := localStepWorld(ctx).shutdownError; err != nil {
			return fmt.Errorf("shutdown failed: %w", err)
		}
		return nil
	})
	sc.Step(`^a run of "([^"]*)" is started on that runtime$`, func(ctx context.Context, flow string) error {
		w := localStepWorld(ctx)
		ref, err := w.runtime(ctx).Start(w.ctx("acme"), runtime.StartRequest{Flow: runtime.FlowRef{Name: flow}})
		w.lastStart, w.lastErr = ref, err
		return nil
	})
	sc.Step(`^(\d+) steps are executing$`, func(ctx context.Context, want int) error {
		w := localStepWorld(ctx)
		w.mu.Lock()
		active := w.activeSteps
		w.mu.Unlock()
		if active != want {
			return fmt.Errorf("%d steps are executing, want %d", active, want)
		}
		return nil
	})
}

// --- helpers ----------------------------------------------------------------

func localStepError(ctx context.Context, name, code, retryable string, execution int) error {
	w := localStepWorld(ctx)
	if err := w.ensureStep(ctx, name); err != nil {
		return err
	}
	behavior := w.behavior(name)
	if execution > 0 {
		if behavior.errors == nil {
			behavior.errors = map[int]*core.Error{}
		}
		behavior.errors[execution] = localPlatformError(code, retryable)
		return nil
	}
	behavior.defaultErr = localPlatformError(code, retryable)
	return nil
}

func localPlatformError(code, retryable string) *core.Error {
	return &core.Error{Code: core.Code(code), Message: "scripted failure", Retryable: retryable == "true"}
}

func localStepFailed(ctx context.Context, code string) error {
	exec := localStepWorld(ctx).currentExec
	if exec == nil {
		return fmt.Errorf("no run has started")
	}
	exec.mu.Lock()
	err := exec.stepErr
	exec.mu.Unlock()
	if localCode(err) != code {
		return fmt.Errorf("the step failed, want code %q: %w", code, err)
	}
	return nil
}

func localStepCancelled(w *localWorld, name string) error {
	if !w.cancelled(name) {
		return fmt.Errorf("step %q did not observe cancellation", name)
	}
	return nil
}

func localRunState(ctx context.Context, want string) error {
	w := localStepWorld(ctx)
	if w.current.RunID == "" {
		return fmt.Errorf("no run has started")
	}
	if want == "running" {
		state, err := w.stateOf(ctx, string(w.current.TenantID), w.current.RunID)
		if err != nil {
			return err
		}
		if state != want {
			return fmt.Errorf("the run is %q, want %q", state, want)
		}
		return nil
	}
	if err := w.waitRunState(ctx, w.current, want); err != nil {
		return err
	}
	return nil
}

func localLastError(w *localWorld, code string) error {
	if w.lastErr == nil {
		return fmt.Errorf("the call succeeded, want code %q", code)
	}
	if got := localCode(w.lastErr); got != code {
		return fmt.Errorf("the call failed with %q, want %q: %w", got, code, w.lastErr)
	}
	return nil
}

func localStartWithTenants(ctx context.Context, runID, flow, contextTenant, requestTenant string) error {
	w := localStepWorld(ctx)
	ref, err := w.runtime(ctx).Start(w.ctx(contextTenant), runtime.StartRequest{
		Flow:     runtime.FlowRef{Name: flow},
		RunID:    runID,
		TenantID: core.TenantID(requestTenant),
	})
	w.lastStart, w.lastErr = ref, err
	if err != nil {
		return nil
	}
	w.mu.Lock()
	w.current = ref
	w.mu.Unlock()
	before := w.execCount(flow)
	return w.spin("the run of "+flow+" to start", func() bool { return w.execCount(flow) > before })
}

func localUnit(count int, unit string) time.Duration {
	switch strings.TrimSuffix(unit, "s") {
	case "minute":
		return time.Duration(count) * time.Minute
	case "hour":
		return time.Duration(count) * time.Hour
	default:
		return time.Duration(count) * time.Second
	}
}

// setChildFlow makes flow return out.
func (w *localWorld) setChildFlow(name, out string) {
	w.setFlow(name, func(_ *localExec, _ runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(out), nil
	})
}

// setParentFlow scripts a parent that starts a child with the given id. With
// await it waits for and records the child's output; otherwise it returns at
// once so the close policy is the only thing that ends the child.
func (w *localWorld) setParentFlow(name, child, childID, policy string, await bool) {
	w.setFlow(name, func(e *localExec, dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		options := runtime.ChildOptions{ID: childID, ParentClose: policy}
		future := dc.Child(runtime.FlowRef{Name: child}, nil, options)
		if !await {
			return json.RawMessage(`{}`), nil
		}
		var out json.RawMessage
		if err := future.Get(&out); err != nil {
			return nil, err
		}
		e.update(func() { e.childOut = out })
		return out, nil
	})
}

// serve starts Serve in the background with a cancelable context.
func (w *localWorld) serve(ctx context.Context) error {
	rt := w.runtime(ctx)
	w.mu.Lock()
	if w.serveCancel != nil {
		w.mu.Unlock()
		return nil
	}
	serveCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	w.serveCancel = cancel
	w.serveDone = done
	w.mu.Unlock()
	go func() { done <- rt.Serve(serveCtx) }()
	return nil
}

// shutdown cancels Serve's context and waits for it to return.
func (w *localWorld) shutdown(ctx context.Context) error {
	w.mu.Lock()
	cancel, done := w.serveCancel, w.serveDone
	w.serveCancel, w.serveDone = nil, nil
	w.mu.Unlock()
	if cancel == nil {
		return fmt.Errorf("the local runtime is not serving")
	}
	cancel()
	select {
	case err := <-done:
		w.shutdownError = err
		return nil
	case <-time.After(localWaitLimit):
		return fmt.Errorf("the local runtime did not shut down")
	}
}

// cleanup releases the scenario's runtimes so no goroutine outlives it. Serving
// with an already-cancelled context cancels every run, children included.
func (w *localWorld) cleanup() {
	w.mu.Lock()
	runtimes := append([]*local.Runtime(nil), w.retired...)
	if w.rt != nil {
		runtimes = append(runtimes, w.rt)
	}
	serveCancel := w.serveCancel
	w.rt = nil
	w.retired = nil
	w.serveCancel = nil
	w.serveDone = nil
	w.mu.Unlock()
	if serveCancel != nil {
		serveCancel()
	}
	for _, rt := range runtimes {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		done := make(chan struct{})
		go func(runtimeUnderTest *local.Runtime) {
			_ = runtimeUnderTest.Serve(ctx)
			close(done)
		}(rt)
		select {
		case <-done:
		case <-time.After(localWaitLimit):
		}
	}
}
