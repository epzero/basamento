package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
	"github.com/epzero/basamento/runtime/local"
)

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

// localListFailure requires a failed List that returned no runs (REQ-RT-052/053).
func localListFailure(w *localWorld, code string) error {
	if err := localLastError(w, code); err != nil {
		return err
	}
	if len(w.lastList) != 0 {
		return fmt.Errorf("listing returned %d runs on error, want none", len(w.lastList))
	}
	return nil
}

func (w *localWorld) execForRun(tenant, runID string) (*localExec, error) {
	w.mu.Lock()
	exec := w.byRun[localRunKey{tenant: core.TenantID(tenant), id: runID}]
	w.mu.Unlock()
	if exec == nil {
		return nil, fmt.Errorf("run %q of tenant %q is not known to the scenario", runID, tenant)
	}
	return exec, nil
}

func localStartWithTenants(ctx context.Context, runID, flow, contextTenant, requestTenant string) error {
	w := localStepWorld(ctx)
	// Sample the baseline before Start: Start runs the flow asynchronously, so
	// a baseline read after it can already include this run's invocation and
	// the wait below would never be satisfied (EP0-198).
	before := w.execCount(flow)
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

// setChildGateFlow registers a child flow that waits at gate before completing.
func (w *localWorld) setChildGateFlow(name, gate string) {
	w.setFlow(name, w.gateFlow(gate))
}

// startRun starts a run with optional idempotency key and records Start outcome.
func (w *localWorld) startRun(ctx context.Context, tenant, runID, flow, idem string, recordAsFirst bool) error {
	rt := w.runtime(ctx)
	ref, err := rt.Start(w.ctx(tenant), runtime.StartRequest{
		Flow:           runtime.FlowRef{Name: flow},
		RunID:          runID,
		IdempotencyKey: idem,
	})
	w.lastStart, w.lastErr = ref, err
	if recordAsFirst && err == nil {
		w.mu.Lock()
		w.firstStartRef = ref
		w.mu.Unlock()
	}
	if err != nil {
		return err
	}
	w.mu.Lock()
	w.current = ref
	w.mu.Unlock()
	return w.waitRunState(ctx, ref, "completed")
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
	guard, stopGuard := context.WithTimeout(context.Background(), waitBound)
	defer stopGuard()
	select {
	case err := <-done:
		w.shutdownError = err
		return nil
	case <-guard.Done():
		return fmt.Errorf("the local runtime did not shut down after %v", waitBound)
	}
}

// timePassed waits until every timer the scripted flow registered with watchTimer
// and due at the clock reading after a duration-pass step is Ready. The shared
// duration-pass step advances the fake clock before calling this. The duration
// argument is unused but present so localWorld can satisfy the Host timeSink shape.
func (w *localWorld) timePassed(ctx context.Context, _ time.Duration) error {
	if w.rt == nil {
		return nil
	}
	w.mu.Lock()
	exec := w.currentExec
	w.mu.Unlock()
	if exec == nil {
		return nil
	}
	reading := world(ctx).Core.clock.Now()
	what := fmt.Sprintf("timers due at %s to become ready", reading.Format(time.RFC3339))
	err := w.spin(what, func() bool { return exec.pendingTimersAt(reading) == 0 })
	if err == nil {
		return nil
	}
	pending := exec.pendingTimersAt(reading)
	return fmt.Errorf("%w (%d timer(s) still not ready at %s)", err, pending, reading.Format(time.RFC3339))
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
		if _, err := recvWithin(done, "runtime shutdown"); err != nil {
			panic(err)
		}
	}
}
