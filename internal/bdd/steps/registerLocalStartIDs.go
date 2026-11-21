package steps

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/runtime"
)

func registerLocalStartIDs(sc *godog.ScenarioContext) {
	sc.Step(`^flow "([^"]*)" calls step "([^"]*)" that returns (\{.*\})$`,
		func(ctx context.Context, flow, step, result string) error {
			w := localStepWorld(ctx)
			if err := w.ensureStep(ctx, step); err != nil {
				return err
			}
			w.behavior(step).defaultResult = json.RawMessage(result)
			w.setFlow(flow, w.stepFlow(step))
			return nil
		})
	sc.Step(`^tenant "([^"]*)" starts run "([^"]*)" of "([^"]*)" with start idempotency key "([^"]*)"$`,
		func(ctx context.Context, tenant, runID, flow, key string) error {
			w := localStepWorld(ctx)
			return w.startRun(ctx, tenant, runID, flow, key, true)
		})
	sc.Step(`^tenant "([^"]*)" starts run "([^"]*)" of "([^"]*)" with an empty start idempotency key$`,
		func(ctx context.Context, tenant, runID, flow string) error {
			return localStepWorld(ctx).startRun(ctx, tenant, runID, flow, "", false)
		})
	sc.Step(`^tenant "([^"]*)" starts a run of "([^"]*)" with start idempotency key "([^"]*)"$`,
		func(ctx context.Context, tenant, flow, key string) error {
			return localStepWorld(ctx).startRun(ctx, tenant, "", flow, key, false)
		})
	sc.Step(`^the run reference the second start returned is run "([^"]*)" of tenant "([^"]*)"$`,
		func(ctx context.Context, runID, tenant string) error {
			w := localStepWorld(ctx)
			if w.lastErr != nil {
				return fmt.Errorf("the second start failed: %w", w.lastErr)
			}
			if w.lastStart.RunID != runID || string(w.lastStart.TenantID) != tenant {
				return fmt.Errorf("the second start returned %q of tenant %q, want %q of tenant %q",
					w.lastStart.RunID, w.lastStart.TenantID, runID, tenant)
			}
			w.mu.Lock()
			first := w.firstStartRef
			w.mu.Unlock()
			if first.RunID != w.lastStart.RunID || first.TenantID != w.lastStart.TenantID {
				return fmt.Errorf("second start returned %+v, want same ref as first start %+v", w.lastStart, first)
			}
			return nil
		})
	sc.Step(`^tenant "([^"]*)" lists (\d+) runs? of flow "([^"]*)"$`,
		func(ctx context.Context, tenant string, want int, flow string) error {
			w := localStepWorld(ctx)
			runs, _, err := w.runtime(ctx).List(w.ctx(tenant), runtime.RunQuery{Flow: flow})
			if err != nil {
				return err
			}
			if len(runs) != want {
				return fmt.Errorf("tenant %q lists %d runs of flow %q, want %d", tenant, len(runs), flow, want)
			}
			return nil
		})
}
