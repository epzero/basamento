package local

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// durableContext is the runtime.DurableContext handed to the flow host. All
// state lives in the run's journal, so the same call sequence replays to the
// same results (REQ-RT-007).
type durableContext struct {
	run *run
}

// Context returns the run's context. It carries the run's tenant and is
// cancelled when the run is cancelled or the runtime shuts down.
func (dc *durableContext) Context() context.Context { return dc.run.ctx }

// Now returns the runtime's clock, never the wall clock (REQ-RT-008).
func (dc *durableContext) Now() time.Time { return dc.run.rt.clock.Now() }

// NewID returns a run-scoped, deterministic id.
func (dc *durableContext) NewID(kind string) string {
	r := dc.run
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids[kind]++
	return fmt.Sprintf("%s_%d", kind, r.ids[kind])
}

// Rand returns the random source of (run, key).
func (dc *durableContext) Rand(key string) *rand.Rand {
	r := dc.run
	r.mu.Lock()
	defer r.mu.Unlock()
	source := r.randoms[key]
	if source == nil {
		source = seededRand(r.id, key)
		r.randoms[key] = source
	}
	return source
}

// Step runs a side-effecting step once per name and run (REQ-RT-007). The
// attempt loop is the runtime's: no backoff on the local runtime (REQ-RT-016,
// REQ-RT-017, REQ-RT-018, REQ-RT-027).
func (dc *durableContext) Step(name string, in any, o runtime.StepOptions) runtime.Future {
	r := dc.run
	raw, err := rawInput(in)
	if err != nil {
		return failedFuture(r, err)
	}
	r.rt.mu.Lock()
	registered, ok := r.rt.steps[name]
	r.rt.mu.Unlock()
	if !ok {
		return failedFuture(r, core.NewError(core.CodeUnsupported,
			fmt.Sprintf("local: step %q is not registered", name), map[string]any{"step": name}))
	}
	options := mergeOptions(o, registered.defaults.Options)

	r.mu.Lock()
	if existing := r.steps[name]; existing != nil {
		f := existing.future
		r.mu.Unlock()
		return f
	}
	if r.ctx.Err() != nil {
		r.mu.Unlock()
		return failedFuture(r, cancelledErr("step not started: run cancelled"))
	}
	f := newFuture(r)
	r.steps[name] = &step{future: f}
	r.stepWG.Add(1)
	r.rt.wg.Add(1)
	r.mu.Unlock()

	go func() {
		defer r.rt.wg.Done()
		defer r.stepWG.Done()
		r.runStep(f, name, registered.fn, raw, options)
	}()
	return f
}

// runStep executes a step's attempts on the step's own goroutine. Attempts run
// immediately, back to back, and the step closes with the last attempt's error
// when the limit is reached.
func (r *run) runStep(f *future, step string, fn runtime.StepFunc, in json.RawMessage, opts runtime.StepOptions) {
	attempts := opts.MaxAttempts
	if attempts <= 0 {
		attempts = defaultAttempts
	}
	var last error
	for attempt := 0; attempt < attempts; attempt++ {
		if r.ctx.Err() != nil {
			f.complete(nil, cancelledErr("step cancelled"))
			return
		}
		var out json.RawMessage
		var err error
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					err = stepPanicErr(step)
					r.rt.logPanic(r.tenant, r.id, step, recovered)
				}
			}()
			out, err = fn(r.ctx, in)
		}()
		if err == nil {
			f.complete(out, nil)
			return
		}
		last = err
		if !retryable(opts, err) {
			f.complete(nil, err)
			return
		}
	}
	if last == nil {
		last = core.NewError(core.CodeInternal, "local: step made no attempt", map[string]any{"step": opts.IdempotencyKey})
	}
	f.complete(nil, last)
}

// retryable applies REQ-RT-016: a code listed in NonRetryable stops retries even
// when the error is marked retryable, and an error not marked retryable stops
// them by itself.
func retryable(opts runtime.StepOptions, err error) bool {
	if !core.IsRetryable(err) {
		return false
	}
	code := string(core.CodeOf(err))
	for _, listed := range opts.NonRetryable {
		if listed == code {
			return false
		}
	}
	return true
}

// Sleep starts a timer that completes only when the runtime's clock reaches
// Now()+d (REQ-RT-008).
func (dc *durableContext) Sleep(d time.Duration) runtime.Future {
	r := dc.run
	r.mu.Lock()
	if r.ctx.Err() != nil {
		r.mu.Unlock()
		return failedFuture(r, cancelledErr("timer not started: run cancelled"))
	}
	f := newFuture(r)
	if d <= 0 {
		r.mu.Unlock()
		f.complete(nil, nil)
		return f
	}
	timer := &runTimer{deadline: r.rt.clock.Now().Add(d), future: f, wake: r.rt.after(d)}
	r.timers = append(r.timers, timer)
	r.rt.wg.Add(1)
	r.mu.Unlock()

	go func() {
		defer r.rt.wg.Done()
		r.waitTimer(timer, nil, func() {
			if !f.Ready() {
				f.complete(nil, cancelledErr("timer cancelled"))
			}
		})
	}()
	return f
}

