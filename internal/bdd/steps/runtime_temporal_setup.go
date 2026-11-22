package steps

import (
	"context"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/runtime"
)

func registerTemporalSetup(sc *godog.ScenarioContext) {
	sc.Step(`^tenant "([^"]*)" has run "([^"]*)" of flow "([^"]*)" on the temporal runtime waiting at gate "([^"]*)"$`,
		func(ctx context.Context, tenant, runID, flow, gate string) error {
			return temporalW(ctx).startWaitingRun(tenant, runID, flow, gate)
		})
}

func (w *temporalWorld) ensureControlFlow(name, gate string) error {
	if err := w.ensureRuntime(); err != nil {
		return err
	}
	w.mu.Lock()
	_, ok := w.flows[name]
	w.mu.Unlock()
	if ok {
		return nil
	}
	return w.addFlow(name, temporalGateAwaitFlow(gate))
}

// startWaitingRun starts a run through the control plane. The stub does not execute
// workflow code, so the execution stays RUNNING; gate waiting in the registered flow
// is not observable here (part (b) data-path scenarios and conformance, WP-L5-04).
func (w *temporalWorld) startWaitingRun(tenant, runID, flow, gate string) error {
	if err := w.ensureControlFlow(flow, gate); err != nil {
		return err
	}
	if err := w.ensureFlowAttribute(); err != nil {
		return err
	}
	rt, err := w.runtimeForControl()
	if err != nil {
		return err
	}
	_, err = rt.Start(w.temporalCtx(tenant), runtime.StartRequest{
		Flow:  runtime.FlowRef{Name: flow},
		RunID: runID,
	})
	return err
}
