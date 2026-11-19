package temporal

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// durableContext is the runtime.DurableContext handed to the flow host. Every
// result it returns is recorded in the run history, so replaying the same call
// sequence returns the same values.
type durableContext struct {
	run *run
}

func (dc *durableContext) Context() context.Context { return dc.run.ctx }
func (dc *durableContext) Now() time.Time           { return dc.run.rt.clock.Now() }
func (dc *durableContext) Capabilities() runtime.Caps {
	return dc.run.rt.Capabilities()
}

func (dc *durableContext) NewID(kind string) string {
	r := dc.run
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids[kind]++
	return fmt.Sprintf("%s_%d", kind, r.ids[kind])
}

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

// Step schedules one activity per run and step name. The first call records the
// result; a later call with the same name returns it without executing the
// activity again.
func (dc *durableContext) Step(name string, in any, o runtime.StepOptions) runtime.Future {
	r := dc.run
	r.call("step", name)
	raw, err := rawInput(in)
	if err != nil {
		return failedFuture(r, err)
	}
	r.rt.mu.Lock()
	registered, ok := r.rt.steps[name]
	r.rt.mu.Unlock()
	if !ok {
		return failedFuture(r, core.NewError(core.CodeUnsupported,
			fmt.Sprintf("temporal: step %q is not registered", name), map[string]any{"step": name}))
	}
	options := mergeOptions(o, registered.defaults.Options)

	r.mu.Lock()
	if existing := r.steps[name]; existing != nil {
		f := existing.future
		r.mu.Unlock()
		return f
	}
	if r.replaying {
		r.mu.Unlock()
		return failedFuture(r, core.NewError(core.CodeDeterminismBroken,
			fmt.Sprintf("temporal: replay called step %q with no recorded result", name), map[string]any{"step": name}))
	}
	if r.ctx.Err() != nil {
		r.mu.Unlock()
		return failedFuture(r, cancelledErr("step not started: run cancelled"))
	}
	f := newFuture(r, true)
	step := &stepRecord{future: f, options: options}
	r.steps[name] = step
	r.stepWG.Add(1)
	r.rt.wg.Add(1)
	r.mu.Unlock()

	go func() {
		defer r.rt.wg.Done()
		defer r.stepWG.Done()
		r.executeStep(step, name, raw, options, registered.fn)
	}()
	return f
}

// executeStep runs a step's attempts on the activity's goroutine. Attempts run
// back to back while the error is retryable and the code is not listed as
// non-retryable; the limit closes the step with the last error (REQ-RT-041).
func (r *run) executeStep(step *stepRecord, name string, in json.RawMessage, opts runtime.StepOptions, fn runtime.StepFunc) {
	r.record(eventWorkflowTask, name, 0, nil)
	var input *Encoded
	if len(in) > 0 {
		encoded, err := r.encode(in)
		if err != nil {
			r.closeStep(step, nil, nil, err)
			return
		}
		input = encoded
	}
	r.record(eventActivityScheduled, name, 0, input)

	attempts := opts.MaxAttempts
	if attempts <= 0 {
		attempts = defaultAttempts
	}
	var last error
	for attempt := 1; attempt <= attempts; attempt++ {
		if r.ctx.Err() != nil {
			r.closeStep(step, nil, nil, cancelledErr("step cancelled"))
			return
		}
		r.record(eventActivityStarted, name, attempt, nil)
		out, err := fn(r.ctx, in)
		if err == nil {
			encoded, encodeErr := r.encode(out)
			if encodeErr != nil {
				r.record(eventActivityFailed, name, attempt, nil)
				r.closeStep(step, nil, nil, encodeErr)
				return
			}
			r.record(eventActivityCompleted, name, attempt, encoded)
			r.closeStep(step, encoded, out, nil)
			return
		}
		last = err
		failure, _ := r.errorPayload(err)
		r.record(eventActivityFailed, name, attempt, failure)
		if !retryable(opts, err) {
			r.closeStep(step, nil, nil, err)
			return
		}
	}
	if last == nil {
		last = core.NewError(core.CodeInternal, "temporal: step made no attempt", map[string]any{"step": name})
	}
	r.closeStep(step, nil, nil, last)
}

