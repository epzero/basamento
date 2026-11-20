package model

import (
	"context"
	"testing"

	"github.com/epzero/basamento/core"
)

func testCtx(t *testing.T) context.Context {
	t.Helper()
	return core.WithTenant(context.Background(), "acme")
}

func tenantCtx(tenant string) context.Context {
	return core.WithTenant(context.Background(), core.TenantID(tenant))
}

func mustBegin(t *testing.T, reg *Registry, ctx context.Context, ref string) *Run {
	t.Helper()
	run, _, err := reg.Begin(ctx, ref)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	return run
}
