package core_test

import (
	"context"
	"strings"
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

func TestTenantFromNil(t *testing.T) {
	// The contract is that a nil context reports no tenant.
	var nilCtx context.Context
	if id, ok := core.TenantFrom(nilCtx); id != "" || ok {
		t.Fatalf("TenantFrom(nil) = (%q, %v), want (\"\", false)", id, ok)
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

func TestTenantFromGrammar(t *testing.T) {
	long128 := strings.Repeat("a", 128)
	long129 := strings.Repeat("a", 129)
	bytes130 := strings.Repeat("é", 65) // 65 × 2 bytes.

	tests := []struct {
		name string
		id   core.TenantID
		ok   bool
	}{
		{name: "short", id: "acme", ok: true},
		{name: "128 bytes", id: core.TenantID(long128), ok: true},
		{name: "non-ASCII", id: "acme-東京", ok: true},
		{name: "interior dot", id: "acme.eu", ok: true},
		{name: "interior double dot", id: "a..b", ok: true},
		{name: "interior whitespace", id: "a b", ok: true},
		{name: "single dot", id: "."},
		{name: "double dot", id: ".."},
		{name: "129 bytes", id: core.TenantID(long129)},
		{name: "130 bytes", id: core.TenantID(bytes130)},
		{name: "invalid UTF-8", id: "\xff\xfe"},
		{name: "NUL", id: "a\x00b"},
		{name: "control U+000A", id: "a\nb"},
		{name: "control U+001F", id: "a\x1fb"},
		{name: "DEL U+007F", id: "a\x7fb"},
		{name: "slash", id: "a/.part"},
		{name: "backslash", id: `a\b`},
		{name: "leading space", id: " acme"},
		{name: "trailing space", id: "acme "},
		{name: "leading U+00A0", id: "\u00a0acme"},
		{name: "trailing U+00A0", id: "acme\u00a0"},
		{name: "leading U+0085", id: "\u0085acme"},
		// U+2028 and U+3000 sit past Latin-1 in the White_Space table, so they
		// only fail if the check asks Unicode, not ASCII or Latin-1.
		{name: "trailing U+2028", id: "acme\u2028"},
		{name: "leading U+3000", id: "\u3000acme"},
		// U+200B is a zero-width space, which Unicode does not classify as
		// White_Space; a check that broadened to all invisible separators would
		// reject it.
		{name: "leading U+200B", id: "\u200bacme", ok: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := core.WithTenant(t.Context(), tt.id)
			id, ok := core.TenantFrom(ctx)
			if ok != tt.ok {
				t.Fatalf("TenantFrom(%q) ok = %v, want %v", tt.id, ok, tt.ok)
			}
			wantID := tt.id
			if !tt.ok {
				wantID = ""
			}
			if id != wantID {
				t.Fatalf("TenantFrom(%q) = (%q, %v), want (%q, %v)", tt.id, id, ok, wantID, tt.ok)
			}
		})
	}
}

func TestTenantFromInvalidOverridesParent(t *testing.T) {
	parent := core.WithTenant(t.Context(), "acme")
	child := core.WithTenant(parent, "a/b")

	if id, ok := core.TenantFrom(child); id != "" || ok {
		t.Fatalf("derived TenantFrom() = (%q, %v), want (\"\", false)", id, ok)
	}
	if id, ok := core.TenantFrom(parent); !ok || id != "acme" {
		t.Fatalf("parent TenantFrom() = (%q, %v), want (acme, true)", id, ok)
	}
}
