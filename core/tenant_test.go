package core_test

import (
	"context"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestTenantFrom(t *testing.T) {
	tests := []struct {
		name   string
		ctx    func(t *testing.T) context.Context
		wantID core.TenantID
		wantOK bool
	}{
		{
			name: "absent",
			ctx:  func(*testing.T) context.Context { return context.Background() },
		},
		{
			name: "empty",
			ctx: func(t *testing.T) context.Context {
				return core.WithTenant(t.Context(), "")
			},
		},
		{
			name: "set",
			ctx: func(t *testing.T) context.Context {
				return core.WithTenant(t.Context(), "tenant-a")
			},
			wantID: "tenant-a",
			wantOK: true,
		},
		{
			name: "overwrite",
			ctx: func(t *testing.T) context.Context {
				return core.WithTenant(core.WithTenant(t.Context(), "tenant-a"), "tenant-b")
			},
			wantID: "tenant-b",
			wantOK: true,
		},
		{
			name: "overwrite with empty removes",
			ctx: func(t *testing.T) context.Context {
				return core.WithTenant(core.WithTenant(t.Context(), "tenant-a"), "")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, ok := core.TenantFrom(tt.ctx(t))
			if id != tt.wantID || ok != tt.wantOK {
				t.Fatalf("TenantFrom() = (%q, %v), want (%q, %v)", id, ok, tt.wantID, tt.wantOK)
			}
		})
	}
}

func TestWithTenantLeavesParentUnchanged(t *testing.T) {
	parent := core.WithTenant(t.Context(), "tenant-a")
	_ = core.WithTenant(parent, "tenant-b")

	if id, ok := core.TenantFrom(parent); !ok || id != "tenant-a" {
		t.Fatalf("parent TenantFrom() = (%q, %v), want (tenant-a, true)", id, ok)
	}
	if _, ok := core.TenantFrom(t.Context()); ok {
		t.Fatal("root context gained a tenant")
	}
}
