package steps

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/runtime"
)

func registerLocalRuns(sc *godog.ScenarioContext) {
	// Runs and their state.
	sc.Step(`^a run of "([^"]*)" runs$`, func(ctx context.Context, flow string) error {
		w := localStepWorld(ctx)
		w.noteClock(ctx)
		w.setFlow(flow, w.stepFlow("charge"))
		ref, _, err := w.start(ctx, "acme", "", flow)
		if err != nil {
			return err
		}
		return w.waitRunClosed(ctx, ref)
	})
	sc.Step(`^a run of "([^"]*)" completes$`, func(ctx context.Context, flow string) error {
		w := localStepWorld(ctx)
		w.noteClock(ctx)
		ref, exec, err := w.start(ctx, "acme", "", flow)
		if err != nil {
			return err
		}
		if err := w.waitRunState(ctx, ref, "completed"); err != nil {
			return err
		}
		w.recordOutput(ref, exec)
		return nil
	})
	sc.Step(`^a run of "([^"]*)" for tenant "([^"]*)" completes$`, func(ctx context.Context, flow, tenant string) error {
		w := localStepWorld(ctx)
		ref, exec, err := w.start(ctx, tenant, "", flow)
		if err != nil {
			return err
		}
		if err := w.waitRunState(ctx, ref, "completed"); err != nil {
			return err
		}
		w.recordOutput(ref, exec)
		return nil
	})
	sc.Step(`^runs "([^"]*)" and "([^"]*)" of "([^"]*)" complete in order on one local runtime$`,
		func(ctx context.Context, first, second, flow string) error {
			w := localStepWorld(ctx)
			for _, id := range []string{first, second} {
				ref, exec, err := w.start(ctx, "acme", id, flow)
				if err != nil {
					return err
				}
				if err := w.waitRunState(ctx, ref, "completed"); err != nil {
					return err
				}
				w.recordOutput(ref, exec)
			}
			return nil
		})
	sc.Step(`^run "([^"]*)" returns (\{.*\}) and run "([^"]*)" returns (\{.*\})$`, func(ctx context.Context, first, firstOut, second, secondOut string) error {
		w := localStepWorld(ctx)
		gotFirst := w.output(runtime.RunRef{RunID: first, TenantID: "acme"})
		gotSecond := w.output(runtime.RunRef{RunID: second, TenantID: "acme"})
		if !jsonEquivalent(gotFirst, json.RawMessage(firstOut)) {
			return fmt.Errorf("run %q returns %s, want %s", first, gotFirst, firstOut)
		}
		if !jsonEquivalent(gotSecond, json.RawMessage(secondOut)) {
			return fmt.Errorf("run %q returns %s, want %s", second, gotSecond, secondOut)
		}
		return nil
	})
	sc.Step(`^both results are (\{.*\})$`, func(ctx context.Context, want string) error {
		w := localStepWorld(ctx)
		exec := w.currentExec
		if exec == nil {
			return fmt.Errorf("no run has started")
		}
		exec.mu.Lock()
		first, second := exec.first, exec.second
		exec.mu.Unlock()
		if !jsonEquivalent(first, json.RawMessage(want)) || !jsonEquivalent(second, json.RawMessage(want)) {
			return fmt.Errorf("results are %s and %s, want both %s", first, second, want)
		}
		return nil
	})
	sc.Step(`^the run is still "([^"]*)"$`, localRunState)
	sc.Step(`^the run is described as "([^"]*)"$`, localRunState)
	sc.Step(`^run "([^"]*)" is "([^"]*)"$`, func(ctx context.Context, runID, want string) error {
		if tw := world(ctx).Temporal; tw != nil && tw.rt != nil {
			tw.waitDone()
			if got := tw.runState(); got != want {
				return fmt.Errorf("run %q is %q, want %q", runID, got, want)
			}
			return nil
		}
		w := localStepWorld(ctx)
		state, err := w.stateOf(ctx, "acme", runID)
		if localCode(err) == "not_found" {
			state, err = w.stateOf(ctx, "globex", runID)
		}
		if err != nil {
			return err
		}
		if state != want {
			return fmt.Errorf("run %q is %q, want %q", runID, state, want)
		}
		return nil
	})

}
