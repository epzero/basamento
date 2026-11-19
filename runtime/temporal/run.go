package temporal

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// Run states. The set is the contract's (runtime.RunStatus.State).
const (
	stateRunning    = "running"
	stateCompleted  = "completed"
	stateFailed     = "failed"
	stateCancelled  = "cancelled"
	stateTerminated = "terminated"
)

// defaultAttempts is the attempt limit of a step whose StepOptions.MaxAttempts
// is 0. The contract leaves the number to the runtime; 3 matches the local
// runtime and Temporal's usual default (REQ-RT-018).
const defaultAttempts = 3

// defaultParentClose is the close policy of a child a parent does not name.
const defaultParentClose = "terminate"

// run is one Temporal workflow execution: its control state, its journal
// (step results, gates, children, outbox) and the goroutines it owns.
type run struct {
	rt     *Runtime
	tenant core.TenantID
	id     string
	flow   runtime.FlowRef
	input  json.RawMessage

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	wakeMu  sync.Mutex
	changed chan struct{} // closed and replaced on every completion

	state     string
	startedAt time.Time
	closedAt  *time.Time
	output    json.RawMessage
	err       error

	steps     map[string]*stepRecord
	gates     map[string]*gateRecord
	children  []*childLink
	waiters   []*future
	timers    []*runTimer
	outbox    []runtime.ProgressEvent
	signals   map[string][]json.RawMessage
	ids       map[string]int
	randoms   map[string]*rand.Rand
	history   []HistoryEvent
	seq       int64
	segment   int
	continues int
	calls     []string

	replaying bool
	shutting  bool
	stepWG    sync.WaitGroup
}

// stepRecord is the journal entry of one step name in a run.
type stepRecord struct {
	future  *future
	options runtime.StepOptions
	raw     json.RawMessage
	encoded *Encoded
	err     error
	done    bool
}

// gateRecord is the journal entry of one gate name in a run.
type gateRecord struct {
	spec     runtime.GateSpec
	future   *future
	resolved bool
	payload  json.RawMessage
}

type childLink struct {
	run    *run
	policy string
	future *future
}

type runTimer struct {
	deadline time.Time
	future   *future
	wake     <-chan time.Time
}

func newRun(rt *Runtime, tenant core.TenantID, id string, flow runtime.FlowRef, input json.RawMessage, parent context.Context) *run {
	base := context.WithoutCancel(parent)
	base = core.WithTenant(base, tenant)
	ctx, cancel := context.WithCancel(base)
	return &run{
		rt:        rt,
		tenant:    tenant,
		id:        id,
		flow:      flow,
		input:     append(json.RawMessage(nil), input...),
		ctx:       ctx,
		cancel:    cancel,
		changed:   make(chan struct{}),
		state:     stateRunning,
		startedAt: rt.clock.Now(),
		steps:     map[string]*stepRecord{},
		gates:     map[string]*gateRecord{},
		signals:   map[string][]json.RawMessage{},
		ids:       map[string]int{},
		randoms:   map[string]*rand.Rand{},
	}
}

func (r *run) ref() runtime.RunRef {
	return runtime.RunRef{RunID: r.id, TenantID: r.tenant}
}

// execute runs the registered flow host on this run's goroutine.
func (r *run) execute() {
	out, err := r.rt.host(&durableContext{run: r}, r.flow, r.input)
	r.finish(out, err)
}

// finish records the run's terminal state and the workflow result payload.
func (r *run) finish(out json.RawMessage, err error) {
	var encoded *Encoded
	historyErr := err
	if err == nil {
		encoded, err = r.encode(out)
	}
	if err != nil {
		encoded, historyErr = r.errorPayload(err)
	}

	r.mu.Lock()
	if r.state == stateRunning && !r.shutting {
		if historyErr != nil {
			r.state = stateFailed
			r.err = historyErr
			r.recordLocked(eventFailed, "", 0, encoded)
		} else {
			r.state = stateCompleted
			r.output = append(json.RawMessage(nil), out...)
			r.recordLocked(eventCompleted, "", 0, encoded)
		}
		r.closedAt = timePtr(r.rt.clock.Now())
		r.closeChildrenLocked()
	}
	waiters := append([]*future(nil), r.waiters...)
	state, out, runErr := r.state, r.output, r.err
	r.mu.Unlock()
	r.notify()
	if state != stateRunning {
		for _, f := range waiters {
			if state == stateFailed {
				f.completeRaw(nil, runErr)
			} else {
				f.completeRaw(out, nil)
			}
		}
	}
}

func (r *run) errorPayload(err error) (*Encoded, error) {
	raw, marshalErr := json.Marshal(map[string]any{"code": string(core.CodeOf(err)), "message": err.Error()})
	if marshalErr != nil {
		return nil, err
	}
	encoded, encodeErr := r.encode(raw)
	if encodeErr != nil {
		return nil, err
	}
	return encoded, err
}

func (r *run) encode(raw json.RawMessage) (*Encoded, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	encoded, err := r.rt.codec.Encode(r.ctx, jsonPayload(raw))
	if err != nil {
		return nil, err
	}
	return &encoded, nil
}

