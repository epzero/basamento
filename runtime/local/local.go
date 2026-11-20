package local

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// Options configures a Runtime. Both ports come from the composition root:
// core.SystemClock and core.ULIDs in the binary, coretest.FakeClock and
// coretest.SeqIds in the gate (see specs/runtime/design.md §Local runtime).
type Options struct {
	// Clock supplies Now, StartedAt, ClosedAt and timer wake-ups. It must
	// implement After(d) <-chan time.Time. nil means core.SystemClock.
	Clock core.Clock
	// IDs issues run ids when StartRequest.RunID is empty. nil means ULIDs
	// over Clock.
	IDs core.Ids
}

// wakeClock is the clock port the local runtime uses for Now and timer wake-ups.
// core.SystemClock and coretest.FakeClock implement it.
type wakeClock interface {
	core.Clock
	After(time.Duration) <-chan time.Time
}

// Run states. The set is the contract's (runtime.RunStatus.State).
const (
	stateRunning    = "running"
	stateCompleted  = "completed"
	stateFailed     = "failed"
	stateCancelled  = "cancelled"
	stateTerminated = "terminated"
)

// defaultAttempts is the attempt limit of a step whose StepOptions.MaxAttempts
// is 0 (REQ-RT-018, specs/runtime/design.md §Steps and retries).
const defaultAttempts = 3

// defaultParentClose is the close policy of a child a parent does not name.
const defaultParentClose = "terminate"

type runKey struct {
	tenant core.TenantID
	id     string
}

type idemKey struct {
	tenant core.TenantID
	key    string
}

// Runtime is the in-process implementation of runtime.Runtime.
type Runtime struct {
	mu    sync.Mutex
	clock wakeClock
	ids   core.Ids
	host  runtime.FlowHost
	steps map[string]registeredStep

	runs  map[runKey]*run
	order []*run
	idem  map[idemKey]*run

	serving bool
	closed  bool
	wg      sync.WaitGroup
}

type registeredStep struct {
	fn       runtime.StepFunc
	defaults runtime.StepDefaults
}

// New returns a local runtime. It starts empty: it has no record of any run
// from an earlier instance (REQ-RT-006).
func New(opts Options) (*Runtime, error) {
	clock := opts.Clock
	if clock == nil {
		clock = core.SystemClock{}
	}
	wake, ok := clock.(wakeClock)
	if !ok {
		return nil, core.NewError(core.CodeInvalidArgument,
			"local: the clock must implement After(duration)", nil)
	}
	r := &Runtime{
		clock: wake,
		ids:   opts.IDs,
		steps: map[string]registeredStep{},
		runs:  map[runKey]*run{},
		idem:  map[idemKey]*run{},
	}
	if r.ids == nil {
		r.ids = core.NewULIDs(r.clock, nil)
	}
	return r, nil
}

