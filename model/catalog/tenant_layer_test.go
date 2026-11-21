package catalog

import (
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
)

func TestRefusedTenantPriceDoesNotLeaveEmptyLayer(t *testing.T) {
	clock := coretest.NewFakeClock(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	c, err := New(clock)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	before := len(c.tenants)
	bad := Price{Rules: []Rule{{Rates: Rates{"input": -1000, "output": 0}}}}
	if err := c.SetTenantPrice("acme", "sim-chat", bad); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("SetTenantPrice bad: code=%s err=%v", core.CodeOf(err), err)
	}
	if len(c.tenants) != before {
		t.Fatalf("tenants len = %d, want %d after refused set", len(c.tenants), before)
	}
	good := Price{Rules: []Rule{{Rates: Rates{"input": 1000, "output": 0}}}}
	if err := c.SetTenantPrice("acme", "sim-chat", good); err != nil {
		t.Fatalf("SetTenantPrice good: %v", err)
	}
	if m, ok := c.tenants["acme"]; !ok || len(m) != 1 {
		t.Fatalf("acme layer = %v, want one entry", m)
	}
}
