package steps

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func registerTemporalDescribe(sc *godog.ScenarioContext) {
	sc.Step(`^run "([^"]*)" is described on the temporal runtime with context tenant "([^"]*)"$`,
		func(ctx context.Context, runID, contextTenant string) error {
			w := temporalW(ctx)
			rt, err := w.runtimeForControl()
			if err != nil {
				return err
			}
			status, err := rt.Describe(w.temporalCtx(contextTenant), w.pendingControlRef())
			w.recordControlDescribe(status, err)
			return nil
		})
	sc.Step(`^run "([^"]*)" is described on the temporal runtime with context tenant "([^"]*)" and an empty reference tenant$`,
		func(ctx context.Context, runID, contextTenant string) error {
			w := temporalW(ctx)
			rt, err := w.runtimeForControl()
			if err != nil {
				return err
			}
			status, err := rt.Describe(w.temporalCtx(contextTenant), runtime.RunRef{RunID: runID})
			w.recordControlDescribe(status, err)
			return nil
		})
	sc.Step(`^run "([^"]*)" is described on the temporal runtime without a context tenant and with reference tenant "([^"]*)"$`,
		func(ctx context.Context, runID, tenant string) error {
			w := temporalW(ctx)
			rt, err := w.runtimeForControl()
			if err != nil {
				return err
			}
			status, err := rt.Describe(w.temporalCtx(""), runtime.RunRef{RunID: runID, TenantID: core.TenantID(tenant)})
			w.recordControlDescribe(status, err)
			return nil
		})
}

func (w *temporalWorld) recordControlDescribe(status runtime.RunStatus, err error) {
	w.mu.Lock()
	w.control.lastStatus = status
	w.control.lastErr = err
	w.mu.Unlock()
}

func temporalStateOf(w *temporalWorld, tenant, runID string) (string, error) {
	rt, err := w.runtimeForControl()
	if err != nil {
		return "", err
	}
	status, err := rt.Describe(w.temporalCtx(tenant), runtime.RunRef{RunID: runID})
	if err != nil {
		return "", err
	}
	return status.State, nil
}

func temporalDescribeAs(ctx context.Context, tenant, runID, want string) error {
	state, err := temporalStateOf(temporalW(ctx), tenant, runID)
	if err != nil {
		return err
	}
	if state != want {
		return fmt.Errorf("run %q of tenant %q is %q, want %q", runID, tenant, state, want)
	}
	return nil
}
