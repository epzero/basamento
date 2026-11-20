package steps

import (
	"context"
	"encoding/json"
	"fmt"
	goruntime "runtime"
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

func localStartWithTenants(ctx context.Context, runID, flow, contextTenant, requestTenant string) error {
	w := localStepWorld(ctx)
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
	before := w.execCount(flow)
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

// setParentFlow scripts a parent that starts a child with the given id. With
// await it waits for and records the child's output; otherwise it returns at
// once so the close policy is the only thing that ends the child.
func (w *localWorld) setParentFlow(name, child, childID, policy string, await bool) {
	w.setFlow(name, func(e *localExec, dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		options := runtime.ChildOptions{ID: childID, ParentClose: policy}
		future := dc.Child(runtime.FlowRef{Name: child}, nil, options)
		if !await {
			return json.RawMessage(`{}`), nil
		}
		var out json.RawMessage
		if err := future.Get(&out); err != nil {
			return nil, err
		}
		e.update(func() { e.childOut = out })
		return out, nil
	})
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
	for i := 0; i < localSpinBudget; i++ {
		select {
		case err := <-done:
			w.shutdownError = err
			return nil
		default:
			goruntime.Gosched()
		}
	}
	return fmt.Errorf("the local runtime did not shut down after %d yields", localSpinBudget)
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
		if _, err := recvWithin(done, localSpinBudget, "runtime shutdown"); err != nil {
			panic(err)
		}
	}
}
