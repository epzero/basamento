package steps

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"
	enumspb "go.temporal.io/api/enums/v1"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
	"github.com/epzero/basamento/runtime/temporal"
)

func registerTemporalControl(sc *godog.ScenarioContext) {
	sc.Step(`^a run of "([^"]*)" without a tenant is started on the temporal runtime$`,
		func(ctx context.Context, flow string) error {
			return temporalW(ctx).controlStart("", flow, "", "")
		})
	sc.Step(`^a run of "([^"]*)" is started on the temporal runtime with context tenant "([^"]*)" and request tenant "([^"]*)"$`,
		func(ctx context.Context, flow, contextTenant, requestTenant string) error {
			return temporalW(ctx).controlStart("", flow, contextTenant, requestTenant)
		})
	sc.Step(`^run "([^"]*)" of "([^"]*)" is started on the temporal runtime without a context tenant and with request tenant "([^"]*)"$`,
		func(ctx context.Context, runID, flow, requestTenant string) error {
			return temporalW(ctx).controlStart(runID, flow, "", requestTenant)
		})
	sc.Step(`^run "([^"]*)" of "([^"]*)" is started on the temporal runtime with context tenant "([^"]*)" and request tenant "([^"]*)"$`,
		func(ctx context.Context, runID, flow, contextTenant, requestTenant string) error {
			return temporalW(ctx).controlStart(runID, flow, contextTenant, requestTenant)
		})
	sc.Step(`^run "([^"]*)" of "([^"]*)" is started on the temporal runtime with context tenant "([^"]*)" and an empty request tenant$`,
		func(ctx context.Context, runID, flow, contextTenant string) error {
			return temporalW(ctx).controlStart(runID, flow, contextTenant, "")
		})
	sc.Step(`^the Temporal test server holds no run history for that start$`, func(ctx context.Context) error {
		w := temporalW(ctx)
		if w.stub == nil {
			return fmt.Errorf("no temporal test server")
		}
		if got := len(w.stub.Starts()); got != 0 {
			return fmt.Errorf("the stub recorded %d starts, want 0", got)
		}
		if got := len(w.recorded()); got != 0 {
			return fmt.Errorf("the payload recorder saw %d payloads, want 0", got)
		}
		return nil
	})
}

func temporalControlLastError(w *temporalWorld, code string) error {
	w.mu.Lock()
	err := w.control.lastErr
	w.mu.Unlock()
	if err == nil {
		return fmt.Errorf("the call succeeded, want code %q", code)
	}
	if got := string(core.CodeOf(err)); got != code {
		return fmt.Errorf("the call failed with %q, want %q: %w", got, code, err)
	}
	return nil
}

// temporalNotSent asserts a rejected control call never reached the stub. The
// stub does not change run state on its own, so a leaked cancel/resolve/describe/list
// shows up only in the request log.
func temporalNotSent(w *temporalWorld, rpc string) error {
	if w.stub == nil {
		return fmt.Errorf("no temporal test server")
	}
	for _, req := range w.stub.Requests() {
		if req == rpc {
			return fmt.Errorf("the rejected call reached the Temporal test server: %s was requested", rpc)
		}
	}
	return nil
}

func (w *temporalWorld) temporalCtx(tenant string) context.Context {
	if tenant == "" {
		return context.Background()
	}
	return core.WithTenant(context.Background(), core.TenantID(tenant))
}

func (w *temporalWorld) runtimeForControl() (runtime.Runtime, error) {
	if err := w.ensureRuntime(); err != nil {
		return nil, err
	}
	return w.rt, nil
}

func (w *temporalWorld) pendingControlRef() runtime.RunRef {
	w.mu.Lock()
	ref := w.control.pendingRef
	w.mu.Unlock()
	return ref
}

// ensureFlowAttribute registers BasamentoFlow on the stub namespace once. In
// production, Serve or an operator performs this setup before control-plane starts.
func (w *temporalWorld) ensureFlowAttribute() error {
	if w.stub == nil {
		return fmt.Errorf("temporal test server is not ready")
	}
	w.mu.Lock()
	if w.control.flowAttrRegistered {
		w.mu.Unlock()
		return nil
	}
	w.control.flowAttrRegistered = true
	w.mu.Unlock()
	w.stub.PreRegisterSearchAttribute(temporal.FlowSearchAttribute, enumspb.INDEXED_VALUE_TYPE_KEYWORD)
	return nil
}

func (w *temporalWorld) controlStart(runID, flow, contextTenant, requestTenant string) error {
	rt, err := w.runtimeForControl()
	if err != nil {
		return err
	}
	if err := w.ensureNamedFlow(flow); err != nil {
		return err
	}
	if err := w.ensureFlowAttribute(); err != nil {
		return err
	}
	ref, err := rt.Start(w.temporalCtx(contextTenant), runtime.StartRequest{
		Flow:     runtime.FlowRef{Name: flow},
		RunID:    runID,
		TenantID: core.TenantID(requestTenant),
	})
	w.mu.Lock()
	w.control.lastStart = ref
	w.control.lastErr = err
	w.mu.Unlock()
	return nil
}
