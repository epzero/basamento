package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// durableContext implements runtime.DurableContext over a Temporal workflow.
// Order-based journal entries (timers, selects, nows) assume the flow
// re-executes the same calls in the same order (every DurableContext method is
// replay-safe). Ready() on a replayed future may report true for results
// recorded before ContinueAsNew; flow code must not branch on Ready() across
// a Checkpoint (use Select).
type durableContext struct {
	wfCtx     workflow.Context
	rt        *Runtime
	runID     string
	tenant    core.TenantID
	threshold int
	hostInput HostInput
	cont      *Continuation

	stepFutures  map[string]runtime.Future
	childFutures map[string]runtime.Future
	childFlows   map[string]runtime.FlowRef
	gates        map[string]*gateState

	timerIdx, selectIdx, nowIdx             int
	replayTimers, replaySelects, replayNows int
	checkpointCalls, emitCalls              uint64

	idSeq        map[string]int
	randoms      map[string]*rand.Rand
	timerFutures []workflow.Future

	inFlight    []runtime.Future
	continuing  bool
	continueErr error
}

func newDurableContext(wfCtx workflow.Context, rt *Runtime, in HostInput, tenant core.TenantID) *durableContext {
	runID := in.RunID
	if runID == "" {
		runID = workflow.GetInfo(wfCtx).WorkflowExecution.ID
	}
	cont := in.Continuation
	if cont == nil {
		cont = &Continuation{}
	}
	cont.ensure()
	threshold := rt.checkpointThreshold
	dc := &durableContext{
		wfCtx:         wfCtx,
		rt:            rt,
		runID:         runID,
		tenant:        tenant,
		threshold:     threshold,
		hostInput:     in,
		cont:          cont,
		stepFutures:   map[string]runtime.Future{},
		childFutures:  map[string]runtime.Future{},
		childFlows:    map[string]runtime.FlowRef{},
		gates:         map[string]*gateState{},
		idSeq:         map[string]int{},
		randoms:       map[string]*rand.Rand{},
		replayNows:    len(cont.Nows),
		replayTimers:  len(cont.Timers),
		replaySelects: len(cont.Selects),
		timerFutures:  make([]workflow.Future, len(cont.Timers)),
	}
	dc.startGateCancelWatcher()
	return dc
}

func (dc *durableContext) Context() context.Context {
	ctx := context.Background()
	if dc.tenant != "" {
		ctx = core.WithTenant(ctx, dc.tenant)
	}
	return ctx
}

func (dc *durableContext) Now() time.Time {
	if dc.nowIdx < dc.replayNows {
		t := dc.cont.Nows[dc.nowIdx]
		dc.nowIdx++
		return t
	}
	now := workflow.Now(dc.wfCtx)
	dc.cont.Nows = append(dc.cont.Nows, now)
	return now
}

func (dc *durableContext) Capabilities() runtime.Caps {
	return runtime.Caps{Durable: true, SyncGateUpdates: true, ContinueAsNew: true, PolyglotQueues: true}
}

// NewID and Rand counters are not carried across ContinueAsNew: the flow
// re-executes from the start and calls them in the same order, so per-execution
// counters starting at zero reproduce the same ids and draws (seeds from run id
// and key), then continue naturally past the replayed calls.
func (dc *durableContext) NewID(kind string) string {
	dc.idSeq[kind]++
	return fmt.Sprintf("%s_%d", kind, dc.idSeq[kind])
}

func seededRand(runID, key string) *rand.Rand {
	h := fnv64(runID)
	h ^= fnv64(key)
	h *= 1099511628211
	return rand.New(rand.NewPCG(h, h^0x9e3779b97f4a7c15))
}

func (dc *durableContext) Rand(key string) *rand.Rand {
	if src := dc.randoms[key]; src != nil {
		return src
	}
	src := seededRand(dc.runID, key)
	dc.randoms[key] = src
	return src
}

