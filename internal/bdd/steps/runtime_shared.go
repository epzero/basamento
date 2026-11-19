package steps

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// temporalScenario reports whether the scenario runs against the Temporal
// runtime. The two runtime harnesses share a set of phrases; each handler here
// routes the call to the harness whose runtime the scenario chose.
func temporalScenario(ctx context.Context) bool {
	w := world(ctx).Temporal
	return w != nil && w.scenario
}

// registerRuntimeShared registers the phrases both runtime scenarios use. It
// replaces the duplicate registrations in runtime_local.go and temporal.go:
// Godog has one step namespace per suite.
func registerRuntimeShared(sc *godog.ScenarioContext) {
	sc.Step(`^a flow calls step "([^"]*)" that returns (\{.*\})$`, func(ctx context.Context, name, result string) error {
		if temporalScenario(ctx) {
			return temporalW(ctx).addStepFlow("fetch", name, json.RawMessage(result), true, nil)
		}
		w := localStepWorld(ctx)
		if err := w.ensureStep(ctx, name); err != nil {
			return err
		}
		w.behavior(name).defaultResult = json.RawMessage(result)
		w.setFlow(name, w.stepFlow(name))
		return nil
	})

	sc.Step(`^flow "([^"]*)" waits at gate "([^"]*)"$`, func(ctx context.Context, flow, gate string) error {
		if temporalScenario(ctx) {
			return temporalW(ctx).ensureNamedFlow(flow)
		}
		w := localStepWorld(ctx)
		w.setFlow(flow, w.gateFlow(gate))
		return nil
	})

	sc.Step(`^step "([^"]*)" fails with code "([^"]*)" marked retryable (true|false)$`, func(ctx context.Context, name, code, retryable string) error {
		if temporalScenario(ctx) {
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
		}
		return localStepError(ctx, name, code, retryable, 0)
	})

	sc.Step(`^the step allows (\d+) attempts$`, func(ctx context.Context, attempts int) error {
		if temporalScenario(ctx) {
			temporalW(ctx).attempts = attempts
			return nil
		}
		w := localStepWorld(ctx)
		w.mu.Lock()
		options := w.stepOptions["charge"]
		options.MaxAttempts = attempts
		w.stepOptions["charge"] = options
		w.mu.Unlock()
		return nil
	})

	sc.Step(`^the step fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		if temporalScenario(ctx) {
			return temporalStepFailure(temporalW(ctx), code)
		}
		return localStepFailed(ctx, code)
	})

	sc.Step(`^the runtime reports capability "([^"]*)" as (true|false)$`, func(ctx context.Context, name, want string) error {
		if temporalScenario(ctx) {
			if err := temporalW(ctx).ensureRuntime(); err != nil {
				return err
			}
			caps := temporalW(ctx).rt.Capabilities()
			var got bool
			switch name {
			case "durable":
				got = caps.Durable
			case "sync_gate_updates":
				got = caps.SyncGateUpdates
			case "continue_as_new":
				got = caps.ContinueAsNew
			case "polyglot_queues":
				got = caps.PolyglotQueues
			default:
				return fmt.Errorf("unknown capability %q", name)
			}
			if fmt.Sprint(got) != want {
				return fmt.Errorf("capability %q is %v, want %s", name, got, want)
			}
			return nil
		}
		caps := localStepWorld(ctx).runtime(ctx).Capabilities()
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

	sc.Step(`^the request names a run reference of tenant "([^"]*)" to run "([^"]*)"$`, func(ctx context.Context, tenant, runID string) error {
		if temporalScenario(ctx) {
			temporalW(ctx).lastRef = runtime.RunRef{RunID: runID, TenantID: core.TenantID(tenant)}
			return nil
		}
		localStepWorld(ctx).pendingRef = runtime.RunRef{RunID: runID, TenantID: core.TenantID(tenant)}
		return nil
	})

	sc.Step(`^the run reference the start returned is run "([^"]*)" of tenant "([^"]*)"$`, func(ctx context.Context, runID, tenant string) error {
		if temporalScenario(ctx) {
			w := temporalW(ctx)
			if w.lastRef.RunID != runID || string(w.lastRef.TenantID) != tenant {
				return fmt.Errorf("ref %+v, want run %q of tenant %q", w.lastRef, runID, tenant)
			}
			return nil
		}
		w := localStepWorld(ctx)
		if w.lastErr != nil {
			return fmt.Errorf("the start failed: %w", w.lastErr)
		}
		if w.lastStart.RunID != runID || string(w.lastStart.TenantID) != tenant {
			return fmt.Errorf("the start returned %q of tenant %q, want %q of tenant %q", w.lastStart.RunID, w.lastStart.TenantID, runID, tenant)
		}
		return nil
	})

	sc.Step(`^the run reference the description returned is run "([^"]*)" of tenant "([^"]*)"$`, func(ctx context.Context, runID, tenant string) error {
		if temporalScenario(ctx) {
			w := temporalW(ctx)
			status, err := w.rt.Describe(core.WithTenant(context.Background(), core.TenantID(tenant)), runtime.RunRef{RunID: runID})
			if err != nil {
				return err
			}
			if status.Ref.RunID != runID || string(status.Ref.TenantID) != tenant {
				return fmt.Errorf("ref %+v, want run %q of tenant %q", status.Ref, runID, tenant)
			}
			return nil
		}
		w := localStepWorld(ctx)
		if w.lastErr != nil {
			return fmt.Errorf("the description failed: %w", w.lastErr)
		}
		if w.lastStatus.Ref.RunID != runID || string(w.lastStatus.Ref.TenantID) != tenant {
			return fmt.Errorf("the description returned %q of tenant %q, want %q of tenant %q", w.lastStatus.Ref.RunID, w.lastStatus.Ref.TenantID, runID, tenant)
		}
		return nil
	})

	sc.Step(`^tenant "([^"]*)" describes run "([^"]*)" as "([^"]*)"$`, func(ctx context.Context, tenant, runID, want string) error {
		if temporalScenario(ctx) {
			w := temporalW(ctx)
			got, err := w.state(tenant, runtime.RunRef{RunID: runID})
			if err != nil {
				return err
			}
			if got != want {
				return fmt.Errorf("tenant %q sees run %q as %q, want %q", tenant, runID, got, want)
			}
			return nil
		}
		w := localStepWorld(ctx)
		state, err := w.stateOf(ctx, tenant, runID)
		if err != nil {
			return err
		}
		if state != want {
			return fmt.Errorf("run %q of tenant %q is %q, want %q", runID, tenant, state, want)
		}
		return nil
	})

	sc.Step(`^run "([^"]*)" is "([^"]*)"$`, func(ctx context.Context, runID, want string) error {
		if temporalScenario(ctx) {
			w := temporalW(ctx)
			got, err := w.state("acme", runtime.RunRef{RunID: runID})
			if err != nil {
				return err
			}
			if got != want {
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

	sc.Step(`^the run is "([^"]*)"$`, func(ctx context.Context, want string) error {
		if temporalScenario(ctx) {
			w := temporalW(ctx)
			got, err := w.state(string(w.lastRef.TenantID), w.lastRef)
			if err != nil {
				return err
			}
			if got != want {
				return fmt.Errorf("run state %q, want %q", got, want)
			}
			return nil
		}
		return localRunState(ctx, want)
	})

	// The remaining phrases are failures of the last call: the harness that made
	// the call kept the error, and the assertion's wording is the same.
	sc.Step(`^starting the run fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		if temporalScenario(ctx) {
			if err := temporalW(ctx).lastErr; err == nil {
				return fmt.Errorf("start succeeded, want %s", code)
			} else if got := string(core.CodeOf(err)); got != code {
				return fmt.Errorf("start code %q, want %q", got, code)
			}
			return nil
		}
		return localLastError(localStepWorld(ctx), code)
	})
	for _, register := range []func(*godog.ScenarioContext){
		func(sc *godog.ScenarioContext) {
			sc.Step(`^cancelling run "([^"]*)" fails with code "([^"]*)"$`, func(ctx context.Context, runID, code string) error {
				if temporalScenario(ctx) {
					return temporalFailure(temporalW(ctx).lastErr, code)
				}
				return localLastError(localStepWorld(ctx), code)
			})
		},
		func(sc *godog.ScenarioContext) {
			sc.Step(`^describing run "([^"]*)" fails with code "([^"]*)"$`, func(ctx context.Context, runID, code string) error {
				if temporalScenario(ctx) {
					return temporalFailure(temporalW(ctx).lastErr, code)
				}
				return localLastError(localStepWorld(ctx), code)
			})
		},
		func(sc *godog.ScenarioContext) {
			sc.Step(`^listing runs fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
				if temporalScenario(ctx) {
					return temporalFailure(temporalW(ctx).lastErr, code)
				}
				return localLastError(localStepWorld(ctx), code)
			})
		},
		func(sc *godog.ScenarioContext) {
			sc.Step(`^resolving the gate fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
				if temporalScenario(ctx) {
					return temporalFailure(temporalW(ctx).lastErr, code)
				}
				return localLastError(localStepWorld(ctx), code)
			})
		},
	} {
		register(sc)
	}
}