// Await registers the run at the gate and returns the wait (REQ-RT-009). A gate
// the journal already holds returns its recorded future, so a replayed wait
// sees the same payload.
func (dc *durableContext) Await(spec runtime.GateSpec) runtime.Future {
	r := dc.run
	r.mu.Lock()
	if existing := r.gates[spec.Name]; existing != nil {
		f := existing.future
		r.mu.Unlock()
		return f
	}
	if r.ctx.Err() != nil {
		r.mu.Unlock()
		return failedFuture(r, cancelledErr("gate not awaited: run cancelled"))
	}
	g := &gate{spec: spec, future: newFuture(r)}
	r.gates[spec.Name] = g
	if spec.Timeout > 0 {
		stop := make(chan struct{})
		g.timeoutStop = stop
		deadline := r.rt.clock.Now().Add(spec.Timeout)
		wake := r.rt.after(spec.Timeout)
		timer := &runTimer{deadline: deadline, future: g.future, wake: wake, gate: g}
		g.timeoutTimer = timer
		r.timers = append(r.timers, timer)
		r.gateTimeouts.Add(1)
		r.rt.wg.Add(1)
		go func() {
			defer r.rt.wg.Done()
			defer r.gateTimeouts.Add(-1)
			r.waitTimer(timer, stop, func() {
				if !g.future.Ready() {
					g.future.complete(nil, cancelledErr("gate cancelled"))
				}
			})
		}()
	}
	r.mu.Unlock()
	return g.future
}

// Select waits for the first pending operation to complete (REQ-RT-011).
//
// At call time, the lowest index among futures that are already Ready wins;
// run-owned futures are observed under r.mu so a timer batch is all-or-nothing.
// While the run is blocked in Select, the ready future with the earliest
// completion seq wins; equal seqs keep the lowest index (one timer batch).
// When steps complete independently, completion order is the order in which
// they closed done—that is the only deterministic order the local runtime has
// and it is not replayable across processes.
func (dc *durableContext) Select(fs ...runtime.Future) (int, error) {
	if len(fs) == 0 {
		return 0, core.NewError(core.CodeInvalidArgument, "local: Select needs at least one future", nil)
	}
	r := dc.run
	if i, ok := r.lowestReady(fs); ok {
		return i, nil
	}
	for {
		// Subscribe before scanning: a completion cannot land between the
		// check and the wait without also closing this wake channel.
		changed := r.wake()
		if index := r.firstCompleted(fs); index >= 0 {
			return index, nil
		}
		select {
		case <-changed:
		case <-r.ctx.Done():
			if i, ok := r.lowestReady(fs); ok {
				return i, nil
			}
			return 0, cancelledErr("select cancelled")
		}
	}
}

// firstCompleted returns the earliest-ready index by completion seq, or -1 when
// none are ready; locals are snapshotted under r.mu, foreign Ready() outside it.
func (r *run) firstCompleted(fs []runtime.Future) int {
	return firstCompletedFromSnap(fs, r.snapshotLocalFutures(fs))
}

// Emit appends to the run's ordered outbox (REQ-RT-006).
func (dc *durableContext) Emit(ev runtime.ProgressEvent) error {
	r := dc.run
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != stateRunning || r.shuttingDown {
		return cancelledErr("emit after the run closed")
	}
	r.outbox = append(r.outbox, runtime.ProgressEvent{
		Type: ev.Type,
		Data: append(json.RawMessage(nil), ev.Data...),
	})
	return nil
}

// Checkpoint is a no-op: the local runtime does not continue runs as new
// (REQ-RT-005).
func (dc *durableContext) Checkpoint(runtime.SnapshotRef) error { return nil }

// Capabilities reports the runtime's capabilities.
func (dc *durableContext) Capabilities() runtime.Caps { return dc.run.rt.Capabilities() }

// Child starts a child run of flow in the parent's tenant and returns a future
// for its output (REQ-RT-014). The parent's close policy applies when the parent
// ends (REQ-RT-015). Re-attach with the same parent run, flow and id returns the
// first call's future and keeps its policy; a closed or shutting-down parent
// returns cancelled without starting a new run unless the child is already
// recorded on the journal.
func (dc *durableContext) Child(flow runtime.FlowRef, in any, o runtime.ChildOptions) runtime.Future {
	r := dc.run
	policy, err := childPolicy(o.ParentClose)
	if err != nil {
		return failedFuture(r, err)
	}
	raw, err := rawInput(in)
	if err != nil {
		return failedFuture(r, err)
	}
	id := o.ID
	if id == "" {
		id = dc.NewID("child")
	}
	r.mu.Lock()
	if f, ok := r.recordedOrClosedChildFuture(id, flow); ok {
		r.mu.Unlock()
		return f
	}
	r.mu.Unlock()

	child, err := r.rt.startChild(r.tenant, id, flow, raw, r)
	if err != nil {
		return failedFuture(r, err)
	}
	return r.linkChild(child, policy)
}

func mergeOptions(o, d runtime.StepOptions) runtime.StepOptions {
	if o.Queue == "" {
		o.Queue = d.Queue
	}
	if o.StartToClose == 0 {
		o.StartToClose = d.StartToClose
	}
	if o.Heartbeat == 0 {
		o.Heartbeat = d.Heartbeat
	}
	if o.MaxAttempts == 0 {
		o.MaxAttempts = d.MaxAttempts
	}
	if len(o.NonRetryable) == 0 {
		o.NonRetryable = d.NonRetryable
	}
	if o.IdempotencyKey == "" {
		o.IdempotencyKey = d.IdempotencyKey
	}
	return o
}

func rawInput(in any) (json.RawMessage, error) {
	switch value := in.(type) {
	case nil:
		return nil, nil
	case json.RawMessage:
		return append(json.RawMessage(nil), value...), nil
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, invalidErr("local: encode input", map[string]any{"cause": err.Error()})
		}
		return raw, nil
	}
}
