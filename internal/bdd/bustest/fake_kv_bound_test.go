package bustest

import (
	"context"
	"testing"
	"time"

	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/platform/bus"
)

func TestFakeKVValueBound(t *testing.T) {
	clk := coretest.NewFakeClock(time.Unix(0, 0))
	f := NewFake(clk, bus.Options{MaxMessageSize: bus.DefaultMaxMessageSize})
	ctx := context.Background()
	key := "cursor.r1"
	lim := bus.LimitsFor(bus.DefaultMaxMessageSize, int(bus.EmbeddedServerMaxPayload(bus.DefaultMaxMessageSize)))
	max := lim.MaxKVValue
	if _, err := f.Put(ctx, key, make([]byte, max), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Put(ctx, key, make([]byte, max+1), 0); err == nil {
		t.Fatal("expected oversize")
	}
}
