package steps

import (
	"context"
	"fmt"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/runtime"
)

func registerLocalShutdown(sc *godog.ScenarioContext) {
	// Shutdown.
	sc.Step(`^a run of "([^"]*)" is in flight on a running local runtime$`, func(ctx context.Context, flow string) error {
		w := localStepWorld(ctx)
		w.setFlow(flow, w.timerFlow(time.Hour))
		if err := w.serve(ctx); err != nil {
			return err
		}
		_, exec, err := w.start(ctx, "acme", "", flow)
		if err != nil {
			return err
		}
		w.currentExec = exec
		return exec.wait("the run to wait on its timer", func() bool { return exec.waiting })
	})
	sc.Step(`^a run of "([^"]*)" has step "([^"]*)" in flight on a running local runtime$`, func(ctx context.Context, flow, step string) error {
		w := localStepWorld(ctx)
		w.behavior(step).block = true
		w.setFlow(flow, w.blockingStepFlow(step))
		if err := w.serve(ctx); err != nil {
			return err
		}
		_, exec, err := w.start(ctx, "acme", "", flow)
		if err != nil {
			return err
		}
		w.currentExec = exec
		return w.spin("step "+step+" to start", func() bool { return w.started(step) })
	})
	sc.Step(`^a running local runtime has shut down$`, func(ctx context.Context) error {
		w := localStepWorld(ctx)
		if err := w.serve(ctx); err != nil {
			return err
		}
		return w.shutdown(ctx)
	})
	sc.Step(`^the local runtime shuts down$`, func(ctx context.Context) error {
		w := localStepWorld(ctx)
		return w.shutdown(ctx)
	})
	sc.Step(`^shutdown completes with no error$`, func(ctx context.Context) error {
		if err := localStepWorld(ctx).shutdownError; err != nil {
			return fmt.Errorf("shutdown failed: %w", err)
		}
		return nil
	})
	sc.Step(`^a run of "([^"]*)" is started on that runtime$`, func(ctx context.Context, flow string) error {
		w := localStepWorld(ctx)
		ref, err := w.runtime(ctx).Start(w.ctx("acme"), runtime.StartRequest{Flow: runtime.FlowRef{Name: flow}})
		w.lastStart, w.lastErr = ref, err
		return nil
	})
	sc.Step(`^(\d+) steps are executing$`, func(ctx context.Context, want int) error {
		w := localStepWorld(ctx)
		w.mu.Lock()
		active := w.activeSteps
		w.mu.Unlock()
		if active != want {
			return fmt.Errorf("%d steps are executing, want %d", active, want)
		}
		return nil
	})
}