func (r *run) closeStep(step *stepRecord, encoded *Encoded, raw json.RawMessage, err error) {
	r.mu.Lock()
	step.encoded = encoded
	step.raw = append(json.RawMessage(nil), raw...)
	step.err = err
	step.done = true
	r.mu.Unlock()
	if err != nil {
		step.future.completeRaw(nil, err)
		return
	}
	step.future.completeEncoded(encoded, raw, nil)
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

// Sleep is a durable timer. It completes only when the runtime's clock reaches
// Now()+d; the run is never woken by the wall clock in tests.
func (dc *durableContext) Sleep(d time.Duration) runtime.Future {
	r := dc.run
	r.call("sleep", d.String())
	f := newFuture(r, false)
	if r.replaying {
		f.completeRaw(nil, nil)
		return f
	}
	r.mu.Lock()
	if r.ctx.Err() != nil {
		r.mu.Unlock()
		f.completeRaw(nil, cancelledErr("timer not started: run cancelled"))
		return f
	}
	if d <= 0 {
		r.mu.Unlock()
		f.completeRaw(nil, nil)
		return f
	}
	timer := &runTimer{deadline: r.rt.clock.Now().Add(d), future: f, wake: r.rt.after(d)}
	r.timers = append(r.timers, timer)
	r.recordLocked(eventTimerStarted, "", 0, nil)
	r.rt.wg.Add(1)
	r.mu.Unlock()
	r.notify()

	go func() {
		defer r.rt.wg.Done()
		select {
		case <-timer.wake:
			r.mu.Lock()
			r.removeTimerLocked(timer)
			r.recordLocked(eventTimerFired, "", 0, nil)
			r.mu.Unlock()
			f.completeRaw(nil, nil)
		case <-r.ctx.Done():
			f.completeRaw(nil, cancelledErr("timer cancelled"))
		}
	}()
	return f
}

func (r *run) removeTimerLocked(target *runTimer) {
	kept := r.timers[:0]
	for _, timer := range r.timers {
		if timer != target {
			kept = append(kept, timer)
		}
	}
	r.timers = kept
}

// Await registers the run at a gate. A signal with the gate's name already
// received resolves it immediately; a resolved gate keeps its payload across a
// continue-as-new.
func (dc *durableContext) Await(spec runtime.GateSpec) runtime.Future {
	r := dc.run
	r.call("gate", spec.Name)
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
	g := &gateRecord{spec: spec, future: newFuture(r, true)}
	r.gates[spec.Name] = g
	payloads := r.signals[spec.Name]
	if len(payloads) > 0 {
		payload := payloads[0]
		r.signals[spec.Name] = payloads[1:]
		g.resolved = true
		r.recordLocked(eventGateResolved, spec.Name, 0, nil)
		r.mu.Unlock()
		g.future.completeRaw(payload, nil)
		return g.future
	}
	r.recordLocked(eventGateAwaited, spec.Name, 0, nil)
	if spec.Timeout > 0 {
		f := g.future
		wake := r.rt.after(spec.Timeout)
		r.rt.wg.Add(1)
		go func() {
			defer r.rt.wg.Done()
			select {
			case <-wake:
				f.completeRaw(nil, core.NewError(core.CodeTimeout,
					fmt.Sprintf("temporal: gate %q timed out", spec.Name), map[string]any{"gate": spec.Name}))
			case <-r.ctx.Done():
				f.completeRaw(nil, cancelledErr("gate cancelled"))
			}
		}()
	}
	r.mu.Unlock()
	r.notify()
	return g.future
}

// Select waits for the lowest-index ready future, at call time or when the
// first pending future completes (REQ-RT-011).
func (dc *durableContext) Select(fs ...runtime.Future) (int, error) {
	if len(fs) == 0 {
		return 0, core.NewError(core.CodeInvalidArgument, "temporal: Select needs at least one future", nil)
	}
	for {
		for i, f := range fs {
			if f.Ready() {
				return i, nil
			}
		}
		changed := dc.run.wake()
		select {
		case <-changed:
		case <-dc.run.ctx.Done():
			for i, f := range fs {
				if f.Ready() {
					return i, nil
				}
			}
			return 0, cancelledErr("select cancelled")
		}
	}
}

// Emit appends to the run's durable, ordered outbox.
func (dc *durableContext) Emit(ev runtime.ProgressEvent) error {
	r := dc.run
	r.call("emit", ev.Type)
	if r.replaying {
		return nil
	}
	encoded, err := r.encode(ev.Data)
	if err != nil {
		return err
	}
	r.mu.Lock()
	if r.state != stateRunning || r.shutting {
		r.mu.Unlock()
		return cancelledErr("emit after the run closed")
	}
	if r.replayEmitAt < r.replayEmits {
		if r.replayEmitAt >= len(r.outbox) || r.outbox[r.replayEmitAt].Type != ev.Type {
			r.mu.Unlock()
			return core.NewError(core.CodeDeterminismBroken,
				fmt.Sprintf("temporal: resumed run emitted %q where the outbox holds another event", ev.Type),
				map[string]any{"run": r.id, "event": r.replayEmitAt})
		}
		r.replayEmitAt++
		r.mu.Unlock()
		return nil
	}
	r.outbox = append(r.outbox, runtime.ProgressEvent{Type: ev.Type, Data: append(json.RawMessage(nil), ev.Data...)})
	r.recordLocked(eventEmitted, ev.Type, 0, encoded)
	r.mu.Unlock()
	return nil
}

// Checkpoint continues the run as new when the current history segment has
// reached the checkpoint threshold. In-flight steps are drained first, so the
// continue-as-new itself never re-dispatches an activity (REQ-RT-034 …
// REQ-RT-036, REQ-RT-038).
func (dc *durableContext) Checkpoint(runtime.SnapshotRef) error {
	r := dc.run
	r.call("checkpoint", "")
	if r.replaying {
		return nil
	}
	for _, f := range r.pendingStepFutures() {
		<-f.done
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != stateRunning || r.shutting {
		return cancelledErr("checkpoint after the run closed")
	}
	limit := r.rt.checkpointThreshold
	if limit <= 0 || r.segment < limit {
		return nil
	}
	r.continues++
	r.recordLocked(eventContinuedAsNew, "", 0, nil)
	r.segment = 0
	return nil
}

// Child starts a child run of flow in the parent's tenant.
func (dc *durableContext) Child(flow runtime.FlowRef, in any, o runtime.ChildOptions) runtime.Future {
	r := dc.run
	policy := o.ParentClose
	if policy == "" {
		policy = defaultParentClose
	}
	switch policy {
	case "terminate", "request_cancel", "abandon":
	default:
		return failedFuture(r, invalidErr(fmt.Sprintf("temporal: unknown parent close policy %q", policy), map[string]any{"policy": policy}))
	}
	raw, err := rawInput(in)
	if err != nil {
		return failedFuture(r, err)
	}
	id := o.ID
	if id == "" {
		id = dc.NewID("child")
	}
	r.call("child", id)
	if r.replaying {
		return failedFuture(r, core.NewError(core.CodeDeterminismBroken,
			fmt.Sprintf("temporal: replay called child %q with no recorded result", id), nil))
	}
	child, err := r.rt.startChild(r.tenant, id, flow, raw, r)
	if err != nil {
		return failedFuture(r, err)
	}
	f := newFuture(r, true)
	child.mu.Lock()
	if child.state == stateRunning {
		child.waiters = append(child.waiters, f)
	}
	out, childErr := child.output, child.err
	state := child.state
	child.mu.Unlock()
	if state != stateRunning {
		if childErr != nil {
			f.completeRaw(nil, childErr)
		} else {
			f.completeRaw(out, nil)
		}
	}
	r.mu.Lock()
	if r.state == stateRunning && !r.shutting {
		r.children = append(r.children, &childLink{run: child, policy: policy, future: f})
		r.mu.Unlock()
		return f
	}
	r.mu.Unlock()
	child.applyParentClose(policy)
	f.completeRaw(nil, cancelledErr("the parent closed before the child was linked"))
	return f
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
			return nil, invalidErr("temporal: encode input", map[string]any{"cause": err.Error()})
		}
		return raw, nil
	}
}

func invalidErr(message string, details map[string]any) error {
	return core.NewError(core.CodeInvalidArgument, message, details)
}
