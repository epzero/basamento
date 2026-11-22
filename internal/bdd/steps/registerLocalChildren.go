package steps

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cucumber/godog"
)

func registerLocalChildren(sc *godog.ScenarioContext) {
	// Children.
	sc.Step(`^tenant "([^"]*)" has parent flow "([^"]*)" that starts child flow "([^"]*)" with child run id "([^"]*)"$`,
		func(ctx context.Context, tenant, parent, child, childID string) error {
			w := localStepWorld(ctx)
			w.setChildFlow(child, `{}`)
			w.setParentFlow(parent, child, childID, "", true)
			return nil
		})
	sc.Step(`^parent flow "([^"]*)" starts child flow "([^"]*)" with child run id "([^"]*)"$`,
		func(ctx context.Context, parent, child, childID string) error {
			w := localStepWorld(ctx)
			w.setChildFlow(child, `{}`)
			w.setParentFlow(parent, child, childID, "", true)
			return nil
		})
	sc.Step(`^parent flow "([^"]*)" starts child flow "([^"]*)" with child run id "([^"]*)" and close policy "([^"]*)"$`,
		func(ctx context.Context, parent, child, childID, policy string) error {
			w := localStepWorld(ctx)
			w.setParentFlow(parent, child, childID, policy, false)
			return nil
		})
	sc.Step(`^flow "([^"]*)" returns (\{.*\})$`, func(ctx context.Context, name, out string) error {
		localStepWorld(ctx).setChildFlow(name, out)
		return nil
	})
	sc.Step(`^flow "([^"]*)" waits at gate "([^"]*)"$`, func(ctx context.Context, name, gate string) error {
		if world(ctx).Temporal != nil {
			return temporalW(ctx).ensureControlFlow(name, gate)
		}
		w := localStepWorld(ctx)
		w.setFlow(name, w.gateFlow(gate))
		return nil
	})
	sc.Step(`^a run of "([^"]*)" completes without waiting for "([^"]*)"$`, func(ctx context.Context, flow, childID string) error {
		w := localStepWorld(ctx)
		ref, _, err := w.start(ctx, "acme", "", flow)
		if err != nil {
			return err
		}
		return w.waitRunState(ctx, ref, "completed")
	})
	sc.Step(`^the parent receives child output (\{.*\})$`, func(ctx context.Context, want string) error {
		w := localStepWorld(ctx)
		exec := w.currentExec
		if exec == nil {
			return fmt.Errorf("no run has started")
		}
		exec.mu.Lock()
		out := exec.childOut
		exec.mu.Unlock()
		if !jsonEquivalent(out, json.RawMessage(want)) {
			return fmt.Errorf("the parent received %s, want %s", out, want)
		}
		return nil
	})

}