func (dc *durableContext) Step(name string, in any, o runtime.StepOptions) runtime.Future {
	if dc.continuing {
		return newImmediate(cancelledContinuing())
	}
	key := stepKey(name)
	if out, ok := dc.cont.Steps[key]; ok {
		return journalStepFuture(out)
	}
	if existing := dc.stepFutures[name]; existing != nil {
		return existing
	}
	raw, err := rawInput(in)
	if err != nil {
		f := newImmediate(err)
		dc.stepFutures[name] = f
		return f
	}
	reg, ok := dc.rt.stepFor(name)
	if !ok {
		f := newImmediate(core.NewError(core.CodeUnsupported,
			fmt.Sprintf("temporal: step %q is not registered", name), map[string]any{"step": name}))
		dc.stepFutures[name] = f
		return f
	}
	opts := mergeOptions(o, reg.defaults.Options)
	queue := dc.rt.taskQueue
	if opts.Queue != "" {
		queue = opts.Queue
	}
	startToClose := opts.StartToClose
	if startToClose <= 0 {
		startToClose = defaultStartToClose
	}
	activityID := opts.IdempotencyKey
	if activityID == "" {
		activityID = name
	}
	actCtx := workflow.WithActivityOptions(dc.wfCtx, workflow.ActivityOptions{
		ActivityID:          activityID,
		TaskQueue:           queue,
		StartToCloseTimeout: startToClose,
		HeartbeatTimeout:    opts.Heartbeat,
		RetryPolicy:         activityRetryPolicy(opts),
	})
	var fut workflow.Future
	var early runtime.Future
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				if _, ok := recovered.(taskFailure); ok {
					panic(recovered)
				}
				var pe *core.Error
				if errors.As(errorFromPanic(recovered), &pe) {
					early = newImmediate(pe)
					dc.stepFutures[name] = early
					return
				}
				panic(recovered)
			}
		}()
		fut = workflow.ExecuteActivity(actCtx, stepActivityName, StepActivityInput{
			Name: name,
			In:   raw,
		})
	}()
	if early != nil {
		dc.trackInFlight(early)
		return early
	}
	wrapped := &stepFuture{ctx: actCtx, f: fut, done: func(res json.RawMessage, err error) {
		if err != nil {
			dc.recordStep(key, nil, activityToCore(err))
			return
		}
		dc.recordStep(key, res, nil)
	}}
	dc.stepFutures[name] = wrapped
	dc.trackInFlight(wrapped)
	return wrapped
}

func (dc *durableContext) Sleep(d time.Duration) runtime.Future {
	if dc.continuing {
		return newImmediate(cancelledContinuing())
	}
	if d <= 0 {
		return newImmediate(nil)
	}
	if dc.timerIdx < dc.replayTimers {
		idx := dc.timerIdx
		j := dc.cont.Timers[idx]
		dc.timerIdx++
		if j.Fired {
			return newImmediate(nil)
		}
		remaining := j.Deadline.Sub(workflow.Now(dc.wfCtx))
		if remaining <= 0 {
			dc.cont.Timers[idx].Fired = true
			return newImmediate(nil)
		}
		fut := workflow.NewTimer(dc.wfCtx, remaining)
		dc.timerFutures[idx] = fut
		return &timerFuture{ctx: dc.wfCtx, f: fut}
	}
	now := workflow.Now(dc.wfCtx)
	deadline := now.Add(d)
	dc.cont.Timers = append(dc.cont.Timers, TimerJournal{Deadline: deadline, Fired: false})
	fut := workflow.NewTimer(dc.wfCtx, d)
	dc.timerFutures = append(dc.timerFutures, fut)
	return &timerFuture{ctx: dc.wfCtx, f: fut}
}

func (dc *durableContext) Child(flow runtime.FlowRef, in any, o runtime.ChildOptions) runtime.Future {
	if dc.continuing {
		return newImmediate(cancelledContinuing())
	}
	policy, err := parentClosePolicy(o.ParentClose)
	childID := o.ID
	if err != nil {
		return newImmediate(err)
	}
	if childID == "" {
		childID = dc.NewID("child")
	}
	if out, ok := dc.cont.Children[childID]; ok {
		if out.Flow != flow {
			return newImmediate(childFlowConflict(childID))
		}
		return journalChildFuture(out)
	}
	if existing := dc.childFutures[childID]; existing != nil {
		if dc.childFlows[childID] != flow {
			return newImmediate(childFlowConflict(childID))
		}
		return existing
	}
	raw, err := rawInput(in)
	if err != nil {
		return newImmediate(err)
	}
	childWfID := workflowID(dc.tenant, childID)
	if childWfID == "" {
		return newImmediate(core.NewError(core.CodeInvalidArgument,
			"temporal: child id cannot contain \"/\"", map[string]any{"child": childID}))
	}
	cwo := workflow.ChildWorkflowOptions{
		WorkflowID:        childWfID,
		ParentClosePolicy: policy,
		TaskQueue:         dc.rt.taskQueue,
	}
	ctx := workflow.WithChildOptions(dc.wfCtx, cwo)
	childIn := HostInput{Flow: flow, Input: raw, RunID: childID}
	var fut workflow.ChildWorkflowFuture
	var early runtime.Future
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				if _, ok := recovered.(taskFailure); ok {
					panic(recovered)
				}
				var pe *core.Error
				if errors.As(errorFromPanic(recovered), &pe) {
					early = newImmediate(pe)
					return
				}
				panic(recovered)
			}
		}()
		fut = workflow.ExecuteChildWorkflow(ctx, HostWorkflowName, childIn)
	}()
	if early != nil {
		dc.trackInFlight(early)
		return early
	}
	wrapped := &childFuture{ctx: dc.wfCtx, f: fut, done: func(res json.RawMessage, err error) {
		dc.recordChild(childID, flow, res, err)
	}}
	dc.childFutures[childID] = wrapped
	dc.childFlows[childID] = flow
	dc.trackInFlight(wrapped)
	return wrapped
}

