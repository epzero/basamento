package temporal

import (
	"context"

	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
)

const tenantHeaderKey = "basamento-tenant"

type tenantWorkflowKey struct{}

// TenantPropagator carries the run tenant between client, workflow and activity
// contexts so the data converter and step functions can read it (REQ-RT-031).
type TenantPropagator struct{}

// Inject writes the context tenant into workflow headers on start.
func (TenantPropagator) Inject(ctx context.Context, writer workflow.HeaderWriter) error {
	t, ok := core.TenantFrom(ctx)
	if !ok {
		return nil
	}
	p, err := converter.GetDefaultDataConverter().ToPayload(string(t))
	if err != nil {
		return err
	}
	writer.Set(tenantHeaderKey, p)
	return nil
}

// Extract reads the tenant header into an activity Go context.
func (TenantPropagator) Extract(ctx context.Context, reader workflow.HeaderReader) (context.Context, error) {
	p, ok := reader.Get(tenantHeaderKey)
	if !ok {
		return ctx, nil
	}
	var tenant string
	if err := converter.GetDefaultDataConverter().FromPayload(p, &tenant); err != nil {
		return ctx, err
	}
	if id := checkedTenant(core.TenantID(tenant)); id != "" {
		return core.WithTenant(ctx, id), nil
	}
	return ctx, nil
}

// InjectFromWorkflow copies the workflow tenant into activity headers.
func (TenantPropagator) InjectFromWorkflow(ctx workflow.Context, writer workflow.HeaderWriter) error {
	if v := ctx.Value(tenantWorkflowKey{}); v != nil {
		if id, ok := v.(core.TenantID); ok {
			if id = checkedTenant(id); id != "" {
				p, err := converter.GetDefaultDataConverter().ToPayload(string(id))
				if err != nil {
					return err
				}
				writer.Set(tenantHeaderKey, p)
			}
		}
	}
	return nil
}

// ExtractToWorkflow copies a workflow start header into workflow context.
func (TenantPropagator) ExtractToWorkflow(ctx workflow.Context, reader workflow.HeaderReader) (workflow.Context, error) {
	p, ok := reader.Get(tenantHeaderKey)
	if !ok {
		return ctx, nil
	}
	var tenant string
	if err := converter.GetDefaultDataConverter().FromPayload(p, &tenant); err != nil {
		return ctx, err
	}
	if id := checkedTenant(core.TenantID(tenant)); id != "" {
		return workflow.WithValue(ctx, tenantWorkflowKey{}, id), nil
	}
	return ctx, nil
}

func tenantFromWorkflow(ctx workflow.Context) core.TenantID {
	if v := ctx.Value(tenantWorkflowKey{}); v != nil {
		if id, ok := v.(core.TenantID); ok {
			return checkedTenant(id)
		}
	}
	return ""
}

// checkedTenant returns id when it satisfies the tenant grammar and "" otherwise.
// core.TenantFrom is the single implementation of that rule.
func checkedTenant(id core.TenantID) core.TenantID {
	if id == "" {
		return ""
	}
	got, ok := core.TenantFrom(core.WithTenant(context.Background(), id))
	if !ok || got != id {
		return ""
	}
	return got
}
