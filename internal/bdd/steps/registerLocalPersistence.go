package steps

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/runtime"
)

func registerLocalPersistence(sc *godog.ScenarioContext) {
	// REQ-RT-006: nothing survives a new runtime.
	sc.Step(`^tenant "([^"]*)" has run "([^"]*)" of "([^"]*)" on an earlier local runtime$`, func(ctx context.Context, tenant, runID, flow string) error {
		w := localStepWorld(ctx)
		w.setFlow(flow, w.gateFlow("approve"))
		_, exec, err := w.start(ctx, tenant, runID, flow)
		if err != nil {
			return err
		}
		return exec.wait("the run to await its gate", func() bool { return exec.atGate != "" })
	})
	sc.Step(`^a new local runtime starts and tenant "([^"]*)" describes run "([^"]*)"$`, func(ctx context.Context, tenant, runID string) error {
		w := localStepWorld(ctx)
		w.newRuntime(ctx)
		status, err := w.runtime(ctx).Describe(w.ctx(tenant), runtime.RunRef{RunID: runID})
		w.lastStatus, w.lastErr = status, err
		return nil
	})
	sc.Step(`^tenant "([^"]*)" ran flow "([^"]*)" on an earlier local runtime$`, func(ctx context.Context, tenant, flow string) error {
		w := localStepWorld(ctx)
		w.setFlow(flow, w.gateFlow("approve"))
		_, exec, err := w.start(ctx, tenant, "", flow)
		if err != nil {
			return err
		}
		return exec.wait("the run to await its gate", func() bool { return exec.atGate != "" })
	})
	sc.Step(`^a new local runtime starts and tenant "([^"]*)" lists runs of flow "([^"]*)"$`, func(ctx context.Context, tenant, flow string) error {
		w := localStepWorld(ctx)
		w.newRuntime(ctx)
		runs, _, err := w.runtime(ctx).List(w.ctx(tenant), runtime.RunQuery{Flow: flow})
		w.lastList, w.lastErr = runs, err
		return nil
	})
	sc.Step(`^tenant "([^"]*)" lists (\d+) runs?$`, func(ctx context.Context, tenant string, want int) error {
		w := localStepWorld(ctx)
		if w.lastErr != nil {
			return w.lastErr
		}
		if len(w.lastList) != want {
			return fmt.Errorf("tenant %q lists %d runs, want %d", tenant, len(w.lastList), want)
		}
		return nil
	})

}
