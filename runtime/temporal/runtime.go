package temporal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// Pinned server, API and SDK versions. The temporalio/cli release this project
// follows ships server v1.32.2, API v1.63.5 and SDK v1.47.0
// (runtime/temporal/embedded/BENCHMARKS.md).
const (
	// PinnedServerVersion is the server version the gate's test server reports.
	PinnedServerVersion = "1.32.2"
	// PinnedServerMajorMinor is the accepted server major and minor version.
	PinnedServerMajorMinor = "1.32"
)

// Options configure a Runtime. The clock, id source and payload codec come from
// the composition root: core.SystemClock and core.ULIDs with the real codec in
// the binary; deterministic fakes and a simulated key service in the gate.
type Options struct {
	Clock core.Clock
	IDs   core.Ids
	Codec PayloadCodec
	// CheckpointThreshold is the event threshold above which an explicit
	// Checkpoint continues the run as new. Zero disables ContinueAsNew.
	CheckpointThreshold int
	// ServerVersion is the Temporal server version Serve accepts. Empty means
	// PinnedServerVersion.
	ServerVersion string
}

type runKey struct {
	tenant core.TenantID
	id     string
}

type idemKey struct {
	tenant core.TenantID
	key    string
}

type registeredStep struct {
	fn       runtime.StepFunc
	defaults runtime.StepDefaults
}

// Runtime is the Temporal runtime: the flow host runs as one workflow, each
// Step as an activity, gates as synchronous updates and Checkpoint as
// ContinueAsNew, all behind runtime.Runtime.
type Runtime struct {
	mu    sync.Mutex
	clock core.Clock
	ids   core.Ids
	codec PayloadCodec
	host  runtime.FlowHost
	steps map[string]registeredStep

	checkpointThreshold int
	serverVersion       string

	runs  map[runKey]*run
	order []*run
	idem  map[idemKey]*run

	serving bool
	closed  bool
	ready   chan struct{}
	wg      sync.WaitGroup
}

// New returns a Temporal runtime. It starts empty and serves no run until a
// flow host is registered. A nil payload codec is refused: the runtime never
// writes an unencrypted history (REQ-RT-031).
func New(opts Options) (*Runtime, error) {
	if opts.Codec == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "temporal: a payload codec is required", nil)
	}
	r := &Runtime{
		clock:               opts.Clock,
		ids:                 opts.IDs,
		codec:               opts.Codec,
		steps:               map[string]registeredStep{},
		runs:                map[runKey]*run{},
		idem:                map[idemKey]*run{},
		checkpointThreshold: opts.CheckpointThreshold,
		serverVersion:       opts.ServerVersion,
		ready:               make(chan struct{}),
	}
	if r.clock == nil {
		r.clock = core.SystemClock{}
	}
	if r.ids == nil {
		r.ids = core.NewULIDs(r.clock, nil)
	}
	if r.serverVersion == "" {
		r.serverVersion = PinnedServerVersion
	}
	return r, nil
}