// RegisterFlowHost installs the single flow entrypoint the composition root
// provides (ADR-0011). A run started before a host is registered fails.
func (r *Runtime) RegisterFlowHost(h runtime.FlowHost) error {
	if h == nil {
		return core.NewError(core.CodeInvalidArgument, "local: nil flow host", nil)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.host != nil {
		return core.NewError(core.CodeConflict, "local: a flow host is already registered", nil)
	}
	r.host = h
	return nil
}

// RegisterStep registers a side-effecting step by name.
func (r *Runtime) RegisterStep(name string, fn runtime.StepFunc, d runtime.StepDefaults) error {
	if name == "" || fn == nil {
		return core.NewError(core.CodeInvalidArgument, "local: a step needs a name and a function", map[string]any{"step": name})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.steps[name]; ok {
		return core.NewError(core.CodeConflict, fmt.Sprintf("local: step %q is already registered", name), map[string]any{"step": name})
	}
	r.steps[name] = registeredStep{fn: fn, defaults: d}
	return nil
}

// Capabilities reports what the local runtime supports: nothing durable, just
// synchronous gate resolution (REQ-RT-005).
func (r *Runtime) Capabilities() runtime.Caps {
	return runtime.Caps{
		Durable:         false,
		SyncGateUpdates: true,
		ContinueAsNew:   false,
		PolyglotQueues:  false,
	}
}

// Start starts a run of req.Flow. The tenant comes from ctx and must agree with
// req.TenantID when that field is set (REQ-RT-042, REQ-RT-044).
func (r *Runtime) Start(ctx context.Context, req runtime.StartRequest) (runtime.RunRef, error) {
	tenant, err := r.tenant(ctx, req.TenantID)
	if err != nil {
		return runtime.RunRef{}, err
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return runtime.RunRef{}, core.NewError(core.CodeUnavailable, "local: the runtime has shut down", nil)
	}
	if r.host == nil {
		r.mu.Unlock()
		return runtime.RunRef{}, core.NewError(core.CodeUnavailable, "local: no flow host is registered", nil)
	}
	if req.IdempotencyKey != "" {
		if existing := r.idem[idemKey{tenant: tenant, key: req.IdempotencyKey}]; existing != nil {
			ref := existing.ref()
			r.mu.Unlock()
			return ref, nil
		}
	}
	runID := req.RunID
	if runID == "" {
		runID, err = r.ids.New("run")
		if err != nil {
			r.mu.Unlock()
			return runtime.RunRef{}, core.NewError(core.CodeInternal, "local: generate run id", map[string]any{"cause": err.Error()})
		}
	}
	key := runKey{tenant: tenant, id: runID}
	if r.runs[key] != nil {
		r.mu.Unlock()
		return runtime.RunRef{}, core.NewError(core.CodeConflict, fmt.Sprintf("local: run %q already exists", runID), map[string]any{"run": runID})
	}
	started := newRun(r, tenant, runID, req.Flow, req.Input, ctx)
	r.runs[key] = started
	r.order = append(r.order, started)
	if req.IdempotencyKey != "" {
		r.idem[idemKey{tenant: tenant, key: req.IdempotencyKey}] = started
	}
	r.wg.Add(1)
	r.mu.Unlock()

	go func() {
		defer r.wg.Done()
		started.execute()
	}()
	return started.ref(), nil
}

// startChild starts a child run in tenant without checking a context tenant:
// the parent's tenant is the child's (REQ-RT-014).
func (r *Runtime) startChild(tenant core.TenantID, id string, flow runtime.FlowRef, in json.RawMessage, parent *run) (*run, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, core.NewError(core.CodeUnavailable, "local: the runtime has shut down", nil)
	}
	if r.host == nil {
		r.mu.Unlock()
		return nil, core.NewError(core.CodeUnavailable, "local: no flow host is registered", nil)
	}
	key := runKey{tenant: tenant, id: id}
	existing := r.runs[key]
	if existing != nil {
		r.mu.Unlock()
		return existing, nil
	}
	ctx := context.WithoutCancel(parent.ctx)
	started := newRun(r, tenant, id, flow, in, ctx)
	r.runs[key] = started
	r.order = append(r.order, started)
	r.wg.Add(1)
	r.mu.Unlock()

	go func() {
		defer r.wg.Done()
		started.execute()
	}()
	return started, nil
}

// Resolve hands payload to the run waiting at gate. The tenant comes from ctx
// and must agree with run.TenantID when set (REQ-RT-009, REQ-RT-010,
// REQ-RT-042, REQ-RT-044, REQ-RT-025).
func (r *Runtime) Resolve(ctx context.Context, ref runtime.RunRef, gate string, payload json.RawMessage) error {
	tenant, err := r.tenant(ctx, ref.TenantID)
	if err != nil {
		return err
	}
	target := r.lookup(tenant, ref.RunID)
	if target == nil {
		return notFound(ref.RunID)
	}
	return target.resolveGate(gate, payload)
}

// Cancel cancels a run and delivers cancellation to its in-flight steps and
// waits (REQ-RT-012, REQ-RT-013).
func (r *Runtime) Cancel(ctx context.Context, ref runtime.RunRef, reason string) error {
	tenant, err := r.tenant(ctx, ref.TenantID)
	if err != nil {
		return err
	}
	target := r.lookup(tenant, ref.RunID)
	if target == nil {
		return notFound(ref.RunID)
	}
	target.cancelRun(reason)
	return nil
}

// Describe returns a run's observable state. The tenant comes from ctx and must
// agree with ref.TenantID when set (REQ-RT-025, REQ-RT-042, REQ-RT-043).
func (r *Runtime) Describe(ctx context.Context, ref runtime.RunRef) (runtime.RunStatus, error) {
	tenant, err := r.tenant(ctx, ref.TenantID)
	if err != nil {
		return runtime.RunStatus{}, err
	}
	target := r.lookup(tenant, ref.RunID)
	if target == nil {
		return runtime.RunStatus{}, notFound(ref.RunID)
	}
	return target.status(), nil
}

// List returns the calling tenant's runs, optionally filtered by flow and
// state (REQ-RT-026, REQ-RT-043).
func (r *Runtime) List(ctx context.Context, q runtime.RunQuery) ([]runtime.RunStatus, string, error) {
	tenant, err := r.tenant(ctx, q.TenantID)
	if err != nil {
		return nil, "", err
	}
	r.mu.Lock()
	runs := append([]*run(nil), r.order...)
	r.mu.Unlock()

	out := make([]runtime.RunStatus, 0, len(runs))
	for _, candidate := range runs {
		status := candidate.status()
		if status.Ref.TenantID != tenant {
			continue
		}
		if q.Flow != "" && candidate.flow.Name != q.Flow {
			continue
		}
		if q.State != "" && status.State != q.State {
			continue
		}
		out = append(out, status)
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
	}
	return out, "", nil
}

// Serve marks the runtime as serving and waits for ctx. On cancellation it
// stops accepting runs (REQ-RT-024), cancels every in-flight run's context
// without changing its state (REQ-RT-022) and returns once every goroutine has
// stopped (REQ-RT-023).
func (r *Runtime) Serve(ctx context.Context) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		r.wg.Wait()
		return nil
	}
	r.serving = true
	r.mu.Unlock()

	<-ctx.Done()

	r.mu.Lock()
	r.closed = true
	runs := append([]*run(nil), r.order...)
	r.mu.Unlock()

	for _, candidate := range runs {
		candidate.shutdown()
	}
	r.wg.Wait()
	return nil
}

