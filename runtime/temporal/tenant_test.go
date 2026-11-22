package temporal

import (
	"context"
	"testing"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	sdkworkflow "go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
)

func TestTenantPropagatorInjectExtract(t *testing.T) {
	var p TenantPropagator
	ctx := core.WithTenant(context.Background(), "acme")
	w := &testHeaderWriter{}
	if err := p.Inject(ctx, w); err != nil {
		t.Fatal(err)
	}
	out, err := p.Extract(context.Background(), testHeaderReader{w.fields})
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := core.TenantFrom(out); !ok || id != "acme" {
		t.Fatalf("tenant %q", id)
	}
}

func TestTenantPropagatorNoHeader(t *testing.T) {
	var p TenantPropagator
	out, err := p.Extract(context.Background(), testHeaderReader{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := core.TenantFrom(out); ok {
		t.Fatal("unexpected tenant")
	}
}

type testHeaderWriter struct{ fields map[string]*commonpb.Payload }

func (h *testHeaderWriter) Set(key string, value *commonpb.Payload) {
	if h.fields == nil {
		h.fields = map[string]*commonpb.Payload{}
	}
	h.fields[key] = value
}
func (h *testHeaderWriter) Get(string) (*commonpb.Payload, bool) { return nil, false }

type testHeaderReader struct{ fields map[string]*commonpb.Payload }

func (h testHeaderReader) Get(key string) (*commonpb.Payload, bool) {
	v, ok := h.fields[key]
	return v, ok
}
func (h testHeaderReader) ForEachKey(func(string, *commonpb.Payload) error) error { return nil }

func TestTenantPropagatorRejectsInvalidTenant(t *testing.T) {
	var p TenantPropagator
	invalid := core.TenantID("acme/eu")
	wfCtx := sdkworkflow.WithValue(nil, tenantWorkflowKey{}, invalid)
	if got := tenantFromWorkflow(wfCtx); got != "" {
		t.Fatalf("tenantFromWorkflow(%q) = %q want none", invalid, got)
	}
	w := &testHeaderWriter{}
	if err := p.InjectFromWorkflow(wfCtx, w); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.fields[tenantHeaderKey]; ok {
		t.Fatal("invalid tenant injected into activity header")
	}
	raw, err := converter.GetDefaultDataConverter().ToPayload(string(invalid))
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.ExtractToWorkflow(nil, testHeaderReader{map[string]*commonpb.Payload{tenantHeaderKey: raw}})
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		t.Fatalf("invalid tenant stored in workflow context: %q", tenantFromWorkflow(out))
	}
}
