package temporal

import (
	"context"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// Kill stops a running run the way a killed process would: in-flight steps and
// pending waits are cancelled, but the run keeps its journal and stays
// `running` instead of closing. It waits until the flow host has returned, so
// Resume starts from a quiescent run.
//
// The conformance suite uses it to model the process boundary the Temporal
// server would otherwise hide (specs/runtime/conformance.feature, RT-002-01).
func (r *Runtime) Kill(ctx context.Context, ref runtime.RunRef) error {
	tenant, err := r.tenant(ctx, ref.TenantID)
	if err != nil {
		return err
	}
	target := r.lookup(tenant, ref.RunID)
	if target == nil {
		return notFound(ref.RunID)
	}
	target.mu.Lock()
	if target.state != stateRunning {
		target.mu.Unlock()
		return core.NewError(core.CodeConflict, "temporal: only a running run can be killed", map[string]any{"run": ref.RunID})
	}
	target.shutting = true
	target.cancel()
	target.completeWaitsLocked(cancelledErr("process killed"))
	done := target.done
	target.mu.Unlock()
	target.notify()

	<-done
	// A step the flow started and no longer awaits must also have returned
	// before Resume resets the journal.
	target.stepWG.Wait()
	return nil
}

// Resume re-executes a killed or suspended run from its journal: a completed
// step returns its recorded result without running again, an incomplete one is
// scheduled anew, and durable events already in the outbox are not written
// twice (REQ-RT-002). It does not change the run's id or tenant.
func (r *Runtime) Resume(ctx context.Context, ref runtime.RunRef) error {
	tenant, err := r.tenant(ctx, ref.TenantID)
	if err != nil {
		return err
	}
	target := r.lookup(tenant, ref.RunID)
	if target == nil {
		return notFound(ref.RunID)
	}

	target.mu.Lock()
	if target.state != stateRunning {
		target.mu.Unlock()
		return core.NewError(core.CodeConflict, "temporal: only a running run can resume", map[string]any{"run": ref.RunID})
	}
	if target.rt.closed {
		target.mu.Unlock()
		return core.NewError(core.CodeUnavailable, "temporal: the runtime has shut down", map[string]any{"run": ref.RunID})
	}
	// Completed steps are the durable results; everything else is scheduled
	// again. A resolved gate stays resolved, an unresolved one is awaited anew.
	for name, step := range target.steps {
		if step.done && step.err == nil {
			continue
		}
		delete(target.steps, name)
	}
	for name, gate := range target.gates {
		if !gate.resolved {
			delete(target.gates, name)
		}
	}
	target.timers = nil
	target.waiters = nil
	target.children = nil
	target.shutting = false
	target.replayEmitAt = 0
	target.replayEmits = len(target.outbox)

	childCtx, cancel := context.WithCancel(target.base)
	target.ctx = childCtx
	target.cancel = cancel
	target.done = make(chan struct{})
	target.mu.Unlock()

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		target.execute()
	}()
	return nil
}

// Suspended reports whether a run is in the state Kill leaves it in: still
// running, with its flow host stopped.
func (r *Runtime) Suspended(ctx context.Context, ref runtime.RunRef) (bool, error) {
	tenant, err := r.tenant(ctx, ref.TenantID)
	if err != nil {
		return false, err
	}
	target := r.lookup(tenant, ref.RunID)
	if target == nil {
		return false, notFound(ref.RunID)
	}
	target.mu.Lock()
	defer target.mu.Unlock()
	return target.state == stateRunning && target.shutting, nil
}
