package steps

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func registerTemporalList(sc *godog.ScenarioContext) {
	sc.Step(`^runs of flow "([^"]*)" are listed on the temporal runtime for request tenant "([^"]*)" with context tenant "([^"]*)"$`,
		func(ctx context.Context, flow, requestTenant, contextTenant string) error {
			return temporalW(ctx).controlList(contextTenant, flow, requestTenant)
		})
	sc.Step(`^runs of flow "([^"]*)" are listed on the temporal runtime with context tenant "([^"]*)" and an empty query tenant$`,
		func(ctx context.Context, flow, contextTenant string) error {
			return temporalW(ctx).controlList(contextTenant, flow, "")
		})
	sc.Step(`^runs of flow "([^"]*)" are listed on the temporal runtime without a context tenant and with query tenant "([^"]*)"$`,
		func(ctx context.Context, flow, queryTenant string) error {
			return temporalW(ctx).controlList("", flow, queryTenant)
		})
}

func (w *temporalWorld) controlList(contextTenant, flow, queryTenant string) error {
	rt, err := w.runtimeForControl()
	if err != nil {
		return err
	}
	if err := w.ensureFlowAttribute(); err != nil {
		return err
	}
	runs, _, err := rt.List(w.temporalCtx(contextTenant), runtime.RunQuery{
		Flow:     flow,
		TenantID: core.TenantID(queryTenant),
	})
	w.mu.Lock()
	w.control.lastList = runs
	w.control.lastErr = err
	w.control.haveListed = true
	w.mu.Unlock()
	return nil
}

func temporalListFailure(w *temporalWorld, code string) error {
	if err := temporalControlLastError(w, code); err != nil {
		return err
	}
	w.mu.Lock()
	n := len(w.control.lastList)
	w.mu.Unlock()
	if n != 0 {
		return fmt.Errorf("listing returned %d runs on error, want none", n)
	}
	return nil
}

func temporalListCount(ctx context.Context, caller string, want int, tenant, runID string) error {
	w := temporalW(ctx)
	w.mu.Lock()
	lastErr := w.control.lastErr
	runs := w.control.lastList
	haveListed := w.control.haveListed
	w.mu.Unlock()
	if lastErr != nil {
		return lastErr
	}
	if !haveListed {
		rt, err := w.runtimeForControl()
		if err != nil {
			return err
		}
		if err := w.ensureFlowAttribute(); err != nil {
			return err
		}
		var listErr error
		runs, _, listErr = rt.List(w.temporalCtx(caller), runtime.RunQuery{})
		if listErr != nil {
			return listErr
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
}