// closeChildrenLocked applies each in-flight child's close policy when the
// parent ends (REQ-RT-015).
func (r *run) closeChildrenLocked() {
	for _, link := range r.children {
		link.run.applyParentClose(link.policy)
	}
}

func (r *run) applyParentClose(policy string) {
	r.mu.Lock()
	if r.state != stateRunning {
		r.mu.Unlock()
		return
	}
	var state string
	switch policy {
	case "terminate":
		state = stateTerminated
	case "request_cancel":
		state = stateCancelled
	default: // abandon
		r.mu.Unlock()
		return
	}
	r.state = state
	r.closedAt = timePtr(r.rt.clock.Now())
	r.recordLocked(eventCancelled, "", 0, nil)
	r.cancel()
	err := cancelledErr("child closed by its parent")
	r.completeWaitsLocked(err)
	r.mu.Unlock()
	r.notify()
	r.stepWG.Wait()
}

// cancelRun is Cancel: the run closes as cancelled, its context is cancelled,
// every pending wait completes with cancelled, and its children get their
// policy.
func (r *run) cancelRun(reason string) {
	r.mu.Lock()
	if r.state != stateRunning {
		r.mu.Unlock()
		return
	}
	err := cancelledErr(cancelMessage(reason))
	r.state = stateCancelled
	r.closedAt = timePtr(r.rt.clock.Now())
	r.err = err
	r.recordLocked(eventCancelled, "", 0, nil)
	r.cancel()
	r.completeWaitsLocked(err)
	r.closeChildrenLocked()
	r.mu.Unlock()
	r.notify()
	r.stepWG.Wait()
}

// shutdown delivers cancellation without closing the run: its state stays
// running and pending waits end with cancelled so the goroutine unwinds.
func (r *run) shutdown() {
	r.mu.Lock()
	r.shutting = true
	r.cancel()
	r.completeWaitsLocked(cancelledErr("temporal runtime shut down"))
	r.mu.Unlock()
	r.notify()
}

func (r *run) completeWaitsLocked(err error) {
	for _, g := range r.gates {
		if !g.resolved {
			g.future.completeRaw(nil, err)
		}
	}
	for _, timer := range r.timers {
		timer.future.completeRaw(nil, err)
	}
	r.timers = nil
	for _, link := range r.children {
		link.future.completeRaw(nil, err)
	}
}

// resolveGate records the encoded payload and completes the wait once.
func (r *run) resolveGate(name string, payload json.RawMessage) error {
	encoded, err := r.encode(payload)
	if err != nil {
		return err
	}
	r.mu.Lock()
	g := r.gates[name]
	if g == nil {
		r.mu.Unlock()
		return core.NewError(core.CodeNotFound, "temporal: no gate with that name on the run", map[string]any{"gate": name, "run": r.id})
	}
	if g.resolved {
		r.mu.Unlock()
		return core.NewError(core.CodeConflict, "temporal: the gate was already resolved", map[string]any{"gate": name, "run": r.id})
	}
	if r.state != stateRunning {
		r.mu.Unlock()
		return core.NewError(core.CodeConflict, "temporal: the run is not running", map[string]any{"run": r.id})
	}
	g.resolved = true
	g.payload = append(json.RawMessage(nil), payload...)
	r.recordLocked(eventGateResolved, name, 0, encoded)
	r.mu.Unlock()
	g.future.completeEncoded(encoded, payload, nil)
	r.notify()
	return nil
}

func (r *run) status() runtime.RunStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := runtime.RunStatus{
		Ref:       runtime.RunRef{RunID: r.id, TenantID: r.tenant},
		State:     r.state,
		StartedAt: r.startedAt,
	}
	if r.closedAt != nil {
		closed := *r.closedAt
		out.ClosedAt = &closed
	}
	return out
}

func (r *run) notify() {
	r.wakeMu.Lock()
	close(r.changed)
	r.changed = make(chan struct{})
	r.wakeMu.Unlock()
}

func (r *run) wake() <-chan struct{} {
	r.wakeMu.Lock()
	defer r.wakeMu.Unlock()
	return r.changed
}

// call records one durable call of the flow, in call order, for replay
// determinism checks.
func (r *run) call(kind, name string) {
	r.mu.Lock()
	r.calls = append(r.calls, kind+":"+name)
	r.mu.Unlock()
}

// pendingStepFutures returns the futures of steps still in flight.
func (r *run) pendingStepFutures() []*future {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*future, 0, len(r.steps))
	for _, step := range r.steps {
		if !step.future.Ready() {
			out = append(out, step.future)
		}
	}
	return out
}

func cancelMessage(reason string) string {
	if reason == "" {
		return "run cancelled"
	}
	return "run cancelled: " + reason
}

func cancelledErr(message string) error {
	return core.NewError(core.CodeCancelled, "temporal: "+message, nil)
}

func timePtr(t time.Time) *time.Time { return &t }

func notFound(runID string) error {
	return core.NewError(core.CodeNotFound, "temporal: run not found", map[string]any{"run": runID})
}

// seededRand returns the deterministic random source of one key on one run.
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
