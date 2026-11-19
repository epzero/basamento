package local

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// run is one execution of a flow on the local runtime: its control state, its
// journal (steps, gates, children, outbox) and the goroutines it owns.
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
	seqs    atomic.Uint64 // completion order of this run's futures

	state     string
	startedAt time.Time
	closedAt  *time.Time
	output    json.RawMessage
	err       error

	steps    map[string]*step
	gates    map[string]*gate
	children []*childLink
	waiters  []*future
	timers   []*runTimer
	outbox   []runtime.ProgressEvent
	ids      map[string]int
	randoms  map[string]*rand.Rand

	shuttingDown bool
	stepWG       sync.WaitGroup
}

// step is the journal entry of one step name in a run (REQ-RT-007).
type step struct {
	future *future
}

// gate is the journal entry of one gate name in a run (REQ-RT-009, REQ-RT-010).
type gate struct {
	spec     runtime.GateSpec
	future   *future
	resolved bool
	payload  json.RawMessage
}

// childLink ties a parent run to a child run it started (REQ-RT-014,
// REQ-RT-015).
type childLink struct {
	run    *run
	policy string
	future *future
}

// runTimer is one pending Sleep. Any timer goroutine of the run that wakes
// completes every timer whose deadline the clock has already reached, so a
// clock jump completes due timers together and Select sees the lowest index.
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
		steps:     map[string]*step{},
		gates:     map[string]*gate{},
		ids:       map[string]int{},
		randoms:   map[string]*rand.Rand{},
	}
}

func (r *run) ref() runtime.RunRef {
	return runtime.RunRef{RunID: r.id, TenantID: r.tenant}
}

// execute runs the registered flow host on this run's goroutine and closes the
// run with its outcome.
func (r *run) execute() {
	out, err := r.rt.host(&durableContext{run: r}, r.flow, r.input)
	r.finish(out, err)
}

// finish records the run's terminal state. A cancelled or terminated run keeps
// its state, and a shut-down run stays running (REQ-RT-022): shutdown cancels
// work, it does not close the run.
func (r *run) finish(out json.RawMessage, err error) {
	r.mu.Lock()
	if r.state == stateRunning && !r.shuttingDown {
		if err != nil {
			r.state = stateFailed
			r.err = err
		} else {
			r.state = stateCompleted
			r.output = append(json.RawMessage(nil), out...)
		}
		r.closedAt = timePtr(r.rt.clock.Now())
		r.closeChildrenLocked()
	}
	var waiters []*future
	if r.state != stateRunning {
		waiters = append(waiters, r.waiters...)
	}
	r.mu.Unlock()
	r.notify()
	for _, f := range waiters {
		f.complete(out, err)
	}
}

// closeChildrenLocked applies each in-flight child's close policy when the
// parent ends (REQ-RT-015).
func (r *run) closeChildrenLocked() {
	for _, link := range r.children {
		link.run.applyParentClose(link.policy)
	}
}

// applyParentClose moves a still-running child as the parent's policy says.
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
	r.cancel()
	err := cancelledErr("child closed by its parent")
	r.completeWaitsLocked(err)
	r.completeWaitersLocked(nil, err)
	r.mu.Unlock()
	r.notify()
}

// cancelRun is Cancel: the run closes as cancelled, its context is cancelled,
// every pending wait completes with cancelled, its children get their policy,
// and the call returns once in-flight steps have observed the cancellation
// (REQ-RT-012, REQ-RT-013).
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
	r.cancel()
	r.completeWaitsLocked(err)
	r.completeWaitersLocked(nil, err)
	r.closeChildrenLocked()
	r.mu.Unlock()
	r.notify()
	r.stepWG.Wait()
}

// shutdown delivers cancellation without closing the run (REQ-RT-022,
// REQ-RT-023): the state stays running and pending waits end with cancelled so
// the goroutine unwinds.
func (r *run) shutdown() {
	r.mu.Lock()
	r.shuttingDown = true
	r.cancel()
	r.completeWaitsLocked(cancelledErr("local runtime shut down"))
	r.mu.Unlock()
	r.notify()
}

// completeWaitsLocked ends every pending gate wait, timer and child wait with
// err. It is a no-op for waits that already completed.
func (r *run) completeWaitsLocked(err error) {
	for _, g := range r.gates {
		if !g.resolved {
			g.future.complete(nil, err)
		}
	}
	for _, timer := range r.timers {
		timer.future.complete(nil, err)
	}
	r.timers = nil
	for _, link := range r.children {
		link.future.complete(nil, err)
	}
}

// fireDueTimers completes every timer of the run whose deadline the clock has
// reached. The first timer goroutine to wake after a clock jump completes them
// all, so due timers become ready together (REQ-RT-008, REQ-RT-011).
func (r *run) fireDueTimers() {
	r.mu.Lock()
	now := r.rt.clock.Now()
	kept := r.timers[:0]
	for _, timer := range r.timers {
		if timer.future.Ready() {
			continue
		}
		if timer.deadline.After(now) {
			kept = append(kept, timer)
			continue
		}
		timer.future.complete(nil, nil)
	}
	r.timers = kept
	r.mu.Unlock()
}

// completeWaitersLocked delivers this run's outcome to the parents waiting on
// it.
func (r *run) completeWaitersLocked(out json.RawMessage, err error) {
	for _, f := range r.waiters {
		if !f.Ready() {
			f.complete(out, err)
		}
	}
}

// resolveGate records the payload and completes the wait once (REQ-RT-009). A
// second resolution of the same gate is a conflict (REQ-RT-010).
func (r *run) resolveGate(name string, payload json.RawMessage) error {
	r.mu.Lock()
	g := r.gates[name]
	if g == nil {
		r.mu.Unlock()
		return core.NewError(core.CodeNotFound, "local: no gate with that name on the run", map[string]any{"gate": name, "run": r.id})
	}
	if g.resolved {
		r.mu.Unlock()
		return core.NewError(core.CodeConflict, "local: the gate was already resolved", map[string]any{"gate": name, "run": r.id})
	}
	g.resolved = true
	g.payload = append(json.RawMessage(nil), payload...)
	g.future.complete(g.payload, nil)
	r.mu.Unlock()
	r.notify()
	return nil
}

// status is a snapshot of the run for Describe and List.
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

// notify wakes every Select waiting on this run.
func (r *run) notify() {
	r.wakeMu.Lock()
	close(r.changed)
	r.changed = make(chan struct{})
	r.wakeMu.Unlock()
}

// wake returns a channel closed at the next completion.
func (r *run) wake() <-chan struct{} {
	r.wakeMu.Lock()
	defer r.wakeMu.Unlock()
	return r.changed
}

func cancelMessage(reason string) string {
	if reason == "" {
		return "run cancelled"
	}
	return "run cancelled: " + reason
}

func timePtr(t time.Time) *time.Time { return &t }