func (r *Runtime) lookup(tenant core.TenantID, id string) *run {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs[runKey{tenant: tenant, id: id}]
}

func (r *Runtime) after(d time.Duration) <-chan time.Time {
	return r.clock.After(d)
}

// tenant enforces the shared rule: the tenant of a call is the context's, and
// a tenant field may only be empty or equal to it (specs/runtime/design.md
// §The request tenant follows the context).
func (r *Runtime) tenant(ctx context.Context, field core.TenantID) (core.TenantID, error) {
	id, ok := core.TenantFrom(ctx)
	if !ok {
		return "", core.NewError(core.CodeInvalidArgument, "local: no tenant in the context", nil)
	}
	if field != "" && field != id {
		return "", core.NewError(core.CodeInvalidArgument, "local: the request tenant differs from the context tenant",
			map[string]any{"context_tenant": string(id), "request_tenant": string(field)})
	}
	return id, nil
}

func notFound(runID string) error {
	return core.NewError(core.CodeNotFound, fmt.Sprintf("local: run %q not found", runID), map[string]any{"run": runID})
}

func cancelledErr(message string) error {
	return core.NewError(core.CodeCancelled, message, nil)
}

// seededRand returns the deterministic random source of one key on one run
// (REQ: DurableContext.Rand is seeded from (run, key)).
func seededRand(runID, key string) *rand.Rand {
	h := fnv64(runID)
	h ^= fnv64(key)
	h *= 1099511628211
	return rand.New(rand.NewPCG(h, h^0x9e3779b97f4a7c15))
}

func fnv64(s string) uint64 {
	const offset, prime = 14695981039346656037, 1099511628211
	h := uint64(offset)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime
	}
	return h
}
