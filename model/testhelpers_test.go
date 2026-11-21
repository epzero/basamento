package model

import (
	"context"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
)

// testClock is a fake clock frozen at a fixed instant, so no registry test
// reads wall time.
func testClock() *coretest.FakeClock {
	return coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
}

// newTestRegistry returns a registry with the default bounds on a fake clock.
func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	reg, err := NewRegistry("test", WithClock(testClock()))
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	return reg
}

// newTestRegistryAlpha is newTestRegistry for the "alpha" provider.
func newTestRegistryAlpha(t *testing.T) *Registry {
	t.Helper()
	reg, err := NewRegistry("alpha", WithClock(testClock()))
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	return reg
}

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
