package bustest

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/platform/bus"
)

func TestFakeKVExpiryAtDeadline(t *testing.T) {
	clk := coretest.NewFakeClock(time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC))
	f := NewFake(clk, bus.Options{Clock: clk})
	ctx := context.Background()
	if _, err := f.Put(ctx, "lock.r1", []byte("ana"), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	clk.Advance(30 * time.Second)
	if _, _, err := f.Get(ctx, "lock.r1"); err == nil || core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("got %v", err)
	}
}

func TestFakeHugeTTLDoesNotExpireImmediately(t *testing.T) {
	clk := coretest.NewFakeClock(time.Unix(0, 0))
	f := NewFake(clk, bus.Options{Clock: clk})
	ctx := context.Background()
	if _, err := f.Put(ctx, "lock.r1", []byte("v"), time.Duration(math.MaxInt64)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Get(ctx, "lock.r1"); err != nil {
		t.Fatalf("got %v", err)
	}
}
