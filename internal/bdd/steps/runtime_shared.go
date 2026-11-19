package steps

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// localActive reports which runtime the scenario named. Both runtime worlds
// exist in a scenario, but only the one the Given step created holds runs.
//
// The steps below have a local and a temporal behaviour and one phrase each:
// godog refuses two definitions for one phrase, so the merge of WP-L5-01 and
// WP-L5-02 keeps one definition that dispatches here.
func localActive(ctx context.Context) bool {
	if name := conformanceW(ctx).runtimeName; name != "" {
		return name == "local"
	}
	w := localStepWorld(ctx)
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.rt != nil
}

func registerRuntimeShared(sc *godog.ScenarioContext) {
	sc.Step(`^the run returns (.+)$`, func(ctx context.Context, want string) error {
		if localActive(ctx) {
			exec := localStepWorld(ctx).currentExec
			if exec == nil {
				return fmt.Errorf("no run has started")
			}
			exec.mu.Lock()
			result := append(json.RawMessage(nil), exec.result...)
			exec.mu.Unlock()
			if !jsonEquivalent(result, json.RawMessage(want)) {
				return fmt.Errorf("the run returns %s, want %s", result, want)
			}
			return nil
		}
		w := temporalW(ctx)
		got, err := w.result(string(w.lastRef.TenantID), w.lastRef)
		if err != nil {
			return err
		}
		if !jsonEquivalent(got, temporalValue(want)) {
			return fmt.Errorf("the run returns %s, want %s", got, want)
		}
		return nil
	})
	sc.Step(`^step "([^"]*)" was executed (\d+) times?$`, func(ctx context.Context, name string, want int) error {
		var got int
		if localActive(ctx) {
			got = localStepWorld(ctx).executionsOf(name)
		} else {
			got = temporalW(ctx).stepCalls(name)
		}
		if got != want {
			return fmt.Errorf("step %q ran %d times, want %d", name, got, want)
		}
		return nil
	})
	sc.Step(`^tenant "([^"]*)" lists (\d+) runs? for tenant "([^"]*)" with run id "([^"]*)"$`, func(ctx context.Context, listTenant string, want int, refTenant, runID string) error {
		if localActive(ctx) {
			w := localStepWorld(ctx)
			if w.lastErr != nil {
				return w.lastErr
			}
			if len(w.lastList) != want {
				return fmt.Errorf("tenant %q lists %d runs, want %d", listTenant, len(w.lastList), want)
			}
			for _, run := range w.lastList {
				if run.Ref.RunID != runID || string(run.Ref.TenantID) != refTenant {
					return fmt.Errorf("listed run is %q of tenant %q, want %q of tenant %q", run.Ref.RunID, run.Ref.TenantID, runID, refTenant)
				}
			}
			return nil
		}
		w := temporalW(ctx)
		statuses, _, err := w.rt.List(core.WithTenant(context.Background(), core.TenantID(listTenant)), runtime.RunQuery{Flow: "review"})
		if err != nil {
			return err
		}
		if len(statuses) != want {
			return fmt.Errorf("listed %d runs, want %d", len(statuses), want)
		}
		for _, status := range statuses {
			if status.Ref.RunID != runID || string(status.Ref.TenantID) != refTenant {
				return fmt.Errorf("listed ref %+v, want run %q of tenant %q", status.Ref, runID, refTenant)
			}
		}
		return nil
	})

	sc.Step(`^the runtime reports capability "([^"]*)" as (true|false)$`, func(ctx context.Context, name, want string) error {
		var caps runtime.Caps
		if localActive(ctx) {
			caps = localStepWorld(ctx).runtime(ctx).Capabilities()
		} else {
			w := temporalW(ctx)
			if err := w.ensureRuntime(); err != nil {
				return err
			}
			caps = w.rt.Capabilities()
		}
		values := map[string]bool{
			"durable":           caps.Durable,
			"sync_gate_updates": caps.SyncGateUpdates,
			"continue_as_new":   caps.ContinueAsNew,
			"polyglot_queues":   caps.PolyglotQueues,
		}
		got, ok := values[name]
		if !ok {
			return fmt.Errorf("unknown capability %q", name)
		}
		if fmt.Sprintf("%t", got) != want {
			return fmt.Errorf("capability %q is %t, want %s", name, got, want)
		}
		return nil
	})

	sc.Step(`^step "([^"]*)" fails with code "([^"]*)" marked retryable (true|false)$`, func(ctx context.Context, name, code, retryable string) error {
		if localActive(ctx) {
			return localStepError(ctx, name, code, retryable, 0)
		}
		w := temporalW(ctx)
		if err := w.ensureStep(name); err != nil {
			return err
		}
		w.mu.Lock()
		b := w.behaviors[name]
		b.code = core.Code(code)
		b.retryable = retryable == "true"
		b.failFirst = -1
		w.mu.Unlock()
		return nil
	})
	sc.Step(`^the step allows (\d+) attempts$`, func(ctx context.Context, attempts int) error {
		if localActive(ctx) {
			w := localStepWorld(ctx)
			w.mu.Lock()
			options := w.stepOptions["charge"]
			options.MaxAttempts = attempts
			w.stepOptions["charge"] = options
			w.mu.Unlock()
			return nil
		}
		temporalW(ctx).attempts = attempts
		return nil
	})
	sc.Step(`^the step fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		if localActive(ctx) {
			return localStepFailed(ctx, code)
		}
		return temporalStepFailure(temporalW(ctx), code)
	})
	sc.Step(`^the run is "([^"]*)"$`, func(ctx context.Context, want string) error {
		if localActive(ctx) {
			return localRunState(ctx, want)
		}
		w := temporalW(ctx)
		got, err := w.state(string(w.lastRef.TenantID), w.lastRef)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("run state %q, want %q", got, want)
		}
		return nil
	})
	sc.Step(`^run "([^"]*)" is "([^"]*)"$`, func(ctx context.Context, runID, want string) error {
		if localActive(ctx) {
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
		}
		w := temporalW(ctx)
		got, err := w.state(string(w.lastRef.TenantID), runtime.RunRef{RunID: runID})
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("run %q is %q, want %q", runID, got, want)
		}
		return nil
	})
	sc.Step(`^a flow calls step "([^"]*)" that returns (\{.*\})$`, func(ctx context.Context, name, result string) error {
		if localActive(ctx) {
			w := localStepWorld(ctx)
			if err := w.ensureStep(ctx, name); err != nil {
				return err
			}
			w.behavior(name).defaultResult = json.RawMessage(result)
			w.setFlow(name, w.stepFlow(name))
			return nil
		}
		return temporalW(ctx).addStepFlow("fetch", name, json.RawMessage(result), true, nil)
	})
	sc.Step(`^starting the run fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		if localActive(ctx) {
			return localLastError(localStepWorld(ctx), code)
		}
		return temporalFailure(temporalW(ctx).lastErr, code)
	})
	sc.Step(`^flow "([^"]*)" waits at gate "([^"]*)"$`, func(ctx context.Context, flowName, gate string) error {
		if localActive(ctx) {
			w := localStepWorld(ctx)
			w.setFlow(flowName, w.gateFlow(gate))
			return nil
		}
		return temporalW(ctx).ensureNamedFlow(flowName)
	})
	sc.Step(`^the request names a run reference of tenant "([^"]*)" to run "([^"]*)"$`, func(ctx context.Context, tenant, runID string) error {
		if localActive(ctx) {
			localStepWorld(ctx).pendingRef = runtime.RunRef{RunID: runID, TenantID: core.TenantID(tenant)}
			return nil
		}
		temporalW(ctx).lastRef = runtime.RunRef{RunID: runID, TenantID: core.TenantID(tenant)}
		return nil
	})
	sc.Step(`^describing run "([^"]*)" fails with code "([^"]*)"$`, func(ctx context.Context, runID, code string) error {
		if localActive(ctx) {
			return localLastError(localStepWorld(ctx), code)
		}
		return temporalFailure(temporalW(ctx).lastErr, code)
	})
	sc.Step(`^cancelling run "([^"]*)" fails with code "([^"]*)"$`, func(ctx context.Context, runID, code string) error {
		if localActive(ctx) {
			return localLastError(localStepWorld(ctx), code)
		}
		return temporalFailure(temporalW(ctx).lastErr, code)
	})
	sc.Step(`^resolving the gate fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		if localActive(ctx) {
			return localLastError(localStepWorld(ctx), code)
		}
		return temporalFailure(temporalW(ctx).lastErr, code)
	})
	sc.Step(`^listing runs fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		if localActive(ctx) {
			return localLastError(localStepWorld(ctx), code)
		}
		return temporalFailure(temporalW(ctx).lastErr, code)
	})
	sc.Step(`^the run reference the start returned is run "([^"]*)" of tenant "([^"]*)"$`, func(ctx context.Context, runID, tenant string) error {
		if localActive(ctx) {
			w := localStepWorld(ctx)
			if w.lastErr != nil {
				return fmt.Errorf("the start failed: %w", w.lastErr)
			}
			if w.lastStart.RunID != runID || string(w.lastStart.TenantID) != tenant {
				return fmt.Errorf("the start returned %q of tenant %q, want %q of tenant %q", w.lastStart.RunID, w.lastStart.TenantID, runID, tenant)
			}
			return nil
		}
		w := temporalW(ctx)
		if w.lastRef.RunID != runID || string(w.lastRef.TenantID) != tenant {
			return fmt.Errorf("ref %+v, want run %q of tenant %q", w.lastRef, runID, tenant)
		}
		return nil
	})
	sc.Step(`^the run reference the description returned is run "([^"]*)" of tenant "([^"]*)"$`, func(ctx context.Context, runID, tenant string) error {
		if localActive(ctx) {
			w := localStepWorld(ctx)
			if w.lastErr != nil {
				return fmt.Errorf("the description failed: %w", w.lastErr)
			}
			if w.lastStatus.Ref.RunID != runID || string(w.lastStatus.Ref.TenantID) != tenant {
				return fmt.Errorf("the description returned %q of tenant %q, want %q of tenant %q", w.lastStatus.Ref.RunID, w.lastStatus.Ref.TenantID, runID, tenant)
			}
			return nil
		}
		w := temporalW(ctx)
		status, err := w.rt.Describe(core.WithTenant(context.Background(), core.TenantID(tenant)), runtime.RunRef{RunID: runID})
		if err != nil {
			return err
		}
		if status.Ref.RunID != runID || string(status.Ref.TenantID) != tenant {
			return fmt.Errorf("ref %+v, want run %q of tenant %q", status.Ref, runID, tenant)
		}
		return nil
	})
}
