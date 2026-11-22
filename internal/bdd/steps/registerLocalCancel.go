package steps

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/runtime"
)

func registerLocalCancel(sc *godog.ScenarioContext) {
	// Cancellation.
	sc.Step(`^a run of "([^"]*)" is waiting on gate "([^"]*)"$`, func(ctx context.Context, flow, gate string) error {
		w := localStepWorld(ctx)
		w.setFlow(flow, w.gateFlow(gate))
		_, exec, err := w.start(ctx, "acme", "", flow)
		if err != nil {
			return err
		}
		w.currentExec = exec
		return exec.wait("the run to await its gate", func() bool { return exec.atGate != "" })
	})
	sc.Step(`^a run of "([^"]*)" is waiting on a "([^"]*)" timer$`, func(ctx context.Context, flow, text string) error {
		w := localStepWorld(ctx)
		d, err := localDuration(text)
		if err != nil {
			return err
		}
		w.setFlow(flow, w.timerFlow(d))
		_, exec, err := w.start(ctx, "acme", "", flow)
		if err != nil {
			return err
		}
		w.currentExec = exec
		return exec.wait("the run to wait on its timer", func() bool { return exec.waiting })
	})
	sc.Step(`^the run is cancelled$`, func(ctx context.Context) error {
		// The worker, model node and authz scenarios act on their own run; the
		// runtime scenarios cancel the local world's current run.
		if wk := world(ctx).Worker; wk != nil && wk.run != nil {
			return wk.cancelRun(ctx)
		}
		if az := world(ctx).Authz; az != nil && az.db != nil {
			if !az.api.cancel {
				return fmt.Errorf("run not cancelled")
			}
			return nil
		}
		if n := world(ctx).Node; n.active() {
			return n.cancelRun()
		}
		w := localStepWorld(ctx)
		return w.runtime(ctx).Cancel(w.ctx(string(w.current.TenantID)), w.current, "")
	})
	sc.Step(`^run "([^"]*)" is cancelled with reason "([^"]*)"$`, func(ctx context.Context, runID, reason string) error {
		w := localStepWorld(ctx)
		return w.runtime(ctx).Cancel(w.ctx("acme"), runtime.RunRef{RunID: runID}, reason)
	})
	sc.Step(`^run "([^"]*)" of "([^"]*)" has step "([^"]*)" in flight$`, func(ctx context.Context, runID, flow, step string) error {
		w := localStepWorld(ctx)
		ref, exec, err := w.start(ctx, "acme", runID, flow)
		if err != nil {
			return err
		}
		w.current, w.currentExec = ref, exec
		return w.spin("step "+step+" to start", func() bool { return w.started(step) })
	})
	sc.Step(`^step "([^"]*)" received cancellation before the run closed$`, func(ctx context.Context, name string) error {
		return localStepCancelled(localStepWorld(ctx), name)
	})
	sc.Step(`^step "([^"]*)" received cancellation$`, func(ctx context.Context, name string) error {
		return localStepCancelled(localStepWorld(ctx), name)
	})
	sc.Step(`^the pending wait fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		w := localStepWorld(ctx)
		exec := w.currentExec
		if exec == nil {
			return fmt.Errorf("no run has started")
		}
		if err := exec.wait("the pending wait to end", func() bool { return exec.waitErr != nil }); err != nil {
			return err
		}
		exec.mu.Lock()
		err := exec.waitErr
		exec.mu.Unlock()
		if localCode(err) != code {
			return fmt.Errorf("the wait failed, want code %q: %w", code, err)
		}
		return nil
	})

}
