package steps

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func registerLocalTenant(sc *godog.ScenarioContext) {
	// Tenancy.
	sc.Step(`^tenant "([^"]*)" has run "([^"]*)" waiting at gate "([^"]*)"$`, func(ctx context.Context, tenant, runID, gate string) error {
		w := localStepWorld(ctx)
		w.setFlow("waiting", w.gateFlow(gate))
		_, exec, err := w.start(ctx, tenant, runID, "waiting")
		if err != nil {
			return err
		}
		return exec.wait("the run to await its gate", func() bool { return exec.atGate != "" })
	})
	sc.Step(`^tenant "([^"]*)" has run "([^"]*)" of flow "([^"]*)" waiting at gate "([^"]*)"$`,
		func(ctx context.Context, tenant, runID, flow, gate string) error {
			w := localStepWorld(ctx)
			w.setFlow(flow, w.gateFlow(gate))
			_, exec, err := w.start(ctx, tenant, runID, flow)
			if err != nil {
				return err
			}
			return exec.wait("the run to await its gate", func() bool { return exec.atGate != "" })
		})
	sc.Step(`^tenant "([^"]*)" runs flow "([^"]*)" as run "([^"]*)" that calls step "([^"]*)"$`,
		func(ctx context.Context, tenant, flow, runID, step string) error {
			w := localStepWorld(ctx)
			w.setFlow(flow, w.stepFlow(step))
			_, _, err := w.start(ctx, tenant, runID, flow)
			return err
		})
	sc.Step(`^both runs complete on one local runtime$`, func(ctx context.Context) error {
		w := localStepWorld(ctx)
		for _, exec := range w.execsOf("work") {
			if err := exec.wait("both runs to complete", func() bool { return exec.done }); err != nil {
				return err
			}
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		for key, exec := range w.byRun {
			if key.id != "shared-1" {
				continue
			}
			exec.mu.Lock()
			if exec.done && len(w.outputs[key]) == 0 {
				w.outputs[key] = append(json.RawMessage(nil), exec.result...)
			}
			exec.mu.Unlock()
		}
		return nil
	})
	sc.Step(`^run "([^"]*)" of tenant "([^"]*)" returns (\{.*\})$`, func(ctx context.Context, runID, tenant, want string) error {
		got := localStepWorld(ctx).output(runtime.RunRef{RunID: runID, TenantID: core.TenantID(tenant)})
		if !jsonEquivalent(got, json.RawMessage(want)) {
			return fmt.Errorf("run %q of tenant %q returns %s, want %s", runID, tenant, got, want)
		}
		return nil
	})
	sc.Step(`^tenant "([^"]*)" describes run "([^"]*)"$`, func(ctx context.Context, tenant, runID string) error {
		w := localStepWorld(ctx)
		status, err := w.runtime(ctx).Describe(w.ctx(tenant), runtime.RunRef{RunID: runID})
		w.lastStatus, w.lastErr = status, err
		return nil
	})
	sc.Step(`^describing run "([^"]*)" fails with code "([^"]*)"$`, func(ctx context.Context, runID, code string) error {
		return localLastError(localStepWorld(ctx), code)
	})
	sc.Step(`^tenant "([^"]*)" describing run "([^"]*)" fails with code "([^"]*)"$`, func(ctx context.Context, tenant, runID, code string) error {
		w := localStepWorld(ctx)
		_, err := w.runtime(ctx).Describe(w.ctx(tenant), runtime.RunRef{RunID: runID})
		if localCode(err) != code {
			return fmt.Errorf("describing run %q from tenant %q failed with %q, want %q: %w", runID, tenant, localCode(err), code, err)
		}
		return nil
	})
	sc.Step(`^tenant "([^"]*)" describes run "([^"]*)" as "([^"]*)"$`, func(ctx context.Context, tenant, runID, want string) error {
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
	sc.Step(`^tenant "([^"]*)" lists runs of flow "([^"]*)"$`, func(ctx context.Context, tenant, flow string) error {
		w := localStepWorld(ctx)
		runs, _, err := w.runtime(ctx).List(w.ctx(tenant), runtime.RunQuery{Flow: flow})
		w.lastList, w.lastErr = runs, err
		w.haveListed = true
		return nil
	})
	sc.Step(`^tenant "([^"]*)" lists (\d+) runs? for tenant "([^"]*)" with run id "([^"]*)"$`,
		func(ctx context.Context, caller string, want int, tenant, runID string) error {
			w := localStepWorld(ctx)
			if w.lastErr != nil {
				return w.lastErr
			}
			runs := w.lastList
			if !w.haveListed {
				var err error
				runs, _, err = w.runtime(ctx).List(w.ctx(caller), runtime.RunQuery{})
				if err != nil {
					return err
				}
			}
			if len(runs) != want {
				return fmt.Errorf("tenant %q lists %d runs, want %d", caller, len(runs), want)
			}
			for _, run := range runs {
				if run.Ref.RunID != runID || string(run.Ref.TenantID) != tenant {
					return fmt.Errorf("listed run is %q of tenant %q, want %q of tenant %q", run.Ref.RunID, run.Ref.TenantID, runID, tenant)
				}
			}
			return nil
		})
	sc.Step(`^tenant "([^"]*)" cancels run "([^"]*)"$`, func(ctx context.Context, tenant, runID string) error {
		w := localStepWorld(ctx)
		err := w.runtime(ctx).Cancel(w.ctx(tenant), runtime.RunRef{RunID: runID}, "")
		w.lastErr = err
		return nil
	})
	sc.Step(`^cancelling run "([^"]*)" fails with code "([^"]*)"$`, func(ctx context.Context, runID, code string) error {
		return localLastError(localStepWorld(ctx), code)
	})
	sc.Step(`^tenant "([^"]*)" resolves gate "([^"]*)" of run "([^"]*)" with payload (\{.*\})$`,
		func(ctx context.Context, tenant, gate, runID, payload string) error {
			w := localStepWorld(ctx)
			err := w.runtime(ctx).Resolve(w.ctx(tenant), runtime.RunRef{RunID: runID}, gate, json.RawMessage(payload))
			w.lastErr = err
			return nil
		})
	sc.Step(`^resolving the gate fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		return localLastError(localStepWorld(ctx), code)
	})
	sc.Step(`^tenant "([^"]*)" has no run with id "([^"]*)"$`, func(ctx context.Context, tenant, runID string) error {
		w := localStepWorld(ctx)
		_, err := w.runtime(ctx).Describe(w.ctx(tenant), runtime.RunRef{RunID: runID})
		if localCode(err) != "not_found" {
			return fmt.Errorf("tenant %q has a run %q (Describe error %w), want not_found", tenant, runID, err)
		}
		return nil
	})

	// The tenant of the call is the context's (REQ-RT-042, REQ-RT-043,
	// REQ-RT-044).
	sc.Step(`^run "([^"]*)" of "([^"]*)" is started with context tenant "([^"]*)" and request tenant "([^"]*)"$`,
		localStartWithTenants)
	sc.Step(`^run "([^"]*)" of "([^"]*)" is started with context tenant "([^"]*)" and an empty request tenant$`,
		func(ctx context.Context, runID, flow, contextTenant string) error {
			return localStartWithTenants(ctx, runID, flow, contextTenant, "")
		})
	sc.Step(`^a run of "([^"]*)" is started without a context tenant and with an empty request tenant$`,
		func(ctx context.Context, flow string) error {
			return localStartWithTenants(ctx, "", flow, "", "")
		})
	sc.Step(`^run "([^"]*)" of "([^"]*)" is started without a context tenant and with request tenant "([^"]*)"$`,
		func(ctx context.Context, runID, flow, requestTenant string) error {
			return localStartWithTenants(ctx, runID, flow, "", requestTenant)
		})
	sc.Step(`^starting the run fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		return localLastError(localStepWorld(ctx), code)
	})
	sc.Step(`^the request names a run reference of tenant "([^"]*)" to run "([^"]*)"$`, func(ctx context.Context, tenant, runID string) error {
		localStepWorld(ctx).pendingRef = runtime.RunRef{RunID: runID, TenantID: core.TenantID(tenant)}
		return nil
	})
	sc.Step(`^run "([^"]*)" is described with context tenant "([^"]*)"$`, func(ctx context.Context, runID, tenant string) error {
		w := localStepWorld(ctx)
		status, err := w.runtime(ctx).Describe(w.ctx(tenant), w.pendingRef)
		w.lastStatus, w.lastErr = status, err
		return nil
	})
	sc.Step(`^run "([^"]*)" is described with context tenant "([^"]*)" and an empty reference tenant$`, func(ctx context.Context, runID, tenant string) error {
		w := localStepWorld(ctx)
		status, err := w.runtime(ctx).Describe(w.ctx(tenant), runtime.RunRef{RunID: runID})
		w.lastStatus, w.lastErr = status, err
		return nil
	})
	sc.Step(`^run "([^"]*)" is described without a context tenant and with reference tenant "([^"]*)"$`,
		func(ctx context.Context, runID, tenant string) error {
			w := localStepWorld(ctx)
			status, err := w.runtime(ctx).Describe(w.ctx(""), runtime.RunRef{RunID: runID, TenantID: core.TenantID(tenant)})
			w.lastStatus, w.lastErr = status, err
			return nil
		})
	sc.Step(`^run "([^"]*)" is cancelled with context tenant "([^"]*)"$`, func(ctx context.Context, runID, tenant string) error {
		w := localStepWorld(ctx)
		w.lastErr = w.runtime(ctx).Cancel(w.ctx(tenant), w.pendingRef, "")
		return nil
	})
	sc.Step(`^run "([^"]*)" is cancelled without a context tenant and with reference tenant "([^"]*)"$`,
		func(ctx context.Context, runID, tenant string) error {
			w := localStepWorld(ctx)
			w.lastErr = w.runtime(ctx).Cancel(w.ctx(""), runtime.RunRef{RunID: runID, TenantID: core.TenantID(tenant)}, "")
			return nil
		})
	sc.Step(`^gate "([^"]*)" of run "([^"]*)" is resolved with context tenant "([^"]*)" and payload (\{.*\})$`,
		func(ctx context.Context, gate, runID, tenant, payload string) error {
			w := localStepWorld(ctx)
			w.lastErr = w.runtime(ctx).Resolve(w.ctx(tenant), w.pendingRef, gate, json.RawMessage(payload))
			return nil
		})
	sc.Step(`^gate "([^"]*)" of run "([^"]*)" is resolved without a context tenant, reference tenant "([^"]*)" and payload (\{.*\})$`,
		func(ctx context.Context, gate, runID, tenant, payload string) error {
			w := localStepWorld(ctx)
			w.lastErr = w.runtime(ctx).Resolve(w.ctx(""), runtime.RunRef{RunID: runID, TenantID: core.TenantID(tenant)}, gate, json.RawMessage(payload))
			return nil
		})
	sc.Step(`^runs of flow "([^"]*)" are listed for request tenant "([^"]*)" with context tenant "([^"]*)"$`,
		func(ctx context.Context, flow, requestTenant, contextTenant string) error {
			w := localStepWorld(ctx)
			runs, _, err := w.runtime(ctx).List(w.ctx(contextTenant), runtime.RunQuery{Flow: flow, TenantID: core.TenantID(requestTenant)})
			w.lastList, w.lastErr = runs, err
			w.haveListed = true
			return nil
		})
	sc.Step(`^runs of flow "([^"]*)" are listed with context tenant "([^"]*)" and an empty query tenant$`,
		func(ctx context.Context, flow, contextTenant string) error {
			w := localStepWorld(ctx)
			runs, _, err := w.runtime(ctx).List(w.ctx(contextTenant), runtime.RunQuery{Flow: flow})
			w.lastList, w.lastErr = runs, err
			w.haveListed = true
			return nil
		})
	sc.Step(`^runs of flow "([^"]*)" are listed without a context tenant and with query tenant "([^"]*)"$`,
		func(ctx context.Context, flow, queryTenant string) error {
			w := localStepWorld(ctx)
			runs, _, err := w.runtime(ctx).List(w.ctx(""), runtime.RunQuery{Flow: flow, TenantID: core.TenantID(queryTenant)})
			w.lastList, w.lastErr = runs, err
			w.haveListed = true
			return nil
		})
	sc.Step(`^listing runs fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		return localListFailure(localStepWorld(ctx), code)
	})
	sc.Step(`^the run reference the start returned is run "([^"]*)" of tenant "([^"]*)"$`, func(ctx context.Context, runID, tenant string) error {
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
		w := localStepWorld(ctx)
		if w.lastErr != nil {
			return fmt.Errorf("the description failed: %w", w.lastErr)
		}
		if w.lastStatus.Ref.RunID != runID || string(w.lastStatus.Ref.TenantID) != tenant {
			return fmt.Errorf("the description returned %q of tenant %q, want %q of tenant %q", w.lastStatus.Ref.RunID, w.lastStatus.Ref.TenantID, runID, tenant)
		}
		return nil
	})

}
