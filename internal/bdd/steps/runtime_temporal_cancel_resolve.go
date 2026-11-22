package steps

import (
	"context"
	"encoding/json"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func registerTemporalCancelResolve(sc *godog.ScenarioContext) {
	sc.Step(`^run "([^"]*)" is cancelled on the temporal runtime with context tenant "([^"]*)"$`,
		func(ctx context.Context, runID, contextTenant string) error {
			w := temporalW(ctx)
			rt, err := w.runtimeForControl()
			if err != nil {
				return err
			}
			err = rt.Cancel(w.temporalCtx(contextTenant), w.pendingControlRef(), "")
			w.recordControlErr(err)
			return nil
		})
	sc.Step(`^run "([^"]*)" is cancelled on the temporal runtime without a context tenant and with reference tenant "([^"]*)"$`,
		func(ctx context.Context, runID, tenant string) error {
			w := temporalW(ctx)
			rt, err := w.runtimeForControl()
			if err != nil {
				return err
			}
			err = rt.Cancel(w.temporalCtx(""), runtime.RunRef{RunID: runID, TenantID: core.TenantID(tenant)}, "")
			w.recordControlErr(err)
			return nil
		})
	sc.Step(`^gate "([^"]*)" of run "([^"]*)" is resolved on the temporal runtime with context tenant "([^"]*)" and payload (\{.*\})$`,
		func(ctx context.Context, gate, runID, contextTenant, payload string) error {
			w := temporalW(ctx)
			rt, err := w.runtimeForControl()
			if err != nil {
				return err
			}
			err = rt.Resolve(w.temporalCtx(contextTenant), w.pendingControlRef(), gate, json.RawMessage(payload))
			w.recordControlErr(err)
			return nil
		})
	sc.Step(`^gate "([^"]*)" of run "([^"]*)" is resolved on the temporal runtime without a context tenant, reference tenant "([^"]*)" and payload (\{.*\})$`,
		func(ctx context.Context, gate, runID, tenant, payload string) error {
			w := temporalW(ctx)
			rt, err := w.runtimeForControl()
			if err != nil {
				return err
			}
			err = rt.Resolve(w.temporalCtx(""), runtime.RunRef{RunID: runID, TenantID: core.TenantID(tenant)}, gate, json.RawMessage(payload))
			w.recordControlErr(err)
			return nil
		})
}

func (w *temporalWorld) recordControlErr(err error) {
	w.mu.Lock()
	w.control.lastErr = err
	w.mu.Unlock()
}