func childFlowConflict(id string) error {
	return core.NewError(core.CodeConflict, "temporal: child id already names a different flow", map[string]any{"child": id})
}

func (dc *durableContext) Select(fs ...runtime.Future) (int, error) {
	if dc.continuing {
		return 0, cancelledContinuing()
	}
	if len(fs) == 0 {
		return 0, core.NewError(core.CodeInvalidArgument, "temporal: Select needs at least one future", nil)
	}
	if dc.selectIdx < dc.replaySelects {
		idx := dc.cont.Selects[dc.selectIdx]
		dc.selectIdx++
		if idx >= 0 && idx < len(fs) {
			return idx, nil
		}
		return 0, core.NewError(core.CodeInternal, "temporal: replayed select does not match the flow", nil)
	}
	for i, f := range fs {
		if f.Ready() {
			dc.appendSelect(i)
			return i, nil
		}
	}
	sel := workflow.NewSelector(dc.wfCtx)
	chosen := -1
	for i, f := range fs {
		idx := i
		switch tf := f.(type) {
		case *stepFuture:
			sel.AddFuture(tf.f, func(workflow.Future) { chosen = idx })
		case *timerFuture:
			sel.AddFuture(tf.f, func(workflow.Future) { chosen = idx })
		case *gateFuture:
			sel.AddFuture(tf.g.wfFut, func(workflow.Future) { chosen = idx })
		case *childFuture:
			sel.AddFuture(tf.f, func(workflow.Future) { chosen = idx })
		case *immediateFuture:
			if tf.Ready() {
				dc.appendSelect(idx)
				return idx, nil
			}
		default:
			return 0, core.NewError(core.CodeInternal, "temporal: unknown future kind in Select", nil)
		}
	}
	sel.Select(dc.wfCtx)
	if chosen < 0 {
		for i, f := range fs {
			if f.Ready() {
				dc.appendSelect(i)
				return i, nil
			}
		}
		return 0, core.NewError(core.CodeInternal, "temporal: select finished with no ready future", nil)
	}
	dc.appendSelect(chosen)
	return chosen, nil
}

func (dc *durableContext) appendSelect(i int) {
	dc.cont.Selects = append(dc.cont.Selects, i)
}

func (dc *durableContext) trackInFlight(f runtime.Future) {
	dc.inFlight = append(dc.inFlight, f)
}

func (dc *durableContext) compactInFlight() {
	alive := dc.inFlight[:0]
	for _, f := range dc.inFlight {
		if f != nil && !f.Ready() {
			alive = append(alive, f)
		}
	}
	dc.inFlight = alive
}

func (dc *durableContext) materializeReadyOutcomes() {
	for _, f := range dc.stepFutures {
		if sf, ok := f.(*stepFuture); ok {
			sf.materialize()
		}
	}
	for _, f := range dc.childFutures {
		if cf, ok := f.(*childFuture); ok {
			cf.materialize()
		}
	}
}

func (dc *durableContext) recordStep(key string, res json.RawMessage, err error) {
	dc.cont.ensure()
	dc.cont.Steps[key] = StepOutcome{Result: append(json.RawMessage(nil), res...), Err: errToJournal(err)}
}

func (dc *durableContext) recordChild(id string, flow runtime.FlowRef, res json.RawMessage, err error) {
	dc.cont.ensure()
	dc.cont.Children[id] = ChildOutcome{
		Flow:   flow,
		Result: append(json.RawMessage(nil), res...),
		Err:    errToJournal(err),
	}
}

func journalStepFuture(out StepOutcome) runtime.Future {
	if out.Err != nil {
		return newImmediate(journalToErr(out.Err))
	}
	return newImmediateResult(out.Result)
}

func journalChildFuture(out ChildOutcome) runtime.Future {
	if out.Err != nil {
		return newImmediate(journalToErr(out.Err))
	}
	return newImmediateResult(out.Result)
}
