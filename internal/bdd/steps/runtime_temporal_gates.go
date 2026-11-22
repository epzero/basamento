package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cucumber/godog"
	sdktemporal "go.temporal.io/sdk/temporal"

	"github.com/epzero/basamento/core"
)

func registerTemporalGates(sc *godog.ScenarioContext) {
	sc.Step(`^a run of "([^"]*)" on the temporal runtime has started and waits at gate "([^"]*)"$`,
		func(ctx context.Context, flow, gate string) error {
			w := temporalW(ctx)
			w.pendingGate = pendingGateRun{flow: flow, runID: "r1", gate: gate}
			return nil
		})
	sc.Step(`^run "([^"]*)" of that flow on the temporal runtime is waiting at gate "([^"]*)" after continuing as new$`,
		func(ctx context.Context, runID, gate string) error {
			w := temporalW(ctx)
			if w.pendingGate.flow == "" {
				w.pendingGate.flow = "long"
			}
			w.pendingGate.runID = runID
			w.pendingGate.gate = gate
			w.pendingGate.afterContinue = true
			return nil
		})
	sc.Step(`^gate "([^"]*)" is resolved with payload (\{.*\})$`, func(ctx context.Context, gate, payload string) error {
		return resolveTemporalGate(ctx, gate, "r1", json.RawMessage(payload), false)
	})
	sc.Step(`^gate "([^"]*)" on run "([^"]*)" is resolved with payload (\{.*\})$`,
		func(ctx context.Context, gate, runID, payload string) error {
			return resolveTemporalGate(ctx, gate, runID, json.RawMessage(payload), true)
		})
}

func resolveTemporalGate(ctx context.Context, gate, runID string, payload json.RawMessage, requireContinue bool) error {
	w := temporalW(ctx)
	flow := w.pendingGate.flow
	if flow == "" {
		flow = "short"
	}
	if runID == "" {
		runID = w.pendingGate.runID
	}
	if runID == "" {
		runID = "r1"
	}
	arm := &gateResolveArm{
		name:    gate,
		payload: append(json.RawMessage(nil), payload...),
	}
	if requireContinue || w.pendingGate.afterContinue {
		arm.requireContinuations = 1
	}
	err := w.runChain(chainRunConfig{
		tenant: core.TenantID("acme"),
		flow:   flow,
		runID:  runID,
		gate:   arm,
	})
	if err != nil {
		return err
	}
	if !arm.callbackFired {
		return fmt.Errorf("the run never waited at gate %q", gate)
	}
	if arm.requireContinuations > 0 && arm.continuesAtFire < arm.requireContinuations {
		return fmt.Errorf("continued %d times at gate resolve, want at least %d", arm.continuesAtFire, arm.requireContinuations)
	}
	if arm.outcome.rejected != nil {
		return gateRejectError(arm.outcome.rejected)
	}
	if !arm.outcome.accepted || !arm.outcome.completed {
		return fmt.Errorf("gate %q update was not accepted and completed", gate)
	}
	return nil
}

func gateRejectError(err error) error {
	var app *sdktemporal.ApplicationError
	if errors.As(err, &app) {
		return fmt.Errorf("gate rejected: code %q message %q", app.Type(), app.Message())
	}
	return fmt.Errorf("gate rejected: %w", err)
}
