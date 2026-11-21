package temporal

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// durableContext implements runtime.DurableContext over a Temporal workflow.
type durableContext struct {
	wfCtx       workflow.Context
	rt          *Runtime
	runID       string
	tenant      core.TenantID
	stepFutures map[string]runtime.Future
	randoms     map[string]*rand.Rand
	// idSeq and randoms change during the run; PR (b) must carry them across ContinueAsNew.
	idSeq map[string]int
}

func newDurableContext(wfCtx workflow.Context, rt *Runtime, runID string, tenant core.TenantID) *durableContext {
	return &durableContext{
		wfCtx:       wfCtx,
		rt:          rt,
		runID:       runID,
		tenant:      tenant,
		stepFutures: map[string]runtime.Future{},
		randoms:     map[string]*rand.Rand{},
		idSeq:       map[string]int{},
	}
}

func (dc *durableContext) Context() context.Context {
	ctx := context.Background()
	if dc.tenant != "" {
		ctx = core.WithTenant(ctx, dc.tenant)
	}
	return ctx
}

func (dc *durableContext) Now() time.Time { return workflow.Now(dc.wfCtx) }

func (dc *durableContext) Capabilities() runtime.Caps {
	return runtime.Caps{Durable: true, PolyglotQueues: true}
}

func (dc *durableContext) NewID(kind string) string {
	dc.idSeq[kind]++
	return fmt.Sprintf("%s_%d", kind, dc.idSeq[kind])
}

func (dc *durableContext) Rand(key string) *rand.Rand {
	if src := dc.randoms[key]; src != nil {
		return src
	}
	src := seededRand(dc.runID, key)
	dc.randoms[key] = src
	return src
}

func seededRand(runID, key string) *rand.Rand {
	h := fnv64(runID)
	h ^= fnv64(key)
	h *= 1099511628211
	return rand.New(rand.NewPCG(h, h^0x9e3779b97f4a7c15))
}

func (dc *durableContext) Step(name string, in any, o runtime.StepOptions) runtime.Future {
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
	wfID := workflow.GetInfo(dc.wfCtx).WorkflowExecution.ID
	var fut workflow.Future
	var early runtime.Future
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				// Encode panics before any activity command is created; safe to memoize permanent failures.
				if pe := dc.rt.handleCodecPanic(dc.tenant, wfID, name, recovered); pe != nil {
					early = newImmediate(pe)
					dc.stepFutures[name] = early
				}
			}
		}()
		fut = workflow.ExecuteActivity(actCtx, stepActivityName, stepActivityInput{
			Name: name,
			In:   raw,
		})
	}()
	if early != nil {
		return early
	}
	wrapped := &stepFuture{
		ctx: actCtx, f: fut,
		rt: dc.rt, tenant: dc.tenant, wfID: wfID, step: name,
	}
	dc.stepFutures[name] = wrapped
	return wrapped
}

func (dc *durableContext) Sleep(d time.Duration) runtime.Future {
	if d <= 0 {
		return newImmediate(nil)
	}
	fut := workflow.NewTimer(dc.wfCtx, d)
	return &timerFuture{ctx: dc.wfCtx, f: fut}
}

func (dc *durableContext) Select(fs ...runtime.Future) (int, error) {
	if len(fs) == 0 {
		return 0, core.NewError(core.CodeInvalidArgument, "temporal: Select needs at least one future", nil)
	}
	for i, f := range fs {
		if f.Ready() {
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
		case *immediateFuture:
			if tf.Ready() {
				return idx, nil
			}
		}
	}
	sel.Select(dc.wfCtx)
	if chosen < 0 {
		for i, f := range fs {
			if f.Ready() {
				return i, nil
			}
		}
		return 0, core.NewError(core.CodeInternal, "temporal: select finished with no ready future", nil)
	}
	return chosen, nil
}

// Child is unimplemented until WP-L5-02 pull request (b) (EP0-31).
func (dc *durableContext) Child(runtime.FlowRef, any, runtime.ChildOptions) runtime.Future {
	return newImmediate(unsupportedOp("Child"))
}

// Await is unimplemented until WP-L5-02 pull request (b) (EP0-31).
func (dc *durableContext) Await(runtime.GateSpec) runtime.Future {
	return newImmediate(unsupportedOp("Await"))
}

// Emit is unimplemented until WP-L5-02 pull request (b) (EP0-31).
func (dc *durableContext) Emit(runtime.ProgressEvent) error {
	return unsupportedOp("Emit")
}

// Checkpoint is unimplemented until WP-L5-02 pull request (b) (EP0-31).
func (dc *durableContext) Checkpoint(runtime.SnapshotRef) error {
	return unsupportedOp("Checkpoint")
}
