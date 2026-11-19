package bustest

import (
	"context"
	"testing"
	"time"

	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/platform/bus"
)

func TestFakeLiveOverflowDuringReplayMatchesStore(t *testing.T) {
	clk := coretest.NewFakeClock(time.Unix(0, 0))
	f := NewFake(clk, bus.Options{SubBuffer: 4, MaxMessageSize: bus.DefaultMaxMessageSize})
	subject := "run.r1.of"
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		if _, err := f.Publish(ctx, subject, []byte{byte('a' + i)}); err != nil {
			t.Fatal(err)
		}
	}
	ch, err := f.Subscribe(ctx, subject, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Publish(ctx, subject, []byte("live")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, ok := <-ch; !ok {
			t.Fatalf("ended at %d", i)
		}
	}
	if _, ok := <-ch; ok {
		t.Fatal("subscription should have ended on overflow")
	}
}