// RegisterFlowHost installs the single flow entrypoint the composition root
// provides (ADR-0011).
func (r *Runtime) RegisterFlowHost(h runtime.FlowHost) error {
	if h == nil {
		return core.NewError(core.CodeInvalidArgument, "temporal: nil flow host", nil)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.host != nil {
		return core.NewError(core.CodeConflict, "temporal: a flow host is already registered", nil)
	}
	r.host = h
	return nil
}

// RegisterStep registers a side-effecting step by name.
func (r *Runtime) RegisterStep(name string, fn runtime.StepFunc, d runtime.StepDefaults) error {
	if name == "" || fn == nil {
		return core.NewError(core.CodeInvalidArgument, "temporal: a step needs a name and a function", map[string]any{"step": name})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.steps[name]; ok {
		return core.NewError(core.CodeConflict, fmt.Sprintf("temporal: step %q is already registered", name), map[string]any{"step": name})
	}
	r.steps[name] = registeredStep{fn: fn, defaults: d}
	return nil
}

// Capabilities reports every Temporal trait as available (REQ-RT-039).
func (r *Runtime) Capabilities() runtime.Caps {
	return runtime.Caps{
		Durable:         true,
		SyncGateUpdates: true,
		ContinueAsNew:   true,
		PolyglotQueues:  true,
	}
}

// Start starts a run. The tenant comes from ctx and must agree with
// req.TenantID when that field is set; a start without a tenant is rejected
// before any step or history (REQ-RT-040, REQ-RT-042).
func (r *Runtime) Start(ctx context.Context, req runtime.StartRequest) (runtime.RunRef, error) {
	tenant, err := r.tenant(ctx, req.TenantID)
	if err != nil {
		return runtime.RunRef{}, err
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return runtime.RunRef{}, core.NewError(core.CodeUnavailable, "temporal: the runtime has shut down", nil)
	}
	if r.host == nil {
		r.mu.Unlock()
		return runtime.RunRef{}, core.NewError(core.CodeUnavailable, "temporal: no flow host is registered", nil)
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
			return runtime.RunRef{}, core.NewError(core.CodeInternal, "temporal: generate run id", map[string]any{"cause": err.Error()})
		}
	}
	key := runKey{tenant: tenant, id: runID}
	if r.runs[key] != nil {
		r.mu.Unlock()
		return runtime.RunRef{}, core.NewError(core.CodeConflict, fmt.Sprintf("temporal: run %q already exists", runID), map[string]any{"run": runID})
	}
	started := newRun(r, tenant, runID, req.Flow, req.Input, ctx)
	started.record(eventWorkflowStarted, "", 0, nil)
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
// the parent's tenant is the child's.
func (r *Runtime) startChild(tenant core.TenantID, id string, flow runtime.FlowRef, in json.RawMessage, parent *run) (*run, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, core.NewError(core.CodeUnavailable, "temporal: the runtime has shut down", nil)
	}
	if r.host == nil {
		r.mu.Unlock()
		return nil, core.NewError(core.CodeUnavailable, "temporal: no flow host is registered", nil)
	}
	key := runKey{tenant: tenant, id: id}
	existing := r.runs[key]
	if existing != nil {
		r.mu.Unlock()
		return existing, nil
	}
	ctx := context.WithoutCancel(parent.ctx)
	started := newRun(r, tenant, id, flow, in, ctx)
	started.record(eventWorkflowStarted, "", 0, nil)
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

// Resolve delivers a gate payload as a synchronous update (REQ-RT-009,
// REQ-RT-010, REQ-RT-042).
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

// Cancel cancels a run and delivers cancellation to its in-flight steps.
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

// Describe returns a run's observable state (REQ-RT-043).
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

// List returns the calling tenant's runs (REQ-RT-043).
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

// Result returns a closed run's decoded output.
func (r *Runtime) Result(ctx context.Context, ref runtime.RunRef) (json.RawMessage, error) {
	tenant, err := r.tenant(ctx, ref.TenantID)
	if err != nil {
		return nil, err
	}
	target := r.lookup(tenant, ref.RunID)
	if target == nil {
		return nil, notFound(ref.RunID)
	}
	target.mu.Lock()
	state, out, runErr := target.state, target.output, target.err
	target.mu.Unlock()
	switch state {
	case stateRunning:
		return nil, core.NewError(core.CodeUnavailable, "temporal: the run is still running", map[string]any{"run": ref.RunID})
	case stateFailed:
		return nil, runErr
	default:
		return append(json.RawMessage(nil), out...), nil
	}
}

// Wait blocks until a run leaves the running state or ctx ends.
func (r *Runtime) Wait(ctx context.Context, ref runtime.RunRef) error {
	tenant, err := r.tenant(ctx, ref.TenantID)
	if err != nil {
		return err
	}
	target := r.lookup(tenant, ref.RunID)
	if target == nil {
		return notFound(ref.RunID)
	}
	for {
		wake := target.wake()
		target.mu.Lock()
		state := target.state
		target.mu.Unlock()
		if state != stateRunning {
			return nil
		}
		select {
		case <-wake:
		case <-ctx.Done():
			return core.NewError(core.CodeCancelled, "temporal: wait cancelled", nil)
		}
	}
}

// WaitForGate blocks until a run is waiting at gate, or ctx ends.
func (r *Runtime) WaitForGate(ctx context.Context, ref runtime.RunRef, gate string) error {
	tenant, err := r.tenant(ctx, ref.TenantID)
	if err != nil {
		return err
	}
	target := r.lookup(tenant, ref.RunID)
	if target == nil {
		return notFound(ref.RunID)
	}
	for {
		wake := target.wake()
		target.mu.Lock()
		_, waiting := target.gates[gate]
		state := target.state
		target.mu.Unlock()
		if waiting {
			return nil
		}
		if state != stateRunning {
			return core.NewError(core.CodeConflict, "temporal: the run ended before the gate", map[string]any{"run": ref.RunID, "gate": gate})
		}
		select {
		case <-wake:
		case <-ctx.Done():
			return core.NewError(core.CodeCancelled, "temporal: wait cancelled", nil)
		}
	}
}

// Signal delivers a named signal to a run. A gate waiting with the same name
// receives the payload, so signals and updates share one mailbox.
func (r *Runtime) Signal(ctx context.Context, ref runtime.RunRef, name string, payload json.RawMessage) error {
	tenant, err := r.tenant(ctx, ref.TenantID)
	if err != nil {
		return err
	}
	target := r.lookup(tenant, ref.RunID)
	if target == nil {
		return notFound(ref.RunID)
	}
	encoded, err := target.encode(payload)
	if err != nil {
		return err
	}
	target.mu.Lock()
	if target.state != stateRunning {
		target.mu.Unlock()
		return core.NewError(core.CodeConflict, "temporal: the run is not running", map[string]any{"run": ref.RunID})
	}
	g := target.gates[name]
	if g != nil && !g.resolved {
		g.resolved = true
		target.recordLocked(eventSignal, name, 0, encoded)
		target.mu.Unlock()
		g.future.completeEncoded(encoded, payload, nil)
		target.notify()
		return nil
	}
	target.signals[name] = append(target.signals[name], append(json.RawMessage(nil), payload...))
	target.recordLocked(eventSignal, name, 0, encoded)
	target.mu.Unlock()
	target.notify()
	return nil
}

// Query answers a read-only question about a run without changing it. Known
// names are "status", "history_length" and "tenant"; any other name is refused.
func (r *Runtime) Query(ctx context.Context, ref runtime.RunRef, name string) (json.RawMessage, error) {
	status, err := r.Describe(ctx, ref)
	if err != nil {
		return nil, err
	}
	events, err := r.History(ctx, ref)
	if err != nil {
		return nil, err
	}
	var value any
	switch name {
	case "status":
		value = status.State
	case "history_length":
		value = len(events)
	case "tenant":
		value = string(status.Ref.TenantID)
	default:
		return nil, core.NewError(core.CodeNotFound, "temporal: unknown query", map[string]any{"query": name})
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, core.NewError(core.CodeInternal, "temporal: encode query result", map[string]any{"cause": err.Error()})
	}
	return raw, nil
}

// Serve checks the server version and serves until ctx ends. A server outside
// the pinned major and minor is refused with unsupported (REQ-RT-037).
func (r *Runtime) Serve(ctx context.Context) error {
	if !serverSupported(r.serverVersion) {
		return core.NewError(core.CodeUnsupported,
			fmt.Sprintf("temporal: server version %q is outside the pinned %s.x range", r.serverVersion, PinnedServerMajorMinor),
			map[string]any{"server_version": r.serverVersion, "pinned": PinnedServerMajorMinor})
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	if !r.serving {
		r.serving = true
		close(r.ready)
	}
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

// Started is closed once Serve has accepted the server and is serving.
func (r *Runtime) Started() <-chan struct{} { return r.ready }

// Serving reports whether Serve accepted the server.
func (r *Runtime) Serving() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.serving && !r.closed
}

// Replay re-executes a closed run's flow against its recorded journal and
// reports the first durable call that diverges from the recorded history. A
// matching replay executes no activity (REQ-RT-004).
func (r *Runtime) Replay(ctx context.Context, ref runtime.RunRef) error {
	tenant, err := r.tenant(ctx, ref.TenantID)
	if err != nil {
		return err
	}
	target := r.lookup(tenant, ref.RunID)
	if target == nil {
		return notFound(ref.RunID)
	}
	target.mu.Lock()
	if target.state == stateRunning {
		target.mu.Unlock()
		return core.NewError(core.CodeConflict, "temporal: cannot replay a running run", map[string]any{"run": ref.RunID})
	}
	recorded := append([]string(nil), target.calls...)
	steps := make(map[string]*stepRecord, len(target.steps))
	for name, step := range target.steps {
		steps[name] = step
	}
	gates := make(map[string]*gateRecord, len(target.gates))
	for name, gate := range target.gates {
		gates[name] = gate
	}
	flow, input := target.flow, append(json.RawMessage(nil), target.input...)
	target.mu.Unlock()

	replay := newRun(r, tenant, ref.RunID+"_replay", flow, input, ctx)
	replay.replaying = true
	for name, step := range steps {
		f := newFuture(replay, true)
		f.completeEncoded(step.encoded, step.raw, step.err)
		replay.steps[name] = &stepRecord{future: f, options: step.options, raw: step.raw, encoded: step.encoded, err: step.err, done: true}
	}
	for name, gate := range gates {
		f := newFuture(replay, true)
		if gate.resolved {
			f.completeRaw(gate.payload, nil)
		}
		replay.gates[name] = &gateRecord{spec: gate.spec, future: f, resolved: gate.resolved, payload: gate.payload}
	}

	out, runErr := r.host(&durableContext{run: replay}, flow, input)
	_ = out
	for i := range recorded {
		if i >= len(replay.calls) {
			return core.NewError(core.CodeDeterminismBroken,
				fmt.Sprintf("temporal: replay made %d of %d recorded durable calls", len(replay.calls), len(recorded)),
				map[string]any{"run": ref.RunID, "event": i})
		}
		if replay.calls[i] != recorded[i] {
			return core.NewError(core.CodeDeterminismBroken,
				fmt.Sprintf("temporal: replay diverged at durable call %d: got %q, want %q", i, replay.calls[i], recorded[i]),
				map[string]any{"run": ref.RunID, "event": i})
		}
	}
	if len(replay.calls) != len(recorded) {
		return core.NewError(core.CodeDeterminismBroken,
			fmt.Sprintf("temporal: replay made %d durable calls, recorded %d", len(replay.calls), len(recorded)),
			map[string]any{"run": ref.RunID, "event": len(recorded)})
	}
	if runErr != nil {
		return runErr
	}
	return nil
}

func (r *Runtime) lookup(tenant core.TenantID, id string) *run {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs[runKey{tenant: tenant, id: id}]
}

type timerClock interface {
	After(time.Duration) <-chan time.Time
}

func (r *Runtime) after(d time.Duration) <-chan time.Time {
	if clock, ok := r.clock.(timerClock); ok {
		return clock.After(d)
	}
	return time.After(d)
}

// tenant enforces the shared rule: the tenant of a call is the context's, and
// a tenant field may only be empty or equal to it (specs/runtime/design.md
// §The request tenant follows the context).
func (r *Runtime) tenant(ctx context.Context, field core.TenantID) (core.TenantID, error) {
	id, ok := core.TenantFrom(ctx)
	if !ok {
		return "", core.NewError(core.CodeInvalidArgument, "temporal: no tenant in the context", nil)
	}
	if field != "" && field != id {
		return "", core.NewError(core.CodeInvalidArgument, "temporal: the request tenant differs from the context tenant",
			map[string]any{"context_tenant": string(id), "request_tenant": string(field)})
	}
	return id, nil
}

func serverSupported(version string) bool {
	parts := strings.Split(strings.TrimPrefix(version, "v"), ".")
	if len(parts) < 2 {
		return false
	}
	return parts[0]+"."+parts[1] == PinnedServerMajorMinor
}
