package temporal

import (
	"context"
	"testing"

	commonpb "go.temporal.io/api/common/v1"

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
