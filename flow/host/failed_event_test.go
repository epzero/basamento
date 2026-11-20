package host

import (
	"testing"

	"github.com/epzero/basamento/core"
)

func TestFailedHandlerEventCarriesSettledCost(t *testing.T) {
	ev := failedEvent("repair", 0, core.NewError(core.CodeVendor, "model failed", nil), 2500)
	if ev.CostMpts != 2500 {
		t.Fatalf("CostMpts %d, want 2500", ev.CostMpts)
	}
}
